#!/usr/bin/env python3
"""
poisson_traffic.py — Poisson-distributed traffic generator for LLM inference testing.

Generates requests with inter-arrival times sampled from an exponential distribution:
    inter_arrival = -ln(U) / λ   where U ~ Uniform(0,1)

This produces a Poisson arrival process, which is more realistic than hey's
fixed-concurrency model for web traffic.

Requests are sent concurrently using threading — the inter-arrival time determines
when each request STARTS, not when the previous one finishes.

Usage:
    python3 poisson_traffic.py --lambda 5 --duration 120 --url http://localhost:8000/v1/chat/completions
    python3 poisson_traffic.py --lambda-min 1 --lambda-max 30 --step 5 --duration 120
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


def run_poisson_traffic(url, payload, lambda_rate, duration_sec, results_file=None):
    """
    Run Poisson traffic at a fixed arrival rate.

    Uses threading to send requests concurrently. The inter-arrival time
    determines when each request STARTS, not when the previous one finishes.

    Returns:
        dict with summary statistics
    """
    latencies = []
    status_codes = []
    timestamps = []
    lock = threading.Lock()
    start_time = time.time()

    print(f"  Lambda: {lambda_rate} req/s, Duration: {duration_sec}s")
    print(f"  Expected requests: ~{lambda_rate * duration_sec:.0f}")

    def send_and_record(request_id, scheduled_time):
        latency, status = send_request(url, payload)
        with lock:
            latencies.append(latency)
            status_codes.append(status)
            timestamps.append(scheduled_time - start_time)

    threads = []
    next_arrival = time.time() + exponential_interarrival(lambda_rate)
    request_id = 0

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

    # Wait for all threads to complete
    for t in threads:
        t.join(timeout=300)

    # Compute summary statistics
    total = len(latencies)
    if total == 0:
        return {"lambda": lambda_rate, "total": 0}

    latencies_sorted = sorted(latencies)
    p50 = latencies_sorted[int(total * 0.50)]
    p95 = latencies_sorted[min(int(total * 0.95), total - 1)]
    p99 = latencies_sorted[min(int(total * 0.99), total - 1)]
    avg_latency = sum(latencies) / total

    success_count = sum(1 for s in status_codes if s == 200)
    error_count = total - success_count

    elapsed = time.time() - start_time
    actual_rps = total / elapsed if elapsed > 0 else 0

    summary = {
        "lambda": lambda_rate,
        "duration_sec": round(elapsed, 2),
        "total_requests": total,
        "actual_rps": round(actual_rps, 2),
        "avg_latency_ms": round(avg_latency * 1000, 1),
        "p50_latency_ms": round(p50 * 1000, 1),
        "p95_latency_ms": round(p95 * 1000, 1),
        "p99_latency_ms": round(p99 * 1000, 1),
        "success_count": success_count,
        "error_count": error_count,
        "error_rate_pct": round(error_count / total * 100, 2) if total > 0 else 0,
    }

    # Write per-request log if requested
    if results_file:
        with open(results_file, "w") as f:
            for i, (ts, lat, status) in enumerate(zip(timestamps, latencies, status_codes)):
                f.write(json.dumps({
                    "request_id": i,
                    "timestamp_sec": round(ts, 3),
                    "latency_ms": round(lat * 1000, 1),
                    "status_code": status,
                }) + "\n")

    return summary


def main():
    parser = argparse.ArgumentParser(description="Poisson traffic generator for LLM inference")
    parser.add_argument("--url", default="http://localhost:8000/v1/chat/completions",
                        help="vLLM endpoint URL")
    parser.add_argument("--model", default="mlx-community/Qwen2.5-3B-Instruct-4bit",
                        help="Model name to use in the request")
    parser.add_argument("--prompt", default="Explain Little Law in queueing theory in exactly 100 words.",
                        help="User prompt content")
    parser.add_argument("--max-tokens", type=int, default=100,
                        help="Max tokens per request")
    parser.add_argument("--temperature", type=float, default=0,
                        help="Sampling temperature")

    # Traffic pattern
    traffic_group = parser.add_mutually_exclusive_group(required=True)
    traffic_group.add_argument("--lambda", type=float, dest="lambda_rate",
                               help="Fixed arrival rate (req/s)")
    traffic_group.add_argument("--lambda-min", type=float,
                               help="Minimum arrival rate for ramp (req/s)")

    parser.add_argument("--lambda-max", type=float, default=30,
                        help="Maximum arrival rate for ramp (req/s)")
    parser.add_argument("--step", type=float, default=5,
                        help="Step size for lambda ramp (req/s)")
    parser.add_argument("--duration", type=int, default=120,
                        help="Duration per lambda level (seconds)")
    parser.add_argument("--cooldown", type=int, default=30,
                        help="Cooldown between lambda levels (seconds)")

    parser.add_argument("--output-dir", default="results",
                        help="Directory for output files")
    parser.add_argument("--timestamp", default=None,
                        help="Timestamp suffix for output files")

    args = parser.parse_args()

    if args.timestamp is None:
        args.timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")

    # Build payload
    payload = {
        "model": args.model,
        "messages": [{"role": "user", "content": args.prompt}],
        "max_tokens": args.max_tokens,
        "temperature": args.temperature,
    }

    # Health check
    print("=== Poisson Traffic Generator — CSC 630 ===")
    print(f"=== Timestamp: {args.timestamp} ===")
    print()
    print("Checking vllm-mlx server health...")
    try:
        health = requests.get(args.url.replace("/v1/chat/completions", "/health"), timeout=5)
        if health.status_code == 200:
            print("Server is healthy. Starting traffic generation.")
        else:
            print(f"WARNING: Server returned status {health.status_code}")
    except Exception as e:
        print(f"ERROR: Cannot reach server at {args.url}: {e}")
        sys.exit(1)
    print()

    import os
    os.makedirs(args.output_dir, exist_ok=True)

    summary_rows = []

    if args.lambda_rate is not None:
        # Single lambda run
        results_file = os.path.join(args.output_dir, f"poisson_l{args.lambda_rate}_{args.timestamp}.jsonl")
        summary = run_poisson_traffic(args.url, payload, args.lambda_rate, args.duration, results_file)

        print(f"  Avg: {summary['avg_latency_ms']}ms  P50: {summary['p50_latency_ms']}ms  "
              f"P95: {summary['p95_latency_ms']}ms  P99: {summary['p99_latency_ms']}ms  "
              f"RPS: {summary['actual_rps']}  Errors: {summary['error_rate_pct']}%")
        print()

        summary_rows.append(summary)
    else:
        # Ramp run
        current_lambda = args.lambda_min
        while current_lambda <= args.lambda_max:
            print(f"─────────────────────────────────────────────")
            print(f"  Lambda: {current_lambda} req/s")
            print(f"  Duration: {args.duration}s")
            print(f"─────────────────────────────────────────────")

            results_file = os.path.join(args.output_dir, f"poisson_l{current_lambda}_{args.timestamp}.jsonl")
            summary = run_poisson_traffic(args.url, payload, current_lambda, args.duration, results_file)

            print(f"  Avg: {summary['avg_latency_ms']}ms  P50: {summary['p50_latency_ms']}ms  "
                  f"P95: {summary['p95_latency_ms']}ms  P99: {summary['p99_latency_ms']}ms  "
                  f"RPS: {summary['actual_rps']}  Errors: {summary['error_rate_pct']}%")
            print()

            summary_rows.append(summary)

            if current_lambda < args.lambda_max:
                print(f"  Cooling down {args.cooldown}s before next level...")
                time.sleep(args.cooldown)
                print()

            current_lambda += args.step

    # Write summary TSV
    summary_file = os.path.join(args.output_dir, f"poisson_summary_{args.timestamp}.tsv")
    with open(summary_file, "w") as f:
        f.write("lambda\tavg_latency_ms\tp50_latency_ms\tp95_latency_ms\tp99_latency_ms\tactual_rps\ttotal_requests\terror_rate_pct\n")
        for row in summary_rows:
            f.write(f"{row['lambda']}\t{row['avg_latency_ms']}\t{row['p50_latency_ms']}\t"
                    f"{row['p95_latency_ms']}\t{row['p99_latency_ms']}\t{row['actual_rps']}\t"
                    f"{row['total_requests']}\t{row['error_rate_pct']}\n")

    print("=============================================")
    print("  POISSON TRAFFIC COMPLETE")
    print(f"  Results in: {args.output_dir}/")
    print(f"  Summary: {summary_file}")
    print("=============================================")


if __name__ == "__main__":
    main()
