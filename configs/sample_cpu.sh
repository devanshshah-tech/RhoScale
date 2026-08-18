#!/bin/bash
# sample_cpu.sh - Sample CPU% of vllm-mlx process during load test
# Usage: bash sample_cpu.sh [duration_seconds] [output_file]

DURATION=${1:-600}
OUTPUT_FILE=${2:-"results/cpu_trace.txt"}
TIMESTAMP_FILE=${2:-"results/cpu_timestamps.txt"}

echo "Starting CPU sampling for ${DURATION}s..."
echo "Output: $OUTPUT_FILE"

# Clear output files
> "$OUTPUT_FILE"
> "$TIMESTAMP_FILE"

# Find vllm-mlx PID
VLLM_PID=$(pgrep -f "vllm-mlx serve" | head -1)
if [ -z "$VLLM_PID" ]; then
    echo "ERROR: vllm-mlx process not found"
    exit 1
fi

echo "Sampling PID: $VLLM_PID"

# Sample every 10 seconds
END_TIME=$(($(date +%s) + DURATION))
while [ $(date +%s) -lt $END_TIME ]; do
    CPU=$(ps -p $VLLM_PID -o %cpu= 2>/dev/null | tr -d ' ')
    TIMESTAMP=$(date +%s)
    
    if [ -n "$CPU" ]; then
        echo "$CPU" >> "$OUTPUT_FILE"
        echo "$TIMESTAMP" >> "$TIMESTAMP_FILE"
    fi
    
    sleep 10
done

echo "CPU sampling complete. Samples: $(wc -l < "$OUTPUT_FILE")"
