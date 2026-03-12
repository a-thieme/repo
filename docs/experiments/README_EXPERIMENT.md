# Mini-NDN Integration Tests

Integration tests for the distributed NDN repository using mini-ndn in Docker.

## Files

| File | Purpose |
|------|---------|
| `Makefile` | Main interface for experiments |
| `helpers.py` | Result collection and calibration analysis |
| `runner.py` | Python script executed inside Docker |
| `Dockerfile.integration` | Docker image with mini-ndn + Go binaries |
| `testbed_topology.conf` | 24-node testbed topology |

## Quick Start

```bash
# Build binaries and Docker image
make -C experiments build

# Calibrate timeouts (recommended first run)
make -C experiments calibrate

# Run experiment suite
make -C experiments run

# View results
make -C experiments results
```

## Make Targets

| Target | Description | Example |
|--------|-------------|---------|
| `build` | Build binaries + Docker image | `make build` |
| `calibrate` | Run timeout calibration | `make calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=5` |
| `run` | Run experiment suite | `make run NODE_COUNTS="5 24" -j5` |
| `single` | Run single experiment | `make single NODES=5 PRODUCERS=1` |
| `shell` | Interactive Docker shell | `make shell NODES=5` |
| `results` | Show last run summary | `make results` |
| `clean` | Remove results (keep calibration) | `make clean` |
| `clean-all` | Remove all + Docker cleanup | `make clean-all` |
| `help` | Show all targets and variables | `make help` |

## Configuration Variables

### Experiment Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `NODE_COUNTS` | `24` | Space-separated node counts for `run` |
| `PRODUCER_COUNTS` | `1 2 4 8 16 24` | Space-separated producer counts for `run` |
| `RF` | `3` | Replication factor |
| `COMMAND_COUNT` | `1` | Commands per producer |
| `COMMAND_TYPE` | `insert` | Command type: `insert`, `join`, or `both` |
| `TIMEOUT` | `120` | Timeout per experiment (seconds) |

### Calibration Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `CALIBRATE_NODES` | `24` | Node count for calibration |
| `CALIBRATE_ITER` | `5` | Number of calibration iterations |

### Single Experiment Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `NODES` | `24` | Node count for `single` |
| `PRODUCERS` | `1` | Producer count for `single` |

### Docker Settings

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKER_MEM` | `16g` | Memory per container |
| `DOCKER_CPUS` | `8` | CPUs per container |
| `DEBUG` | `false` | Enable debug logging |

## Running Experiments

### Step 1: Build

```bash
make -C experiments build
```

This compiles Go binaries and builds the Docker image.

### Step 2: Calibrate Timeouts (Recommended First Run)

```bash
# Default: 24 nodes, 3 iterations
make -C experiments calibrate

# Custom configuration
make -C experiments calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=3
```

Calibration measures realistic timeouts and outputs recommendations:
- SVS convergence time
- Replication time
- Update propagation time

Results are saved to `experiments/results/calibration/`.

### Step 3: Run Experiments

```bash
# Default: 24 nodes, producers 1 2 4 8 16 24
make -C experiments run

# Custom configuration
make -C experiments run NODE_COUNTS="5 24" PRODUCER_COUNTS="1 4"

# Parallel execution (5 experiments at a time, 3.5x faster)
make -C experiments run -j5
```

### Single Experiment

```bash
make -C experiments single NODES=5 PRODUCERS=1
```

### Interactive Debugging

```bash
make -C experiments shell NODES=5
```

This starts an interactive Docker container with mini-ndn running.

## Results

Results are saved to `experiments/results/run_<timestamp>/`:

| File | Contents |
|------|----------|
| `summary.csv` | CSV summary of all experiments |
| `RESULTS.md` | Human-readable results summary |
| `nodes_<n>_producers_<p>/metadata.json` | Full metadata for each run |
| `nodes_<n>_producers_<p>/events-*.jsonl` | Per-node event logs |
| `nodes_<n>_producers_<p>/command_timelines/` | Per-command timeline JSON files |

### View Results

```bash
make -C experiments results
```

### Evaluation Metrics

| # | Metric | Description |
|---|--------|-------------|
| 0 | Total commands | Number of commands processed |
| 1 | Commands at RF | Commands ending at target replication |
| 2 | Commands over | Commands ending over-replicated |
| 3 | Commands under | Commands ending under-replicated |
| 4 | Replication time | Time from command received to RF claims |
| 5 | Update propagation | Time for claims to propagate to all nodes |

### Statistics

Each run includes detailed timing statistics in `metadata.json`:

- **Min/Max/Avg**: Basic statistics
- **Median**: 50th percentile
- **p95**: 95th percentile (useful for timeout calibration)
- **p99**: 99th percentile (recommended for safe timeout settings)

Example: `replication_time_p95_ms`, `replication_time_p99_ms`, `update_propagation_p95_ms`

### Per-Command Timelines

Each command's full lifecycle is captured in `command_timelines/{sanitized_target}.json`:

```bash
# View timeline for a specific command
cat results/run_001/nodes_24_producers_1/command_timelines/ndn_col_0_1699999999.json
```

Each timeline contains:
- `commandId`: The command target name
- `eventCount`: Number of events
- `events`: All events for this command, sorted by timestamp

### Event Types

| Event | Description |
|-------|-------------|
| `command_received` | Command received directly from producer |
| `command_synced` | Command received via SVS from another node |
| `command_published` | Command published to SVS for replication |
| `decision_started` | Node started processing replication decision |
| `decision_made` | Node completed replication decision with full reasoning |
| `job_claimed` | Node claimed a job for a target |
| `job_released` | Node released a job |
| `node_update` | Received node status update via SVS |
| `sync_interest_sent` | SVS sync interest sent |
| `data_sent` | Data packet sent |

## Resource Allocation

Each experiment uses:
- **16 GB RAM** per container
- **8 CPUs** per container

Use `-j` flag for parallel execution:

| System | Recommended | Example |
|--------|-------------|---------|
| 8GB, 4 CPU | Sequential | `make run` |
| 16GB, 8 CPU | 1-2 parallel | `make run -j2` |
| 32GB, 16 CPU | 2-4 parallel | `make run -j4` |

## Troubleshooting

```bash
# Clean up Docker resources
make -C experiments clean-all

# Rebuild image (after code changes)
make -C experiments build

# Manual run inside container
make -C experiments shell NODES=5

# Kill hanging Docker containers
docker ps -q | xargs -r docker stop
```

## Failure Simulation (Mini-NDN)

The experiment runner supports simulating node failures to test recovery. Use the failure flags with the `single` target:

```bash
# Kill 1 node with most jobs after replication (auto-selects node)
make single NODES=24 PRODUCERS=1 COMMAND_COUNT=20 FAILURE_COUNT=1

# Kill specific node after 5 seconds
make single NODES=24 PRODUCERS=1 COMMAND_COUNT=20 FAILURE_COUNT=1 FAILURE_WAIT=5 FAILURE_NODES=wu

# Kill 2 nodes with most jobs
make single NODES=24 PRODUCERS=1 COMMAND_COUNT=20 FAILURE_COUNT=2

# Run multiple experiments with failure simulation
make run NODE_COUNTS=24 PRODUCER_COUNTS=1 COMMAND_COUNT=20 FAILURE_COUNT=1 -j2
```

### Failure Flags

| Variable | Default | Description |
|----------|---------|-------------|
| `FAILURE_COUNT` | 0 | Number of repos to kill (0 = no failure) |
| `FAILURE_NODES` | (auto) | Comma-separated node names to kill (default: nodes with most claims) |
| `FAILURE_WAIT` | 0 | Seconds to wait after replication before killing |
| `FAILURE_RECOVERY_TIMEOUT` | 30 | Timeout for recovery in seconds |

### Output Metrics

When failure simulation is enabled, results include:

| Field | Description |
|-------|-------------|
| `failure_enabled` | Whether failure was simulated |
| `failure_count` | Number of nodes killed |
| `failure_nodes` | List of killed node names |
| `pre_failure_commands_at_rf` | Commands at RF before failure |
| `pre_failure_commands_under` | Commands below RF before failure |
| `pre_failure_affected_commands` | Commands that had claims on killed nodes |
| `recovery_achieved` | Whether all affected commands recovered to RF |
| `recovery_time_ms` | Time for recovery in milliseconds |
| `recovery_commands_recovered` | Number of commands that recovered |
| `recovery_commands_lost` | Number of commands that couldn't recover |
| `post_failure_commands_at_rf` | Commands at RF after recovery |
| `post_failure_commands_under` | Commands below RF after recovery |

## Local Failure Tests

The `repo/integration_failure_test.go` file contains tests that simulate repo failures and measure recovery time.

```bash
# Run with defaults (5 nodes, RF=3, kill 1 repo)
go test ./repo -run TestFailureRecovery -v

# Custom configuration
go test ./repo -run TestFailureRecovery -v \
  -failure-nodes 7 \
  -failure-rf 3 \
  -failure-count 2
```

## Requirements

- Docker (with `--privileged` support)
- 4GB+ RAM
- 4+ CPUs
