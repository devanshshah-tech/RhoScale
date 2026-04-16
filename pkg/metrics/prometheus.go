// Package metrics implements the Monitor phase of the MAPE-K loop.
// It queries Prometheus for vLLM-specific metrics derived from the
// observability layer described in the Week 3-4 baseline analysis.
package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go.uber.org/zap"
)

// InferenceMetrics holds a single point-in-time snapshot of all
// vLLM Prometheus metrics relevant to the scaling decision.
// These map directly to the metric table in the Weeks 3-4 baseline report.
type InferenceMetrics struct {
	// QoS Latency Signals (from Annotated Bibliography [2])
	TTFT_P50_Seconds float64 // vllm:time_to_first_token_seconds (p50)
	TTFT_P95_Seconds float64 // vllm:time_to_first_token_seconds (p95)
	TPOT_P50_Seconds float64 // vllm:time_per_output_token_seconds (p50)

	// Queue State Signals — primary scaling inputs (Little's Law observable)
	// L = λW means: QueueDepth = ArrivalRate × WaitTime
	// A growing QueueDepth with stable ArrivalRate means WaitTime is increasing.
	QueueDepth     float64 // vllm:num_requests_waiting  — requests not yet scheduled
	RunningRequests float64 // vllm:num_requests_running  — requests in active decode

	// Saturation Signal — secondary scaling input (USE Method, Annotated Bibliography [7])
	// KVCacheUsage approaching 1.0 means GPU VRAM is the bottleneck, not compute.
	KVCacheUsageFraction float64 // vllm:gpu_cache_usage_perc

	// Throughput
	RequestsPerSecond float64 // derived: rate(vllm:request_success_total[1m])

	ScrapedAt time.Time
}

// PrometheusClient wraps HTTP calls to the Prometheus query API.
type PrometheusClient struct {
	baseURL    string
	httpClient *http.Client
	logger     *zap.Logger
}

// NewPrometheusClient constructs a client pointing at a Prometheus instance.
func NewPrometheusClient(baseURL string, logger *zap.Logger) *PrometheusClient {
	return &PrometheusClient{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
		logger: logger,
	}
}

// Scrape performs the Monitor phase: queries all required metrics from
// Prometheus in a single pass and returns a consolidated InferenceMetrics snapshot.
func (p *PrometheusClient) Scrape(ctx context.Context) (*InferenceMetrics, error) {
	m := &InferenceMetrics{ScrapedAt: time.Now()}
	var err error

	// --- Queue Depth (primary scaling signal) ---
	// This is L in Little's Law: L = λW
	// When L > θ (knee-point threshold), WaitTime W is growing super-linearly.
	if m.QueueDepth, err = p.queryScalar(ctx, "vllm:num_requests_waiting"); err != nil {
		return nil, fmt.Errorf("scraping queue depth: %w", err)
	}

	// --- Running Requests (batch fill level) ---
	if m.RunningRequests, err = p.queryScalar(ctx, "vllm:num_requests_running"); err != nil {
		return nil, fmt.Errorf("scraping running requests: %w", err)
	}

	// --- KV-Cache Saturation (VRAM bottleneck proxy) ---
	// PagedAttention (Annotated Bib [1]): when KV-cache is full, new requests
	// cannot be scheduled regardless of compute availability.
	if m.KVCacheUsageFraction, err = p.queryScalar(ctx, "vllm:gpu_cache_usage_perc"); err != nil {
		return nil, fmt.Errorf("scraping kv-cache usage: %w", err)
	}

	// --- TTFT Percentiles (QoS SLO tracking) ---
	if m.TTFT_P50_Seconds, err = p.queryScalar(ctx,
		`histogram_quantile(0.50, rate(vllm:time_to_first_token_seconds_bucket[1m]))`); err != nil {
		p.logger.Warn("TTFT p50 unavailable", zap.Error(err))
	}
	if m.TTFT_P95_Seconds, err = p.queryScalar(ctx,
		`histogram_quantile(0.95, rate(vllm:time_to_first_token_seconds_bucket[1m]))`); err != nil {
		p.logger.Warn("TTFT p95 unavailable", zap.Error(err))
	}

	// --- TPOT p50 ---
	if m.TPOT_P50_Seconds, err = p.queryScalar(ctx,
		`histogram_quantile(0.50, rate(vllm:time_per_output_token_seconds_bucket[1m]))`); err != nil {
		p.logger.Warn("TPOT p50 unavailable", zap.Error(err))
	}

	// --- Throughput (λ in Little's Law) ---
	if m.RequestsPerSecond, err = p.queryScalar(ctx,
		`rate(vllm:request_success_total[1m])`); err != nil {
		p.logger.Warn("Throughput metric unavailable", zap.Error(err))
	}

	p.logger.Debug("Metrics scraped",
		zap.Float64("queueDepth", m.QueueDepth),
		zap.Float64("kvCacheFraction", m.KVCacheUsageFraction),
		zap.Float64("ttft_p95_ms", m.TTFT_P95_Seconds*1000),
		zap.Float64("rps", m.RequestsPerSecond),
	)

	return m, nil
}

// prometheusQueryResponse is the JSON envelope returned by /api/v1/query.
type prometheusQueryResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Value [2]interface{} `json:"value"` // [unixTimestamp, "valueString"]
		} `json:"result"`
	} `json:"data"`
}

// queryScalar executes an instant PromQL query and returns its scalar result.
func (p *PrometheusClient) queryScalar(ctx context.Context, promQL string) (float64, error) {
	endpoint := p.baseURL + "/api/v1/query"
	params := url.Values{"query": {promQL}}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
	if err != nil {
		return 0, err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("prometheus HTTP error: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var pResp prometheusQueryResponse
	if err := json.Unmarshal(body, &pResp); err != nil {
		return 0, fmt.Errorf("prometheus JSON parse error: %w", err)
	}
	if pResp.Status != "success" || len(pResp.Data.Result) == 0 {
		return 0, nil // metric not yet populated (e.g., no traffic yet)
	}

	valStr, ok := pResp.Data.Result[0].Value[1].(string)
	if !ok {
		return 0, fmt.Errorf("unexpected value type in prometheus response")
	}
	return strconv.ParseFloat(valStr, 64)
}
