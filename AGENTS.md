# AGENTS.md - NDN Distributed Repository

## Project Overview

This is a Go project implementing a distributed Named Data Networking (NDN) data repository. It consists of three main components:

- **repo**: Main repository that manages storage commands, distributes jobs across nodes
- **producer**: Sends commands to the repository
- **tlv**: Defines TLV data structures for command/node communication

## Build Commands

```bash
# Build binaries
make build              # Builds ./bin/repo and ./bin/producer
make clean              # Remove built binaries
go fmt ./...            # Format all Go code before committing

# All tests
make test               # Run all tests (5m timeout)
make test-short         # Quick tests only (~30s, skips integration/failure)

# Specific test categories
make test-unit          # Unit tests only (no NFD required)
make test-integration   # Integration tests (requires NFD running)
make test-failure       # Failure recovery tests (requires NFD)
make test-concurrent    # Concurrent command tests
make test-multi         # Multiple sequential commands
make test-edge          # Edge case tests
make test-timing        # Timing calibration (requires Docker/mini-ndn)
make test-mini-ndn      # Mini-NDN Docker tests

# Run a single test manually
go test -v -run 'TestEventLogger_WriteAndParse' -timeout 30s ./repo/...
go test -v -run 'TestFailureRecovery' -timeout 5m ./repo/...
```

### Experiment Commands (in experiments/)

```bash
# Build + copy keys + build Docker image
make -C experiments build

# Run timeout calibration (outputs to experiments/results/calibration/)
# Use CALIBRATE_ITER=5 for more accurate measurements
make -C experiments calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=5

# Run single experiment
make -C experiments single NODES=24 PRODUCERS=1

# Run single experiment with failure simulation (kill 1 node after replication)
make -C experiments single NODES=24 PRODUCERS=1 COMMAND_COUNT=20 FAILURE_COUNT=1

# Run experiment suite (use -j5 for parallel - 3.5x faster, identical results)
make -C experiments run NODE_COUNTS="5 24" PRODUCER_COUNTS="1 4" -j5

# Interactive debugging shell
make -C experiments shell NODES=5
```

## Code Style Guidelines

### General

- Language: Go 1.25.6
- Format: Run `go fmt` before committing
- No linter config found - follow standard Go conventions

### Imports

Group imports in this order with blank lines between:

1. Standard library (`slices`, `sync`, `time`, etc.)
2. External packages (`github.com/a-thieme/repo/...`)
3. NDN packages (`github.com/named-data/ndnd/...`)

```go
import (
    "slices"
    "sync"
    "time"

    "github.com/a-thieme/repo/repo/util"
    "github.com/a-thieme/repo/tlv"

    enc "github.com/named-data/ndnd/std/encoding"
    "github.com/named-data/ndnd/std/engine"
    "github.com/named-data/ndnd/std/log"
)
```

### Naming Conventions

- **Types**: PascalCase (e.g., `Repo`, `NodeStatus`, `EventLogger`)
- **Struct fields**: camelCase, unexported (e.g., `groupPrefix`, `storageCapacity`)
- **Functions**: camelCase (e.g., `myNodeName()`, `amIDoingJob()`)
- **Constants**: UPPER_SNAKE_CASE or PascalCase (e.g., `NOTIFY`, `DEFAULT_HEARTBEAT_INTERVAL`)
- **Interfaces**: PascalCase, often with `er` suffix if simple (e.g., `Signer`)
- **Acronyms**: Keep uppercase in names (e.g., `encodeXML`, not `encodeXml`)

### Types and Structs

- Use struct composition for configuration
- Embed interfaces for delegation when appropriate
- Use explicit field names; avoid anonymous fields unless embedding

```go
type Repo struct {
    groupPrefix     enc.Name
    notifyPrefix    *enc.Name
    nodePrefix      enc.Name
    engine          ndn.Engine
    mu              sync.Mutex
    nodeStatus      map[string]NodeStatus
    commands        map[string]*tlv.Command
}
```

### Error Handling

- Return errors from functions when possible
- Use `log.Fatal()` for fatal errors that should stop the program
- Use `log.Warn()` for non-fatal issues that should be logged
- Check errors immediately after calls

```go
if err != nil {
    return err
}

if err := r.engine.Start(); err != nil {
    return err
}
```

### Mutex and Concurrency

- Always use `defer r.mu.Unlock()` after `Lock()`
- Consider `sync.RWMutex` for read-heavy operations
- Document if a function requires the lock to be held

```go
func (r *Repo) getStorageStats() (capacity uint64, used uint64) {
    r.mu.Lock()
    defer r.mu.Unlock()
    return r.storageCapacity, r.storageUsed
}
```

### Testing

- Test files: `*_test.go` in same package
- Use `testing.T` methods: `t.Fatalf()`, `t.Errorf()`, `t.Skip()`
- Use `t.TempDir()` for temporary test files
- Name test functions: `Test<Component>_<Behavior>`

```go
func TestEventLogger_WriteAndParse(t *testing.T) {
    tmpDir := t.TempDir()
    logger, err := util.NewEventLogger(logPath, "test-node")
    if err != nil {
        t.Fatalf("Failed to create event logger: %v", err)
    }
    // ... test code
}
```

### Running Integration Tests

Integration tests require a local NFD instance to be running:

```bash
# Check if NFD is running
pgrep nfd

# Start NFD if not running
nfd &

# Run integration tests
go test -v -timeout 120s ./repo/...
```

**Requirements:**

- NFD must be installed and running locally
- No special routes needed - tests create their own prefixes under `/ndn/drepo`
- Tests automatically set multicast strategies for SVS sync prefixes

**Test Configuration:**

- Tests set multicast strategy for `/ndn/drepo/group-messages/32=svs` (all modes)
- In auction mode, tests also set `/ndn/drepo/heartbeat/32=svs`
- Tests use `-short` flag to skip integration tests

### TLV Definitions

- TLV structs use struct tags for code generation
- Follow pattern in `tlv/definitions.go`
- Run `go generate` after modifying TLV definitions

```go
type Command struct {
    Type             string  `tlv:"0x252"`
    Target           enc.Name `tlv:"0x253"`
    SnapshotThreshold uint64 `tlv:"0x255"`
}
```

### Logging

- Use the NDN log package: `log.Info()`, `log.Warn()`, `log.Fatal()`
- Include structured keys: `log.Info(r, "repo_start")`
- Use error keys: `log.Fatal(r, "node_update_pub_failed", "err", err)`

### Code Organization

- Main logic in `repo/repo.go`, `producer/producer.go`
- Utilities in `repo/util/` (event_log.go, counting_face.go)
- TLV definitions in `tlv/definitions.go`
- Tests alongside implementation files

### File Headers

- Use `//go:generate` directives for code generation
- Embed directives: `//go:embed testbed-root.decoded`

```go
//go:generate gondn_tlv_gen
//go:embed testbed-root.decoded
var testbedRootCert []byte
```

## Dependencies

Key external packages:

- `github.com/named-data/ndnd` - NDN SDK
- `github.com/cloudflare/cloudflare-go` - Cloudflare DNS integration

## Common Tasks

### Running specific tests

```bash
# Single test
go test -v -run 'TestEventLogger_WriteAndParse' -timeout 30s ./repo/...

# Tests matching pattern
go test -v -run 'TestEventLogger' -timeout 30s ./repo/...

# All in package
go test -v -timeout 30s ./repo/...
```

### Building

```bash
make build
# Or manually:
go build -o bin/repo ./repo
go build -o bin/producer ./producer
```

### Timeout Configuration

Default timeouts are calibrated for the testbed topology (real link delays):

- `--svs-timeout`: 8s (SVS health check)
- `--producer-timeout`: 1s (Producer command timeout)
- `--replication-timeout`: 1s (Replication wait timeout)

Run calibration to measure realistic values:

```bash
make test-timing  # Uses Docker/mini-ndn
# Or in experiments/:
make calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=5
```

Calibration now uses 99th percentile + 50% buffer for recommended timeouts. Results include:

- `replication_time_p95_ms`, `replication_time_p99_ms`
- `update_propagation_p95_ms`, `update_propagation_p99_ms`

### Topology Files

The project includes mini-ndn topology configurations in `experiments/`:

- `testbed_topology.conf` - Real NDN testbed topology with 24 nodes and actual link latencies
- `full_mesh_topology.conf` - Full mesh topology for testing (all 10ms links)

The testbed topology uses real link delays (RTT/2 in ms) from the NDN testbed. Delays MUST include the `ms` suffix:

```ini
UCLA:FRANKFURT delay=75ms   # Correct
UCLA:FRANKFURT delay=75     # Wrong - will be ignored!
```

### Auction Distribution Mechanism

The project includes an alternative distribution mechanism called "auction" (see `docs/specs/auction-spec` for full specification).

**Key Terms (see docs/specs/auction-spec GLOSSARY for complete list):**

- **Job Target (Target)**: The command identifier to be replicated (e.g., `/ndn/producer/mytarget/t=123`)
- **Bid Interest Name**: `/<peer-node-prefix>/bid/v=<timestamp>` - where bids are sent
- **Results Interest Name**: `/<auctioneer-node-prefix>/results/v=<timestamp>` - where results are published

**Running experiments with auction:**

```bash
# Calibrate for auction mode
make -C experiments calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=5 DISTRIBUTION=auction

# Run experiments with auction
make -C experiments run NODE_COUNTS="4 8 12" PRODUCER_COUNTS="1" -j5 DISTRIBUTION=auction
```

**Important:** When debugging auction issues, reference `docs/specs/auction-spec` first. The spec includes:

- Complete flow diagram with examples
- TLV definitions
- Timestamp collision resolution
- Pending assignment buffer handling

---

## Lessons Learned

### Debugging Distributed Systems

1. **Test before and after** - Always verify the original code's behavior with tests before making changes. We found `TestFailureRecovery_MultipleReposDown` was already failing before our fix, preventing wrong conclusions about our changes.

2. **Small changes, test incrementally** - Make minimal changes and verify each step. This makes it easier to identify what broke when tests fail.

3. **Race conditions in SVS sync** - The order of message delivery matters:
   - A `JobAssignment` can arrive BEFORE the `NewCommand`
   - Nodes must buffer pending assignments and process them when the command arrives
   - This is handled in `handleHydraJobAssignments()` which stores pending assignments

4. **Pending assignment handling differs by path**:
   - `onCommand` (direct from producer): Needed pending check for Hydra - nodes may receive JobAssignment before NewCommand
   - `onGroupSync` (via SVS): Already had pending check

5. **Heartbeat-based recovery has limitations** - When multiple nodes fail, redistribution may not recover all commands. `TestFailureRecovery_MultipleReposDown` is flaky even with original code.

### Operational Insights

1. **Always set multicast strategy** - Without it, SVS doesn't work properly
2. **Announce your prefixes** - AttachCommandHandler alone is not enough
3. **Publish heartbeats after state changes** - Otherwise peers don't see updates
4. **Use time-bounded metrics** - Cumulative stats include startup overhead
5. **Test incrementally** - Start with 2 nodes, then 3, then 5
6. **Check FIB entries** - Most connectivity issues show up there first
7. **Leader determination must happen AFTER dead node removal** - Computing `willBeLeader` before `delete(allNodes, downNode)` causes all nodes to think they're leader
8. **Source of truth** - Tests determine actual status, not documentation
