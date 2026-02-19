# LLM Context: NDN Distributed Repository

## Current Session Status

### Working
- **All integration tests PASS**:
  - `TestLocalReplication_FiveNodes` - 5 nodes, 3 claims (~26s)
  - `TestNodeUpdateExchange` - 3 nodes, verifies SVS heartbeat sync (~24s)
  - `TestCommandPropagation` - 3 nodes, verifies command propagation via SVS (~55s)
  - `TestMiniNDNIntegration` - Docker-based 5-node testbed (~43s)
  - `TestMiniNDNIntegration_CustomNodeCount` - Configurable node count via `NODE_COUNT` env var
- **Experiment runner**: `experiments/run_experiments.sh` runs tests with multiple node counts
- **Unit tests**: All replication logic tests pass

### Recent Changes (This Session)
- Added `TestMiniNDNIntegration_CustomNodeCount` for configurable node count tests
- Updated `runner.py` to track replication timeline and detect over/under-replication
- Added `run_experiments.sh` for running multi-node experiments
- New metrics: `max_replication`, `over_replicated`, `under_replicated`
- Packet stats now summed across all nodes (not max)

### Fixed This Session
- **Race condition**: NFD route registration race fixed with 500ms staggered repo starts
- **SVS sync failure**: Caused by routes not being registered before repos started communicating

## System
Distributed NDN repo with resilient storage. Replication factor (rf)=3, node detection via 3 missed heartbeats (15s timeout). Go 1.25.6.

## Key Files
| Path | Purpose |
|------|---------|
| `repo/repo.go` | Main repo impl (~680 lines) |
| `repo/main.go` | Entry point, flag parsing |
| `repo/integration_test.go` | Integration tests (local, 3-5 nodes) |
| `repo/mini_ndn_integration_test.go` | Docker mini-ndn tests (including CustomNodeCount) |
| `repo/repo_test.go` | Unit tests |
| `repo/testutil/testutil.go` | Test utilities for event parsing |
| `repo/util/event_log.go` | Event logging with replication decisions |
| `tlv/definitions.go` | TLV structs (45 lines) |
| `tlv/zz_generated.go` | Generated TLV code |
| `producer/producer.go` | Command sender |
| `experiments/runner.py` | Docker runner for mini-ndn (tracks over/under-replication) |
| `experiments/run_experiments.sh` | Multi-node experiment runner |

## Event Logging

### Event Types
| Event | Description | Key Fields |
|-------|-------------|------------|
| `sync_interest_sent` | SVS sync interest | `total` |
| `data_sent` | Data packet sent | `name`, `total` |
| `command_received` | Command received | `type`, `target` |
| `job_claimed` | Job claimed | `target`, `replication` (node's local view) |
| `job_released` | Job released | `target` |
| `node_update` | Node status received | `from`, `jobs`, `capacity`, `used` |
| `replication_check` | Replication decision | See below |
| `storage_changed` | Storage updated | `used`, `delta` |

### Replication Decision Logging
`replication_check` events now include full decision details:

```json
{
  "event": "replication_check",
  "target": "/ndn/target/...",
  "shouldClaim": false,
  "reason": "not_selected",
  "currentReplication": 0,
  "neededReplication": 3,
  "candidates": ["/ndn/repo/c", "/ndn/repo/d", "/ndn/repo/e", "/ndn/repo/a", "/ndn/repo/b"],
  "selectedCandidates": ["/ndn/repo/c", "/ndn/repo/d", "/ndn/repo/e"],
  "freeSpace": {"/ndn/repo/a": 5e9, "/ndn/repo/b": 4.8e9, ...}
}
```

**Reasons:**
- `replication_satisfied`: Already have rf nodes doing the job
- `selected_as_candidate`: This node is in top N by free space
- `not_selected`: This node is not in top N

### Global Replication Analysis
Use test utilities to compute oracle view from all logs:

```go
// True replication count at a point in time
count := testutil.ComputeGlobalReplicationAtTime(allEvents, target, timestamp)

// Full timeline of replication changes
timeline := testutil.ComputeGlobalReplicationTimeline(allEvents, target)
```

## Bugs Fixed This Session

### 1. `onGroupSync()` not handling `NewCommand` field
- **Location**: `repo/repo.go:518-564`
- **Problem**: Sync updates were received but `update.NewCommand` was ignored
- **Fix**: Added handling for `NewCommand` and `JobRelease` fields in `onGroupSync()`

### 2. Replication candidate sorting bug
- **Location**: `repo/repo.go:605-662`
- **Problem**: "mine" was compared with node prefixes like "/ndn/repo/afa", causing inconsistent sorting
- **Fix**: Now uses actual node prefix string (`myPrefix`) for consistent sorting

### 3. Signing identity vs node prefix confusion
- **Location**: `repo/repo.go`, `repo/main.go`
- **Problem**: All repos used same prefix, and signing identity needed to match keychain keys
- **Fix**: Split into `--node-prefix` (unique per node) and `--signing-identity` (matches `/ndn/repo.teame.dev/repo` key)

### 4. "mine" node status had zero free space
- **Location**: `repo/repo.go:307-309` (initialization), `repo/repo.go:317-323` (Start)
- **Problem**: Node's own status was stored under key "mine" with only `Jobs` initialized, but `Capacity` and `Used` were 0. This caused `FreeSpace()` to return 0, making every node sort itself LAST in candidate selection.
- **Fix**: 
  - Changed key from "mine" to actual node prefix string for consistency
  - Added `myNodeName()` helper method
  - Initialize `Capacity` and `Used` in `Start()` after storage stats are computed
  - Updated all references to use actual node name

### 5. Mini-NDN routing using manual nfdc commands
- **Location**: `experiments/runner.py`
- **Problem**: Manual `nfdc face create` and `nfdc route add` commands were unreliable
- **Fix**: Replaced with `NdnRoutingHelper` from mini-ndn, which computes routes centrally and installs FIB entries properly. Added origins for `/ndn/drepo`, `/ndn/drepo/ndn`, `/ndn/repo/<node>`, `/ndn/drepo/ndn/repo/<node>`

### 6. Multicast forwarding for local test
- **Fix**: `nfdc strategy set /ndn/drepo /localhost/nfd/strategy/multicast`

### 7. NFD route registration race condition (THIS SESSION)
- **Location**: `repo/integration_test.go`
- **Problem**: When multiple repos started simultaneously, NFD rejected some prefix registrations with "error 403: authorization rejected". This caused SVS sync to fail because some repos couldn't register their data prefixes.
- **Fix**: 
  - Add 500ms delay between starting each repo
  - Add 3s routing convergence wait after setting multicast strategy
  - This ensures all FIB entries are registered before SVS communication begins

## Core Functions (repo.go)
- `onCommand()` - Handle incoming commands, trigger replication
- `onGroupSync()` - Handle sync updates, process NewCommand and JobRelease
- `replicate()` - Execute replication logic
- `shouldClaimJobHydra()` - Determine if node should claim job (sorted by free space)
- `claimJob()` - Claim job, update storage
- `publishCommand()` - Broadcast new command via group sync
- `publishNodeUpdate()` - Broadcast node status via group sync
- `runHeartbeat()` - 5s periodic status broadcast
- `runHeartbeatMonitor()` - Detect offline nodes (3 missed beats)
- `runStorageSimulation()` - Deterministic growth for JOIN commands
- `myNodeName()` - Returns the node's own prefix string for consistent status lookups

## CLI Flags (repo/main.go)
```bash
--event-log <path>         # Event log file (default: events.jsonl)
--node-prefix <prefix>     # Unique node prefix (e.g., /ndn/repo/local/a)
--signing-identity <name>  # Signing identity for keychain (default: /ndn/repo.teame.dev/repo)
--debug                    # Enable debug logging
```

## TLV Types (0x prefix)
| Type | Code | Field |
|------|------|-------|
| Type | 252 | Command type string |
| Target | 253, 280 | Command target name |
| SnapshotThreshold | 255 | Threshold value |
| StorageSpace | 294 | Storage size |
| Status | 281 | Response status |
| Jobs | 290 | Job name list |
| NewCommand | 291 | New command |
| StorageCapacity | 292 | Total capacity |
| StorageUsed | 293 | Used capacity |
| JobRelease | 294 | Job release signal |

## Structs
```
Repo: groupPrefix, notifyPrefix, nodePrefix, signingIdentity, engine, store, client, groupSync, nodeStatus, commands, storageCapacity, storageUsed, rf, eventLogger

Command: Type, Target, SnapshotThreshold
InternalCommand: Type, Target, SnapshotThreshold, StorageSpace (job release)
StatusResponse: Target, Status
NodeUpdate: Jobs, NewCommand, StorageCapacity, StorageUsed, JobRelease
NodeStatus: Capacity, Used, LastUpdated, Jobs, MissingHeartbeats
```

## Data Flow
```
Producer --[Command]--> Repo.onCommand() --> addCommand() + publishCommand() + replicate()
                                                    |
                              GroupSync <--[NodeUpdate with NewCommand]--> Other nodes
                                                    |
                                              onGroupSync() --> addCommand() --> replicate()
                                                    |
                                              shouldClaimJobHydra() --> claimJob()
```

## How to Run Tests

```bash
# All integration tests
go test -v ./repo -run "TestNodeUpdateExchange|TestCommandPropagation|TestLocalReplication" -timeout 3m

# Mini-NDN test (requires Docker)
docker rmi mini-ndn-integration  # Force rebuild
go test -v ./repo -run TestMiniNDNIntegration -timeout 2m

# Custom node count test
NODE_COUNT=10 go test -v ./repo -run TestMiniNDNIntegration_CustomNodeCount -timeout 30m

# Multi-node experiments
./experiments/run_experiments.sh

# Unit tests
go test -v ./repo -run "TestRepo_" -timeout 30s
```

## Important Commands

```bash
# Set multicast strategy
nfdc strategy set /ndn/drepo /localhost/nfd/strategy/multicast

# Check FIB entries
nfdc fib list

# Rebuild Docker image
docker rmi mini-ndn-integration
go test -v ./repo -run TestMiniNDNIntegration -timeout 2m

# Kill hanging processes
pkill -9 -f "repo --event-log"
```

## Build
```
go build ./repo/...  # ✓
go vet ./repo/...    # ✓
gofmt -w ./repo/     # ✓
```
