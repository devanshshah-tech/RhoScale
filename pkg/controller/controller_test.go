package controller

import (
	"context"
	"testing"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/devanshu/controller/pkg/metrics"
	"github.com/devanshu/controller/pkg/scaler"
)

var testLogger = zap.NewNop()

type mockScraper struct {
	metrics *metrics.InferenceMetrics
	err     error
}

func (m *mockScraper) Scrape(ctx context.Context) (*metrics.InferenceMetrics, error) {
	return m.metrics, m.err
}

type mockAnalyzer struct {
	decision scaler.ScalingDecision
}

func (m *mockAnalyzer) Analyze(metrics *metrics.InferenceMetrics, currentReplicas int32) scaler.ScalingDecision {
	return m.decision
}

type mockAnalyzerFunc struct {
	fn func(*metrics.InferenceMetrics, int32) scaler.ScalingDecision
}

func (m *mockAnalyzerFunc) Analyze(metrics *metrics.InferenceMetrics, currentReplicas int32) scaler.ScalingDecision {
	return m.fn(metrics, currentReplicas)
}

func int32Ptr(i int32) *int32 { return &i }

func makeDeployment(ns, name string, replicas int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32Ptr(replicas),
		},
	}
}

func TestReconcile_HoldSignal(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 2))
	scraper := &mockScraper{metrics: &metrics.InferenceMetrics{QueueDepth: 3}}
	analyzer := &mockAnalyzer{decision: scaler.ScalingDecision{
		DesiredReplicas: 2,
		ScaleDirection:  scaler.Hold,
		Reason:          "normal region",
	}}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 2,
	}, client, scraper, analyzer, nil, testLogger)

	err := ctrl.reconcile(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	deploy, _ := client.AppsV1().Deployments("default").Get(context.Background(), "vllm", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 2 {
		t.Errorf("expected replicas=2, got %d", *deploy.Spec.Replicas)
	}
}

func TestReconcile_ScaleOut_ConfirmationTicksMet(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 2))
	scraper := &mockScraper{metrics: &metrics.InferenceMetrics{QueueDepth: 10}}
	analyzer := &mockAnalyzer{decision: scaler.ScalingDecision{
		DesiredReplicas: 4,
		ScaleDirection:  scaler.ScaleOut,
		Reason:          "queue past knee",
	}}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 2,
	}, client, scraper, analyzer, nil, testLogger)

	// First tick: consecutiveTicks=1, still < 2 → skip (confirmation not met)
	err := ctrl.reconcile(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	// Second tick: consecutiveTicks=2, 2 >= 2 → scale happens
	err = ctrl.reconcile(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	deploy, _ := client.AppsV1().Deployments("default").Get(context.Background(), "vllm", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 4 {
		t.Errorf("expected replicas=4, got %d", *deploy.Spec.Replicas)
	}
}

func TestReconcile_ScaleOut_ConfirmationTicksNotMet(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 2))
	scraper := &mockScraper{metrics: &metrics.InferenceMetrics{QueueDepth: 10}}
	analyzer := &mockAnalyzer{decision: scaler.ScalingDecision{
		DesiredReplicas: 4,
		ScaleDirection:  scaler.ScaleOut,
		Reason:          "queue past knee",
	}}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 3,
	}, client, scraper, analyzer, nil, testLogger)

	for i := 0; i < 2; i++ {
		err := ctrl.reconcile(context.Background())
		if err != nil {
			t.Errorf("unexpected error on tick %d: %v", i+1, err)
		}
	}

	deploy, _ := client.AppsV1().Deployments("default").Get(context.Background(), "vllm", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 2 {
		t.Errorf("expected replicas=2 (no scale yet), got %d", *deploy.Spec.Replicas)
	}
}

func TestReconcile_ScaleIn_NoConfirmationTicksNeeded(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 3))
	scraper := &mockScraper{metrics: &metrics.InferenceMetrics{QueueDepth: 1}}
	analyzer := &mockAnalyzer{decision: scaler.ScalingDecision{
		DesiredReplicas: 2,
		ScaleDirection:  scaler.ScaleIn,
		Reason:          "over-provisioned",
	}}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 2,
	}, client, scraper, analyzer, nil, testLogger)

	err := ctrl.reconcile(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	deploy, _ := client.AppsV1().Deployments("default").Get(context.Background(), "vllm", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 2 {
		t.Errorf("expected replicas=2, got %d", *deploy.Spec.Replicas)
	}
}

func TestReconcile_CoolDownBlocksAction(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 2))
	scraper := &mockScraper{metrics: &metrics.InferenceMetrics{QueueDepth: 10}}
	analyzer := &mockAnalyzer{decision: scaler.ScalingDecision{
		DesiredReplicas: 4,
		ScaleDirection:  scaler.ScaleOut,
		Reason:          "queue past knee",
	}}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 1,
	}, client, scraper, analyzer, nil, testLogger)

	err := ctrl.reconcile(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	deploy, _ := client.AppsV1().Deployments("default").Get(context.Background(), "vllm", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 4 {
		t.Errorf("expected replicas=4 after first scale, got %d", *deploy.Spec.Replicas)
	}

	analyzer.decision.DesiredReplicas = 6
	err = ctrl.reconcile(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	deploy, _ = client.AppsV1().Deployments("default").Get(context.Background(), "vllm", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 4 {
		t.Errorf("expected replicas=4 (cooldown active), got %d", *deploy.Spec.Replicas)
	}
}

func TestReconcile_AlreadyAtDesired(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 4))
	scraper := &mockScraper{metrics: &metrics.InferenceMetrics{QueueDepth: 10}}
	analyzer := &mockAnalyzer{decision: scaler.ScalingDecision{
		DesiredReplicas: 4,
		ScaleDirection:  scaler.ScaleOut,
		Reason:          "queue past knee",
	}}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 1,
	}, client, scraper, analyzer, nil, testLogger)

	err := ctrl.reconcile(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	deploy, _ := client.AppsV1().Deployments("default").Get(context.Background(), "vllm", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 4 {
		t.Errorf("expected replicas=4 (already at desired), got %d", *deploy.Spec.Replicas)
	}
}

func TestReconcile_ScrapeError(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 2))
	scraper := &mockScraper{err: context.DeadlineExceeded}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 2,
	}, client, scraper, nil, nil, testLogger)

	err := ctrl.reconcile(context.Background())
	if err == nil {
		t.Error("expected error from scrape failure")
	}
}

func TestReconcile_AnalyzeCalledWithCurrentReplicas(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 5))
	scraper := &mockScraper{metrics: &metrics.InferenceMetrics{QueueDepth: 10}}

	var capturedReplicas int32
	analyzer := &mockAnalyzerFunc{fn: func(m *metrics.InferenceMetrics, currentReplicas int32) scaler.ScalingDecision {
		capturedReplicas = currentReplicas
		return scaler.ScalingDecision{
			DesiredReplicas: currentReplicas,
			ScaleDirection:  scaler.Hold,
			Reason:          "test",
		}
	}}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 1,
	}, client, scraper, analyzer, nil, testLogger)

	ctrl.reconcile(context.Background())

	if capturedReplicas != 5 {
		t.Errorf("expected analyzer called with currentReplicas=5, got %d", capturedReplicas)
	}
}

func TestReconcile_KVCacheUrgent_SubjectToConfirmationTicks(t *testing.T) {
	client := fake.NewSimpleClientset(makeDeployment("default", "vllm", 2))
	scraper := &mockScraper{metrics: &metrics.InferenceMetrics{
		QueueDepth:           2,
		KVCacheUsageFraction: 0.90,
	}}

	analyzer := &mockAnalyzerFunc{fn: func(m *metrics.InferenceMetrics, currentReplicas int32) scaler.ScalingDecision {
		if m.KVCacheUsageFraction >= 0.80 {
			return scaler.ScalingDecision{
				DesiredReplicas: currentReplicas + 1,
				ScaleDirection:  scaler.ScaleOut,
				Reason:          "kv-cache urgent",
			}
		}
		return scaler.ScalingDecision{
			DesiredReplicas: currentReplicas,
			ScaleDirection:  scaler.Hold,
			Reason:          "normal",
		}
	}}

	ctrl := New(ControllerConfig{
		Namespace:         "default",
		DeploymentName:    "vllm",
		ScrapeInterval:    10 * time.Second,
		CooldownPeriod:    120 * time.Second,
		ConfirmationTicks: 5,
	}, client, scraper, analyzer, nil, testLogger)

	// KV-cache urgent returns ScaleOut, which is subject to confirmation ticks like any
	// other scale-out — the controller does not special-case KV-cache urgency at the
	// execute layer. With ConfirmationTicks=5, scale fires on the 5th consecutive tick.
	for i := 0; i < 4; i++ {
		err := ctrl.reconcile(context.Background())
		if err != nil {
			t.Errorf("unexpected error on tick %d: %v", i+1, err)
		}
	}
	err := ctrl.reconcile(context.Background())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	deploy, _ := client.AppsV1().Deployments("default").Get(context.Background(), "vllm", metav1.GetOptions{})
	if *deploy.Spec.Replicas != 3 {
		t.Errorf("expected replicas=3 (KV-cache urgent after confirmation ticks), got %d", *deploy.Spec.Replicas)
	}
}
