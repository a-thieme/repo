# LLM Context File

**PURPOSE:** Read this at the start of each session to understand the codebase quickly. Update this file after learning new important details, but keep it concise to minimize context usage.

## System Overview

Distributed NDN repository with resilient storage. Uses State Vector Sync (SVS) for group communication. Replication factor (rf)=3 by default. Node detection via 3 missed heartbeats (~15s timeout). Go 1.25.6, uses `ndnd` SDK.

## Key Files

| Path | Purpose |
|------|---------|
| `repo/repo.go` | Main repo impl (~865 lines) |
| `repo/main.go` | Entry point, flag parsing |
| `repo/timeouts.go` | Timeout constants with CLI flags |
| `repo/integration_test.go` | Local integration + concurrent + storage tests |
| `repo/integration_failure_test.go` | Failure recovery + cascading + edge case tests |
| `repo/mini_ndn_integration_test.go` | Docker mini-ndn tests |
| `repo/util/event_log.go` | Event logging |
| `repo/util/counting_face.go` | Packet counting (supports LpPacket) |
| `tlv/definitions.go` | TLV struct definitions (~53 lines) |
| `producer/producer.go` | Command sender |
| `experiments/runner.py` | Docker orchestrator for mini-ndn |
| `experiments/Makefile` | Experiment runner interface |
| `scripts/run_all_experiments.sh` | Run all tests and produce summary |
| `TEST_RESULTS.md` | Latest test results documentation |

## Event Types

| Event | Description | Key Fields |
|-------|-------------|------------|
| `sync_interest_sent` | SVS sync interest | `total` |
| `data_sent` | Data packet served | `name`, `total` |
| `command_received` | Command received | `type`, `target` |
| `job_claimed` | Job claimed | `target`, `replication` |
| `job_released` | Job released | `target` |
| `node_update` | Node status received | `from`, `jobs`, `capacity`, `used` |
| `replication_check` | Replication decision | `shouldClaim`, `reason`, `candidates`, `selectedCandidates`, `freeSpace` |
| `storage_changed` | Storage updated | `used`, `delta` |

## Core Functions (repo.go)

- `onCommand()` - Handle incoming commands, calculate winners, publish with JobAssignment
- `onGroupSync()` - Handle sync updates, process NewCommand, JobRelease, JobAssignments
- `determineWinnersHydra()` - Select nodes for job (sorted by free space, top N)
- `handleJobAssignment()` - Process incoming JobAssignment, claim or publish disagreement
- `claimJob()` - Claim job, update storage
- `publishCommand()` / `publishNodeUpdate()` / `publishJobAssignments()` - Broadcast via SVS
- `runHeartbeat()` - Periodic status broadcast (default 5s)
- `runHeartbeatMonitor()` - Detect offline nodes, trigger re-replication
- `runStorageSimulation()` - Deterministic growth for JOIN commands

## CLI Flags

**Repo:**
```bash
--event-log <path>           # Event log file (default: events.jsonl)
--node-prefix <prefix>       # Unique node prefix (e.g., /ndn/repo/local/a)
--signing-identity <name>    # Signing identity (default: /ndn/repo.teame.dev/repo)
--heartbeat-interval <dur>   # Heartbeat interval (default: 5s)
--no-release                 # Disable automatic job release at 75% capacity
--max-join-growth-rate <n>   # Max JOIN storage growth/sec (default: 10MB)
--debug                      # Enable debug logging
```

**Producer:**
```bash
--count <n>           # Number of commands (default: 1)
--rate <n>            # Commands per second (default: 1)
--timeout <dur>       # Response timeout (default: 10s)
--type <type>         # Command type: insert, join, or both (default: insert)
--join-ratio <ratio>  # JOIN ratio when type=both (default: 0.5)
```

## Data Flow

```
Producer --[Command]--> Repo.onCommand() 
    --> addCommand() + determineWinnersHydra() + publishCommand(winners)
    --> GroupSync <--[NodeUpdate with JobAssignment]--> Other nodes
    --> onGroupSync() --> handleJobAssignment()
    --> If I'm assignee and should claim --> claimJob()
    --> If I'm assignee but disagree --> publishJobAssignments(my view)
```

## TLV Types (0x prefix)

| Type | Code | Field |
|------|------|-------|
| Command Type | 252 | Command type string |
| Target | 253, 280, 295 | Command target name |
| SnapshotThreshold | 255 | Threshold value |
| Status | 281 | Response status |
| Jobs | 290 | Job name list |
| NewCommand | 291 | New command struct |
| StorageCapacity | 292 | Total capacity |
| StorageUsed | 293 | Used capacity |
| JobRelease | 294 | InternalCommand for release |
| StorageSpace | 294 | Storage size in InternalCommand |
| JobAssignment Assignees | 296 | List of assignee names |
| JobAssignments | 297 | List of JobAssignment structs |

## Important Commands

```bash
# Set multicast strategy for SVS (MUST use keyword component prefix!)
nfdc strategy set /ndn/drepo/group-messages/32=svs /localhost/nfd/strategy/multicast

# Set best-route for notify
nfdc strategy set /ndn/drepo/notify /localhost/nfd/strategy/best-route

# Run experiments
make -C experiments calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=3
make -C experiments run NODE_COUNTS="24" PRODUCER_COUNTS="1 2 4"

# Run tests
make test-short       # Quick unit tests (~30s)
make test-unit        # Unit tests only (no NFD)
make test-integration # Local NFD integration tests
make test-concurrent  # Concurrent command tests
make test-storage     # Storage pressure tests
make test-edge        # RF edge cases (RF=1, RF=nodes) + node join
make test-failure     # Failure recovery tests
make test-all-local   # All local NFD tests (no Docker)
make test-mini-ndn    # Docker mini-ndn tests
./scripts/run_all_experiments.sh  # Run all + produce summary

# Kill hanging processes
pkill -9 -f "repo --event-log"
```

## Known Issues

1. **Failure recovery not implemented** - `TestFailureRecovery_*` tests fail because surviving nodes don't re-claim jobs when a node dies. The `runHeartbeatMonitor()` detects offline nodes but doesn't trigger re-replication.

2. **Flaky initial replication** - Sometimes initial replication fails with 0 claims due to SVS sync timing. Consider increasing `--svs-timeout`.

## Build Verification

```bash
go build ./repo/...  # Build
go vet ./repo/...    # Static analysis
gofmt -w ./repo/     # Format
```
