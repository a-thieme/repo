# Auction Mechanism Implementation Plan

**NOTE FOR AGENT**: This document provides a high-level overview. For technical details (TLV formats, exact algorithms, timing constants, edge cases), always refer to `docs/specs/auction-spec`.

**Working Guidelines:**
- Use sub-agents (Task tool) for parallel work where appropriate (e.g., implementing tests while doing core logic)
- Use todos to track step-by-step progress through each phase
- When in doubt or stuck, refer back to this plan to stay on track
- Keep working until the implementation is complete - do not stop to ask questions
- If context is compacted (session summarized due to length limits), re-read this plan and continue from where you left off
- Manage token usage: keep total input/output within ~200k tokens, make full use of context window for thoroughness

---

## Executive Summary

The auction mechanism is not yet implemented. The TLV definitions, CLI flags, and event logging stubs exist. This plan provides a broad outline for implementation.

---

## High-Level Data Flow

```
Producer → Command → Any Node detects under-replication → Runs Auction
Auctioneer → Sends Bid Interests → Collects Responses → Publishes Results
Peers → Receive Bid Interests → Respond with metrics → Subscribe to Results
Peers → Receive Results → Claim jobs OR Buffer if command not yet received
Any Node → Detects under-replication (from command/jobs removed/node death) → Runs Auction
```

---

## Phase 1: Infrastructure & Startup

Set up auction-specific state, prefixes, and heartbeat mechanism.

### Key Components
- Auction constants in Repo struct (`auctionTimeout`, `auctionBackupDelay`, `pendingAssignTimeout`)
- Auction state fields (`currentAuctionTimestamp`, `scheduledAuctions`, `completedAuctions`)
- Prefix registration: `/<node prefix>/bid`, `/<node prefix>/results`
- Separate SVS group for auction heartbeats: `/<group prefix>/heartbeat`

### Reference
- Prefix registration: auction-spec "AUCTION PREFIXES TO REGISTER"
- Auction constants: auction-spec "AUCTION CONSTANTS"
- Heartbeat mechanism: auction-spec "AUCTION HEARTBEATS"

---

## Phase 2: Auction Core Logic

Implements the auctioneer and peer-side logic for running auctions.

### Key Components
- `runAuction(cmd)` - Entry point, queues and executes auction
- `onBidInterest()` - Handles incoming bid requests from peers
- `AuctionMechanism.DetermineWinners()` - Winner selection algorithm

### Reference
- Auction flow: auction-spec "RUNNING THE AUCTION DISTRIBUTION MECHANISM"
- Bid interest handling: auction-spec "HANDLING BID INTERESTS"
- Timestamp collision: auction-spec "AUCTION TIMESTAMP & COLLISION RESOLUTION"
- Winner determination: auction-spec "Step 6" in running section

---

## Phase 3: Results & Pending Buffer

Handles auction results and buffers assignments received before commands.

### Key Components
- Results subscription (ExpressR for long-lived Interests)
- Results handler - process JobAssignment, claim jobs
- Pending assignment buffer - buffer assignments until command arrives

### Reference
- Results handling: auction-spec "HANDLING RESULTS DATA"
- Pending buffer: auction-spec "PENDING ASSIGNMENT BUFFER"
- Auctioneer cancel handling: auction-spec "AUCTIONEER HANDLING CANCEL"

---

## Phase 4: Triggers & Coordination

Integrates auction triggers into the main command flow.

### Triggers
- Command receipt from producer (via `onCommand`)
- JobAssignment received from another node (via `onGroupSync`)
- Job removal from another node (via `onGroupSync`)
- Node death / heartbeat timeout (via `checkStaleNodes`)

### Key Components
- `checkReplicationAndRunAuction()` - Detect under-replication, trigger auction

### Reference
- Under-replication detection should check: current assignee count vs target RF

---

## Phase 5: Testing Strategy

### Unit Tests (no NFD required)
- Nonce/timestamp generation
- Winner selection algorithm
- Pending buffer logic

### Integration Tests (requires NFD)
- Basic multi-node auction
- Concurrent commands
- Node failure / re-auction
- Timestamp collision handling

### Mini-NDN Tests
- Various topologies (5, 24 nodes)

Run with: `make test-integration`, `make test-mini-ndn`

---

## Phase 6: Calibration & Experiments

### Calibration
```bash
make -C experiments calibrate CALIBRATE_NODES=5 CALIBRATE_ITER=5 DISTRIBUTION=auction
make -C experiments calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=5 DISTRIBUTION=auction
```

### Experiments
Run various configurations and compare with Hydra:
- Nodes: 5, 24
- Producers: 1, 2, 4, 8, 16, 24

---

## Key Implementation Files

| File | Changes |
|------|---------|
| `repo/repo.go` | Core auction logic, handlers |
| `repo/helpers.go` | AuctionMechanism winner selection |
| `repo/timeouts.go` | Auction-specific timeouts |
| `repo/*_test.go` | Tests |

---

## Important Notes

1. **TLV definitions already exist** - don't recreate them (see `tlv/definitions.go`)
2. **Event logging already defined** - use existing event types
3. **Hydra must remain unchanged** - auction is a separate code path
4. **Spec is authoritative** - if in doubt, check auction-spec

---

## Auction Constants

See auction-spec "AUCTION CONSTANTS" for detailed derivation.

Default values:
- AUCTION_TIMEOUT: ~1.75s (3.5 × 500ms RTT)
- AUCTION_BACKUP_DELAY: ~3.75s (timeout + 2s)

---

## TLV Types (Already Implemented)

See `tlv/definitions.go` for exact structures:
- `MetricRequest` - Sent in bid Interest app parameters
- `MetricResponse` - Sent as Data in response to bid Interest
- `JobAssignment` - Published at results name

---

## Estimated Complexity

- **Phase 1-2 (Infrastructure & Core)**: ~400 lines of code
- **Phase 3-4 (Results & Triggers)**: ~300 lines of code
- **Phase 5 (Testing)**: ~400 lines of test code
- **Total**: ~1100 lines
