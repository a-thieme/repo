# Technical Architecture: Distributed NDN Data Repository

## Overview

This document provides detailed technical information about the implementation of the distributed NDN data repository system, focusing on core components, data structures, communication patterns, and monitoring mechanisms.

## Core Components

### Repo Component
The main repository component provides:

- **Storage Command Management**: Handles storage commands received from producers
- **Job Distribution**: Distributes jobs across multiple nodes based on replication factor
- **Replication Management**: Manages replication factor (default: 3) for job distribution
- **Node Status Tracking**: Tracks node status including capacity, usage, and jobs
- **Heartbeat Monitoring**: Implements heartbeat mechanism for node status monitoring
- **Job Release Mechanism**: Automatically releases jobs when storage exceeds 75% capacity
- **Storage Simulation**: Deterministic storage growth simulation for JOIN commands

### Producer Component
The producer component provides:

- **Command Sending**: Sends commands to the repository for processing
- **TLV Communication**: Uses TLV protocol for data communication
- **Command Handling**: Implements command sending and validation

### TLV Component
Defines data structures and protocols:

- **Command Structure**: Defines command data format
- **InternalCommand Structure**: Defines internal command format for job release signaling
- **NodeUpdate Structure**: Defines node update format with job release support
- **StatusResponse Structure**: Defines status response format

## Data Structures

### Command Structure
```go
type Command struct {
    Type string          // Command type (e.g., "INSERT", "JOIN")
    Target enc.Name      // Command target name
    SnapshotThreshold uint64 // Snapshot threshold
}
```

### NodeStatus Structure
```go
type NodeStatus struct {
    Capacity    uint64      // Total capacity
    Used        uint64      // Used capacity
    LastUpdated time.Time   // Last update timestamp
    Jobs        []enc.Name   // List of jobs
    MissingHeartbeats int    // Consecutive missed heartbeats
}
```

### NodeUpdate Structure
```go
type NodeUpdate struct {
    Jobs              []enc.Name // Job list
    NewCommand        *Command    // New command
    StorageCapacity   uint64      // Storage capacity
    StorageUsed       uint64      // Used storage
    JobRelease        *InternalCommand // Job release (optional)
}
```
Published via group sync to share node status. The JobRelease field signals when a node is releasing a job due to storage pressure.

### InternalCommand Structure
```go
type InternalCommand struct {
    Type              string       // Command type (same as original command)
    Target            enc.Name      // Command target
    SnapshotThreshold uint64       // Snapshot threshold
    StorageSpace      uint64       // Storage space (TLV type 0x294)
}
```
Used for job release signaling between nodes. When a node needs to release a job due to storage constraints, it sends an InternalCommand with the same Type as the original command.

## Communication Patterns

### Command Handling
```go
func (r *Repo) onCommand(name enc.Name, content enc.Wire, reply func(wire enc.Wire) error) {
    // Parse command
    // Respond with status
    // Add command to storage
    // Publish command
    // Execute replication
}
```

### Node Update Publishing
```go
func (r *Repo) publishNodeUpdate() {
    // Publish node update with current status
    // Include capacity, usage, and jobs
}
```

### Group Synchronization
```go
// Uses SvsALO for group synchronization
// Subscribe to publisher updates
// Update node status based on received updates
```

## Storage and Replication

### Storage Management - Real-time storage management
- **Storage Capacity Tracking**: Real-time storage capacity monitoring
- **Usage Tracking**: Storage usage monitoring across nodes
- **Job Distribution**: Intelligent job distribution based on capacity
- **Capacity Management**: Automatic capacity management and redistribution
- **Storage Simulation**: Deterministic growth simulation for JOIN commands

### Job Release Mechanism
When storage usage exceeds 75% of capacity, the system automatically releases jobs:

```go
func (r *Repo) findJobToRelease() *tlv.InternalCommand {
    // Select last job from current job list
    // Create InternalCommand with same Type as original command
    // Return nil if no jobs to release
}

func (r *Repo) removeJobFromLocal(target enc.Name) {
    // Remove job from local job list
    // Update storage used by subtracting job storage usage
    // Clean up job storage usage tracking
}
```

The job release is published via `NodeUpdate.JobRelease` to notify other nodes.

### Replication Logic
- **Replication Factor**: Default replication factor (rf) = 3
- **Job Distribution**: Jobs distributed based on replication factor
- **Job Claiming**: Intelligent job claiming based on node availability
- **Storage Capacity Tracking**: Real-time capacity tracking across nodes

### Job Distribution Mechanism
The system uses a sophisticated job distribution mechanism:

1. **Current Replication Count**: Count nodes currently executing the job
2. **Candidate Selection**: Select nodes not currently executing the job
3. **Capacity Comparison**: Compare capacity and free space between nodes
4. **Job Distribution**: Distribute jobs based on available capacity
5. **Re-evaluation**: Re-evaluate distribution when nodes go offline

## Node Detection and Monitoring

### Heartbeat Mechanism
```go
const HEARTBEAT_TIME = 5 * time.Second

func (r *Repo) runHeartbeat() {
    ticker := time.NewTicker(HEARTBEAT_TIME)
    for range ticker.C {
        r.publishNodeUpdate()
    }
}
```

### Node Status Tracking
- **Real-time Tracking**: Continuous node status tracking
- **Status Updates**: Node status updates published periodically
- **Last Updated**: Tracks last update timestamp
- **Status Monitoring**: Real-time status monitoring across nodes

### Offline Detection
```go
offlineTimeout := 3 * heartbeatInterval + 500 * time.Millisecond

func (r *Repo) runHeartbeatMonitor() {
    ticker := time.NewTicker(heartbeatInterval)
    for range ticker.C {
        for node, status := range r.nodeStatus {
            timeSinceLastUpdate := time.Since(status.LastUpdated)
            if timeSinceLastUpdate > offlineTimeout {
                // Trigger re-evaluation when node goes offline
            }
        }
    }
}
```

### Detection Mechanism
- **Detection Threshold**: 3 consecutive missed heartbeats
- **Detection Timeout**: 15 seconds (3 × 5s + 0.5s delay)
- **Re-evaluation**: Automatic re-evaluation when node goes offline
- **Resilience**: Automatic handling of node failures

### MissingHeartbeats Field
```go
type NodeStatus struct {
    // ... other fields
    MissingHeartbeats int // Consecutive missed heartbeats
}
```

## Monitoring and Re-evaluation

### Heartbeat Monitoring System
```go
func (r *Repo) runHeartbeatMonitor() {
    offlineTimeout := 3 * heartbeatInterval + 500 * time.Millisecond
    ticker := time.NewTicker(heartbeatInterval)
    for range ticker.C {
        // Monitor node status
        // Detect offline nodes
        // Trigger re-evaluation
    }
}
```

### Job Claim Monitoring
```go
func (r *Repo) monitorJobClaims() {
    ticker := time.NewTicker(HEARTBEAT_TIME)
    for range ticker.C {
        // Monitor active jobs
        // Trigger replication for jobs with storage usage
    }
}
```

### Storage Simulation
```go
func (r *Repo) runStorageSimulation() {
    ticker := time.NewTicker(STORAGE_TICK_TIME) // 1 second
    for range ticker.C {
        // Simulate deterministic storage growth for JOIN commands
        // Growth is deterministic based on command target hash
        // Track per-job storage usage
    }
}
```
The storage simulation provides deterministic growth for testing:
- JOIN commands grow storage based on `hashFromString(cmd.Target.String())`
- Growth is tracked per-job in `jobStorageUsage` map
- INSERT commands have immediate storage cost at claim time

### Monitoring Triggers

1. **Heartbeat Failure Detection**
   - Detects nodes offline after 3 consecutive missed heartbeats
   - Triggers re-evaluation when node first goes offline

2. **Job Claim Delay Monitoring**
   - Monitors job claim delays
   - Implements job timeout mechanism
   - Triggers re-evaluation for job claims

3. **Job Timeout Mechanism**
   - Implements timeout mechanism for job execution
   - Monitors job claim delays
   - Triggers re-evaluation when timeouts occur

## Implementation Details

### Event Logging System

The system logs all significant events to a JSONL file for debugging and analysis:

#### Event Types
| Event | Description | Key Fields |
|-------|-------------|------------|
| `sync_interest_sent` | SVS sync interest sent | `total` |
| `data_sent` | Data packet sent | `name`, `total` |
| `command_received` | Command received from producer | `type`, `target` |
| `job_claimed` | Node claimed a job | `target`, `replication` |
| `job_released` | Node released a job | `target` |
| `node_update` | Node status update received | `from`, `jobs`, `capacity`, `used` |
| `replication_check` | Replication decision made | See below |
| `storage_changed` | Storage updated | `used`, `delta` |

#### Replication Decision Logging

When a node decides whether to claim a job, it logs the full decision process:

```json
{
  "ts": "2026-02-19T21:14:29.534Z",
  "event": "replication_check",
  "node": "/ndn/repo/local/a",
  "target": "/ndn/target/example",
  "shouldClaim": false,
  "reason": "not_selected",
  "currentReplication": 0,
  "neededReplication": 3,
  "candidates": ["/ndn/repo/c", "/ndn/repo/d", "/ndn/repo/e", "/ndn/repo/a", "/ndn/repo/b"],
  "selectedCandidates": ["/ndn/repo/c", "/ndn/repo/d", "/ndn/repo/e"],
  "freeSpace": {
    "/ndn/repo/a": 5000000000,
    "/ndn/repo/b": 4800000000,
    "/ndn/repo/c": 5500000000,
    "/ndn/repo/d": 5200000000,
    "/ndn/repo/e": 5100000000
  }
}
```

**Decision reasons:**
- `replication_satisfied`: Already have `rf` nodes doing the job
- `selected_as_candidate`: This node is in the top N candidates (sorted by free space)
- `not_selected`: This node is not in the top N candidates

#### Global Replication Analysis

Since each node's view may be temporarily out of sync, use the test utilities to compute the accurate "oracle" view:

```go
import "github.com/a-thieme/repo/repo/testutil"

// Compute true replication count at a point in time
count := testutil.ComputeGlobalReplicationAtTime(allEvents, target, timestamp)

// Get full timeline of replication changes
timeline := testutil.ComputeGlobalReplicationTimeline(allEvents, target)
// Returns []ReplicationState{ {Timestamp, Count, Nodes}, ... }
```

### Recent Implementation
The system has been enhanced with:

- **Node Detection Mechanism**: Nodes detected offline after 3 consecutive missed heartbeats
- **Heartbeat Monitoring System**: Background monitoring of node status
- **MissingHeartbeats Field**: Tracks consecutive missed heartbeats
- **Re-evaluation Mechanism**: Re-evaluates replication decisions when needed
- **Job Release Mechanism**: Automatic job release when storage exceeds 75% capacity
- **Storage Simulation**: Deterministic storage growth for JOIN commands
- **InternalCommand**: TLV structure for job release signaling

### Code Structure

**Main Files:**
- `/home/adam/ndn/repo/repo/repo.go`: Main repository implementation
- `/home/adam/ndn/repo/producer/producer.go`: Producer component
- `/home/adam/ndn/repo/tlv/definitions.go`: TLV definitions

**Key Functions:**
- `runHeartbeat()`: Heartbeat publishing mechanism
- `runHeartbeatMonitor()`: Node status monitoring
- `runStorageSimulation()`: Storage growth simulation for JOIN commands
- `monitorJobClaims()`: Job claim monitoring
- `shouldClaimJobHydra()`: Job claiming logic
- `replicate()`: Replication execution
- `onCommand()`: Command handling
- `publishNodeUpdate()`: Node update publishing
- `findJobToRelease()`: Find job to release when storage > 75%
- `removeJobFromLocal()`: Remove job from local state

### Dependencies
- **Go Version**: Go 1.25.6
- **Core Dependencies**: Named Data Networking (NDN) libraries
- **TLV Protocol**: Type-Length-Value data structures
- **Group Synchronization**: SvsALO for distributed state management

### Build and Testing
- **Build Process**: `go build` successfully compiles
- **Testing**: Smoke tests pass
- **No Errors**: No syntax or type errors detected
- **No Warnings**: No linting issues detected

## Development Notes

### File Organization
- **repo/**: Main repository implementation
- **producer/**: Producer component
- **tlv/**: TLV definitions
- **repo.go**: Main repository implementation file

### Key Files and Purposes
- **repo.go**: Main repository implementation with monitoring (665 lines)
- **producer.go**: Producer command sending
- **definitions.go**: TLV structure definitions (45 lines)
- **main.go**: Main entry point
- **util/**: Utility packages (EventLogger, CountingFace)

### Development Environment
- **Go Version**: 1.25.6
- **Dependencies**: NDN libraries and related packages
- **Build Process**: Standard Go build process
- **Testing**: Smoke tests pass successfully

## Technical Architecture Summary

The system implements a distributed NDN data repository with resilient operation:

1. **Storage Management**: Distributed storage management across multiple nodes
2. **Replication Management**: Replication factor management (default: 3)
3. **Node Detection**: Node detection mechanism (3 missed heartbeats)
4. **Heartbeat Monitoring**: Continuous monitoring of node status
5. **Monitoring Mechanisms**: Multiple monitoring mechanisms for job and node status
6. **Re-evaluation**: Re-evaluates replication decisions when needed

The system automatically handles node failures and maintains operation through intelligent monitoring and re-evaluation mechanisms.
