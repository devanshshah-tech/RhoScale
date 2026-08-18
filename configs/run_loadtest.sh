#!/bin/bash
# =============================================================================
# run_loadtest.sh  —  Step-up concurrency load test
# Runs entirely locally against vllm-mlx on localhost:8000. No tunnel needed.
# Usage:  bash configs/run_loadtest.sh
# =============================================================================

# Activate vllm-mlx venv for matplotlib (chart generation)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
VENV_DIR="$PROJECT_ROOT/../vllm-mlx/.venv"
if [ -f "$VENV_DIR/bin/activate" ]; then
    source "$VENV_DIR/bin/activate"
fi

VLLM_URL="http://localhost:8000/v1/chat/completions"
RESULTS_DIR="results"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
DURATION="120s"          # hold each concurrency level for 2 minutes

# Fixed prompt — consistent token budget across all runs (~100 output tokens).
# A fixed prompt ensures service-time variance comes from the inference engine
# itself, not from different input lengths. Required for G/G/1 model validity.
# Update "model" below to whatever you passed to `vllm-mlx serve --model ...`
PAYLOAD='{
  "model": "mlx-community/Qwen2.5-3B-Instruct-4bit",
  "messages": [
    {
      "role": "user",
      "content": "Explain Little Law in queueing theory in exactly 100 words."
    }
  ],
  "max_tokens": 100,
  "temperature": 0
}'

# Concurrency levels — matches the 7-step plan from the baseline report
CONCURRENCY_LEVELS=(1 3 5 10 20 35 50)

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------
echo "=== vLLM-MLX Load Test — CSC 630 ==="
echo "=== Timestamp: $TIMESTAMP ==="
echo ""

if ! command -v hey &> /dev/null; then
    echo "ERROR: 'hey' not found. Install with: go install github.com/rakyll/hey@latest"
    exit 1
fi

echo "Checking vllm-mlx server health..."
if ! curl -s --fail "http://localhost:8000/health" > /dev/null 2>&1; then
    echo "ERROR: server not reachable at localhost:8000."
    echo "       Make sure 'vllm-mlx serve --enable-metrics --port 8000' is running."
    exit 1
fi
echo "Server is healthy. Starting load test."
echo ""

mkdir -p "$RESULTS_DIR"

SUMMARY_FILE="$RESULTS_DIR/summary_${TIMESTAMP}.tsv"
echo -e "concurrency\tp50_latency_ms\tp95_latency_ms\tp99_latency_ms\trps\ttotal_requests\terror_rate" \
    > "$SUMMARY_FILE"

# ---------------------------------------------------------------------------
# Step-up loop
# ---------------------------------------------------------------------------
LAST_INDEX=$((${#CONCURRENCY_LEVELS[@]} - 1))
for i in "${!CONCURRENCY_LEVELS[@]}"; do
    C="${CONCURRENCY_LEVELS[$i]}"

    echo "─────────────────────────────────────────────"
    echo "  Concurrency: $C concurrent users"
    echo "  Duration:    $DURATION"
    echo "─────────────────────────────────────────────"

    RAW_OUTPUT="$RESULTS_DIR/hey_c${C}_${TIMESTAMP}.txt"

    hey \
        -c "$C" \
        -z "$DURATION" \
        -m POST \
        -H "Content-Type: application/json" \
        -d "$PAYLOAD" \
        "$VLLM_URL" \
        > "$RAW_OUTPUT" 2>&1

    # Parse hey output — extract latency percentiles and RPS
    # Note: hey prints "50%% in 0.8058 secs" (double %), match literally with %%
    P50=$(grep -E "^[[:space:]]+50%% in" "$RAW_OUTPUT" | awk '{print $3}' | sed 's/secs//' | awk '{printf "%.0f", $1 * 1000}')
    P95=$(grep -E "^[[:space:]]+95%% in" "$RAW_OUTPUT" | awk '{print $3}' | sed 's/secs//' | awk '{printf "%.0f", $1 * 1000}')
    P99=$(grep -E "^[[:space:]]+99%% in" "$RAW_OUTPUT" | awk '{print $3}' | sed 's/secs//' | awk '{printf "%.0f", $1 * 1000}')
    
    # Fallback: if P99 is 0 or empty (hey can't compute with <100 responses), use Slowest
    if [ -z "$P99" ] || [ "$P99" = "0" ]; then
        P99=$(grep "Slowest:" "$RAW_OUTPUT" | awk '{printf "%.0f", $2 * 1000}')
    fi
    
    RPS=$(grep "Requests/sec:" "$RAW_OUTPUT" | awk '{printf "%.2f", $2}')

    # Sum all status code counts for total requests (200 + any non-200)
    TOTAL=$(awk '/Status code distribution:/{flag=1; next} /^$/{flag=0} flag' "$RAW_OUTPUT" \
        | awk '{sum+=$2} END{print sum+0}')

    # Count non-200 status codes from the Status code distribution section
    # Lines look like: "  [503] 128 responses"
    NON_200=$(awk '/Status code distribution:/{flag=1; next} /^$/{flag=0} flag' "$RAW_OUTPUT" \
        | grep -v "\[200\]" \
        | awk '{sum+=$2} END{print sum+0}')
    ERROR_RATE=$(echo "$NON_200 $TOTAL" | awk '{printf "%.2f", ($1>0 ? $1/$2*100 : 0)}')

    echo "  P50: ${P50}ms  P95: ${P95}ms  P99: ${P99}ms  RPS: ${RPS}  Errors: ${ERROR_RATE}%"

    echo -e "${C}\t${P50}\t${P95}\t${P99}\t${RPS}\t${TOTAL}\t${ERROR_RATE}" \
        >> "$SUMMARY_FILE"

    # Cooldown between steps — let the queue drain before next level
    if [ "$i" -lt "$LAST_INDEX" ]; then
        echo "  Cooling down 30s before next level..."
        sleep 30
    fi

    echo ""
done

# ---------------------------------------------------------------------------
# Print summary table and analyze Knee Point
# ---------------------------------------------------------------------------
echo "============================================="
echo "  LOAD TEST COMPLETE"
echo "  Results in: $RESULTS_DIR/"
echo "  Summary:    $SUMMARY_FILE"
echo "============================================="
echo ""
echo "Summary (all latencies in ms):"
echo ""
TAB=$(printf '\t')
column -t -s "$TAB" "$SUMMARY_FILE"
echo ""

# Generate chart and detect Knee Point automatically
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CHART_FILE="${SUMMARY_FILE%.tsv}_chart.png"

echo "Analyzing results and generating chart..."
python3 "$SCRIPT_DIR/analyze_load_test.py" "$SUMMARY_FILE" "$CHART_FILE"
