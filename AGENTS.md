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
