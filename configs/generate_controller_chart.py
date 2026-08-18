#!/usr/bin/env python3
"""
Generate time-series chart for §4.2.1 showing queue depth and replica count.
Reads the controller JSONL log and reconstructs the replica count timeline.
"""

import json
import matplotlib.pyplot as plt
from datetime import datetime
import sys

# Read the controller JSONL log
log_file = sys.argv[1] if len(sys.argv) > 1 else "results/controller_20260718_163058.log"

decisions = []
with open(log_file, 'r') as f:
    for line in f:
        if line.strip():
            decisions.append(json.loads(line))

# Extract timeline data
timestamps = []
queue_depths = []
replica_counts = []

# Track replica count over time
current_replicas = 1  # Starting replica count

for decision in decisions:
    ts = datetime.fromisoformat(decision['ts'].replace('Z', '+00:00'))
    timestamps.append(ts)
    queue_depths.append(decision['queue_depth'])
    
    # Update replica count based on decision
    if decision['direction'] == 'ScaleOut':
        current_replicas = decision['to']
    elif decision['direction'] == 'ScaleIn':
        current_replicas = decision['to']
    
    replica_counts.append(current_replicas)

# Create the chart with two y-axes
fig, ax1 = plt.subplots(figsize=(12, 6))

# Plot queue depth on left y-axis
color1 = '#2E86AB'
ax1.set_xlabel('Time (UTC)', fontsize=12, fontweight='bold')
ax1.set_ylabel('Queue Depth', fontsize=12, fontweight='bold', color=color1)
line1 = ax1.plot(timestamps, queue_depths, marker='o', linewidth=2, markersize=6, 
                 label='Queue Depth', color=color1)
ax1.tick_params(axis='y', labelcolor=color1)
ax1.grid(True, alpha=0.3, linestyle='--')

# Format x-axis timestamps with fixed locator
import matplotlib.dates as mdates
ax1.xaxis.set_major_locator(mdates.MinuteLocator(interval=2))
ax1.xaxis.set_major_formatter(mdates.DateFormatter('%H:%M'))
plt.setp(ax1.xaxis.get_majorticklabels(), rotation=45, ha='right')

# Create second y-axis for replica count
ax2 = ax1.twinx()
color2 = '#A23B72'
ax2.set_ylabel('Replica Count', fontsize=12, fontweight='bold', color=color2)
line2 = ax2.plot(timestamps, replica_counts, marker='s', linewidth=2, markersize=6, 
                 label='Replica Count', color=color2, linestyle='--')
ax2.tick_params(axis='y', labelcolor=color2)

# Set y-axis limits
ax1.set_ylim(-0.5, max(queue_depths) + 1)
ax2.set_ylim(0, max(replica_counts) + 1)

# Title
plt.title('Controller Scaling Behavior — Queue Depth and Replica Count Over Time', 
          fontsize=14, fontweight='bold', pad=20)

# Combine legends
lines = line1 + line2
labels = [l.get_label() for l in lines]
ax1.legend(lines, labels, loc='upper right', fontsize=10, framealpha=0.9)

# Annotate key events
# ScaleOut event
scaleout_idx = 0
ax1.annotate('ScaleOut\n(θ=1)', xy=(timestamps[scaleout_idx], queue_depths[scaleout_idx]),
             xytext=(timestamps[scaleout_idx], queue_depths[scaleout_idx] + 1.5),
             arrowprops=dict(arrowstyle='->', color='red', lw=1.5),
             fontsize=9, color='red', fontweight='bold', ha='center')

# ScaleIn start
scalein_start_idx = 1
ax2.annotate('ScaleIn\n(cooldown=120s)', xy=(timestamps[scalein_start_idx], replica_counts[scalein_start_idx]),
             xytext=(timestamps[scalein_start_idx], replica_counts[scalein_start_idx] + 0.5),
             arrowprops=dict(arrowstyle='->', color='green', lw=1.5),
             fontsize=9, color='green', fontweight='bold', ha='center')

# Adjust layout
plt.tight_layout()

# Save the chart
output_file = log_file.replace('.log', '_timeseries.png')
plt.savefig(output_file, dpi=300, bbox_inches='tight')
print(f"Chart saved to: {output_file}")

# Also show it
plt.show()
