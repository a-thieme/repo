# Plan: Combine Specifications into Single Document

## Objective

Merge `shared-spec`, `auction-spec`, and `hydra-spec` into a single comprehensive specification document, similar to RFC style, suitable for an opencode agent to read and build the system.

## Proposed Document Structure

```
================================================================================
Distributed Repository Specification
================================================================================

1. Introduction

   - Overview of the distributed repository
   - Purpose of this document
   - Brief description of distribution mechanisms (auction, hydra)
   - Relationship to NDN SVS and producer commands

2. Terminology

   All terms from all three specs, organized logically:

   2.1 Core Concepts
       - Job Target (Target)
       - JobAssignment
       - NodeUpdate
       - Command

   2.2 Auction-Specific
       - ResultsName
       - Bid Interest Name
       - Auction Timestamp

   2.3 Hydra-Specific
       (None - uses core concepts)

3. Constants

   All constants from all specs:

   3.1 Timing
       - HEARTBEAT_INTERVAL
       - HEARTBEAT_TIMEOUT

   3.2 Storage
       - STORAGE_THRESHOLD
       - REPLICATION_FACTOR

4. TLV Definitions

   All TLV types, organized by message:

   4.1 Command (received from producer)
       - Type, Target, SnapshotThreshold

   4.2 NodeUpdate (SVS-published)
       - Jobs, NewCommand, StorageCapacity, StorageUsed, JobRelease, JobAssignments

   4.3 JobAssignment
       - Target, Assignees

   4.4 Auction Messages
       - MetricRequest (Target, Timestamp, ResultsName)
       - MetricResponse (Capacity, Used, Delay)

5. Shared Semantics

   Content from shared-spec Sections 5-9:

   5.1 Heartbeat Mechanism
       - 5.1 Publication
       - 5.2 Reception
       - 5.3 Timeout

   5.2 Replication Check
       - 6.1 Triggers
       - 6.2 Algorithm

   5.3 Command Processing

   5.4 Winner Determination
       - 8.1 Filter
       - 8.2 Sort
       - 8.3 Select

   5.5 Ordering Semantics

6. Auction Distribution Mechanism

   Content from auction-spec, with internal section references updated:

   6.1 Message Formats
       - MetricRequest
       - MetricResponse

   6.2 Auction Initiation

   6.3 Collision Resolution
       - 6.3.1 Timestamp Ordering
       - 6.3.2 Delay Flag Semantics
       - 6.3.3 Rescheduled Auctions

   6.4 Processing Rules
       - 6.4.1 Auctioneer
       - 6.4.2 Peer
       - 6.4.3 Results Handling

7. Hydra Distribution Mechanism

   Content from hydra-spec:

   7.1 Command Processing

   7.2 Job Assignment Processing

   7.3 Failure Recovery

8. Configuration

   (Optional - not in current specs but useful for agent)
   - CLI flags
   - Default values
```

## Content Migration Details

### shared-spec → Combined Document

| Original Section | New Location | Notes |
|-----------------|--------------|-------|
| 1. Introduction | 1. Introduction | Expand to cover all mechanisms |
| 2. Terminology | 2.1 Core Concepts | |
| 3. Constants | 3. Constants | |
| 4. TLV Definitions | 4.1-4.3 | |
| 5. Heartbeat Mechanism | 5.1 | |
| 6. Replication Check | 5.2 | |
| 7. Command Processing | 5.3 | |
| 8. Winner Determination | 5.4 | |
| 9. Ordering Semantics | 5.5 | |

### auction-spec → Combined Document

| Original Section | New Location | Notes |
|-----------------|--------------|-------|
| 1. Introduction | 1. Introduction | Merge with shared-spec intro |
| 2. Terminology | 2.2 Auction-Specific | |
| 3. Message Formats | 6.1 | |
| 4. Auction Initiation | 6.2 | |
| 5. Collision Resolution | 6.3 | |
| 6. Processing Rules | 6.4 | |

### hydra-spec → Combined Document

| Original Section | New Location | Notes |
|-----------------|--------------|-------|
| 1. Introduction | 1. Introduction | Merge with shared-spec intro |
| 2. Terminology | (reference to 2.1) | Already covered in core |
| 3. Command Processing | 7.1 | |
| 4. Job Assignment Processing | 7.2 | |
| 5. Failure Recovery | 7.3 | |

## Required Adjustments

1. **Section numbering**: Update all cross-references from "shared-spec Section X" to new section numbers

2. **Terminology consolidation**: 
   - "JobAssignment" appears in all three - now in single location (2.1)
   - Remove "See shared-spec" references

3. **TLV numbering**: 
   - No changes needed - TLV numbers remain the same
   - Document that 0x292/0x293 are reused in auction MetricResponse with same semantic meaning

4. **Introduction expansion**: 
   - Combine introductions from all three specs
   - Add brief overview explaining the two distribution mechanisms

## Implementation Notes

- Total estimated lines: ~350-400 (vs. 331 combined currently)
- Document should remain self-contained - no external references needed
- All details from original specs must be preserved verbatim where possible

## Alternative Approaches Considered

### Option A: Two Documents (Recommended above)
- Combined spec for building
- Keep auction/hydra as "mode selection" reference

### Option B: Keep Separate, Add Master Index
- Add index document linking sections
- Rejected: adds complexity for agent, potential for drift

### Option C: Single Document with Profiles
- Single spec with "profile" sections for auction/hydra
- More complex structure, less clear separation

## Open Questions

1. **Should we include a configuration section?** 
   - Current specs don't have CLI flags/constants
   - Could add for agent benefit, but might duplicate code
   
2. **Should we add a "Quick Start" or "Overview" section?**
   - Would help agent understand which sections to read for each mode
   - RFCs often have an "Overview" section

3. **Naming the combined document?**
   - Option: `spec.md` or `distributed-repo-spec.md`
   - Or: `SPEC.md` (convention for project specs)

## Recommended Filename

`docs/specs/SPEC.md` - aligns with convention for specification documents

---

Plan prepared: 2026-03-12
