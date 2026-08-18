// Package scaler implements the Analyze and Plan phases of the MAPE-K loop.
//
// Theoretical basis: G/G/1 Queueing Model
// =========================================
// The vLLM inference server is modelled as a G/G/1 queue where:
//   - Arrivals follow a general distribution (real traffic is bursty / Poisson-ish)
//   - Service times follow a general distribution (token count per request varies)
//   - There is 1 logical server (the GPU — or N servers after scale-out)
//
// Little's Law: L = λ × W
//
//	L = mean number of requests in system  (QueueDepth + RunningRequests)
//	λ = mean arrival rate                  (RequestsPerSecond from Prometheus)
//	W = mean time a request spends in system (E2E latency)
//
// At low load (ρ = λ/μ << 1): W ≈ 1/μ (just service time, no waiting).
// As ρ → 1 (Knee Point): W → ∞ due to the queue term in the Pollaczek-Khinchine formula.
//
// The Knee Point (θ) from baseline experiments is the queue depth at which
// this super-linear growth begins. The scaler uses θ to decide when to act.
package scaler

import (
	"fmt"
	"math"

	"go.uber.org/zap"

	"github.com/devanshu/controller/pkg/metrics"
)

// ScalingDecision is the output of the Analyze+Plan phases.
type ScalingDecision struct {
	DesiredReplicas int32
	Reason          string
	ScaleDirection  Direction
}

// Direction is an enum for the scaling action type.
type Direction int

const (
	Hold     Direction = iota // No action required
	ScaleOut                  // Increase replicas (load exceeds capacity)
	ScaleIn                   // Decrease replicas (system is over-provisioned)
)

func (d Direction) String() string {
	return [...]string{"Hold", "ScaleOut", "ScaleIn"}[d]
}

// ScalerConfig holds the θ thresholds calibrated from Weeks 3-4 baseline data.
type ScalerConfig struct {
	// QueueDepthThreshold (θ_queue): the queue depth at the Knee Point.
	// Below this value, the system is in the linear region (no scaling needed).
	// Above this value, latency grows super-linearly — scale out immediately.
	QueueDepthThreshold int

	// KVCacheThreshold (θ_kv): GPU VRAM saturation proxy.
	// When PagedAttention KV-cache blocks are >80% utilised, new requests
	// cannot be scheduled regardless of compute. This triggers urgent scale-out.
	KVCacheThreshold float64

	MinReplicas int32
	MaxReplicas int32
}

// QueueDepthScaler is the primary scaling logic implementation.
type QueueDepthScaler struct {
	cfg    ScalerConfig
	logger *zap.Logger
}

func NewQueueDepthScaler(cfg ScalerConfig, logger *zap.Logger) *QueueDepthScaler {
	return &QueueDepthScaler{cfg: cfg, logger: logger}
}

// Analyze examines the current metric snapshot and returns a ScalingDecision.
//
// Decision logic implements a two-signal OR gate:
//
//	Signal 1 — Queue Depth (Little's Law signal):
//	  If L_queue > θ_queue  →  the system is past the Knee Point. Scale out.
//	  If L_queue < θ_queue/4 →  system is over-provisioned.   Scale in.
//
//	Signal 2 — KV-Cache Saturation (USE Method signal):
//	  If KV-Cache > θ_kv  →  VRAM is the bottleneck. Urgent scale-out.
//
// Desired replica count formula (proportional control):
//
//	desiredReplicas = ceil( currentQueue / θ_queue ) × currentReplicas
//
// This is analogous to the Kubernetes HPA ratio formula but substitutes
// queue depth for CPU utilisation.
func (s *QueueDepthScaler) Analyze(
	m *metrics.InferenceMetrics,
	currentReplicas int32,
) ScalingDecision {

	queueDepth := int(math.Round(m.QueueDepth))
	theta := s.cfg.QueueDepthThreshold

	// --- Signal 1: KV-Cache VRAM saturation (high urgency) ---
	if m.KVCacheUsageFraction >= s.cfg.KVCacheThreshold {
		desired := s.clamp(currentReplicas + 1)
		return ScalingDecision{
			DesiredReplicas: desired,
			ScaleDirection:  ScaleOut,
			Reason: fmt.Sprintf(
				"VRAM saturation: KV-cache at %.1f%% >= threshold %.1f%%. "+
					"PagedAttention cannot schedule new prefills. Scale-out to %d replicas.",
				m.KVCacheUsageFraction*100, s.cfg.KVCacheThreshold*100, desired,
			),
		}
	}

	// --- Signal 2: Queue Depth past Knee Point ---
	// Proportional formula: desired = ceil(L / θ) × current
	// Derivation: if current replicas handle θ requests at steady-state,
	// then (L/θ) times as many replicas are needed to keep queue depth at θ.
	if queueDepth > theta {
		ratio := math.Ceil(float64(queueDepth) / float64(theta))
		desired := s.clamp(int32(ratio) * currentReplicas)
		return ScalingDecision{
			DesiredReplicas: desired,
			ScaleDirection:  ScaleOut,
			Reason: fmt.Sprintf(
				"Queue depth %d > θ=%d (Knee Point). "+
					"Little's Law: system past linear region, W growing super-linearly. "+
					"Proportional scale: ceil(%d/%d)×%d = %d replicas.",
				queueDepth, theta, queueDepth, theta, currentReplicas, desired,
			),
		}
	}

	// --- Scale-In: system is over-provisioned ---
	// Conservative threshold: queue must be < θ/4 to avoid premature scale-in.
	// This implements the hysteresis window to prevent flapping.
	scaleInThreshold := theta / 4
	if scaleInThreshold < 1 {
		scaleInThreshold = 1
	}
	if queueDepth < scaleInThreshold && currentReplicas > s.cfg.MinReplicas {
		desired := s.clamp(currentReplicas - 1)
		return ScalingDecision{
			DesiredReplicas: desired,
			ScaleDirection:  ScaleIn,
			Reason: fmt.Sprintf(
				"Queue depth %d < scale-in threshold %d (θ/4). "+
					"System over-provisioned. Scaling in to %d replicas.",
				queueDepth, scaleInThreshold, desired,
			),
		}
	}

	// --- Hold: system is in the linear region, no action needed ---
	return ScalingDecision{
		DesiredReplicas: currentReplicas,
		ScaleDirection:  Hold,
		Reason: fmt.Sprintf(
			"Queue depth %d within bounds [%d, %d]. System in linear region. No action.",
			queueDepth, scaleInThreshold, theta,
		),
	}
}

// clamp ensures the desired replica count stays within configured bounds.
func (s *QueueDepthScaler) clamp(desired int32) int32 {
	if desired < s.cfg.MinReplicas {
		return s.cfg.MinReplicas
	}
	if desired > s.cfg.MaxReplicas {
		return s.cfg.MaxReplicas
	}
	return desired
}

// LittlesLawEstimate returns the theoretical mean wait time W given
// observed L and λ. Useful for SLO reporting and verification.
//
// Little's Law: W = L / λ
// This is distribution-agnostic — it holds for any stable G/G/1 queue.
func LittlesLawEstimate(queueDepth float64, arrivalRateRPS float64) float64 {
	if arrivalRateRPS <= 0 {
		return 0
	}
	return queueDepth / arrivalRateRPS // seconds
}

// UtilisationRho estimates server utilisation ρ = λ / μ
// where μ is derived from observed throughput at baseline (1/serviceTime).
// ρ approaching 1.0 means the system is near saturation (the Knee Point).
func UtilisationRho(arrivalRateRPS float64, meanServiceTimeSec float64) float64 {
	if meanServiceTimeSec <= 0 {
		return 0
	}
	mu := 1.0 / meanServiceTimeSec // service rate
	return arrivalRateRPS / mu
}
