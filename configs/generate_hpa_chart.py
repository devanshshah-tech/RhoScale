#!/usr/bin/env python3
"""
Generate HPA scaling timeline chart for §4.2.2.
"""

import matplotlib.pyplot as plt
import matplotlib.patches as mpatches
from datetime import datetime, timedelta

# HPA scaling events (relative times from load start)
hpa_events = [
    (0, 1, "Initial"),
    (8, 2, "ScaleOut"),
    (23, 4, "ScaleOut"),
    (38, 8, "ScaleOut"),
    (53, 10, "ScaleOut (max)"),
]

# Custom controller would have done this (based on queue depth detection)
controller_events = [
    (0, 1, "Initial"),
    (4, 8, "ScaleOut (single decision)"),
]

# Create figure
fig, ax = plt.subplots(figsize=(12, 6))

# Plot HPA scaling
hpa_times = [e[0] for e in hpa_events]
hpa_replicas = [e[1] for e in hpa_events]
ax.step(hpa_times, hpa_replicas, where='post', linewidth=2, marker='o', 
        markersize=8, label='HPA (CPU-based)', color='#A23B72')

# Plot custom controller scaling
ctrl_times = [e[0] for e in controller_events]
ctrl_replicas = [e[1] for e in controller_events]
ax.step(ctrl_times, ctrl_replicas, where='post', linewidth=2, marker='s', 
        markersize=8, label='Custom Controller (Queue-based)', color='#2E86AB', linestyle='--')

# Annotate HPA events
for i, (t, r, label) in enumerate(hpa_events):
    if i > 0:  # Skip initial
        ax.annotate(f'{r} replicas', xy=(t, r), xytext=(t+2, r+0.3),
                    fontsize=9, fontweight='bold', color='#A23B72')

# Annotate controller event
ax.annotate(f'8 replicas\n(single decision)', xy=(4, 8), xytext=(10, 9),
            fontsize=9, fontweight='bold', color='#2E86AB',
            arrowprops=dict(arrowstyle='->', color='#2E86AB', lw=1.5))

# Mark load start and end
ax.axvline(x=0, color='green', linestyle=':', linewidth=1.5, alpha=0.7, label='Load start')
ax.axvline(x=120, color='red', linestyle=':', linewidth=1.5, alpha=0.7, label='Load end')

# Labels and title
ax.set_xlabel('Time (seconds)', fontsize=12, fontweight='bold')
ax.set_ylabel('Replica Count', fontsize=12, fontweight='bold')
ax.set_title('HPA vs Custom Controller — Replica Scaling Over Time', 
             fontsize=14, fontweight='bold', pad=20)

# Grid and legend
ax.grid(True, alpha=0.3, linestyle='--')
ax.legend(loc='upper left', fontsize=10, framealpha=0.9)

# Set axis limits
ax.set_xlim(-5, 130)
ax.set_ylim(0, 12)

# Set x-ticks
ax.set_xticks([0, 20, 40, 60, 80, 100, 120])

# Adjust layout
plt.tight_layout()

# Save the chart
output_file = '/Users/dash7/Documents/NCSU/Sem-4/controller/results/hpa_scaling_timeline.png'
plt.savefig(output_file, dpi=300, bbox_inches='tight')
print(f"Chart saved to: {output_file}")
