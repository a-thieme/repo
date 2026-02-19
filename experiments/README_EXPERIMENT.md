# Mini-NDN Integration Tests

Integration tests for the distributed NDN repository using mini-ndn in Docker.

## Files

| File | Purpose |
|------|---------|
| `Dockerfile.integration` | Docker image with mini-ndn + Go binaries |
| `runner.py` | Python script executed inside Docker |
| `run_experiments.sh` | Multi-node experiment runner |
| `testbed_topology.conf` | 24-node testbed topology |

## Running Tests

### Single Test (Recommended for development)

```bash
# Standard test (5 nodes, ~3 min)
go test -v ./repo -run TestMiniNDNIntegration -timeout 10m

# Custom node count
NODE_COUNT=10 go test -v ./repo -run TestMiniNDNIntegration_CustomNodeCount -timeout 30m

# Full topology (24 nodes, ~8 min)
RUN_FULL_TOPOLOGY=1 go test -v ./repo -run TestMiniNDNIntegration_FullTopology -timeout 15m
```

### Multi-Node Experiments

Run experiments with multiple node counts. The script automatically detects available system resources and runs experiments in parallel:

```bash
# Default: 5, 10, 15, 20, 24 nodes (auto-parallel)
./experiments/run_experiments.sh

# Custom node counts
NODE_COUNTS="5 8 12" ./experiments/run_experiments.sh

# Custom replication factor
REPLICATION_FACTOR=5 ./experiments/run_experiments.sh

# Multiple producers with multiple commands
PRODUCER_COUNT=2 COMMAND_COUNT=5 COMMAND_RATE=10 ./experiments/run_experiments.sh
```

Results are saved to `experiment_results/run_<ts>_p<producers>_c<commands>_r<rate>_rf<rf>/`:

Example: `run_20260219_143052_p2_c5_r10_rf3/` = 2 producers, 5 commands each, 10 cmds/sec, replication factor 3

Contents:
- `summary.csv` - Results in CSV format
- `nodes_<n>/metadata.json` - Full metadata for each run
- `nodes_<n>/docker.log` - Docker output log

### Resource Allocation

Each experiment uses:
- **4 GB RAM** per container
- **4 CPUs** per container

The script detects available resources and calculates max parallel jobs:
```
Max parallel = min(available_memory_GB / 4, available_CPUs / 4)
```

| System | Max Parallel | 5 Experiments Runtime |
|--------|--------------|----------------------|
| 8GB, 4 CPU | 1 (sequential) | ~12 min |
| 16GB, 8 CPU | 2 | ~6 min |
| 32GB, 16 CPU | 4 | ~3 min |
| 64GB, 32 CPU | 8 | ~1.5 min |

### Optimizations

1. **Smart SVS Health Detection**: Instead of waiting a fixed 15s for heartbeat exchange, the runner polls event logs and proceeds immediately when all nodes have received updates from all peers.

2. **Parallel Execution**: Multiple experiments run simultaneously in separate Docker containers, each with isolated resources.

3. **Reduced Timeout**: Fixed 30s timeout (down from 60-140s) since replication typically completes in <1s.

## Test Phases

1. **Build**: Create Docker image with Go binaries
2. **Setup**: Start mini-ndn topology, NFD, NLSR
3. **Deploy**: Run `repo` on each node with event logging
4. **Test**: Run producer(s) to send commands at specified rate
5. **Evaluate**: Parse event logs, compute metrics

## Evaluation Metrics

| # | Metric | Source | Description |
|---|--------|--------|-------------|
| 0 | Command replicated | `job_claimed` events >= expected | Final replication success |
| 1 | Replication time | `last_claim.ts - first_command.ts` | Time to reach final replication |
| 2 | Sync interests | Sum of `sync_interest_sent.total` | Total sync interests (all nodes) |
| 3 | Data packets | Sum of `data_sent.total` | Total data packets (all nodes) |
| 4 | Max replication | Timeline analysis | Peak number of nodes with job |
| 5 | Over-replicated | `max_replication > rf` | Did claims exceed target? |
| 6 | Under-replicated | `final_replication < rf` | Did claims fall short? |

Expected claims = `producer_count × command_count × replication_factor`

## Output Files

| File | Contents |
|------|----------|
| `events-{node}.jsonl` | Per-node event logs |
| `metadata.json` | Test config + all metrics |
| `test_results.json` | Final evaluation metrics |
| `summary.csv` | Experiment results summary |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `NODE_COUNT` | - | Node count for single test |
| `NODE_COUNTS` | `5 10 15 20 24` | Node counts for experiment runner |
| `REPLICATION_FACTOR` | `3` | Replication factor for experiments |
| `PRODUCER_COUNT` | `1` | Number of producers (runs on first N nodes) |
| `COMMAND_COUNT` | `1` | Commands sent per producer |
| `COMMAND_RATE` | `1` | Commands per second per producer |

## Requirements

- Docker (with `--privileged` support)
- 4GB+ RAM
- 4+ CPUs

## Troubleshooting

```bash
# Clean up Docker resources
docker system prune -f

# Rebuild image (after code changes)
docker build -t mini-ndn-integration -f experiments/Dockerfile.integration .

# Manual run inside container
docker run -it --privileged -v /lib/modules:/lib/modules mini-ndn-integration bash

# Kill hanging Docker containers
docker ps -q | xargs -r docker stop

# Check Docker resource limits
docker stats --no-stream
```
