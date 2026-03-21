================================================================================
AUCTION SPECIFICATION - WORK IN PROGRESS
================================================================================

Last Updated: 2026-03-11

OVERVIEW
================================================================================

The auction specification has been restructured into two documents:

1. auction-spec (normative, RFC-style)
   - Describes protocol behavior, message formats, processing rules
   - Uses RFC-style language (MUST, SHOULD, MAY)
   - ~282 lines

2. auction-spec-legacy (non-normative, implementation guidance)
   - Contains pseudocode, CLI flags, constants, event logging
   - ~254 lines

RECENT CHANGES
================================================================================

Fixed timestamp collision resolution to include target matching:
- Timestamp comparison only applies when auctions are for the SAME target
- Concurrent auctions for different targets are independent

Files modified:
- docs/specs/auction-spec (Section 5.1, Section 7.2)
- docs/specs/auction-spec-legacy (Peer pseudocode)

CURRENT STATE
================================================================================

The auction-spec documents are complete and consistent. No outstanding issues
known at this time.

POTENTIAL FUTURE WORK (NOT STARTED)
================================================================================

1. Refactor hydra-spec and shared-spec in a similar RFC-style manner
2. Add formal security considerations section to auction-spec
3. Review TLV type assignments for consistency

================================================================================
