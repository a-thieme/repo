# Distributed NDN Repository - Technical Overview

## Overview
Distributed NDN data repository using SVS for group sync. Nodes replicate data with configurable factor (default: 3).

## Components

**Repo** (`repo/repo.go`): Receives commands, distributes jobs, tracks status via SVS heartbeats, timer-based failure detection.

**Producer** (`producer/producer.go`): Sends INSERT/JOIN commands via TLV.

**TLV** (`tlv/definitions.go`): Command, NodeUpdate, JobAssignment, InternalCommand.

## Data Structures

```go
type NodeStatus struct {
    Capacity    uint64
    Used        uint64
    LastUpdated time.Time
    Jobs        []enc.Name
    TimerID     uint64  // failure detection
}

type NodeUpdate struct {
    Jobs           []enc.Name
    NewCommand     *Command
    StorageCapacity uint64
    StorageUsed    uint64
    JobRelease     []*InternalCommand  // unused
    JobAssignments []*JobAssignment
}
```

## Communication
1. Producer → Command to notify prefix
2. `determineWinnersHydra()` selects candidates by free space
3. NodeUpdate with JobAssignments published via SVS
4. Recipients check if assignee, claim job

## Replication
1. Count current replication
2. Filter: exclude busy nodes (>75% full)
3. Sort by free space desc, capacity desc, name asc
4. Select top N (rf - current)

## Failure Detection
- Heartbeat every 5s via SVS
- Peers reset 15.5s timer on receive
- Timer fires → `onHeartbeatTimeoutHydra()` → redistribute jobs

## Storage
- INSERT: immediate 0-500MB
- JOIN: grows 0-10MB/s
- Claiming blocked when >75% full
- No automatic release

## CLI Flags
| Flag | Default |
|------|---------|
| --svs-timeout | 8s |
| --producer-timeout | 1s |
| --replication-timeout | 1s |

## Events
`command_received`, `command_synced`, `decision_made`, `job_claimed`, `job_released`, `node_update`, `replication_check`, `storage_changed`

Use `testutil.ComputeGlobalReplicationTimeline()` for analysis.

## Build
```bash
make build
```
