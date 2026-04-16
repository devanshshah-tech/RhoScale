# vLLM Autoscaler Controller

A Kubernetes autoscaling controller for vLLM inference deployments. It implements the **MAPE-K loop** (Monitor-Analyze-Plan-Execute over a Knowledge base) with a queuing-theory-driven approach derived from the G/G/1 queueing model.

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         MAPE-K Loop                             │
│                                                                 │
│  ┌──────────┐   ┌──────────┐   ┌──────────┐   ┌─────────────┐   │
│  │ Monitor  │ → │ Analyze  │ → │   Plan   │ → │   Execute   │   │
│  └──────────┘   └──────────┘   └──────────┘   └─────────────┘   │
│       │                                                         │
│       ▼                                                         │
│  pkg/metrics/              pkg/scaler/         pkg/controller/  │
│  prometheus.go             scaler.go           controller.go    │
└─────────────────────────────────────────────────────────────────┘
```

## Data Flow

```
Prometheus
    │
    │  /api/v1/query (Scrape)
    ▼
pkg/metrics/prometheus.go          ←── InferenceMetrics struct
    │ (InferenceMetrics)                          │
    ▼                                             │
pkg/scaler/scaler.go              ────────────→   │ (Analyzer interface)
    │ (ScalingDecision)                           │
    ▼                                             │
pkg/controller/controller.go      ←── Kubernetes API (Observe actual state)
    │
    │ PATCH /apis/apps/v1/namespaces/{ns}/deployments/{name}
    ▼
Kubernetes Deployment  ──────────────── Updates replica count
```

## Packages

### `main.go` — Entry Point & Wiring

Application bootstrap. Parses CLI flags, builds all components, wires them together, and starts the controller with graceful shutdown.

Key flags:
| Flag | Default | Description |
|------|---------|-------------|
| `-namespace` | `default` | K8s namespace of vLLM deployment |
| `-deployment` | `vllm-inference` | Deployment name to scale |
| `-prometheus` | `http://prometheus:9090` | Prometheus server address |
| `-queue-threshold` | `5` | Queue depth knee-point (θ_queue) |
| `-kvcache-threshold` | `0.80` | KV-cache utilisation threshold |
| `-scrape-interval` | `10s` | Prometheus poll frequency |
| `-cooldown` | `120s` | Minimum time between scale actions |
| `-confirmation-ticks` | `2` | Consecutive ticks before scale-out |
| `-min-replicas` | `1` | Minimum replica count |
| `-max-replicas` | `10` | Maximum replica count |

### `pkg/metrics/prometheus.go` — Monitor Phase

Queries Prometheus for vLLM-specific metrics via the `/api/v1/query` HTTP API. Returns a single `InferenceMetrics` snapshot containing:

- **Queue signals** (Little's Law primary): `QueueDepth`, `RunningRequests`
- **Saturation signal** (USE Method): `KVCacheUsageFraction`
- **QoS signals**: `TTFT_P50/P95`, `TPOT_P50` (latency percentiles)
- **Throughput**: `RequestsPerSecond`

`PrometheusClient` wraps HTTP calls and parses the Prometheus JSON response format.

### `pkg/scaler/scaler.go` — Analyze + Plan Phases

Implements a **two-signal OR gate** using the G/G/1 queueing model:

**Signal 1 — KV-Cache Saturation** (high urgency):
```
if KVCacheUsageFraction >= θ_kv (0.80) → Scale out immediately
```
VRAM saturation means PagedAttention cannot schedule new requests regardless of compute.

**Signal 2 — Queue Depth past Knee Point** (proportional control):
```
if L_queue > θ_queue (5) → desired = ceil(L/θ) × currentReplicas
if L_queue < θ/4        → Scale in (conservative)
```
Derived from Little's Law: when queue depth exceeds the calibrated knee-point, wait time grows super-linearly and proportional scaling is needed.

**Signal 3 — Hold**: queue depth within `[θ/4, θ]` → no action.

### `pkg/controller/controller.go` — Execute Phase + Loop Orchestration

Runs the reconciliation loop at `ScrapeInterval` frequency. Each tick:

1. **Monitor**: calls `scraper.Scrape()` → `InferenceMetrics`
2. **Observe**: GET deployment from Kubernetes API → current replica count
3. **Analyze + Plan**: calls `analyzer.Analyze()` → `ScalingDecision`
4. **Execute**: PATCHes deployment replicas if decision != `Hold`

**Flapping Prevention** (two mechanisms):
- **Cooldown period**: after any scale action, waits `CooldownPeriod` (120s default) before acting again
- **Confirmation ticks**: for scale-out only, metric must exceed threshold for N consecutive ticks (filters transient spikes)

## Theoretical Basis

### Little's Law
```
L = λ × W
```
L = requests in system, λ = arrival rate, W = mean wait time

### G/G/1 Queueing Model
At low load (ρ = λ/μ << 1), W ≈ 1/μ (just service time). As ρ → 1 (Knee Point), W → ∞ due to queue term in the Pollaczek-Khinchine formula.

The **Knee Point** (θ = 5) is the queue depth at which super-linear latency growth begins. The scaler uses θ to decide when to act, avoiding unnecessary scaling in the linear region.

### Scaling Formula (Proportional Control)
```
desiredReplicas = ceil(queueDepth / θ_queue) × currentReplicas
```
This keeps queue depth anchored near the knee point across replica counts.

## Local Development

Requires a Kubernetes cluster (in-cluster or kubeconfig) and a Prometheus instance scraping the target vLLM deployment.

```bash
# Build
go build ./...

# Run (requires kubeconfig pointing to a cluster with vLLM + Prometheus)
go run . \
  -deployment vllm-inference \
  -namespace default \
  -prometheus http://prometheus:9090

# Run unit tests
go test ./...
```

## Prometheus Metric Requirements

The controller expects the following vLLM metrics to be exposed and scraped by Prometheus:

| Metric | Description | Used For |
|--------|-------------|----------|
| `vllm:num_requests_waiting` | Requests queued, not yet scheduled | Queue depth (primary signal) |
| `vllm:num_requests_running` | Requests in active decode | Batch fill monitoring |
| `vllm:gpu_cache_usage_perc` | KV-cache VRAM utilisation (0-1) | VRAM saturation (urgent signal) |
| `vllm:time_to_first_token_seconds_bucket` | TTFT histogram | QoS SLO tracking |
| `vllm:time_per_output_token_seconds_bucket` | TPOT histogram | QoS SLO tracking |
| `vllm:request_success_total` | Successful request counter | Throughput (λ) derivation |

## Dependencies

| Module | Version | Purpose |
|--------|---------|---------|
| `go.uber.org/zap` | v1.27.0 | Structured logging |
| `k8s.io/api` | v0.29.3 | Kubernetes API types |
| `k8s.io/apimachinery` | v0.29.3 | Kubernetes runtime utilities |
| `k8s.io/client-go` | v0.29.3 | Kubernetes client library |
