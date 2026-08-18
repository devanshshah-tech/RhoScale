#!/usr/bin/env python3
"""
latency_vs_cost_analysis.py — Generate latency vs cost analysis with theoretical controller behavior.

Shows the trade-off between P95 latency and cost (replica count) for:
1. Baseline (1 replica, no controller)
2. Theoretical controller behavior (proportional scaling based on queue depth)
"""

import csv
import matplotlib.pyplot as plt
from pathlib import Path


def load_summary(tsv_file):
    """Load summary TSV from load test."""
    concurrency = []
    p50 = []
    p95 = []
    p99 = []
    rps = []
    
    with open(tsv_file, 'r') as f:
        reader = csv.DictReader(f, delimiter='\t')
        for row in reader:
            concurrency.append(int(row['concurrency']))
            p50.append(int(row['p50_latency_ms']))
            p95.append(int(row['p95_latency_ms']))
            p99.append(int(row['p99_latency_ms']))
            rps.append(float(row['rps']))
    
    return concurrency, p50, p95, p99, rps


def estimate_controller_replicas(concurrency, knee_point_concurrency=5):
    """
    Estimate what the controller would do at each concurrency level.
    
    The controller uses: desiredReplicas = ceil(L/θ) × currentReplicas
    
    For simplicity, assume:
    - Queue depth L ≈ concurrency / 2 (rough approximation)
    - θ = 1 (from our calibration)
    - currentReplicas starts at 1
    """
    theta = 1
    replicas = []
    
    for c in concurrency:
        # Estimate queue depth (this is a simplification)
        # At low concurrency, queue is small; at high concurrency, queue grows
        if c <= knee_point_concurrency:
            # Below knee point: queue is small, controller stays at 1
            replicas.append(1)
        else:
            # Above knee point: queue grows, controller scales proportionally
            # Rough estimate: L ≈ (c - knee_point) / 2
            L = max(1, (c - knee_point_concurrency) // 2)
            desired = max(1, int(L / theta))
            replicas.append(min(desired, 10))  # Cap at 10
    
    return replicas


def generate_chart(concurrency, p95, baseline_replicas, controller_replicas, output_file):
    """Generate latency vs cost comparison chart."""
    fig, (ax1, ax2) = plt.subplots(1, 2, figsize=(16, 6))
    
    # Left: Latency vs Concurrency
    ax1.plot(concurrency, p95, marker='s', linewidth=2, markersize=8, 
             label='P95 Latency', color='#A23B72')
    ax1.set_xlabel('Concurrency Level', fontsize=12, fontweight='bold')
    ax1.set_ylabel('P95 Latency (ms)', fontsize=12, fontweight='bold')
    ax1.set_title('Latency vs Concurrency', fontsize=14, fontweight='bold')
    ax1.grid(True, alpha=0.3, linestyle='--')
    ax1.legend(fontsize=10)
    ax1.set_xticks(concurrency)
    
    # Right: Replica Count vs Concurrency
    ax2.plot(concurrency, baseline_replicas, marker='o', linewidth=2, markersize=8,
             label='Baseline (no controller)', color='#888888', linestyle='--')
    ax2.plot(concurrency, controller_replicas, marker='s', linewidth=2, markersize=8,
             label='Custom Controller (theoretical)', color='#2E86AB')
    ax2.set_xlabel('Concurrency Level', fontsize=12, fontweight='bold')
    ax2.set_ylabel('Replica Count (Cost Proxy)', fontsize=12, fontweight='bold')
    ax2.set_title('Cost vs Concurrency', fontsize=14, fontweight='bold')
    ax2.grid(True, alpha=0.3, linestyle='--')
    ax2.legend(fontsize=10)
    ax2.set_xticks(concurrency)
    ax2.set_ylim(0, max(controller_replicas) + 2)
    
    plt.tight_layout()
    plt.savefig(output_file, dpi=300, bbox_inches='tight')
    plt.close()


def main():
    import argparse
    parser = argparse.ArgumentParser(description="Latency vs Cost analysis")
    parser.add_argument("--summary", required=True, help="Summary TSV file")
    parser.add_argument("--knee-point", type=int, default=5, help="Knee Point concurrency")
    parser.add_argument("--output", default="results/latency_vs_cost_analysis.png", help="Output chart")
    
    args = parser.parse_args()
    
    # Load data
    concurrency, p50, p95, p99, rps = load_summary(args.summary)
    
    # Estimate replica counts
    baseline_replicas = [1] * len(concurrency)
    controller_replicas = estimate_controller_replicas(concurrency, args.knee_point)
    
    # Generate chart
    generate_chart(concurrency, p95, baseline_replicas, controller_replicas, args.output)
    
    print(f"Chart saved to: {args.output}")
    print()
    print("Analysis:")
    print(f"{'Concurrency':<15} {'P95 (ms)':<15} {'Baseline':<15} {'Controller':<15} {'Cost Increase':<15}")
    print("-" * 75)
    for c, p, b, ctrl in zip(concurrency, p95, baseline_replicas, controller_replicas):
        increase = (ctrl - b) / b * 100 if b > 0 else 0
        print(f"{c:<15} {p:<15} {b:<15} {ctrl:<15} (+{increase:.0f}%)")


if __name__ == "__main__":
    main()
