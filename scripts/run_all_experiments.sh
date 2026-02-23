#!/bin/bash
# Run all experiments and produce a summary report

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
RESULTS_DIR="$REPO_DIR/experiments/results/run_$TIMESTAMP"
SUMMARY_FILE="$RESULTS_DIR/EXPERIMENT_SUMMARY.md"

mkdir -p "$RESULTS_DIR"

echo "# Experiment Results - $TIMESTAMP" > "$SUMMARY_FILE"
echo "" >> "$SUMMARY_FILE"

run_test() {
    local name="$1"
    local cmd="$2"
    local log_file="$RESULTS_DIR/${name}.log"
    
    echo "Running $name..."
    echo "## $name" >> "$SUMMARY_FILE"
    echo '```' >> "$SUMMARY_FILE"
    
    if eval "$cmd" > "$log_file" 2>&1; then
        echo "✓ PASS" >> "$SUMMARY_FILE"
        echo "  ✓ $name PASS"
    else
        echo "✗ FAIL" >> "$SUMMARY_FILE"
        echo "  ✗ $name FAIL"
    fi
    
    tail -50 "$log_file" >> "$SUMMARY_FILE"
    echo '```' >> "$SUMMARY_FILE"
    echo "" >> "$SUMMARY_FILE"
}

cd "$REPO_DIR"

echo "=== NDN Repository Experiment Runner ==="
echo "Results directory: $RESULTS_DIR"
echo ""

echo "## 1. Unit Tests" >> "$SUMMARY_FILE"
run_test "unit_tests" "make test-short"

echo "## 2. Integration Tests (Local NFD)" >> "$SUMMARY_FILE"
run_test "integration_tests" "make test-integration"

echo "## 3. Failure Recovery Tests" >> "$SUMMARY_FILE"
run_test "failure_tests" "make test-failure"

echo "## 4. Concurrent Command Tests" >> "$SUMMARY_FILE"
run_test "concurrent_tests" "go test -v -run 'TestConcurrentCommands' -timeout 3m ./repo/..."

echo "## 5. Multiple Sequential Commands Tests" >> "$SUMMARY_FILE"
run_test "multi_tests" "go test -v -run 'TestMultipleCommands' -timeout 3m ./repo/..."

echo "## 6. Edge Case Tests (RF=1, RF=Nodes)" >> "$SUMMARY_FILE"
run_test "edge_case_tests" "go test -v -run 'TestReplicationFactor_EdgeCases' -timeout 3m ./repo/..."

echo "## 7. Node Join Tests" >> "$SUMMARY_FILE"
run_test "node_join_tests" "go test -v -run 'TestNodeJoin' -timeout 3m ./repo/..."

echo "## 8. Cascading Failure Tests" >> "$SUMMARY_FILE"
run_test "cascading_failure_tests" "go test -v -run 'TestFailureRecovery_CascadingFailures' -timeout 5m ./repo/..."

if command -v docker &> /dev/null; then
    echo "## 9. Mini-NDN Docker Tests" >> "$SUMMARY_FILE"
    run_test "mini_ndn_tests" "make test-mini-ndn"
else
    echo "## 9. Mini-NDN Docker Tests" >> "$SUMMARY_FILE"
    echo "SKIPPED - Docker not available" >> "$SUMMARY_FILE"
    echo "  ⊘ Mini-NDN tests SKIPPED (Docker not available)"
fi

echo ""
echo "=== SUMMARY ==="
echo "Results saved to: $SUMMARY_FILE"
echo ""
grep -E "^(✓|✗)" "$SUMMARY_FILE" | sort | uniq -c
