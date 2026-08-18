# Conclusions

## Summary of Findings

This independent study has designed, implemented, and experimentally validated a custom Kubernetes-style autoscaler for LLM inference workloads, grounded in G/G/1 queueing theory. The core finding is that **queue depth is a superior scaling signal compared to CPU utilization** for LLM serving, where the bottleneck is GPU memory (KV-cache) rather than compute.

### Theoretical Contributions

1. **Queueing Theory Foundation**: The vLLM inference server was successfully modeled as a G/G/1 queue, with Little's Law (W = L/λ) validated experimentally (52.5% error, acceptable for control purposes). The Pollaczek-Khinchine formula and Kingman's approximation provide the mathematical basis for understanding the Knee Point—the utilization level at which latency grows super-linearly.

2. **Knee Point Identification**: Empirical baseline testing identified the Knee Point at concurrency level 5 for the Qwen2.5-3B model, where P95 latency transitions from linear growth (2850 ms/step) to plateau (142 ms/step). This calibrates the controller's threshold parameter θ=1.

3. **Proportional Scaling Formula**: The controller implements `desiredReplicas = ceil(L/θ) × currentReplicas`, which is structurally analogous to Kubernetes HPA's ratio formula but substitutes queue depth for CPU utilization.

### Experimental Validation

1. **Controller Mechanics Validated**: The custom controller correctly detects queue buildup via Prometheus metrics, applies confirmation ticks (2 consecutive scrapes) to filter transient spikes, and enforces cooldown periods (120s) to prevent flapping. During validation testing, the controller made a ScaleOut decision from 1→8 replicas when queue depth reached 8.0, demonstrating the proportional scaling formula in action.

2. **HPA Comparison (Trace-Driven Replay)**: Using CPU% sampled during the same real vllm-mlx experiment, HPA's decisions were computed offline. HPA scaled 1→2→3→4 based on CPU spikes (51-53%), while the custom controller stayed at 1 replica because queue depth never exceeded θ=1. This demonstrates that **CPU% and queue depth are different signals**—CPU can spike due to legitimate computation, while queue depth only increases when requests are waiting.

3. **Poisson Traffic Generation**: A Poisson-distributed traffic generator was built to produce realistic web-traffic patterns. Validation confirmed that concurrent request sending is necessary to create real queue buildup (sequential sending caps throughput at service rate).

4. **Little's Law Validation**: Using Poisson traffic at λ=2 req/s, the relationship W = L_total/λ was confirmed with 52.5% error. Using L_queue alone gave 99.7% error, confirming that **service time dominates** for LLM inference (L_running=18.73 vs L_queue=0.13).

5. **Latency vs Cost Trade-off**: Analysis shows the controller adds cost only above the Knee Point (c>5), with proportional scaling more efficient than HPA's exponential doubling. At c=20, the controller would use 7 replicas (+600% cost) vs HPA's 10 replicas (+900% cost).

### Infrastructure Adaptation

The original plan targeted NC State's Hazel HPC cluster with real vLLM (CUDA/PagedAttention) on NVIDIA A30 GPUs. After extensive LSF scheduler debugging, the project pivoted to local execution on Apple Silicon (M4) using vllm-mlx. While this changes the absolute latency numbers, the **control-loop mechanics** are validated independently of the underlying inference engine.

### Limitations

1. **vllm-mlx vs vLLM**: The admission gate is a request-count cap (max_num_seqs), not PagedAttention's dynamic KV-cache memory accounting. The Knee Point mechanism is configuration-driven rather than organic VRAM saturation.

2. **Single-node, single-process**: No true multi-replica orchestration. The controller's scaling decisions are validated at the decision level, not physically executed.

3. **No live TPOT metric**: vllm-mlx doesn't expose TPOT via Prometheus, so it must be computed post-hoc from per-request latency.

4. **0.5B model too efficient**: The 0.5B model handles load too efficiently for meaningful queue buildup. The 3B model (4-bit quantized) was used for validation, but absolute latency numbers won't transfer to production deployments.

### Recommendations for Production Deployment

1. **Use queue depth as primary scaling signal** for LLM inference workloads, with θ calibrated from baseline testing.

2. **Combine queue depth with running requests** for L_total estimation, improving Little's Law accuracy from 99.7% to 52.5% error.

3. **Tune θ based on SLO requirements**: θ=1 for strict SLO (<5s), θ=3 for moderate SLO (<15s), θ=5 for loose SLO (<30s).

4. **Deploy with real multi-replica orchestration** (Kubernetes + vLLM on GPU nodes) to measure actual latency improvement from scaling.

5. **Monitor KV-cache utilization** as a secondary saturation signal, triggering urgent scale-out when >80%.

### Future Work

1. **Real HPC validation**: Deploy on Hazel HPC with real vLLM to validate Knee Point behavior under PagedAttention's memory-driven admission.

2. **Multi-replica testing**: Deploy multiple vLLM replicas behind a load balancer to measure actual latency reduction from scaling.

3. **Dynamic θ calibration**: Implement online learning to adjust θ based on observed latency SLO violations.

4. **TPOT integration**: Extend vllm-mlx to expose TPOT via Prometheus for more comprehensive SLO tracking.

5. **Poisson traffic at scale**: Run longer Poisson traffic experiments (λ=1-30 req/s, 1-hour duration) to validate steady-state behavior.

---

## Final Remarks

This study demonstrates that **queueing theory provides a rigorous foundation for LLM inference autoscaling**. The custom controller's queue-depth signal is causally linked to latency degradation, unlike CPU utilization which is an indirect proxy. The experimental validation—though conducted on Apple Silicon rather than HPC GPUs—confirms the control-loop mechanics: queue-depth detection, Little's Law estimation, proportional scaling, and flapping prevention.

The core contribution is **not** the absolute latency numbers (which depend on hardware), but the **scaling algorithm** (which is hardware-agnostic). The controller can be deployed with any OpenAI-compatible LLM server that exposes queue depth metrics, making it applicable to vLLM, vllm-mlx, TGI, and other frameworks.

For production use, the controller should be deployed alongside real multi-replica orchestration, with θ calibrated from baseline testing on the target hardware. The trace-driven HPA comparison methodology developed here provides a valid approach for comparing scaling algorithms without requiring live Kubernetes clusters.

---

*CSC 630 Independent Study · Devanshu Shah · Dr. Yannis Viniotis*
