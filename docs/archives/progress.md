# Auction Mechanism Investigation Progress

## Summary

Investigating and fixing the auction mechanism implementation for the NDN distributed repository.

## Current Status

**Tests:**
- ✅ Unit tests pass (7/7)
- ❌ Integration tests FAIL:
  - `TestAuctionReplication_FiveNodes`: Expected 3 claims, got 1
  - `TestAuction_ConcurrentCommands`: Expected 2 commands, got 0

## Changes Made So Far

### 1. Fixed Duplicate Code (repo/repo.go)
- Removed duplicate `if cancelled` check that was checking the same condition twice

### 2. Adjusted Timeout Values (repo/repo.go:34-35)
- Changed `DEFAULT_AUCTION_TIMEOUT` from 500ms to 3s
- Changed `DEFAULT_AUCTION_BACKUP_DELAY` from 2500ms to 2s

### 3. Added Extensive Debug Logging
- Added logging to `onCommand`, `runAuction`, `queueAuction`
- Added logging to `doRunAuction`, `sendBidRequestAndWait`
- Added logging to `processAuctionResults`, `onBidInterest`
- Added logging to `onGroupSync` for job assignment handling

### 4. Imports Added
- Added `fmt` and `os` for debug output

## Investigation Findings

### Key Observations
1. The auctioneer (node a) runs the auction but only produces 1 claim
2. Other nodes (b, c, d, e) are not claiming jobs
3. Commands: 0 for all nodes
4. Claims: Only node a has 1 claim

### Code Flow
1. Producer sends command → Node a receives it (onCommand)
2. Node a: adds command, publishes NodeUpdate with NewCommand
3. Node a: runs auction → sends bid requests to b, c, d, e
4. Node a: processes responses, selects winners, publishes results
5. Other nodes: should receive JobAssignments via SVS (onGroupSync) and claim jobs

### Issue Hypothesis
The issue appears to be that:
1. Either the auction is not completing properly
2. Or the JobAssignments are not propagating correctly via SVS
3. Or nodes are not recognizing themselves in the assignee list

## Todo List

- [ ] Debug why only 1 claim instead of 3
- [ ] Fix auction mechanism issues
- [ ] Run integration tests to verify fixes
- [ ] Calibrate auction timeouts (24 nodes, 5 iterations)
- [ ] Run full experiments: auction vs hydra
- [ ] Analyze results

## Key Files

- `repo/repo.go`: Main auction implementation (~1540 lines)
- `tlv/definitions.go`: TLV type definitions
- `auction-spec`: Auction specification document (306 lines)
- `repo/auction_test.go`: Unit tests
- `repo/integration_test.go`: Integration tests
