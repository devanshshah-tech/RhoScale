// Package controller — stub.go
//
// LoggingExecutor replaces the real Kubernetes patchReplicas() call when
// running on Hazel HPC, where no Kubernetes API server exists.
//
// Instead of calling k8s.AppsV1().Deployments().Update(), it writes a
// structured JSON line to a log file for every scaling decision.
// This log file becomes the primary experimental dataset for Weeks 10-12.
//
// Usage: build the controller with the STUB build tag:
//
//	go build -tags stub -o controller-linux ./main.go
//
// Without the tag, the real Kubernetes client is used (production mode).
package controller

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/devanshu/controller/pkg/scaler"
)

// ScalingEvent is one JSON-serialisable line written to the log file.
// Each line represents a single scaling decision from the MAPE-K loop.
type ScalingEvent struct {
	Timestamp       string  `json:"ts"`
	Direction       string  `json:"direction"`
	FromReplicas    int32   `json:"from"`
	ToReplicas      int32   `json:"to"`
	Reason          string  `json:"reason"`
	QueueDepth      float64 `json:"queue_depth"`
	KVCacheFraction float64 `json:"kv_cache_fraction"`
	TTFT_P95_ms     float64 `json:"ttft_p95_ms"`
}

// LoggingExecutor writes scaling decisions to a JSONL file instead of
// calling the Kubernetes API. It is the "Execute" phase stub.
type LoggingExecutor struct {
	mu      sync.Mutex
	file    *os.File
	encoder *json.Encoder
	logger  *zap.Logger

	// simulated replica count — starts at 1, updated by each decision
	simulatedReplicas int32
}

// NewLoggingExecutor opens (or creates) the log file at the given path
// and returns a LoggingExecutor ready to record decisions.
func NewLoggingExecutor(logPath string, logger *zap.Logger) (*LoggingExecutor, error) {
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("cannot open decision log %s: %w", logPath, err)
	}
	return &LoggingExecutor{
		file:              f,
		encoder:           json.NewEncoder(f),
		logger:            logger,
		simulatedReplicas: 1,
	}, nil
}

// Close flushes and closes the underlying log file.
func (e *LoggingExecutor) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.file.Close()
}

// SimulatedReplicas returns the current simulated replica count.
// The reconcile loop calls this instead of querying the K8s API.
func (e *LoggingExecutor) SimulatedReplicas() int32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.simulatedReplicas
}

// Record writes a scaling decision as a JSON line and updates the
// simulated replica count.
func (e *LoggingExecutor) Record(
	decision scaler.ScalingDecision,
	currentReplicas int32,
	queueDepth float64,
	kvCache float64,
	ttftP95sec float64,
) {
	e.mu.Lock()
	defer e.mu.Unlock()

	event := ScalingEvent{
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Direction:       decision.ScaleDirection.String(),
		FromReplicas:    currentReplicas,
		ToReplicas:      decision.DesiredReplicas,
		Reason:          decision.Reason,
		QueueDepth:      queueDepth,
		KVCacheFraction: kvCache,
		TTFT_P95_ms:     ttftP95sec * 1000,
	}

	if err := e.encoder.Encode(event); err != nil {
		e.logger.Error("Failed to write decision log entry", zap.Error(err))
		return
	}

	if err := e.file.Sync(); err != nil {
		e.logger.Warn("Failed to sync decision log", zap.Error(err))
	}

	e.simulatedReplicas = decision.DesiredReplicas

	e.logger.Info("Decision logged",
		zap.String("direction", event.Direction),
		zap.Int32("from", event.FromReplicas),
		zap.Int32("to", event.ToReplicas),
		zap.Float64("queueDepth", event.QueueDepth),
	)
}

// ───────────────────────────────────────────────────────────────────
// How to wire LoggingExecutor into the Controller
// ───────────────────────────────────────────────────────────────────
//
// In controller.go, replace the patchReplicas() call with:
//
//   // At the top of Controller struct, add:
//   executor *LoggingExecutor   // nil when using real K8s
//
//   // In reconcile(), replace:
//   if err := c.patchReplicas(ctx, deploy, decision.DesiredReplicas); err != nil { ... }
//
//   // With:
//   if c.executor != nil {
//       // Hazel / stub mode — log the decision, no K8s call
//       c.executor.Record(decision, currentReplicas,
//           snapshot.QueueDepth, snapshot.KVCacheUsageFraction, snapshot.TTFT_P95_Seconds)
//   } else {
//       // Production mode — real Kubernetes API
//       if err := c.patchReplicas(ctx, deploy, decision.DesiredReplicas); err != nil {
//           return fmt.Errorf("execute phase failed: %w", err)
//       }
//   }
//
//   // In New(), add an optional logPath parameter:
//   if logPath != "" {
//       exec, err := NewLoggingExecutor(logPath, logger)
//       if err != nil { logger.Fatal("cannot create executor", zap.Error(err)) }
//       ctrl.executor = exec
//   }
//
// In main.go, add:
//   flag.StringVar(&cfg.LogOutput, "log-output", "", "Path for decision log (enables stub mode)")
//
// ───────────────────────────────────────────────────────────────────
// Output format — one JSON object per line (JSONL):
// ───────────────────────────────────────────────────────────────────
//
// {"ts":"2024-04-21T14:32:10Z","direction":"ScaleOut","from":1,"to":2,"reason":"Queue depth 8 > θ=5...","queue_depth":8,"kv_cache_fraction":0.42,"ttft_p95_ms":1240}
// {"ts":"2024-04-21T14:34:55Z","direction":"Hold","from":2,"to":2,"reason":"Queue depth 3 within bounds","queue_depth":3,"kv_cache_fraction":0.38,"ttft_p95_ms":480}
// {"ts":"2024-04-21T14:50:12Z","direction":"ScaleIn","from":2,"to":1,"reason":"Queue depth 0 < scale-in threshold 1","queue_depth":0,"kv_cache_fraction":0.21,"ttft_p95_ms":210}
//
// Parse the log in Python for analysis:
//   import json, pandas as pd
//   df = pd.read_json("results/controller_20240421.log", lines=True)
//   df["ts"] = pd.to_datetime(df["ts"])
//   df.set_index("ts", inplace=True)
//   print(df[["direction","from","to","queue_depth","ttft_p95_ms"]])
