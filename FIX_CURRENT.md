# Agent Execution Guide: Phase 1 - Fix Current Implementation

---

## Overview

This document provides the specific instructions for running an agent to fix the current Auction implementation so both Hydra and Auction can be fairly compared.

**Goal:** Fix the auction mechanism without breaking Hydra.

---

## Prerequisites

Before starting, ensure:
- [ ] You have a working Docker environment
- [ ] NDN keys are available at `~/.ndn/keys`
- [ ] You have sufficient resources (8GB+ RAM, 4+ CPUs) for mini-ndn experiments
- [ ] You are in the repository directory: `/home/adam/ndn/repo`

---

## Setup

```bash
cd /home/adam/ndn/repo
```

---

## Agent Prompt

Copy and run exactly this command:

```bash
opencode --task "
You are working on fixing the Auction distribution mechanism in this repository.

## Your Mission

Fix the auction implementation so that both Hydra and Auction work correctly and can be fairly compared.

## Steps to Follow

### Step 1: Read the Specification
Read the entire file 'docs/specs/distributed-repo-spec.md' thoroughly. This is the source of truth.

### Step 2: Analyze Current Auction Issues
Read the auction-related code in these files:
- repo/repo.go (focus on: runAuction, onBidInterest, handleAuctionResults, checkReplicationAndRunAuction)
- repo/helpers.go (focus on: AuctionMechanism struct and DetermineWinners method)
- producer/producer.go (to understand how commands are sent)

Identify all specific issues where the code doesn't match Section 6 of the spec. For each issue, note:
- The specific problem
- What the spec says should happen
- What the code currently does

### Step 3: Fix the Auction Implementation
Fix the issues you identified. The key areas to fix (per spec Section 6):

1. Auction Initiation (6.1): Verify timestamp generation is unique 64-bit nanosecond
2. Collision Resolution (6.2): 
   - Timestamp ordering: incoming < local = cancel, incoming > local = Delay=true
   - Equal timestamps: lexicographic tiebreaker
   - Different targets: no comparison needed
3. Processing Rules (6.3):
   - Auctioneer: send MetricRequest, wait for responses, determine winners, publish JobAssignment
   - Peer: respond with MetricResponse including Delay flag
   - Results Handling: handle empty assignees, claim job if assigned, schedule follow-up

### Step 4: Test Your Fixes

Build and test:
cd /home/adam/ndn/repo
go build -o bin/repo ./repo
go build -o bin/producer ./producer

### Step 5: Run Integration Tests

Run with Hydra (baseline - should work):
go test -v -timeout 120s -run 'TestIntegration' ./repo/...

Run with Auction (your target):
# Set distribution flag in tests or run via Docker

### Step 6: Run Docker Experiments

Build Docker image:
docker build -t mini-ndn-integration -f experiments/Dockerfile.integration .

Run single experiment with Hydra:
make single NODES=5 PRODUCERS=1 DISTRIBUTION=hydra

Run single experiment with Auction:
make single NODES=5 PRODUCERS=1 DISTRIBUTION=auction

If Auction fails, note exactly what happens and iterate on fixes.

## What You May Modify

You may modify any file to fix the issues:
- repo/repo.go - Main implementation
- repo/helpers.go - Winner determination
- repo/integration_test.go - Tests
- Any other file as needed to fix issues

## Success Criteria

You have succeeded when:
1. Hydra still works (baseline not broken)
2. Auction works (commands replicate to RF nodes via bidding)
3. Both can run in Docker experiments
4. Metrics can be captured from both

## Critical Debugging Notes

- Do NOT break Hydra - it already works
- Focus on making Auction work without breaking existing functionality
- Use extensive logging to debug issues
- The spec is the source of truth - if code and spec disagree, fix the code
- Start simple: Test with NODES=5, PRODUCERS=1, RF=3
- Check logs: Look at Docker output and event logs
- Add logging: Don't hesitate to add more logging to understand flow
- Two-node test: Simplest case - 2 nodes, RF=2, 1 command
- **Always check routing first** when debugging networking issues:
  - The application (repo) must publish routes
  - For mini-ndn and real deployments, routing must publish routes (done in runner.py)
  - See examples in: github.com/named-data, github.com/ucla-irl, github.com/pulsejet
  - Documentation: https://minindn.memphis.edu/
  - Once routing and names are set up correctly, things usually flow well

## Output

When complete, report:
1. What specific issues you found and fixed
2. Whether Hydra still works
3. Whether Auction now works
4. Any remaining issues or limitations
"
```

---

## Expected Timeline

- **Step 1-2 (Analysis):** 1-2 hours
- **Step 3 (Fix Implementation):** 2-4 hours
- **Step 4-6 (Testing):** 2-4 hours

**Total:** ~8-12 hours

---

## If This Phase Fails

If Auction cannot be fixed in a reasonable time:

1. Note the specific issues found
2. Do NOT continue trying to fix - move to Phase 2
3. Proceed to BUILD_SCRATCH.md for fresh implementation

---

## Document Location

This guide is for **Phase 1** (fix current).

For **Phase 2** (build from scratch), see BUILD_SCRATCH.md.
