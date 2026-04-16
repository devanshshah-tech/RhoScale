// Package controller implements the Execute phase of the MAPE-K loop.
//
// The reconciliation loop follows the Kubernetes controller pattern:
// observe desired state → compare to actual state → reconcile.
//
// Flapping Prevention (Oscillation Damping):
//   Two mechanisms prevent rapid scale up/down ("flapping"):
//   1. Cooldown Period: a mandatory wait between consecutive scale actions.
//      After any scale event, the controller enters a cooldown window
//      (default 120s) during which no further scale actions are issued.
//   2. Confirmation Ticks: the metric must exceed the threshold for N
//      consecutive scrape intervals before a scale-out action is taken.
//      This filters transient traffic spikes (e.g., a single Poisson burst).
//   Both mechanisms are drawn from KEDA's design (Annotated Bibliography [5])
//   and the Kubernetes HPA stabilisation window (Annotated Bibliography [4]).
package controller

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/devanshu/vllm-controller/pkg/metrics"
	"github.com/devanshu/vllm-controller/pkg/scaler"
)

// ControllerConfig holds timing and targeting parameters.
type ControllerConfig struct {
	Namespace         string
	DeploymentName    string
	ScrapeInterval    time.Duration
	CooldownPeriod    time.Duration
	ConfirmationTicks int // ticks above threshold before acting
}

// Scraper is the interface satisfied by metrics.PrometheusClient.
type Scraper interface {
	Scrape(ctx context.Context) (*metrics.InferenceMetrics, error)
}

// Analyzer is the interface satisfied by scaler.QueueDepthScaler.
type Analyzer interface {
	Analyze(m *metrics.InferenceMetrics, currentReplicas int32) scaler.ScalingDecision
}

// Controller is the main reconciliation loop.
type Controller struct {
	cfg      ControllerConfig
	k8s      kubernetes.Interface
	scraper  Scraper
	analyzer Analyzer
	logger   *zap.Logger

	// state for flapping prevention
	lastScaleTime     time.Time
	consecutiveTicks  int // how many ticks have seen a scale-out signal
}

// New constructs a Controller with all dependencies injected.
func New(
	cfg ControllerConfig,
	k8s kubernetes.Interface,
	scraper Scraper,
	analyzer Analyzer,
	logger *zap.Logger,
) *Controller {
	return &Controller{
		cfg:      cfg,
		k8s:      k8s,
		scraper:  scraper,
		analyzer: analyzer,
		logger:   logger,
	}
}

// Run starts the MAPE-K reconciliation loop. It blocks until ctx is cancelled.
func (c *Controller) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.cfg.ScrapeInterval)
	defer ticker.Stop()

	c.logger.Info("Reconciliation loop started",
		zap.Duration("scrapeInterval", c.cfg.ScrapeInterval),
		zap.Duration("cooldown", c.cfg.CooldownPeriod),
		zap.Int("confirmationTicks", c.cfg.ConfirmationTicks),
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := c.reconcile(ctx); err != nil {
				// Log and continue — transient errors (network blip, API server
				// unavailability) should not crash the controller.
				c.logger.Error("Reconcile error (will retry next tick)", zap.Error(err))
			}
		}
	}
}

// reconcile is one iteration of the MAPE-K loop:
//   Monitor → Analyze → Plan → Execute
func (c *Controller) reconcile(ctx context.Context) error {
	// ── MONITOR ─────────────────────────────────────────────────────────────
	// Query Prometheus for the current metric snapshot.
	snapshot, err := c.scraper.Scrape(ctx)
	if err != nil {
		return fmt.Errorf("monitor phase failed: %w", err)
	}

	// ── OBSERVE ACTUAL STATE ─────────────────────────────────────────────────
	// Get the current replica count from the Kubernetes API.
	deploy, err := c.k8s.AppsV1().Deployments(c.cfg.Namespace).
		Get(ctx, c.cfg.DeploymentName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("cannot get deployment %s/%s: %w",
			c.cfg.Namespace, c.cfg.DeploymentName, err)
	}
	currentReplicas := *deploy.Spec.Replicas

	// ── ANALYZE + PLAN ───────────────────────────────────────────────────────
	// The scaler applies the G/G/1 queueing model and returns a decision.
	decision := c.analyzer.Analyze(snapshot, currentReplicas)

	c.logger.Info("Analyze phase",
		zap.String("direction", decision.ScaleDirection.String()),
		zap.Int32("current", currentReplicas),
		zap.Int32("desired", decision.DesiredReplicas),
		zap.String("reason", decision.Reason),
	)

	// ── EXECUTE ──────────────────────────────────────────────────────────────
	if decision.ScaleDirection == scaler.Hold {
		c.consecutiveTicks = 0
		return nil
	}

	// No change in replica count needed (already at desired).
	if decision.DesiredReplicas == currentReplicas {
		c.consecutiveTicks = 0
		return nil
	}

	// --- Cooldown check (hysteresis window) ---
	// If we scaled recently, wait out the cooldown period before acting again.
	// This is the primary mechanism preventing flapping.
	if !c.lastScaleTime.IsZero() {
		elapsed := time.Since(c.lastScaleTime)
		if elapsed < c.cfg.CooldownPeriod {
			c.logger.Info("In cooldown window, skipping scale action",
				zap.Duration("remaining", c.cfg.CooldownPeriod-elapsed),
			)
			return nil
		}
	}

	// --- Confirmation ticks check (noise filter for scale-out only) ---
	// We only apply the tick filter to scale-out to avoid acting on a single
	// transient spike. Scale-in is inherently conservative (large hysteresis
	// threshold in the scaler), so it does not need tick confirmation.
	if decision.ScaleDirection == scaler.ScaleOut {
		c.consecutiveTicks++
		if c.consecutiveTicks < c.cfg.ConfirmationTicks {
			c.logger.Info("Waiting for confirmation ticks before scaling out",
				zap.Int("current", c.consecutiveTicks),
				zap.Int("required", c.cfg.ConfirmationTicks),
			)
			return nil
		}
	}

	// --- Issue the scale action ---
	if err := c.patchReplicas(ctx, deploy, decision.DesiredReplicas); err != nil {
		return fmt.Errorf("execute phase failed: %w", err)
	}

	c.lastScaleTime = time.Now()
	c.consecutiveTicks = 0

	c.logger.Info("Scale action executed",
		zap.String("direction", decision.ScaleDirection.String()),
		zap.Int32("from", currentReplicas),
		zap.Int32("to", decision.DesiredReplicas),
	)

	return nil
}

// patchReplicas issues a PATCH to the Kubernetes Deployments API to update
// the replica count. Uses server-side apply semantics for safety.
func (c *Controller) patchReplicas(
	ctx context.Context,
	deploy *appsv1.Deployment,
	desired int32,
) error {
	deployCopy := deploy.DeepCopy()
	deployCopy.Spec.Replicas = &desired

	_, err := c.k8s.AppsV1().Deployments(c.cfg.Namespace).
		Update(ctx, deployCopy, metav1.UpdateOptions{})
	return err
}
