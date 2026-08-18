#!/usr/bin/env python3
"""
Generate baseline latency chart for §2.4 of the final report.
Reads the summary TSV and produces a line chart showing P50 and P95 latency
vs concurrency level, with the Knee Point annotated.
"""

import csv
import matplotlib.pyplot as plt
import sys

# Read the summary TSV
tsv_file = sys.argv[1] if len(sys.argv) > 1 else "results/summary_20260718_140929.tsv"

concurrency = []
p50 = []
p95 = []

with open(tsv_file, 'r') as f:
    reader = csv.DictReader(f, delimiter='\t')
    for row in reader:
        concurrency.append(int(row['concurrency']))
        p50.append(int(row['p50_latency_ms']))
        p95.append(int(row['p95_latency_ms']))

# Create the chart
fig, ax = plt.subplots(figsize=(10, 6))

# Plot P50 and P95 lines
ax.plot(concurrency, p50, marker='o', linewidth=2, markersize=8, label='P50 Latency', color='#2E86AB')
ax.plot(concurrency, p95, marker='s', linewidth=2, markersize=8, label='P95 Latency', color='#A23B72')

# Mark the Knee Point at c=35
knee_idx = concurrency.index(35)
ax.axvline(x=35, color='red', linestyle='--', linewidth=2, alpha=0.7, label='Knee Point (c=35)')
ax.annotate('Knee Point\n(c=35)', xy=(35, p95[knee_idx]), xytext=(25, p95[knee_idx] + 2000),
            arrowprops=dict(arrowstyle='->', color='red', lw=1.5),
            fontsize=10, color='red', fontweight='bold')

# Labels and title
ax.set_xlabel('Concurrency Level', fontsize=12, fontweight='bold')
ax.set_ylabel('Latency (ms)', fontsize=12, fontweight='bold')
ax.set_title('Baseline Latency vs Concurrency — Knee Point Identification', fontsize=14, fontweight='bold', pad=20)

# Grid and legend
ax.grid(True, alpha=0.3, linestyle='--')
ax.legend(loc='upper left', fontsize=10, framealpha=0.9)

# Set x-axis to show all concurrency levels
ax.set_xticks(concurrency)
ax.set_xticklabels(concurrency)

# Adjust layout
plt.tight_layout()

# Save the chart
output_file = tsv_file.replace('.tsv', '_latency_chart.png')
plt.savefig(output_file, dpi=300, bbox_inches='tight')
print(f"Chart saved to: {output_file}")

# Also show it
plt.show()
