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
| `calibrate` | Run timeout calibration | `make calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=3` |
| `run` | Run experiment suite | `make run NODE_COUNTS="5 24" -j4` |
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
| `CALIBRATE_ITER` | `3` | Number of calibration iterations |

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

# Parallel execution (2 experiments at a time)
make -C experiments run -j2
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

### Event Types

| Event | Description |
|-------|-------------|
| `command_received` | Command received directly from producer |
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

## Failure Simulation Tests

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
