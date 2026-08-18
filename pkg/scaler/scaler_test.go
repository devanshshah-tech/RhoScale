package scaler

import (
	"testing"

	"github.com/devanshu/controller/pkg/metrics"
)

func TestAnalyze_KVCacheUrgent(t *testing.T) {
	s := NewQueueDepthScaler(ScalerConfig{
		QueueDepthThreshold: 5,
		KVCacheThreshold:    0.80,
		MinReplicas:         1,
		MaxReplicas:         10,
	}, nil)

	m := &metrics.InferenceMetrics{
		QueueDepth:           2,
		KVCacheUsageFraction: 0.85,
	}

	decision := s.Analyze(m, 2)

	if decision.ScaleDirection != ScaleOut {
		t.Errorf("expected ScaleOut, got %s", decision.ScaleDirection)
	}
	if decision.DesiredReplicas != 3 {
		t.Errorf("expected desired=3 (current+1), got %d", decision.DesiredReplicas)
	}
}

func TestAnalyze_QueueDepthPastKneePoint(t *testing.T) {
	s := NewQueueDepthScaler(ScalerConfig{
		QueueDepthThreshold: 5,
		KVCacheThreshold:    0.80,
		MinReplicas:         1,
		MaxReplicas:         10,
	}, nil)

	m := &metrics.InferenceMetrics{
		QueueDepth:           10,
		KVCacheUsageFraction: 0.30,
	}

	decision := s.Analyze(m, 2)

	if decision.ScaleDirection != ScaleOut {
		t.Errorf("expected ScaleOut, got %s", decision.ScaleDirection)
	}
	// ceil(10/5) * 2 = 4
	if decision.DesiredReplicas != 4 {
		t.Errorf("expected desired=4, got %d", decision.DesiredReplicas)
	}
}

func TestAnalyze_QueueDepthAtKneePointBoundary(t *testing.T) {
	s := NewQueueDepthScaler(ScalerConfig{
		QueueDepthThreshold: 5,
		KVCacheThreshold:    0.80,
		MinReplicas:         1,
		MaxReplicas:         10,
	}, nil)

	m := &metrics.InferenceMetrics{
		QueueDepth:           5,
		KVCacheUsageFraction: 0.30,
	}

	decision := s.Analyze(m, 2)

	if decision.ScaleDirection != Hold {
		t.Errorf("expected Hold at boundary (queue=theta), got %s", decision.ScaleDirection)
	}
}

func TestAnalyze_NormalRegion(t *testing.T) {
	s := NewQueueDepthScaler(ScalerConfig{
		QueueDepthThreshold: 5,
		KVCacheThreshold:    0.80,
		MinReplicas:         1,
		MaxReplicas:         10,
	}, nil)

	m := &metrics.InferenceMetrics{
		QueueDepth:           3,
		KVCacheUsageFraction: 0.30,
	}

	decision := s.Analyze(m, 2)

	if decision.ScaleDirection != Hold {
		t.Errorf("expected Hold, got %s", decision.ScaleDirection)
	}
	if decision.DesiredReplicas != 2 {
		t.Errorf("expected desired=current=2, got %d", decision.DesiredReplicas)
	}
}

func TestAnalyze_ScaleIn(t *testing.T) {
	s := NewQueueDepthScaler(ScalerConfig{
		QueueDepthThreshold: 8,
		KVCacheThreshold:    0.80,
		MinReplicas:         1,
		MaxReplicas:         10,
	}, nil)

	m := &metrics.InferenceMetrics{
		QueueDepth:           1,
		KVCacheUsageFraction: 0.30,
	}

	decision := s.Analyze(m, 3)

	if decision.ScaleDirection != ScaleIn {
		t.Errorf("expected ScaleIn, got %s", decision.ScaleDirection)
	}
	if decision.DesiredReplicas != 2 {
		t.Errorf("expected desired=2 (current-1), got %d", decision.DesiredReplicas)
	}
}

func TestAnalyze_MinReplicasClamp(t *testing.T) {
	s := NewQueueDepthScaler(ScalerConfig{
		QueueDepthThreshold: 8,
		KVCacheThreshold:    0.80,
		MinReplicas:         2,
		MaxReplicas:         10,
	}, nil)

	m := &metrics.InferenceMetrics{
		QueueDepth:           1,
		KVCacheUsageFraction: 0.30,
	}

	decision := s.Analyze(m, 3)

	if decision.ScaleDirection != ScaleIn {
		t.Errorf("expected ScaleIn, got %s", decision.ScaleDirection)
	}
	if decision.DesiredReplicas != 2 {
		t.Errorf("expected desired=2 (min clamped from 3→2), got %d", decision.DesiredReplicas)
	}
}

func TestAnalyze_MaxReplicasClamp(t *testing.T) {
	s := NewQueueDepthScaler(ScalerConfig{
		QueueDepthThreshold: 5,
		KVCacheThreshold:    0.80,
		MinReplicas:         1,
		MaxReplicas:         5,
	}, nil)

	m := &metrics.InferenceMetrics{
		QueueDepth:           100,
		KVCacheUsageFraction: 0.30,
	}

	decision := s.Analyze(m, 4)

	if decision.ScaleDirection != ScaleOut {
		t.Errorf("expected ScaleOut, got %s", decision.ScaleDirection)
	}
	if decision.DesiredReplicas != 5 {
		t.Errorf("expected desired=5 (max clamped), got %d", decision.DesiredReplicas)
	}
}

func TestAnalyze_KVCacheTakesPrecedence(t *testing.T) {
	s := NewQueueDepthScaler(ScalerConfig{
		QueueDepthThreshold: 5,
		KVCacheThreshold:    0.80,
		MinReplicas:         1,
		MaxReplicas:         10,
	}, nil)

	m := &metrics.InferenceMetrics{
		QueueDepth:           1,
		KVCacheUsageFraction: 0.90,
	}

	decision := s.Analyze(m, 2)

	if decision.ScaleDirection != ScaleOut {
		t.Errorf("expected ScaleOut (KV-cache urgent), got %s", decision.ScaleDirection)
	}
	if decision.DesiredReplicas != 3 {
		t.Errorf("expected desired=3 (current+1), got %d", decision.DesiredReplicas)
	}
}

func TestLittlesLawEstimate(t *testing.T) {
	w := LittlesLawEstimate(10, 2)
	if w != 5.0 {
		t.Errorf("expected 5.0, got %f", w)
	}
}

func TestLittlesLawEstimate_ZeroArrivalRate(t *testing.T) {
	w := LittlesLawEstimate(10, 0)
	if w != 0 {
		t.Errorf("expected 0 for zero arrival rate, got %f", w)
	}
}

func TestUtilisationRho(t *testing.T) {
	rho := UtilisationRho(10, 0.1)
	if rho != 1.0 {
		t.Errorf("expected 1.0, got %f", rho)
	}
}

func TestDirection_String(t *testing.T) {
	if Hold.String() != "Hold" {
		t.Errorf("expected Hold, got %s", Hold.String())
	}
	if ScaleOut.String() != "ScaleOut" {
		t.Errorf("expected ScaleOut, got %s", ScaleOut.String())
	}
	if ScaleIn.String() != "ScaleIn" {
		t.Errorf("expected ScaleIn, got %s", ScaleIn.String())
	}
}
