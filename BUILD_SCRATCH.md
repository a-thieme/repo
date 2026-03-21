# Agent Execution Guide: Phase 2 - Build from Scratch

---

## Overview

This document provides specific instructions for running an agent to build the distributed repository from scratch. Use this only if Phase 1 (FIX_CURRENT.md) failed.

**Goal:** Implement both Hydra and Auction fairly from a clean slate.

---

## Prerequisites

Before starting, ensure:
- [ ] You have a working Docker environment
- [ ] NDN keys are available at `~/.ndn/keys`
- [ ] You have sufficient resources (8GB+ RAM, 4+ CPUs)

---

## Setup

Create a fresh directory and copy only the infrastructure (not the implementation):

```bash
# Create fresh directory
mkdir -p /home/adam/ndn/repo-scratch
cd /home/adam/ndn/repo-scratch

# Copy only infrastructure (not implementation logic)
cp -r /home/adam/ndn/repo/experiments .
cp /home/adam/ndn/repo/docs/specs/distributed-repo-spec.md .
cp /home/adam/ndn/repo/tlv/definitions.go tlv/
mkdir -p repo
cp -r /home/adam/ndn/repo/repo/util repo/
cp -r /home/adam/ndn/repo/repo/testutil repo/

# Copy producer (works fine, not part of the problem)
cp -r /home/adam/ndn/repo/producer .

# Generate TLV code
cd tlv && go generate && cd ..

# Verify setup
ls -la
```

---

## Agent Prompt

Copy and run exactly this command in the repo-scratch directory:

```bash
cd /home/adam/ndn/repo-scratch
opencode --task "
You are building the distributed repository from scratch to fairly compare Hydra vs Auction.

## Your Mission

Implement the distributed repository per docs/specs/distributed-repo-spec.md. Implement BOTH Hydra and Auction mechanisms correctly so they can be fairly compared.

## Critical Requirements

1. Read docs/specs/distributed-repo-spec.md FIRST - this is your specification
2. Implement BOTH mechanisms - don't focus on one over the other
3. The goal is FAIR COMPARISON - both should work equally well
4. Reuse existing infrastructure: tlv/definitions.go, repo/util/, repo/testutil/, experiments/
5. The producer is copied as-is - it works fine, don't modify it

## Implementation Order (Recommended)

### Part 1: Core Infrastructure
1. Create repo/main.go with CLI flags (--distribution=hydra|auction, --replication-factor, --heartbeat-interval, etc.)
2. Create repo/repo.go with Repo struct and Start() method
3. Set up NDN engine, SVS sync, prefix registration

### Part 2: Shared Semantics (Section 5)
Implement these BEFORE distribution mechanisms:
1. Heartbeat mechanism (5.1): publish NodeUpdate at intervals, detect stale nodes
2. Replication check (5.2): count nodes doing job, trigger if < RF
3. Command processing (5.3): parse, respond, store, publish, check, distribute
4. Winner determination (5.4): Filter > Sort > Select
5. Ordering semantics (5.5): buffer assignment until command received

### Part 3: Hydra (Section 7)
1. Implement HydraMechanism struct with DetermineWinners()
2. Command processing: compute winners, claim if in winners, publish NodeUpdate with JobAssignments
3. Job assignment processing: handle received assignments
4. Failure recovery: on timeout, redistribute jobs

### Part 4: Auction (Section 6)
This is the critical part - implement carefully:

1. AuctionMechanism struct with DetermineWinners()
2. Auction Initiation (6.1): generate unique 64-bit nanosecond timestamp
3. Collision Resolution (6.2):
   - When receiving MetricRequest:
     - If target matches local auction AND incoming timestamp < local: cancel and reschedule
     - If target matches local auction AND incoming timestamp > local: set Delay=true
     - If timestamps equal: lexicographic tiebreaker
     - If targets differ: no comparison, Delay=false
   - On receiving Delay=true: cancel, publish empty JobAssignment, reschedule
4. Processing Rules:
   - Auctioneer: send MetricRequest to peers, wait for responses, determine winners, publish JobAssignment to ResultsName
   - Peer: respond with MetricResponse, subscribe to ResultsName
   - Results: handle empty assignees, claim if assigned, schedule follow-up

## Testing

Generate TLV code:
cd tlv && go generate && cd ..

Build:
go build -o bin/repo ./repo
go build -o bin/producer ./producer

Docker:
docker build -t mini-ndn-integration -f experiments/Dockerfile.integration .

Test Hydra:
make single NODES=5 PRODUCERS=1 DISTRIBUTION=hydra

Test Auction:
make single NODES=5 PRODUCERS=1 DISTRIBUTION=auction

## What to Create

You should create:
- repo/main.go - CLI entry point
- repo/repo.go - Core Repo struct and methods
- repo/hydra.go - Hydra distribution mechanism
- repo/auction.go - Auction distribution mechanism
- Other files as needed

## Success Criteria

You have succeeded when:
1. Hydra works: commands replicate to RF nodes
2. Auction works: commands replicate to RF nodes via bidding
3. Both mechanisms can be fairly compared
4. Metrics can be captured from both

## Critical Debugging Notes

- Start with Hydra: It's simpler, get it working first
- Two-node test: NODES=2, RF=2, 1 command - simplest case
- Add logging: Extensive logging is your friend
- Check event logs: Docker writes event logs to results directory
- **Always check routing first** when debugging networking issues:
  - The application (repo) must publish routes
  - For mini-ndn and real deployments, routing must publish routes (done in runner.py)
  - See examples in: github.com/named-data, github.com/ucla-irl, github.com/pulsejet
  - Documentation: https://minindn.memphis.edu/
  - Once routing and names are set up correctly, things usually flow well

## Output

When complete:
1. Confirm Hydra works (replication succeeds)
2. Confirm Auction works (replication succeeds via bidding)
3. Note any differences in behavior between mechanisms
4. Document any issues or limitations
"
```

---

## Expected Timeline

- **Part 1-2 (Core + Shared):** 3-4 hours
- **Part 3 (Hydra):** 2-3 hours
- **Part 4 (Auction):** 3-4 hours
- **Testing:** 2-3 hours

**Total:** ~12-18 hours

---

## Document Location

This guide is for **Phase 2** (build from scratch).

For **Phase 1** (fix current), see FIX_CURRENT.md.
