# Test & Experiment Results

**Date**: 2026-02-23

## Summary

| Test Suite | Status | Pass/Fail | Notes |
|------------|--------|-----------|-------|
| Unit tests (`make test-short`) | PASS | 9/9 | Event logging, counting face, replication logic |
| Integration (`make test-integration`) | PASS | 9/9 | Local NFD, 3-5 nodes |
| Failure recovery (`make test-failure`) | FAIL | 0/2 | Recovery logic not implemented |
| Mini-NDN (`make test-mini-ndn`) | PASS | 8/8 | Docker, 5-24 nodes, up to 100 commands |

## Detailed Results

### Unit Tests (PASS)

All unit tests pass. Tests cover:
- Event logger write/parse
- Sync interest and data packet counting
- Node update parsing
- TLV type parsing
- Replication logic (determineWinnersHydra)
- Multi-node sync simulation

### Integration Tests (PASS)

All local NFD integration tests pass:

| Test | Nodes | RF | Result |
|------|-------|-----|--------|
| TestLocalReplication_FiveNodes | 5 | 3 | PASS (4 claims) |
| TestLocalReplication_FiveNodes_NoCache | 5 | 3 | PASS (3 claims) |
| TestCommandPropagation | 3 | 2 | PASS (2 claims) |
| TestEventLog_* | - | - | PASS |
| TestTLV_* | - | - | PASS |

### Failure Recovery Tests (FAIL)

**TestFailureRecovery_SingleRepoDown**: FAIL
- Config: 5 nodes, RF=3, kill 1 repo
- Error: "Initial replication failed: expected 3 claims, got 0"
- Root cause: Likely stale routes or SVS sync timing issue

**TestFailureRecovery_MultipleReposDown**: FAIL
- Config: 5 nodes, RF=3, kill 2 repos
- Error: "Recovery not achieved within 30s"
- Initial replication: 3 claims (n2, n3, n4)
- After killing n3, n4: 1 claim remaining
- Root cause: **Re-replication logic not implemented** - surviving nodes don't reclaim jobs when node dies

### Mini-NDN Docker Tests (PASS)

All Docker/mini-ndn tests pass:

| Test | Nodes | Producers | Commands | Result |
|------|-------|-----------|----------|--------|
| TestMiniNDNIntegration | 5 | 1 | 1 | PASS (3 claims, 31ms) |
| TestMiniNDNIntegration_TwoProducers_SingleCommand | 5 | 2 | 1 each | PASS (2 cmds at RF) |
| TestMiniNDNIntegration_TwoProducers_MultiCommand | 5 | 2 | 5 each | PASS (10 cmds at RF) |
| TestMiniNDNIntegration_100Commands_2Producers | 24 | 2 | 50 each | PASS (100/100 at RF) |
| TestMiniNDNIntegration_100Commands_5Producers | 24 | 5 | 20 each | PASS (100/100 at RF) |
| TestMiniNDNIntegration_100Commands_10Producers | 24 | 10 | 10 each | PASS (100/100 at RF) |
| TestMiniNDNIntegration_100Commands_20Producers | 24 | 20 | 5 each | PASS (100/100 at RF) |

### New Experiment Tests (PASS)

| Test | Description | Result |
|------|-------------|--------|
| TestConcurrentCommands_TwoProducers | 2 producers sending simultaneously | PASS (2 cmds at RF) |
| TestMultipleCommands_Sequential | 10 sequential commands | PASS (10 cmds at RF) |
| TestReplicationFactor_EdgeCases/RF1 | RF=1 (no redundancy) | PASS (1 claim) |
| TestReplicationFactor_EdgeCases/RFEqualsNodes | RF=node count | PASS (3 claims) |
| TestNodeJoin_MidStream | Add node after commands | PASS (logs only, no assertions) |

## Known Issues

### 1. Failure Recovery Not Implemented (HIGH)

When a node dies, surviving nodes detect it via heartbeat monitor but don't re-claim jobs that were on the dead node.

**File**: `repo/repo.go`
**Function**: `runHeartbeatMonitor()`
**Fix needed**: When detecting offline node, iterate through jobs and trigger re-replication for any jobs now below RF.

### 2. Flaky Initial Replication (MEDIUM)

Sometimes initial replication fails with 0 claims, likely due to SVS sync timing.

**Mitigation**: Increase SVS health timeout or add retry logic.

## Missing Test Coverage

The following scenarios are not tested:

1. **Storage pressure** - Filling nodes to capacity, job release behavior (requires JOIN commands with simulated storage growth)
2. **Network partition** - Split-brain scenarios, healing behavior
3. **Cascading failures** - Killing RF-1 nodes sequentially (test exists but recovery logic not implemented)
4. **Node join** - Adding nodes mid-stream (test exists but only logs, no assertions)

## Recommendations

1. **Fix failure recovery** - Implement re-replication in heartbeat monitor
2. **Add concurrent command tests** - Test race conditions
3. **Add storage pressure tests** - Verify job release at 75% capacity
4. **Add network partition tests** - Use iptables in mini-ndn to partition network
5. **Increase SVS health timeout** - Make initial replication more reliable
