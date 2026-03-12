# Plan 1: Fix and Extend Current Implementation

## Objective

Fix the auction distribution mechanism in the existing codebase, ensuring both Hydra and Auction work correctly for fair comparison.

## Overview

Work with the existing codebase, identify and fix issues in the auction implementation, and ensure the spec is properly implemented. Leverage existing infrastructure (Docker, topology files, test runners).

---

## Phase 1: Assessment (Pre-Work)

### 1.1 Read and Understand the Spec
- Read `docs/specs/distributed-repo-spec.md` thoroughly
- Understand all sections: Terminology, Constants, TLV Definitions, Shared Semantics, Auction Mechanism, Hydra Mechanism

### 1.2 Audit Current Implementation
- Read all source files to understand current structure:
  - `repo/repo.go` - Main repository logic
  - `repo/helpers.go` - Winner determination, distribution mechanism implementations
  - `repo/main.go` - CLI entry point
  - `producer/producer.go` - Command producer
  - `tlv/definitions.go` - TLV type definitions

### 1.3 Identify Gaps and Issues
- Compare implementation against spec
- Document specific issues with auction mechanism:
  - Timestamps: How are they generated and compared?
  - Collision resolution: How are ties handled?
  - Bid/Response flow: What happens when nodes don't respond?
  - Results publication: How are winners determined and published?
  - Edge cases: What happens with race conditions?

---

## Phase 2: Fix Implementation

### 2.1 Fix Auction Implementation

**Priority fixes (based on spec Section 6):**

1. **Auction Initiation (Section 6.1)**
   - Verify timestamp generation (64-bit nanosecond)
   - Verify ResultsName construction
   - Ensure uniqueness guarantees

2. **Collision Resolution (Section 6.2)**
   - Fix timestamp ordering logic:
     - Incoming < local → cancel and reschedule
     - Incoming > local → set Delay=true
     - Equal timestamp → lexicographic tiebreaker
   - Handle target mismatch case (no comparison needed)
   - Verify Delay flag semantics

3. **Processing Rules (Section 6.3)**
   - Auctioneer rules:
     - Generate timestamp, construct ResultsName
     - Send MetricRequest to peers (exclude self and assigned)
     - Wait for responses or timeout
     - If Delay=true, publish empty and cancel
     - Otherwise determine winners and publish JobAssignment
     - Schedule follow-up auction
   - Peer rules:
     - Determine Delay flag per Section 6.2
     - Send MetricResponse with storage metrics
     - Subscribe to ResultsName

4. **Results Handling**
   - Empty Assignees → schedule new auction
   - In Assignees + command received → claim job
   - Still under-replicated → schedule additional auction

### 2.2 Verify Hydra Implementation

Review `repo/helpers.go` HydraMechanism:
- Confirm winner determination matches spec Section 5.4 (Filter/Sort/Select)
- Verify command processing (Section 7.1)
- Verify job assignment processing (Section 7.2)
- Verify failure recovery (Section 7.3)

### 2.3 Fix Shared Components

- Heartbeat mechanism (Section 5.1)
- Replication check (Section 5.2)
- Command processing (Section 5.3)
- Winner determination (Section 5.4)
- Ordering semantics (Section 5.5)

---

## Phase 3: Testing and Validation

### 3.1 Unit Tests
- Test winner determination algorithm
- Test auction collision resolution
- Test timestamp comparison logic
- Test tiebreaker logic

### 3.2 Integration Tests
- Run existing integration tests
- Run with Hydra distribution
- Run with Auction distribution

### 3.3 Experiment Framework Testing
- Build binaries: `go build -o bin/repo ./repo && go build -o bin/producer ./producer`
- Build Docker: `docker build -t mini-ndn-integration -f experiments/Dockerfile.integration .`
- Run calibration: `make calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=3`
- Run single experiment: `make single NODES=24 PRODUCERS=1`
- Run full suite: `make run`

### 3.4 Capture Metrics
Compare metrics between Hydra and Auction:
- Replication time
- Update propagation time
- Commands processed
- Auction-specific: bids received, delays triggered, collisions resolved

---

## Phase 4: Document and Compare

### 4.1 Document Findings
- What works in Hydra
- What was fixed in Auction
- Remaining issues or limitations

### 4.2 Run Comparison Experiments
- Run same experiments with both mechanisms
- Compare metrics:
  - Latency (replication time)
  - Message overhead (SVS messages, auction bids)
  - Scalability (different node counts)
  - Failure recovery

---

## Agent Freedom

The agent may:
- Modify any source file to fix issues
- Add new test cases
- Modify test infrastructure if needed
- Refactor code for clarity
- Add logging/debugging to diagnose issues

---

## Success Criteria

1. Both Hydra and Auction distribution mechanisms work correctly
2. Integration tests pass
3. Docker experiments run successfully
4. Metrics can be captured and compared
5. Implementation matches `distributed-repo-spec.md`

---

## Estimated Timeline

- Assessment: 1-2 hours
- Fix Implementation: 2-4 hours
- Testing: 2-4 hours
- Comparison: 1-2 hours

Total: ~8-12 hours

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Existing code has hidden assumptions | Extensive logging during testing |
| Auction may have deeper issues | Start with simple tests, iterate |
| Race conditions hard to reproduce | Add instrumentation, run multiple times |
| Comparison may not be fair | Document differences, run many iterations |

---

## Open Questions for Agent

1. What specific auction issues are most critical to fix first?
2. Should tests be added before or after fixing?
3. How to verify fair comparison between mechanisms?
4. What metrics are most important for comparison?
