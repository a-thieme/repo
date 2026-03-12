# Plan 2: Build Implementation from Scratch

## Objective

Implement the distributed repository from scratch, matching the specification exactly, to ensure both Hydra and Auction mechanisms work correctly for fair comparison.

## Overview

Discard the existing implementation logic but keep the infrastructure (Docker, topology, test runners). Implement fresh code that precisely follows `distributed-repo-spec.md`.

---

## Phase 1: Specification Analysis

### 1.1 Read the Spec Thoroughly
- Read `docs/specs/distributed-repo-spec.md` multiple times
- Understand all concepts:
  - Section 1: Introduction
  - Section 2: Terminology (Core + Auction-specific)
  - Section 3: Constants
  - Section 4: TLV Definitions
  - Section 5: Shared Semantics
  - Section 6: Auction Distribution Mechanism
  - Section 7: Hydra Distribution Mechanism

### 1.2 Identify Key Components

| Component | Spec Section | Description |
|-----------|-------------|-------------|
| TLV Types | Section 4 | Command, NodeUpdate, JobAssignment, MetricRequest, MetricResponse |
| Constants | Section 3 | HEARTBEAT_INTERVAL, HEARTBEAT_TIMEOUT, STORAGE_THRESHOLD, REPLICATION_FACTOR |
| Heartbeat | 5.1 | Publication, Reception, Timeout |
| Replication | 5.2 | Triggers, Algorithm |
| Command Processing | 5.3 | Parse, Respond, Store, Publish, Check, Distribute |
| Winner Determination | 5.4 | Filter, Sort, Select |
| Ordering | 5.5 | Buffer assignment until command received |
| Auction Initiation | 6.1 | Timestamp generation |
| Auction Collision | 6.2 | Timestamp ordering, Delay flag, Rescheduling |
| Auction Processing | 6.3 | Auctioneer, Peer, Results Handling |
| Hydra Processing | 7.1-7.3 | Command, Assignment, Failure Recovery |

---

## Phase 2: Infrastructure Setup

### 2.1 Keep Existing Infrastructure (DO NOT MODIFY)
- `experiments/` - Docker, Makefile, runner.py
- `experiments/testbed_topology.conf` - 24-node topology
- `experiments/full_mesh_topology.conf` - Full mesh topology

### 2.2 TLV Definitions
Create `tlv/definitions.go` with all required types from Section 4:

```go
// Command (Section 4.1)
Type: 0x252, Target: 0x253, SnapshotThreshold: 0x255

// NodeUpdate (Section 4.2)
Jobs: 0x290, NewCommand: 0x291, StorageCapacity: 0x292, 
StorageUsed: 0x293, JobRelease: 0x294, JobAssignments: 0x297

// JobAssignment (Section 4.3)
Target: 0x295, Assignees: 0x296

// MetricRequest (Section 4.4)
Target: 0x298, ResultsName: 0x299, Timestamp: 0x29A

// MetricResponse (Section 4.4)
Capacity: 0x292, Used: 0x293, Timestamp: 0x29A, Delay: 0x29B
```

Run code generation after defining types.

---

## Phase 3: Core Implementation

### 3.1 Main Entry Point (`repo/main.go`)

CLI flags:
- `--node-prefix` - Unique node prefix
- `--signing-identity` - Signing identity
- `--distribution` - "hydra" or "auction"
- `--heartbeat-interval` - Default 5s
- `--replication-factor` - Default 3
- `--event-log` - Path to event log

### 3.2 Repository Core (`repo/repo.go`)

**Struct: Repo**
- groupPrefix, notifyPrefix, nodePrefix, signingIdentity
- engine, store, client
- groupSync, heartbeatSync (auction only)
- nodeStatus, commands
- storageCapacity, storageUsed, jobs
- rf, heartbeatInterval, distributionMechanism
- distributor (interface)
- eventLogger

**Methods:**
- `NewRepo()` - Constructor
- `Start()` - Initialize NDN, SVS, handlers
- `runHeartbeat()` - Publish NodeUpdate at intervals
- `checkStaleNodes()` - Detect offline nodes
- `onCommand()` - Handle producer commands
- `onGroupSync()` - Process SVS updates

### 3.3 Shared Semantics (Section 5)

**Heartbeat (5.1):**
- Publish NodeUpdate every HEARTBEAT_INTERVAL
- Reset peer timeout on reception
- On timeout: retrieve jobs, check replication, invoke distribution

**Replication Check (5.2):**
- Triggers: command receipt, jobs update, timeout
- Algorithm: count nodes doing job, if < RF then trigger

**Command Processing (5.3):**
1. Parse command (Type, Target)
2. Send StatusResponse "received"
3. Store internally
4. Publish NodeUpdate with NewCommand
5. Perform replication check
6. If under-replicated, invoke distribution

**Winner Determination (5.4):**
1. Filter: exclude already assigned, exclude > STORAGE_THRESHOLD
2. Sort: by storage% asc, capacity desc, name asc
3. Select: top N = RF - current

**Ordering Semantics (5.5):**
- Buffer JobAssignment if command not yet received

---

## Phase 4: Hydra Implementation

### 4.1 Hydra Distribution (`repo/hydra.go`)

Implement as struct with `DetermineWinners()` method.

**Command Processing (7.1):**
1. Compute winners per Section 5.4
2. If this node in winners, claim job
3. Publish NodeUpdate with NewCommand + JobAssignments

**Job Assignment Processing (7.2):**
1. For each JobAssignment:
   - If already doing job, skip
   - If in Assignees and command available, claim
2. Respect ordering semantics (Section 5.5)

**Failure Recovery (7.3):**
1. On heartbeat timeout (per 5.1.3)
2. For each job offline node was doing:
   - Check replication count
   - If < RF, compute winners per 5.4
3. If in winners, claim job
4. Publish NodeUpdate with JobAssignments

---

## Phase 5: Auction Implementation

### 5.1 Auction Distribution (`repo/auction.go`)

Implement with `DetermineWinners()` method.

**Auction Initiation (6.1):**
- Generate 64-bit nanosecond timestamp
- Combine with node name for unique ID

**Collision Resolution (6.2):**

*Timestamp Ordering:*
- Target matches local auction?
  - Incoming < local: cancel and reschedule (Delay=false response)
  - Incoming > local: set Delay=true in response
  - Equal: lexicographic comparison, lesser name wins
- Target differs: no comparison, Delay=false

*Delay Flag:*
- On receiving Delay=true:
  1. Cancel current auction
  2. Publish JobAssignment with empty Assignees
  3. Reschedule

*Rescheduled Auctions:*
- Allow time for completion and propagation
- Check under-replication when timer expires
- May cancel if no longer needed

**Processing Rules (6.3):**

*Auctioneer:*
1. Generate timestamp, construct ResultsName
2. Send MetricRequest to peers (exclude self, exclude assigned)
3. Wait for responses or timeout
4. If any Delay=true: publish empty, cancel
5. Else: determine winners, publish JobAssignment
6. Schedule follow-up auction

*Peer:*
1. Determine Delay flag per 6.2
2. Send MetricResponse with metrics + Delay
3. Subscribe to ResultsName

*Results Handling:*
- Empty Assignees: schedule new auction
- In Assignees + command: claim job
- Still under-replicated: schedule additional auction

---

## Phase 6: Testing and Validation

### 6.1 Build
```bash
go build -o bin/repo ./repo
go build -o bin/producer ./producer
```

### 6.2 Docker
```bash
docker build -t mini-ndn-integration -f experiments/Dockerfile.integration .
```

### 6.3 Run Experiments

**Calibration:**
```bash
make calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=3
```

**Hydra:**
```bash
make single NODES=24 PRODUCERS=1 DISTRIBUTION=hydra
```

**Auction:**
```bash
make single NODES=24 PRODUCERS=1 DISTRIBUTION=auction
```

**Full Suite:**
```bash
make run DISTRIBUTION=hydra
make run DISTRIBUTION=auction
```

### 6.4 Capture Metrics

Compare:
- Replication time (P95, P99)
- Update propagation time
- Message counts (SVS, auction bids)
- Failure recovery time

---

## Phase 7: Comparison

### 7.1 Run Multiple Iterations
- Same topology, same command patterns
- Multiple runs for statistical significance

### 7.2 Analyze Results
- Latency comparison
- Overhead comparison
- Scalability comparison
- Failure handling comparison

---

## Agent Freedom

The agent may:
- Create new files as needed
- Structure code however appropriate
- Modify infrastructure if needed
- Add tests
- Refactor for clarity

---

## File Structure Recommendation

```
repo/
├── main.go           # CLI entry
├── repo.go          # Core Repo struct and methods
├── heartbeat.go     # Heartbeat mechanism
├── replication.go   # Replication checking
├── winner.go        # Winner determination
├── hydra.go         # Hydra distribution mechanism
├── auction.go       # Auction distribution mechanism
├── handlers.go      # Command and sync handlers
└── helpers.go       # Utility functions
```

---

## Success Criteria

1. Both Hydra and Auction work correctly
2. Implementation matches spec exactly
3. Docker experiments run successfully
4. Fair comparison possible between mechanisms
5. All metrics captured

---

## Estimated Timeline

- Phase 1-2: 1-2 hours
- Phase 3-4: 4-6 hours  
- Phase 5: 4-6 hours
- Phase 6-7: 2-4 hours

Total: ~12-18 hours

---

## Advantages of This Approach

| Advantage | Description |
|-----------|-------------|
| No legacy issues | Start fresh, no hidden bugs |
| Spec-driven | Implementation must match spec exactly |
| Cleaner design | Can design for both mechanisms from start |
| Fair comparison | Both mechanisms implemented correctly |

---

## Disadvantages of This Approach

| Disadvantage | Description |
|--------------|-------------|
| Longer | Takes more time than fixing |
| Infrastructure changes | May need to adapt tests |
| More risk | New code may have new bugs |

---

## Key Design Decisions for Agent

1. How to structure the distribution mechanism interface?
2. How to handle concurrent auctions for different targets?
3. How to implement the results subscription pattern?
4. How to handle storage simulation for JOIN commands?
5. What logging/events to capture for metrics?
