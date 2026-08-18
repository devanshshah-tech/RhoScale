#!/usr/bin/env python3
"""
Analyze load test results and generate chart with automatic Knee Point detection.

This script:
1. Reads the summary TSV from the load test
2. Calculates P95 latency slopes between consecutive concurrency levels
3. Identifies the Knee Point (where slope increases by >2×)
4. Generates a chart with P50/P95 latency and Knee Point marked
5. Outputs the Knee Point concurrency level to stdout
"""

import csv
import sys
import matplotlib.pyplot as plt
import matplotlib.dates as mdates
from pathlib import Path

def analyze_load_test(tsv_file):
    """Read TSV and return data structures."""
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

def detect_knee_point(concurrency, p95):
    """
    Detect Knee Point by finding where the second derivative is maximum.
    
    The Knee Point is where the slope changes most dramatically (maximum curvature).
    This is found by calculating the change in slope between consecutive intervals.
    
    Returns:
        tuple: (knee_point_index, knee_point_concurrency, slope_analysis)
    """
    slopes = []
    
    # Calculate slopes between consecutive points
    for i in range(1, len(concurrency)):
        delta_c = concurrency[i] - concurrency[i-1]
        delta_p95 = p95[i] - p95[i-1]
        slope = delta_p95 / delta_c if delta_c > 0 else 0
        slopes.append(slope)
    
    # Calculate second derivative (change in slope between consecutive intervals)
    second_derivatives = []
    for i in range(1, len(slopes)):
        change = slopes[i] - slopes[i-1]
        second_derivatives.append(change)
    
    # Find Knee Point: where absolute second derivative is maximum
    knee_idx = None
    max_abs_change = 0
    
    for i, change in enumerate(second_derivatives):
        abs_change = abs(change)
        if abs_change > max_abs_change:
            max_abs_change = abs_change
            knee_idx = i + 1  # +1 because second_derivatives[0] is between slopes[0] and slopes[1]
    
    # Build slope analysis table
    slope_analysis = []
    for i in range(len(slopes)):
        c_from = concurrency[i]
        c_to = concurrency[i+1]
        slope = slopes[i]
        second_deriv = second_derivatives[i-1] if i > 0 else 0
        slope_analysis.append({
            'transition': f'c={c_from} → c={c_to}',
            'slope': slope,
            'second_deriv': second_deriv,
            'is_knee': i == knee_idx
        })
    
    # knee_idx is the slope index; the Knee Point concurrency is the source of that slope
    knee_concurrency = concurrency[knee_idx] if knee_idx is not None else None
    
    return knee_idx, knee_concurrency, slope_analysis

def generate_chart(concurrency, p50, p95, knee_concurrency, output_file):
    """Generate latency chart with Knee Point marked."""
    fig, ax = plt.subplots(figsize=(10, 6))
    
    # Plot P50 and P95 lines
    ax.plot(concurrency, p50, marker='o', linewidth=2, markersize=8, 
            label='P50 Latency', color='#2E86AB')
    ax.plot(concurrency, p95, marker='s', linewidth=2, markersize=8, 
            label='P95 Latency', color='#A23B72')
    
    # Mark the Knee Point
    if knee_concurrency is not None:
        knee_p95 = p95[concurrency.index(knee_concurrency)]
        
        ax.axvline(x=knee_concurrency, color='red', linestyle='--', linewidth=2, 
                   alpha=0.7, label=f'Knee Point (c={knee_concurrency})')
        
        # Annotate the Knee Point
        ax.annotate(f'Knee Point\n(c={knee_concurrency})', 
                    xy=(knee_concurrency, knee_p95),
                    xytext=(knee_concurrency + 5, knee_p95 - 2000),
                    arrowprops=dict(arrowstyle='->', color='red', lw=1.5),
                    fontsize=10, color='red', fontweight='bold')
    
    # Labels and title
    ax.set_xlabel('Concurrency Level', fontsize=12, fontweight='bold')
    ax.set_ylabel('Latency (ms)', fontsize=12, fontweight='bold')
    ax.set_title('Baseline Latency vs Concurrency — Knee Point Identification', 
                 fontsize=14, fontweight='bold', pad=20)
    
    # Grid and legend
    ax.grid(True, alpha=0.3, linestyle='--')
    ax.legend(loc='upper left', fontsize=10, framealpha=0.9)
    
    # Set x-axis to show all concurrency levels
    ax.set_xticks(concurrency)
    ax.set_xticklabels(concurrency)
    
    # Adjust layout
    plt.tight_layout()
    
    # Save the chart
    plt.savefig(output_file, dpi=300, bbox_inches='tight')
    plt.close()

def main():
    if len(sys.argv) < 2:
        print("Usage: analyze_load_test.py <summary.tsv> [output_chart.png]")
        sys.exit(1)
    
    tsv_file = sys.argv[1]
    output_chart = sys.argv[2] if len(sys.argv) > 2 else tsv_file.replace('.tsv', '_chart.png')
    
    # Analyze the load test data
    concurrency, p50, p95, p99, rps = analyze_load_test(tsv_file)
    knee_idx, knee_concurrency, slope_analysis = detect_knee_point(concurrency, p95)
    
    # Generate the chart
    generate_chart(concurrency, p50, p95, knee_concurrency, output_chart)
    
    # Print results
    print(f"\n{'='*60}")
    print(f"  KNEE POINT ANALYSIS")
    print(f"{'='*60}\n")
    
    print("P95 Slope Analysis (second derivative = change in slope):")
    print(f"{'Transition':<20} {'Slope (ms/step)':<20} {'2nd Derivative':<20} {'Knee?'}")
    print("-" * 65)
    
    for entry in slope_analysis:
        knee_marker = " ← KNEE POINT" if entry['is_knee'] else ""
        deriv_str = f"{entry['second_deriv']:.1f}"
        print(f"{entry['transition']:<20} {entry['slope']:<20.1f} {deriv_str:<20}{knee_marker}")
    
    print(f"\n{'='*60}")
    if knee_concurrency is not None:
        print(f"  KNEE POINT DETECTED: c={knee_concurrency}")
        print(f"  Chart saved to: {output_chart}")
    else:
        print(f"  NO CLEAR KNEE POINT DETECTED")
        print(f"  Chart saved to: {output_chart}")
    print(f"{'='*60}\n")
    
    # Output Knee Point to stdout for script consumption
    if knee_concurrency is not None:
        print(f"KNEE_POINT_CONCURRENCY={knee_concurrency}")

if __name__ == "__main__":
    main()
