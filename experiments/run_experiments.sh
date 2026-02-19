#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
RESULTS_DIR="$REPO_DIR/experiment_results"

TIMESTAMP=$(date +%Y%m%d_%H%M%S)
RUN_DIR="$RESULTS_DIR/run_${TIMESTAMP}_p${PRODUCER_COUNT}_c${COMMAND_COUNT}_r${COMMAND_RATE}_rf${REPLICATION_FACTOR}"
mkdir -p "$RUN_DIR"

NODE_COUNTS="${NODE_COUNTS:-5 10 15 20 24}"
REPLICATION_FACTOR="${REPLICATION_FACTOR:-3}"
PRODUCER_COUNT="${PRODUCER_COUNT:-1}"
COMMAND_COUNT="${COMMAND_COUNT:-1}"
COMMAND_RATE="${COMMAND_RATE:-1}"

MEMORY_PER_CONTAINER=4
CPUS_PER_CONTAINER=4
TIMEOUT=30
ROUTING_WAIT_BASE=30

echo "=== NDN Repository Experiment Runner ==="
echo "Node counts: $NODE_COUNTS"
echo "Replication factor: $REPLICATION_FACTOR"
echo "Producer count: $PRODUCER_COUNT"
echo "Command count: $COMMAND_COUNT"
echo "Command rate: $COMMAND_RATE cmds/sec"
echo "Results directory: $RUN_DIR"
echo ""

detect_parallel_capacity() {
    local available_mem_mb=$(free -m | awk '/^Mem:/{print $7}')
    local available_cpus=$(nproc)
    
    local mem_gb=$((available_mem_mb / 1024))
    
    local by_mem=$((mem_gb / MEMORY_PER_CONTAINER))
    local by_cpu=$((available_cpus / CPUS_PER_CONTAINER))
    
    local parallel=$((by_mem < by_cpu ? by_mem : by_cpu))
    echo $((parallel < 1 ? 1 : parallel))
}

run_experiment() {
    local node_count=$1
    local result_dir="$RUN_DIR/nodes_${node_count}"
    mkdir -p "$result_dir"
    
    local routing_wait=$((ROUTING_WAIT_BASE + (node_count - 5) * 2))
    
    echo "[PID $$] Starting experiment with $node_count nodes..."
    
    docker run --rm --privileged \
        -m ${MEMORY_PER_CONTAINER}g --cpus ${CPUS_PER_CONTAINER} \
        -v /lib/modules:/lib/modules \
        -v "$result_dir:/results" \
        mini-ndn-integration \
        -c "python3 /usr/local/bin/runner.py --node-count $node_count --timeout $TIMEOUT --replication-factor $REPLICATION_FACTOR --routing-wait $routing_wait --results-dir /results --producer-count $PRODUCER_COUNT --command-count $COMMAND_COUNT --command-rate $COMMAND_RATE" \
        > "$result_dir/docker.log" 2>&1
    
    local exit_code=$?
    
    if [ $exit_code -ne 0 ]; then
        echo "[PID $$] Experiment with $node_count nodes failed (exit code: $exit_code)"
    fi
    
    local result_file="$result_dir/metadata.json"
    
    if [ -f "$result_file" ]; then
        replicated=$(python3 -c "import json; d=json.load(open('$result_file')); print('true' if d.get('replicated') else 'false')")
        rep_time=$(python3 -c "import json; d=json.load(open('$result_file')); print('%.2f' % (d.get('replication_time_seconds') or 0))")
        sync_int=$(python3 -c "import json; d=json.load(open('$result_file')); print(d.get('sync_interests', 0))")
        data_pkt=$(python3 -c "import json; d=json.load(open('$result_file')); print(d.get('data_packets', 0))")
        max_rep=$(python3 -c "import json; d=json.load(open('$result_file')); print(d.get('max_replication', 0))")
        over_rep=$(python3 -c "import json; d=json.load(open('$result_file')); print('true' if d.get('over_replicated') else 'false')")
        under_rep=$(python3 -c "import json; d=json.load(open('$result_file')); print('true' if d.get('under_replicated') else 'false')")
        final_count=$(python3 -c "import json; d=json.load(open('$result_file')); print(d.get('final_replication', d.get('job_claims', 0)))")
        
        echo "$node_count,$replicated,$rep_time,$sync_int,$data_pkt,$max_rep,$over_rep,$under_rep,$final_count" >> "$SUMMARY_FILE"
        echo "[PID $$] Result: replicated=$replicated, time=${rep_time}s, sync=$sync_int, data=$data_pkt"
    else
        echo "[PID $$] Error: metadata.json not found"
        echo "$node_count,false,0,0,0,0,false,true,0" >> "$SUMMARY_FILE"
    fi
}

run_batch() {
    local batch=("$@")
    local pids=()
    
    echo "Starting batch of ${#batch[@]} experiments in parallel..."
    
    for node_count in "${batch[@]}"; do
        run_experiment "$node_count" &
        pids+=($!)
    done
    
    for pid in "${pids[@]}"; do
        wait $pid || true
    done
    
    echo "Batch completed."
    echo ""
}

cd "$REPO_DIR"

echo "Building binaries..."
mkdir -p bin
go build -o bin/repo ./repo
go build -o bin/producer ./producer
echo "Binaries built."

echo "Copying NDN keys..."
rm -rf keys
cp -r ~/.ndn/keys keys 2>/dev/null || {
    echo "Error: NDN keys not found at ~/.ndn/keys"
    exit 1
}

echo "Building Docker image..."
docker build -t mini-ndn-integration -f experiments/Dockerfile.integration . > /dev/null 2>&1 || {
    echo "Failed to build Docker image"
    exit 1
}
echo "Docker image built."
echo ""

PARALLEL=$(detect_parallel_capacity)

echo "=== Resource Detection ==="
echo "Memory per container: ${MEMORY_PER_CONTAINER}GB"
echo "CPUs per container: ${CPUS_PER_CONTAINER}"
echo "Max parallel jobs: $PARALLEL"
echo ""

SUMMARY_FILE="$RUN_DIR/summary.csv"
echo "nodes,replicated,replication_time_seconds,sync_interests,data_packets,max_replication,over_replicated,under_replicated,final_count" > "$SUMMARY_FILE"

batch=()
count=0
for n in $NODE_COUNTS; do
    batch+=($n)
    count=$((count + 1))
    
    if [ $count -ge $PARALLEL ]; then
        run_batch "${batch[@]}"
        batch=()
        count=0
    fi
done

if [ ${#batch[@]} -gt 0 ]; then
    run_batch "${batch[@]}"
fi

echo ""
echo "=========================================="
echo "         EXPERIMENT RESULTS SUMMARY"
echo "=========================================="
echo ""
echo "Config: Replication factor = $REPLICATION_FACTOR"
echo ""
printf "%-6s %-10s %-10s %-12s %-12s %-8s %-12s %-12s\n" "Nodes" "Replicated" "Time(s)" "SyncInt" "DataPkt" "MaxRep" "OverRep" "UnderRep"
echo "-----------------------------------------------------------------------------------------"
while IFS=',' read -r nodes replicated rep_time sync_int data_pkt max_rep over_rep under_rep final_count; do
    if [ "$nodes" != "nodes" ]; then
        printf "%-6s %-10s %-10s %-12s %-12s %-8s %-12s %-12s\n" "$nodes" "$replicated" "$rep_time" "$sync_int" "$data_pkt" "$max_rep" "$over_rep" "$under_rep"
    fi
done < "$SUMMARY_FILE"
echo ""
echo "Results directory: $RUN_DIR"
echo "CSV file: $SUMMARY_FILE"

RESULTS_MD="$RUN_DIR/RESULTS.md"
cat > "$RESULTS_MD" << 'HEADER'
# Experiment Results

HEADER

cat >> "$RESULTS_MD" << EOF
**Run:** $(date -d "${TIMESTAMP:0:8} ${TIMESTAMP:9:2}:${TIMESTAMP:11:2}:${TIMESTAMP:13:2}" '+%Y-%m-%d %H:%M:%S' 2>/dev/null || echo "$TIMESTAMP")

## Configuration

| Parameter | Value |
|-----------|-------|
| Node counts | $NODE_COUNTS |
| Replication factor | $REPLICATION_FACTOR |
| Producer count | $PRODUCER_COUNT |
| Commands per producer | $COMMAND_COUNT |
| Command rate | $COMMAND_RATE cmds/sec |
| Expected claims per run | $((PRODUCER_COUNT * COMMAND_COUNT * REPLICATION_FACTOR)) |

## Results

| Nodes | Replicated | Time (s) | Sync Int | Data Pkt | Max Rep | Over Rep | Under Rep | Final Count |
|-------|------------|----------|----------|----------|---------|----------|-----------|-------------|
EOF

while IFS=',' read -r nodes replicated rep_time sync_int data_pkt max_rep over_rep under_rep final_count; do
    if [ "$nodes" != "nodes" ]; then
        echo "| $nodes | $replicated | $rep_time | $sync_int | $data_pkt | $max_rep | $over_rep | $under_rep | $final_count |" >> "$RESULTS_MD"
    fi
done < "$SUMMARY_FILE"

cat >> "$RESULTS_MD" << EOF

## Per-Experiment Details

EOF

for n in $NODE_COUNTS; do
    metadata_file="$RUN_DIR/nodes_${n}/metadata.json"
    if [ -f "$metadata_file" ]; then
        cat >> "$RESULTS_MD" << EOF
### $n Nodes

\`\`\`json
$(python3 -c "import json; d=json.load(open('$metadata_file')); del d['timeline']; del d['nodes']; print(json.dumps(d, indent=2))" 2>/dev/null || echo "Error reading metadata")
\`\`\`

EOF
    fi
done

echo "Results markdown: $RESULTS_MD"
