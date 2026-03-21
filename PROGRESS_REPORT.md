# Auction Mechanism Investigation - Progress Report

## Goal
Fix the Auction distribution mechanism to achieve full replication factor (RF=3) with multiple producers (2+) in the 24-node mini-ndn testbed.

## Background
The distributed repo has two distribution mechanisms:
- **Hydra**: Works correctly for all producer counts
- **Auction**: Only achieves full RF with 1 producer; fails with 2+ producers

## What Was Done

### 1. Strategy Setting Fix (runner.py)
**File**: `/home/adam/ndn/repo/experiments/runner.py`

**Problem**: The original code used regex pattern for bid prefix strategy:
```python
host.cmd("nfdc strategy set /ndn/repo/.*/bid /localhost/nfd/strategy/multicast 2>&1")
```

**Fix**: Changed to explicit per-node prefixes:
```python
for other_host in ndn.net.hosts:
    bid_prefix = f"/ndn/repo/{other_host.name}/bid"
    host.cmd(f"nfdc strategy set {bid_prefix} /localhost/nfd/strategy/multicast 2>&1")
```

Also added verification for bid prefix strategies (lines 318-347).

### 2. Auction Timeout Increase (repo/main.go)
**File**: `/home/adam/ndn/repo/repo/main.go`

**Problem**: Default timeout was 1750ms, too short for 24-node topology

**Fix**: Changed to 6000ms:
```go
auctionTimeout := flag.Duration("auction-timeout", 6000*time.Millisecond, "Auction timeout...")
```

### 3. Debug Logging (repo/repo.go)
**File**: `/home/adam/ndn/repo/repo/repo.go`

**Changes**:
- Line 650: Changed from Debug to Info level for bid interest receipt:
  ```go
  log.Info(r, "onBidInterest_received", "name", name.String(), "from", name.Prefix(2).String())
  ```
- Added logging when sending bid interests (line 865):
  ```go
  log.Info(r, "runAuction_sending_bid_interest", "peer", peerCopy, "bidName", bidName.String())
  ```
- Added logging when bid received (line 880):
  ```go
  log.Info(r, "runAuction_bid_received", "peer", peerCopy, "result", args.Result)
  ```

### 4. Wait for Full Timeout (repo/repo.go)
**File**: `/home/adam/ndn/repo/repo/repo.go`

**Problem**: Auction was exiting early when all bids received, not waiting for SVS convergence

**Fix**: Changed auction loop to always wait for full timeout (lines 912-925):
```go
auctionTimer := time.NewTimer(r.auctionTimeout)
receivedCount := 0
for {
    select {
    case <-auctionTimer.C:
        log.Info(r, "runAuction_timeout", "target", targetStr, "received", receivedCount, "total_peers", len(peers))
        goto determine_winners
    case <-responsesCh:
        receivedCount++
        log.Debug(r, "runAuction_bid_received", "peer", "unknown", "count", receivedCount, "total", len(peers))
        // Don't exit early - wait for full timeout to allow SVS convergence
    }
}
```

### 5. Route Propagation (runner.py)
**File**: `/home/adam/ndn/repo/experiments/runner.py`

**Changes**:
- Added RIB-to-FIB sync (line ~300):
  ```python
  for host in ndn.net.hosts:
      host.cmd("nfdc rib announce 2>&1")
  ```
- Added 5-second delay for route propagation

## Test Results

### Current State
| Test | Nodes | Producers | Result |
|------|-------|----------|--------|
| Hydra | 24 | 2 | ✅ PASS (RF=3) |
| Auction | 5 | 2 | ⚠️ RF=1 (partial) |
| Auction | 24 | 2 | ❌ RF=0 (fails) |

### Detailed Logs Analysis

**5-node test timeline (from stdout-afa.log)**:
```
19:08:53.998 - Sending bid interests to savi, neu, osaka, ucla
19:09:10.041 - All bids timeout (~16 seconds later)
19:09:10.042 - Auction publishes with 0 received bids
```

**Key observation**: Interests are being sent but timing out after ~16 seconds (not the 6-second timeout), suggesting retries are happening.

## Root Cause Analysis

### What's Working
1. ✅ SVS sync - Commands propagate to all nodes via multicast
2. ✅ Bid prefix registration - Each node registers `/ndn/repo/{node}/bid`
3. ✅ FIB entries - Routes exist for all bid prefixes
4. ✅ Strategy setting - Multicast strategy is set for bid prefixes

### What's NOT Working
1. ❌ Bid interests from auctioneer to other nodes never arrive
2. ❌ Receiving nodes never log "onBidInterest_received"
3. ❌ Interests time out despite valid routes in FIB

### Hypothesis
The Go NDN library (`github.com/named-data/ndnd`) may not be properly integrating with NFD's forwarding in mini-ndn. When expressing interests via `r.client.ExpressR()`, the library might be:
- Not properly using NFD's FIB for forwarding
- Trying to forward directly without going through NFD
- Having issues with face creation or interest transmission in the mini-ndn environment

**Evidence**:
- FIB entries exist and look correct
- SVS sync works (uses same transport)
- No errors logged about interest sending
- Interest simply never reaches destination

## Files Modified

### `/home/adam/ndn/repo/experiments/runner.py`
- Lines 300-316: Explicit strategy setting for bid prefixes
- Lines ~300: Added RIB-to-FIB sync
- Lines 318-347: Added bid prefix verification

### `/home/adam/ndn/repo/repo/main.go`
- Line 22: Increased auction-timeout to 6000ms

### `/home/adam/ndn/repo/repo/repo.go`
- Line 650: Changed debug to info logging for onBidInterest
- Line 865: Added logging for sending bid interests
- Line 880: Added logging for received bids
- Lines 912-925: Changed to wait for full timeout

## Remaining Issues

1. **Bid interest routing**: Interests from auctioneer to peers timeout despite valid FIB entries
2. **No responses received**: `receivedCount=0` in all tests
3. **24-node topology fails completely**: Even RF=1 not achieved

## Suggested Next Steps

### Option 1: Debug NDN Library Integration
- Add more verbose logging at the NDN library level
- Check if faces are being created properly
- Verify interest packets are actually being sent to NFD

### Option 2: Change Metric Collection Approach
Instead of direct peer-to-peer bid interests, use a shared metric collection approach:
- All nodes publish their metrics to a shared prefix (e.g., `/ndn/drepo/metrics/{node}`)
- Auctioneer subscribes to this prefix via SVS
- Collects metrics from all nodes without direct interests

### Option 3: Debug Mini-NDN Specific Issues
- Check if there's a firewall or network policy blocking
- Verify face creation between nodes works
- Test basic connectivity between nodes

### Option 4: Accept Partial Replication
- Document Auction as only working reliably with single producer
- Hydra works correctly for all producer counts

## Test Commands

```bash
# Build
cd /home/adam/ndn/repo
go build -o bin/repo ./repo
go build -o bin/producer ./producer

# Build Docker
cd /home/adam/ndn/repo/experiments
make build

# Run test
cd /home/adam/ndn/repo/experiments
TIMESTAMP=test NODES=24 PRODUCERS=2 COMMAND_COUNT=1 DISTRIBUTION=auction make single
```

## Key Code Locations

- **Auction entry point**: `repo/repo.go:813` - `runAuction()` function
- **Bid interest sending**: `repo/repo.go:866` - `ExpressR()` call
- **Bid interest handling**: `repo/repo.go:649` - `onBidInterest()` function
- **Winner determination**: `repo/helpers.go:363` - `DetermineWinners()` function
- **Strategy setting**: `experiments/runner.py:300-316`
