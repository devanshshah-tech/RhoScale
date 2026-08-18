package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/devanshu/controller/pkg/controller"
	"github.com/devanshu/controller/pkg/metrics"
	"github.com/devanshu/controller/pkg/scaler"
)

// Config holds all tunable parameters for the controller.
// These are the θ (theta) values calibrated from the Weeks 3-4 baseline experiments.
type Config struct {
	// Kubernetes target
	Namespace      string
	DeploymentName string

	// Prometheus scrape endpoint
	PrometheusAddr string

	// Queueing-theory-derived thresholds (calibrated from Knee Point data)
	// θ_queue: queue depth at which scale-out is triggered (vllm:num_requests_waiting)
	QueueDepthThreshold int
	// θ_util: KV-cache utilisation % above which scale-out is urgent
	KVCacheThresholdPct float64

	// MAPE-K loop timing
	ScrapeInterval    time.Duration // How often to poll Prometheus (Monitor phase)
	CooldownPeriod    time.Duration // Min time between consecutive scale actions (hysteresis)
	ConfirmationTicks int           // Consecutive ticks above threshold before acting (noise filter)

	// Replica bounds
	MinReplicas int64
	MaxReplicas int64

	// Stub / logging mode
	LogOutput string
}

func main() {
	cfg := &Config{}

	flag.StringVar(&cfg.Namespace, "namespace", "default", "Kubernetes namespace of the vLLM deployment")
	flag.StringVar(&cfg.DeploymentName, "deployment", "vllm-inference", "Name of the vLLM Deployment to scale")
	flag.StringVar(&cfg.PrometheusAddr, "prometheus", "http://prometheus:9090", "Prometheus server address")
	flag.IntVar(&cfg.QueueDepthThreshold, "queue-threshold", 5, "Queue depth (num_requests_waiting) that triggers scale-out")
	flag.Float64Var(&cfg.KVCacheThresholdPct, "kvcache-threshold", 0.80, "KV-cache utilisation fraction that triggers scale-out")
	flag.DurationVar(&cfg.ScrapeInterval, "scrape-interval", 10*time.Second, "How often to poll Prometheus")
	flag.DurationVar(&cfg.CooldownPeriod, "cooldown", 120*time.Second, "Minimum time between scale actions")
	flag.IntVar(&cfg.ConfirmationTicks, "confirmation-ticks", 2, "Consecutive ticks above threshold before scaling")
	flag.Int64Var(&cfg.MinReplicas, "min-replicas", 1, "Minimum replica count")
	flag.Int64Var(&cfg.MaxReplicas, "max-replicas", 10, "Maximum replica count")
	flag.StringVar(&cfg.LogOutput, "log-output", "", "Path for decision log (enables stub/K8s-less mode)")
	flag.Parse()

	logger, _ := zap.NewProduction()
	defer logger.Sync()

	logger.Info("vLLM Autoscaler Controller starting",
		zap.String("deployment", cfg.DeploymentName),
		zap.String("namespace", cfg.Namespace),
		zap.Int("queueThreshold", cfg.QueueDepthThreshold),
		zap.Float64("kvCacheThreshold", cfg.KVCacheThresholdPct),
		zap.Duration("cooldown", cfg.CooldownPeriod),
	)

	// Stub mode: log decisions to JSONL, no Kubernetes cluster needed.
	// Production mode: use real Kubernetes API.
	var exec *controller.LoggingExecutor
	var k8sClient kubernetes.Interface

	if cfg.LogOutput != "" {
		var err error
		exec, err = controller.NewLoggingExecutor(cfg.LogOutput, logger)
		if err != nil {
			logger.Fatal("Cannot create decision log", zap.Error(err))
		}
		defer exec.Close()
		logger.Info("Stub mode enabled — scaling decisions written to JSONL", zap.String("path", cfg.LogOutput))
	} else {
		k8sClient = buildK8sClient(logger)
	}

	promClient := metrics.NewPrometheusClient(cfg.PrometheusAddr, logger)

	scalingLogic := scaler.NewQueueDepthScaler(scaler.ScalerConfig{
		QueueDepthThreshold: cfg.QueueDepthThreshold,
		KVCacheThreshold:    cfg.KVCacheThresholdPct,
		MinReplicas:         int32(cfg.MinReplicas),
		MaxReplicas:         int32(cfg.MaxReplicas),
	}, logger)

	ctrl := controller.New(controller.ControllerConfig{
		Namespace:         cfg.Namespace,
		DeploymentName:    cfg.DeploymentName,
		ScrapeInterval:    cfg.ScrapeInterval,
		CooldownPeriod:    cfg.CooldownPeriod,
		ConfirmationTicks: cfg.ConfirmationTicks,
	}, k8sClient, promClient, scalingLogic, exec, logger)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logger.Info("Controller running. Press Ctrl+C to stop.")
	if err := ctrl.Run(ctx); err != nil {
		logger.Fatal("Controller exited with error", zap.Error(err))
	}
	logger.Info("Controller shut down cleanly.")
}

func buildK8sClient(logger *zap.Logger) kubernetes.Interface {
	// Try in-cluster config first (when running inside Kubernetes pod)
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Fall back to kubeconfig file (local development)
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = os.Getenv("HOME") + "/.kube/config"
		}
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			logger.Fatal("Cannot build Kubernetes client config", zap.Error(err))
		}
		logger.Info("Using kubeconfig (local dev mode)", zap.String("path", kubeconfig))
	} else {
		logger.Info("Using in-cluster Kubernetes config")
	}

	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		logger.Fatal("Cannot create Kubernetes client", zap.Error(err))
	}
	return client
}
