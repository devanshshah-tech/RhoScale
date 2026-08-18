#!/usr/bin/env python3
"""
hpa_replay.py - Compute HPA scaling decisions from CPU trace

Reads CPU% samples and applies HPA's ratio formula:
  desiredReplicas = ceil(currentMetric / desiredMetric × currentReplicas)

With HPA's default stabilization windows:
- Scale-up: 30s stabilization
- Scale-down: 300s (5min) stabilization

Usage:
  python3 hpa_replay.py <cpu_trace.txt> <cpu_timestamps.txt> [output_jsonl]
"""

import sys
import json
import math
from datetime import datetime

def compute_hpa_decisions(cpu_samples, timestamps, target_cpu=50, min_replicas=1, max_replicas=10):
    """
    Compute HPA scaling decisions from CPU trace.
    
    Args:
        cpu_samples: List of CPU% values
        timestamps: List of Unix timestamps
        target_cpu: Target CPU utilization (%)
        min_replicas: Minimum replica count
        max_replicas: Maximum replica count
    
    Returns:
        List of (timestamp, direction, from_replicas, to_replicas, reason)
    """
    decisions = []
    current_replicas = min_replicas
    last_scale_time = None
    scale_up_stabilization = 30  # seconds
    scale_down_stabilization = 300  # seconds
    
    for i, (cpu, ts) in enumerate(zip(cpu_samples, timestamps)):
        ts_dt = datetime.fromtimestamp(ts)
        
        # Compute desired replicas using HPA ratio formula
        if cpu > 0:
            desired = math.ceil((cpu / target_cpu) * current_replicas)
        else:
            desired = min_replicas
        
        # Clamp to min/max
        desired = max(min_replicas, min(max_replicas, desired))
        
        # Determine direction
        if desired > current_replicas:
            direction = "ScaleOut"
        elif desired < current_replicas:
            direction = "ScaleIn"
        else:
            direction = "Hold"
        
        # Apply stabilization windows
        if last_scale_time is not None:
            elapsed = ts - last_scale_time
            
            if direction == "ScaleOut" and elapsed < scale_up_stabilization:
                direction = "Hold"
            elif direction == "ScaleIn" and elapsed < scale_down_stabilization:
                direction = "Hold"
        
        # Record decision if it's a scale action
        if direction != "Hold" and desired != current_replicas:
            reason = f"CPU {cpu:.1f}% vs target {target_cpu}%. desired=ceil({cpu}/{target_cpu}×{current_replicas})={desired}"
            decisions.append({
                "ts": ts_dt.strftime("%Y-%m-%dT%H:%M:%SZ"),
                "direction": direction,
                "from": current_replicas,
                "to": desired,
                "cpu_percent": round(cpu, 2),
                "reason": reason
            })
            current_replicas = desired
            last_scale_time = ts
    
    return decisions

def main():
    if len(sys.argv) < 3:
        print("Usage: python3 hpa_replay.py <cpu_trace.txt> <cpu_timestamps.txt> [output_jsonl]")
        sys.exit(1)
    
    cpu_file = sys.argv[1]
    ts_file = sys.argv[2]
    output_file = sys.argv[3] if len(sys.argv) > 3 else "results/hpa_decisions.jsonl"
    
    # Read CPU samples
    with open(cpu_file, 'r') as f:
        cpu_samples = [float(line.strip()) for line in f if line.strip()]
    
    # Read timestamps
    with open(ts_file, 'r') as f:
        timestamps = [int(line.strip()) for line in f if line.strip()]
    
    if len(cpu_samples) != len(timestamps):
        print(f"ERROR: Mismatched samples ({len(cpu_samples)}) and timestamps ({len(timestamps)})")
        sys.exit(1)
    
    print(f"Loaded {len(cpu_samples)} CPU samples")
    print(f"CPU range: {min(cpu_samples):.1f}% - {max(cpu_samples):.1f}%")
    
    # Compute HPA decisions
    decisions = compute_hpa_decisions(cpu_samples, timestamps)
    
    # Write decisions to JSONL
    with open(output_file, 'w') as f:
        for decision in decisions:
            f.write(json.dumps(decision) + '\n')
    
    print(f"\nHPA made {len(decisions)} scaling decisions:")
    for d in decisions:
        print(f"  {d['ts']}: {d['from']}→{d['to']} ({d['direction']}) - CPU {d['cpu_percent']}%")
    
    print(f"\nDecisions written to: {output_file}")

if __name__ == "__main__":
    main()
