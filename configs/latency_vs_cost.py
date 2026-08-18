#!/usr/bin/env python3
"""
latency_vs_cost.py — Generate scatter plot of P95 latency vs replica count (cost proxy).

Uses the step-up load test data and controller decisions to show the trade-off
between latency and cost (replica count).

Usage:
    python3 latency_vs_cost.py --summary results/summary_*.tsv --controller results/controller_*.log
"""

import argparse
import csv
import json
import matplotlib.pyplot as plt
from pathlib import Path


def load_summary(tsv_file):
    """Load summary TSV from load test."""
    concurrency = []
    p95 = []
    
    with open(tsv_file, 'r') as f:
        reader = csv.DictReader(f, delimiter='\t')
        for row in reader:
            concurrency.append(int(row['concurrency']))
            p95.append(int(row['p95_latency_ms']))
    
    return concurrency, p95


def load_controller_decisions(log_file):
    """Load controller decisions from JSONL log."""
    decisions = []
    
    with open(log_file, 'r') as f:
        for line in f:
            if line.strip():
                decisions.append(json.loads(line))
    
    return decisions


def estimate_replica_count(decisions, concurrency_levels):
    """
    Estimate replica count at each concurrency level based on controller decisions.
    
    For each concurrency level, find the controller decision that corresponds to
    that queue depth and use the 'to' replica count.
    """
    # Map queue depth to replica count from decisions
    queue_to_replicas = {}
    for d in decisions:
        queue_depth = d.get('queue_depth', 0)
        replicas = d.get('to', 1)
        queue_to_replicas[queue_depth] = replicas
    
    # For each concurrency level, estimate replica count
    # (This is a simplification - in reality, replica count varies over time)
    replica_counts = []
    for c in concurrency_levels:
        # Use a simple heuristic: higher concurrency → higher queue depth → more replicas
        # For the baseline test (no controller), assume 1 replica
        # For controller test, use the decision log
        if queue_to_replicas:
            # Find the decision with queue depth closest to this concurrency
            # (This is a rough approximation)
            avg_replicas = sum(queue_to_replicas.values()) / len(queue_to_replicas) if queue_to_replicas else 1
            replica_counts.append(max(1, int(avg_replicas)))
        else:
            replica_counts.append(1)
    
    return replica_counts


def generate_chart(concurrency, p95, replica_counts, output_file):
    """Generate latency vs cost scatter plot."""
    fig, ax = plt.subplots(figsize=(10, 6))
    
    # Scatter plot
    scatter = ax.scatter(replica_counts, p95, c=concurrency, cmap='viridis', 
                         s=100, edgecolors='black', linewidth=1.5, zorder=5)
    
    # Add labels for each point
    for i, (c, p, r) in enumerate(zip(concurrency, p95, replica_counts)):
        ax.annotate(f'c={c}', xy=(r, p), xytext=(5, 5), 
                    textcoords='offset points', fontsize=9, fontweight='bold')
    
    # Labels and title
    ax.set_xlabel('Replica Count (Cost Proxy)', fontsize=12, fontweight='bold')
    ax.set_ylabel('P95 Latency (ms)', fontsize=12, fontweight='bold')
    ax.set_title('Latency vs Cost Trade-off — Step-Up Load Test', 
                 fontsize=14, fontweight='bold', pad=20)
    
    # Colorbar
    cbar = plt.colorbar(scatter, ax=ax)
    cbar.set_label('Concurrency Level', fontsize=10, fontweight='bold')
    
    # Grid
    ax.grid(True, alpha=0.3, linestyle='--')
    
    # Adjust layout
    plt.tight_layout()
    
    # Save
    plt.savefig(output_file, dpi=300, bbox_inches='tight')
    plt.close()


def main():
    parser = argparse.ArgumentParser(description="Latency vs Cost scatter plot")
    parser.add_argument("--summary", required=True, help="Summary TSV file")
    parser.add_argument("--controller", help="Controller JSONL log (optional)")
    parser.add_argument("--output", default="results/latency_vs_cost.png", help="Output chart file")
    
    args = parser.parse_args()
    
    # Load data
    concurrency, p95 = load_summary(args.summary)
    
    # Load controller decisions if available
    replica_counts = []
    if args.controller and Path(args.controller).exists():
        decisions = load_controller_decisions(args.controller)
        replica_counts = estimate_replica_count(decisions, concurrency)
    else:
        # Assume 1 replica for all levels (no controller)
        replica_counts = [1] * len(concurrency)
    
    # Generate chart
    generate_chart(concurrency, p95, replica_counts, args.output)
    
    print(f"Chart saved to: {args.output}")
    print()
    print("Data points:")
    print(f"{'Concurrency':<15} {'P95 (ms)':<15} {'Replicas':<15}")
    print("-" * 45)
    for c, p, r in zip(concurrency, p95, replica_counts):
        print(f"{c:<15} {p:<15} {r:<15}")


if __name__ == "__main__":
    main()
