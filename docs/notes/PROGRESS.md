# NDN Distributed Repository - Auction Mechanism Implementation

## Summary

Implementing an auction-based distribution mechanism for the NDN repo to compare performance with Hydra on testbed topology.

## Accomplished So Far

### Core Implementation
- ✅ **Removed SVS JobAssignment publishing** - Was incorrect protocol usage per user clarification
- ✅ **Auction results publishing** - Auctioneer publishes results Data via `client.Store().Put()` (verified with logs)
- ✅ **Self-claiming works** - Auctioneer successfully claims its own job (countReplication returns correct count)
- ✅ **Unit tests pass** - All 16 unit tests pass

### Experiment Infrastructure
- ✅ **Added `/results` prefix to FIB** - Modified `runner.py` to register `/ndn/repo/{hostname}/results` prefix so bidders can fetch results

### Known Issues
- ⚠️ **Results fetching timeout** - Bidders call `subscribeToResults()` but ExpressR for results Data always times out
- ⚠️ **FIB prefix issue** - The FIB doesn't show `/ndn/repo/X/results` prefixes in mini-ndn (may need the NDN libs to be loaded via system restart)

## Current State

The auction runs and publishes results Data successfully (logs confirm `publish_results_success`), but bidders cannot fetch the results Data via ExpressR - it times out. This appears to be a routing/FIB issue in mini-ndn.

### Last Action Taken
Added `/ndn/repo/{hostname}/results` prefix to the routing helper in `experiments/runner.py`:

```python
# Line ~175-177
results_prefix = f'{node_prefix}/results'
grh.addOrigin([host], ["/ndn/drepo/group-messages/32=svs", node_prefix, sync_data_prefix, results_prefix])
```

## Next Steps

1. **Restart system** - Let mini-ndn libs load properly (user is doing this)
2. **Run unit tests**: `make test-unit`
3. **Run small test**: 5 nodes, 1 command, RF=3
   ```bash
   cd experiments && make single NODES=5 PRODUCERS=1 COMMAND_COUNT=1 RF=3 DISTRIBUTION=auction TIMEOUT=120
   ```
4. **Debug if needed**: Check FIB entries and routing
5. **Run calibration**: `make calibrate CALIBRATE_NODES=24 CALIBRATE_ITER=10`
6. **Run comparison experiments**: hydra vs auction with 1,4,8,16,24 producers

## Relevant Files

- `repo/repo.go` - Main auction implementation
  - `publishAuctionResults` (line ~1044)
  - `subscribeToResults` (line ~1323)
  - `onBidInterest` (line ~1269)
  - `processAuctionResults` (line ~1076)

- `experiments/runner.py` - Experiment runner (modified to add results prefix)
- `experiments/Makefile` - Build and run targets

## Experiment Configuration

- Nodes: 5, 24
- Producers: 1, 4, 8, 16, 24
- Replication Factor: 3
- Commands per producer: 1
- Calibration iterations: 10
- Distribution: hydra, auction
