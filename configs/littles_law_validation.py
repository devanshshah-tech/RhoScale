#!/usr/bin/env python3
"""
littles_law_validation.py — Validate Little's Law (W = L/λ) using Poisson traffic.

Runs Poisson traffic at a given λ, captures Prometheus queue depth during the run,
and compares predicted latency (W = L/λ) vs observed latency from the generator.

Usage:
    python3 littles_law_validation.py --lambda 5 --duration 120 --prometheus http://localhost:9090
"""

import argparse
import json
import math
import random
import sys
import time
import threading
from datetime import datetime

try:
    import requests
except ImportError:
    print("ERROR: requests library not found. Install with: pip install requests")
    sys.exit(1)


def exponential_interarrival(lambda_rate):
    """Sample inter-arrival time from exponential distribution."""
    u = random.random()
    while u == 0:
        u = random.random()
    return -math.log(u) / lambda_rate


def send_request(url, payload, timeout=300):
    """Send a single request and return (latency_sec, status_code)."""
    start = time.time()
    try:
        resp = requests.post(url, json=payload, timeout=timeout)
        latency = time.time() - start
        return latency, resp.status_code
    except requests.exceptions.Timeout:
        latency = time.time() - start
        return latency, 408
    except requests.exceptions.ConnectionError:
        latency = time.time() - start
        return latency, 0
    except Exception as e:
        latency = time.time() - start
        return latency, 0


def capture_queue_depth(prometheus_url, interval_sec, duration_sec, results):
    """Capture queue depth and running requests from Prometheus at regular intervals."""
    start_time = time.time()
    while time.time() - start_time < duration_sec:
        try:
            # Get waiting requests
            resp_waiting = requests.get(
                f"{prometheus_url}/api/v1/query",
                params={"query": "vllm:num_requests_waiting"},
                timeout=5
            )
            # Get running requests
            resp_running = requests.get(
                f"{prometheus_url}/api/v1/query",
                params={"query": "vllm:num_requests_running"},
                timeout=5
            )
            
            waiting = 0
            running = 0
            
            if resp_waiting.status_code == 200:
                data = resp_waiting.json()
                if data["status"] == "success" and data["data"]["result"]:
                    waiting = float(data["data"]["result"][0]["value"][1])
            
            if resp_running.status_code == 200:
                data = resp_running.json()
                if data["status"] == "success" and data["data"]["result"]:
                    running = float(data["data"]["result"][0]["value"][1])
            
            timestamp = time.time() - start_time
            results.append({
                "timestamp": timestamp,
                "queue_depth": waiting,
                "running": running,
                "total_in_system": waiting + running
            })
        except Exception as e:
            pass
        time.sleep(interval_sec)


def run_poisson_with_monitoring(url, payload, lambda_rate, duration_sec, prometheus_url, scrape_interval=2):
    """Run Poisson traffic while monitoring queue depth."""
    latencies = []
    status_codes = []
    timestamps = []
    queue_depth_samples = []
    lock = threading.Lock()
    start_time = time.time()

    # Start Prometheus monitoring thread
    monitor_thread = threading.Thread(
        target=capture_queue_depth,
        args=(prometheus_url, scrape_interval, duration_sec, queue_depth_samples)
    )
    monitor_thread.start()

    def send_and_record(request_id, scheduled_time):
        latency, status = send_request(url, payload)
        with lock:
            latencies.append(latency)
            status_codes.append(status)
            timestamps.append(scheduled_time - start_time)

    threads = []
    next_arrival = time.time() + exponential_interarrival(lambda_rate)
    request_id = 0

    print(f"  Lambda: {lambda_rate} req/s, Duration: {duration_sec}s")
    print(f"  Expected requests: ~{lambda_rate * duration_sec:.0f}")
    print(f"  Monitoring queue depth every {scrape_interval}s...")

    while time.time() - start_time < duration_sec:
        now = time.time()

        if now >= next_arrival:
            scheduled_time = now
            t = threading.Thread(target=send_and_record, args=(request_id, scheduled_time))
            t.start()
            threads.append(t)
            request_id += 1

            next_arrival = now + exponential_interarrival(lambda_rate)
        else:
            time.sleep(min(0.001, next_arrival - now))

    # Wait for all threads
    for t in threads:
        t.join(timeout=300)

    # Wait for monitor thread
    monitor_thread.join(timeout=10)

    # Compute statistics
    total = len(latencies)
    if total == 0:
        return None

    latencies_sorted = sorted(latencies)
    p50 = latencies_sorted[int(total * 0.50)]
    p95 = latencies_sorted[min(int(total * 0.95), total - 1)]
    p99 = latencies_sorted[min(int(total * 0.99), total - 1)]
    avg_latency = sum(latencies) / total

    # Queue depth statistics
    queue_depths = [s["queue_depth"] for s in queue_depth_samples]
    running_requests = [s["running"] for s in queue_depth_samples]
    total_in_system = [s["total_in_system"] for s in queue_depth_samples]
    
    avg_queue_depth = sum(queue_depths) / len(queue_depths) if queue_depths else 0
    avg_running = sum(running_requests) / len(running_requests) if running_requests else 0
    avg_total = sum(total_in_system) / len(total_in_system) if total_in_system else 0
    max_queue_depth = max(queue_depths) if queue_depths else 0
    max_total = max(total_in_system) if total_in_system else 0

    # Little's Law validation
    # For the full system: W = L_total / λ
    # For the queue only: W_queue = L_queue / λ
    predicted_W_total = avg_total / lambda_rate if lambda_rate > 0 else 0
    predicted_W_queue = avg_queue_depth / lambda_rate if lambda_rate > 0 else 0

    elapsed = time.time() - start_time
    actual_rps = total / elapsed if elapsed > 0 else 0

    return {
        "lambda": lambda_rate,
        "duration_sec": round(elapsed, 2),
        "total_requests": total,
        "actual_rps": round(actual_rps, 2),
        "avg_latency_ms": round(avg_latency * 1000, 1),
        "p50_latency_ms": round(p50 * 1000, 1),
        "p95_latency_ms": round(p95 * 1000, 1),
        "p99_latency_ms": round(p99 * 1000, 1),
        "avg_queue_depth": round(avg_queue_depth, 2),
        "avg_running": round(avg_running, 2),
        "avg_total_in_system": round(avg_total, 2),
        "max_queue_depth": round(max_queue_depth, 2),
        "max_total_in_system": round(max_total, 2),
        "queue_depth_samples": len(queue_depth_samples),
        "predicted_W_total_ms": round(predicted_W_total * 1000, 1),
        "predicted_W_queue_ms": round(predicted_W_queue * 1000, 1),
        "observed_W_ms": round(avg_latency * 1000, 1),
        "error_total_pct": round(abs(predicted_W_total - avg_latency) / avg_latency * 100, 1) if avg_latency > 0 else 0,
        "error_queue_pct": round(abs(predicted_W_queue - avg_latency) / avg_latency * 100, 1) if avg_latency > 0 else 0,
    }


def main():
    parser = argparse.ArgumentParser(description="Little's Law validation using Poisson traffic")
    parser.add_argument("--url", default="http://localhost:8000/v1/chat/completions",
                        help="vLLM endpoint URL")
    parser.add_argument("--prometheus", default="http://localhost:9090",
                        help="Prometheus URL")
    parser.add_argument("--model", default="mlx-community/Qwen2.5-3B-Instruct-4bit",
                        help="Model name")
    parser.add_argument("--prompt", default="Explain Little Law in queueing theory in exactly 100 words.",
                        help="User prompt")
    parser.add_argument("--max-tokens", type=int, default=100)
    parser.add_argument("--temperature", type=float, default=0)
    parser.add_argument("--lambda", type=float, dest="lambda_rate", required=True,
                        help="Arrival rate (req/s)")
    parser.add_argument("--duration", type=int, default=120,
                        help="Duration (seconds)")
    parser.add_argument("--scrape-interval", type=float, default=2,
                        help="Prometheus scrape interval (seconds)")
    parser.add_argument("--output-dir", default="results")
    parser.add_argument("--timestamp", default=None)

    args = parser.parse_args()

    if args.timestamp is None:
        args.timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")

    payload = {
        "model": args.model,
        "messages": [{"role": "user", "content": args.prompt}],
        "max_tokens": args.max_tokens,
        "temperature": args.temperature,
    }

    print("=== Little's Law Validation — CSC 630 ===")
    print(f"=== Timestamp: {args.timestamp} ===")
    print()

    # Health check
    print("Checking vllm-mlx server health...")
    try:
        health = requests.get(args.url.replace("/v1/chat/completions", "/health"), timeout=5)
        if health.status_code == 200:
            print("Server is healthy.")
        else:
            print(f"WARNING: Server returned status {health.status_code}")
    except Exception as e:
        print(f"ERROR: Cannot reach server: {e}")
        sys.exit(1)

    # Check Prometheus
    print("Checking Prometheus...")
    try:
        prom_health = requests.get(f"{args.prometheus}/-/healthy", timeout=5)
        if prom_health.status_code == 200:
            print("Prometheus is healthy.")
        else:
            print(f"WARNING: Prometheus returned status {prom_health.status_code}")
    except Exception as e:
        print(f"ERROR: Cannot reach Prometheus: {e}")
        sys.exit(1)
    print()

    # Run Poisson traffic with monitoring
    print("Running Poisson traffic with queue depth monitoring...")
    result = run_poisson_with_monitoring(
        args.url, payload, args.lambda_rate, args.duration,
        args.prometheus, args.scrape_interval
    )

    if result is None:
        print("ERROR: No requests completed")
        sys.exit(1)

    # Print results
    print()
    print("=============================================")
    print("  LITTLE'S LAW VALIDATION RESULTS")
    print("=============================================")
    print()
    print(f"Arrival rate (λ):        {result['lambda']} req/s")
    print(f"Duration:                {result['duration_sec']}s")
    print(f"Total requests:          {result['total_requests']}")
    print(f"Actual throughput:       {result['actual_rps']} req/s")
    print()
    print(f"Queue Depth (from Prometheus):")
    print(f"  Average L_queue:       {result['avg_queue_depth']}")
    print(f"  Average L_running:     {result['avg_running']}")
    print(f"  Average L_total:       {result['avg_total_in_system']}")
    print(f"  Maximum L_queue:       {result['max_queue_depth']}")
    print(f"  Maximum L_total:       {result['max_total_in_system']}")
    print(f"  Samples:               {result['queue_depth_samples']}")
    print()
    print(f"Latency (observed):")
    print(f"  Average W:             {result['observed_W_ms']} ms")
    print(f"  P50:                   {result['p50_latency_ms']} ms")
    print(f"  P95:                   {result['p95_latency_ms']} ms")
    print(f"  P99:                   {result['p99_latency_ms']} ms")
    print()
    print(f"Little's Law Predictions (W = L/λ):")
    print(f"  Predicted W (total):   {result['predicted_W_total_ms']} ms  (using L_total)")
    print(f"  Predicted W (queue):   {result['predicted_W_queue_ms']} ms  (using L_queue only)")
    print(f"  Observed W:            {result['observed_W_ms']} ms")
    print()
    print(f"Error Analysis:")
    print(f"  Error (total L):       {result['error_total_pct']}%")
    print(f"  Error (queue L only):  {result['error_queue_pct']}%")
    print()
    print("Interpretation:")
    if result['error_total_pct'] < result['error_queue_pct']:
        print("  ✓ Using L_total gives better prediction than L_queue alone")
        print("  ✓ This confirms Little's Law applies to the full system (queue + service)")
    else:
        print("  ⚠ L_queue alone gives similar or better prediction")
        print("  ⚠ This may indicate service time is negligible compared to queue time")
    print()
    print("=============================================")

    # Save results
    import os
    os.makedirs(args.output_dir, exist_ok=True)
    results_file = os.path.join(args.output_dir, f"littles_law_{args.timestamp}.json")
    with open(results_file, "w") as f:
        json.dump(result, f, indent=2)
    print(f"Results saved to: {results_file}")


if __name__ == "__main__":
    main()
