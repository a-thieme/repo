#!/usr/bin/env python3
"""
NDN Repository Experiment Trace Analyzer

Analyzes command traces and sync interests across experiments.
Produces detailed breakdowns of:
- Command phase durations (ingestion, propagation, assignment chain, replication, sync)
- Sync interest categorization (heartbeat, new command, assignment, job release)
- Assignment conflict analysis

Usage:
    python3 analyze_traces.py --experiments <dir1> <dir2> ... [--output-dir <dir>] [--format json,csv,markdown]
"""

import argparse
import bisect
import json
import sys
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime
from pathlib import Path
from typing import Dict, List, Any, Optional, Tuple
from collections import defaultdict
import statistics


PHASE_CONFIG = {
    "heartbeat_interval_ms": 5000,
    "heartbeat_tolerance_ms": 500,
    "new_command_window_ms": 100,
    "assignment_window_ms": 200,
    "job_release_window_ms": 200,
    "conflict_window_ms": 500,
    "replication_factor": 3,
}


class GroundTruthRTT:
    """Container for pre-measured ground truth RTTs between node pairs."""

    def __init__(self, rtt_dict: Dict[str, Dict[str, float]]):
        # rtt_dict: {node1: {node2: rtt_ms, ...}, ...}
        self.rtts = {}
        for n1, inner in rtt_dict.items():
            self.rtts[n1.lower()] = {}
            for n2, rtt in inner.items():
                self.rtts[n1.lower()][n2.lower()] = rtt

    def get_rtt(self, node1: str, node2: str) -> Optional[float]:
        """Get RTT between two nodes. Returns None if not found."""
        n1 = node1.lower() if isinstance(node1, str) else "unknown"
        n2 = node2.lower() if isinstance(node2, str) else "unknown"
        return self.rtts.get(n1, {}).get(n2)


def extract_short_name(node_path: str) -> str:
    """Extract short node name from full path like /ndn/repo/ucla -> ucla."""
    if not node_path:
        return "unknown"
    # Handle full paths like /ndn/repo/ucla
    parts = node_path.strip("/").split("/")
    return parts[-1].lower() if parts else "unknown"


def parse_topology_delays(topo_path: Path) -> tuple:
    """Parse link delays from topology file.
    Returns: ({node_pair: delay_ms}, median_delay_ms, all_nodes_set)
    """
    delays = {}
    median_delays = []
    all_nodes = set()

    if not topo_path.exists():
        return delays, None, all_nodes

    try:
        with open(topo_path) as f:
            in_links = False
            for line in f:
                line = line.strip()
                if line == "[nodes]":
                    continue
                if line == "[links]":
                    in_links = True
                    continue
                if line.startswith("[") and in_links:
                    break
                # Parse nodes section for node names
                if not in_links and ":" in line and not line.startswith("/"):
                    parts = line.split()
                    if parts:
                        node_name = parts[0].lower()
                        all_nodes.add(node_name)
                # Parse links section for delays
                if in_links and ":" in line and "delay" in line:
                    parts = line.split()
                    node_part = parts[0]
                    delay_part = [p for p in parts if "delay" in p][0]

                    nodes = node_part.split(":")
                    if len(nodes) != 2:
                        continue

                    node1, node2 = nodes[0].lower(), nodes[1].lower()
                    all_nodes.add(node1)
                    all_nodes.add(node2)
                    delay_str = delay_part.split("=")[1].rstrip("ms")
                    try:
                        delay_ms = float(delay_str)
                        delays[(node1, node2)] = delay_ms
                        delays[(node2, node1)] = delay_ms
                        median_delays.append(delay_ms)
                    except (ValueError, IndexError):
                        continue
    except IOError:
        pass

    median = statistics.median(median_delays) if median_delays else None
    return delays, median, all_nodes


def compute_all_pair_delays(
    direct_delays: Dict[Tuple[str, str], float], all_nodes: set, fallback_delay: float = 50.0
) -> Dict[Tuple[str, str], float]:
    """Compute shortest path delays between all node pairs using Floyd-Warshall.

    Args:
        direct_delays: Dict of (node1, node2) -> direct link delay
        all_nodes: Set of all node names
        fallback_delay: Default delay for pairs without any path

    Returns:
        Dict of (node1, node2) -> shortest path delay in ms
    """
    if not all_nodes:
        return {}

    nodes = sorted(all_nodes)
    n = len(nodes)
    node_idx = {node: i for i, node in enumerate(nodes)}

    # Initialize distance matrix with infinity
    INF = float("inf")
    dist = [[INF] * n for _ in range(n)]

    # Self-distance is 0
    for i in range(n):
        dist[i][i] = 0

    # Set direct link distances
    for (n1, n2), delay in direct_delays.items():
        if n1 in node_idx and n2 in node_idx:
            dist[node_idx[n1]][node_idx[n2]] = delay

    # Floyd-Warshall
    for k in range(n):
        for i in range(n):
            for j in range(n):
                if dist[i][k] + dist[k][j] < dist[i][j]:
                    dist[i][j] = dist[i][k] + dist[k][j]

    # Convert back to dict with (node1, node2) keys
    pair_delays = {}
    for i, n1 in enumerate(nodes):
        for j, n2 in enumerate(nodes):
            if i != j and dist[i][j] < INF:
                pair_delays[(n1, n2)] = dist[i][j]
            elif i != j:
                # No path exists, use fallback
                pair_delays[(n1, n2)] = fallback_delay

    return pair_delays


def parse_ts(ts_str: str) -> Optional[datetime]:
    """Parse ISO timestamp from event log."""
    if not ts_str:
        return None
    ts = ts_str.replace("Z", "+00:00")
    if "." in ts:
        parts = ts.split(".")
        frac_and_tz = parts[1]
        if "+" in frac_and_tz:
            frac, tz = frac_and_tz.split("+")
            frac = frac[:6]
            ts = f"{parts[0]}.{frac}+{tz}"
        elif "-" in frac_and_tz[1:]:
            idx = frac_and_tz.rfind("-")
            frac = frac_and_tz[:idx][:6]
            tz = frac_and_tz[idx:]
            ts = f"{parts[0]}.{frac}{tz}"
    try:
        return datetime.fromisoformat(ts)
    except ValueError:
        return None


def parse_events(exp_dir: Path) -> Tuple[List[Dict], Dict]:
    """Parse all event files from an experiment directory."""
    events = []
    metadata = {}

    metadata_file = exp_dir / "metadata.json"
    if metadata_file.exists():
        with open(metadata_file) as f:
            metadata = json.load(f)

    event_files = list(exp_dir.glob("events-*.jsonl"))

    def parse_file(event_file: Path) -> List[Dict]:
        file_events = []
        node_name = event_file.stem.replace("events-", "")
        with open(event_file) as f:
            for line in f:
                try:
                    event = json.loads(line.strip())
                    event["_node_name"] = node_name
                    file_events.append(event)
                except json.JSONDecodeError:
                    continue
        return file_events

    if len(event_files) == 0:
        return events, metadata

    with ThreadPoolExecutor(max_workers=min(8, len(event_files))) as executor:
        futures = [executor.submit(parse_file, ef) for ef in event_files]
        for future in as_completed(futures):
            events.extend(future.result())

    events.sort(key=lambda e: parse_ts(e.get("ts", "")) or datetime.min)

    return events, metadata


def build_command_timelines(events: List[Dict], metadata: Dict) -> Dict[str, Any]:
    """Reconstruct timeline for each command from events."""
    timelines = {}

    replication_factor = metadata.get(
        "replication_factor", PHASE_CONFIG["replication_factor"]
    )
    commands = metadata.get("commands", {})

    for cmd_id, cmd_data in commands.items():
        timeline = {
            "cmd_id": cmd_id,
            "producer_node": cmd_data.get("timeline", [{}])[0].get("node", "unknown"),
            "timeline_events": cmd_data.get("timeline", []),
            "final_replication": cmd_data.get("final_replication", 0),
            "max_replication": cmd_data.get("max_replication", 0),
            "was_ever_over_replicated": cmd_data.get("was_ever_over_replicated", False),
            "first_claim_ts": None,
            "first_claim_node": None,
            "all_claims_ts": [],
            "all_claim_nodes": [],
            "decisions": [],
            "first_decision_node": None,
            "assignments": [],
            "command_received_ts": None,
            "command_received_node": None,
            "command_published_ts": None,
            "command_published_node": None,
            "command_synced_ts": None,
            "command_synced_from": [],
            "svs_syncs": [],  # List of {publisher, receiver, synced_ts}
        }
        timelines[cmd_id] = timeline

    for event in events:
        event_type = event.get("event")
        ts = parse_ts(event.get("ts"))
        if ts is None:
            continue

        target = event.get("target") or event.get("cmdId")

        if target and target in timelines:
            tl = timelines[target]

            if event_type == "command_received":
                if tl["command_received_ts"] is None:
                    tl["command_received_ts"] = ts
                    tl["command_received_node"] = extract_short_name(event.get("node", ""))

            elif event_type == "command_synced":
                if tl["command_synced_ts"] is None:
                    tl["command_synced_ts"] = ts
                tl["command_synced_from"].append(event.get("from", "unknown"))
                # Track ALL syncs for SVS propagation analysis
                publisher = extract_short_name(event.get("from", ""))
                receiver = extract_short_name(event.get("node", ""))
                tl["svs_syncs"].append({
                    "publisher": publisher,
                    "receiver": receiver,
                    "synced_ts": ts,
                })

            elif event_type == "command_published":
                if tl["command_published_ts"] is None:
                    tl["command_published_ts"] = ts
                    tl["command_published_node"] = extract_short_name(event.get("node", ""))

            elif event_type == "job_claimed":
                claim_ts = ts
                claim_node = extract_short_name(event.get("node", ""))
                if tl["first_claim_ts"] is None:
                    tl["first_claim_ts"] = claim_ts
                    tl["first_claim_node"] = claim_node
                tl["all_claims_ts"].append(claim_ts)
                tl["all_claim_nodes"].append(claim_node)

            elif event_type == "decision_made":
                decision_node = extract_short_name(event.get("node", ""))
                if tl["first_decision_node"] is None:
                    tl["first_decision_node"] = decision_node
                tl["decisions"].append(
                    {
                        "ts": ts,
                        "node": decision_node,
                        "should_claim": event.get("shouldClaim", False),
                        "reason": event.get("reason", ""),
                        "current_replication": event.get("currentReplication", 0),
                        "needed_replication": event.get("neededReplication", 0),
                        "selected_candidates": event.get("selectedCandidates", []),
                    }
                )

            elif event_type == "assignment_handled":
                tl["assignments"].append(
                    {
                        "ts": ts,
                        "from": event.get("from", ""),
                        "action": event.get("action", ""),
                        "reason": event.get("reason", ""),
                        "assignees": event.get("assignees", []),
                    }
                )

    return timelines


def compute_phase_durations(
    timelines: Dict, metadata: Dict, pair_delays: Dict[Tuple[str, str], float] = None
) -> Dict[str, Any]:
    """Calculate duration for each phase per command.

    Args:
        timelines: Command timelines dict
        metadata: Experiment metadata
        pair_delays: Dict of (node1, node2) -> shortest path delay in ms. If None, skip RTT.
    """
    replication_factor = metadata.get(
        "replication_factor", PHASE_CONFIG["replication_factor"]
    )

    # Fallback delay if pair_delays not available
    fallback_delay = 10.0

    def get_pair_delay(node1: str, node2: str) -> float:
        """Get shortest path delay between two nodes."""
        if pair_delays is None:
            return fallback_delay
        # Normalize node names to lowercase
        n1 = node1.lower() if isinstance(node1, str) else "unknown"
        n2 = node2.lower() if isinstance(node2, str) else "unknown"
        if n1 == "unknown" or n2 == "unknown":
            return fallback_delay
        return pair_delays.get((n1, n2), fallback_delay)

    def get_rtt(node1: str, node2: str) -> float:
        """Get RTT (2 * one-way delay) between two nodes."""
        return 2 * get_pair_delay(node1, node2)

    phase_stats = {
        "ingestion_ms": [],
        "propagation_ms": [],
        "assignment_chain_ms": [],
        "replication_ms": [],
        "sync_ms": [],
        # RTT-normalized versions
        "ingestion_rtt": [],
        "propagation_rtt": [],
        "replication_rtt": [],
        "sync_rtt": [],
        "total_commands": len(timelines),
    }

    for cmd_id, tl in timelines.items():
        cmd_phases = {}

        cmd_received = tl["command_received_ts"]
        cmd_received_node = tl["command_received_node"]
        first_decision = tl["decisions"][0]["ts"] if tl["decisions"] else None
        first_decision_node = tl["first_decision_node"]
        first_claim = tl["first_claim_ts"]
        first_claim_node = tl["first_claim_node"]
        all_claims = sorted(tl["all_claims_ts"])
        all_claim_nodes = tl["all_claim_nodes"]
        command_synced = tl["command_synced_ts"]

        # Ingestion: cmd_received node -> decision node
        if cmd_received and first_decision:
            ingestion_ms = (first_decision - cmd_received).total_seconds() * 1000
            cmd_phases["ingestion_ms"] = ingestion_ms
            phase_stats["ingestion_ms"].append(ingestion_ms)
            expected_rtt = get_rtt(cmd_received_node, first_decision_node)
            if expected_rtt > 0:
                ingestion_rtt = ingestion_ms / expected_rtt
                cmd_phases["ingestion_rtt"] = ingestion_rtt
                phase_stats["ingestion_rtt"].append(ingestion_rtt)
        else:
            cmd_phases["ingestion_ms"] = None
            cmd_phases["ingestion_rtt"] = None

        # Propagation: decision node -> first claim node
        if first_decision and first_claim:
            propagation_ms = (first_claim - first_decision).total_seconds() * 1000
            cmd_phases["propagation_ms"] = propagation_ms
            phase_stats["propagation_ms"].append(propagation_ms)
            expected_rtt = get_rtt(first_decision_node, first_claim_node)
            if expected_rtt > 0:
                propagation_rtt = propagation_ms / expected_rtt
                cmd_phases["propagation_rtt"] = propagation_rtt
                phase_stats["propagation_rtt"].append(propagation_rtt)
        else:
            cmd_phases["propagation_ms"] = None
            cmd_phases["propagation_rtt"] = None

        # Assignment chain: time from first_claim to RFth_claim (we track ms but don't compute RTT)
        if first_claim and len(all_claims) >= replication_factor:
            rf_claim_time = all_claims[replication_factor - 1]
            assignment_chain_ms = (rf_claim_time - first_claim).total_seconds() * 1000
            cmd_phases["assignment_chain_ms"] = assignment_chain_ms
            phase_stats["assignment_chain_ms"].append(assignment_chain_ms)
        else:
            cmd_phases["assignment_chain_ms"] = None

        # Replication: cmd_received -> RFth claim (full cycle)
        if first_claim and len(all_claims) >= replication_factor:
            rf_claim_time = all_claims[replication_factor - 1]
            replication_ms = (rf_claim_time - cmd_received).total_seconds() * 1000
            cmd_phases["replication_ms"] = replication_ms
            phase_stats["replication_ms"].append(replication_ms)
            # For replication, we use the sum of ingestion and assignment chain RTT contributions
            # Or simply: total path delay from producer to last claimer
            if len(all_claim_nodes) >= replication_factor:
                last_claim_node = all_claim_nodes[replication_factor - 1]
                expected_rtt = get_rtt(cmd_received_node, last_claim_node)
                if expected_rtt > 0:
                    replication_rtt = replication_ms / expected_rtt
                    cmd_phases["replication_rtt"] = replication_rtt
                    phase_stats["replication_rtt"].append(replication_rtt)
        else:
            cmd_phases["replication_ms"] = None
            cmd_phases["replication_rtt"] = None

        # Sync: cmd_received -> command_synced
        if cmd_received and command_synced:
            sync_ms = (command_synced - cmd_received).total_seconds() * 1000
            cmd_phases["sync_ms"] = sync_ms
            phase_stats["sync_ms"].append(sync_ms)
            # For sync, we use the producer node as reference
            if cmd_received_node and first_decision_node:
                expected_rtt = get_rtt(cmd_received_node, first_decision_node)
                if expected_rtt > 0:
                    sync_rtt = sync_ms / expected_rtt
                    cmd_phases["sync_rtt"] = sync_rtt
                    phase_stats["sync_rtt"].append(sync_rtt)
        else:
            cmd_phases["sync_ms"] = None
            cmd_phases["sync_rtt"] = None

        tl["phases"] = cmd_phases

    summary = {}
    for phase, values in phase_stats.items():
        if phase == "total_commands":
            summary[phase] = len(timelines)
        elif values:
            sorted_values = sorted(values)
            summary[f"{phase}_min"] = min(values)
            summary[f"{phase}_max"] = max(values)
            summary[f"{phase}_avg"] = statistics.mean(values)
            summary[f"{phase}_median"] = statistics.median(values)
            # Percentiles
            n = len(sorted_values)
            summary[f"{phase}_p95"] = (
                sorted_values[int(n * 0.95)] if n >= 20 else sorted_values[-1]
            )
            summary[f"{phase}_p99"] = (
                sorted_values[int(n * 0.99)] if n >= 100 else sorted_values[-1]
            )
            if len(values) > 1:
                summary[f"{phase}_stdev"] = statistics.stdev(values)

    return {"commands": timelines, "summary": summary, "raw_stats": phase_stats}


def categorize_sync_interests(
    events: List[Dict], timelines: Dict, metadata: Dict
) -> Dict[str, Any]:
    """Categorize sync interests by trigger using binary search for efficiency."""
    sync_events = [e for e in events if e.get("event") == "sync_interest_sent"]
    node_updates = [e for e in events if e.get("event") == "node_update"]
    job_released = [e for e in events if e.get("event") == "job_released"]
    assignment_handled = [e for e in events if e.get("event") == "assignment_handled"]

    heartbeat_interval = PHASE_CONFIG["heartbeat_interval_ms"]
    heartbeat_tol = PHASE_CONFIG["heartbeat_tolerance_ms"]
    new_cmd_window = PHASE_CONFIG["new_command_window_ms"]
    assign_window = PHASE_CONFIG["assignment_window_ms"]
    job_release_window = PHASE_CONFIG["job_release_window_ms"]

    categories = {
        "heartbeat": [],
        "new_command": [],
        "assignment": [],
        "job_release": [],
        "other": [],  # Uncategorized sync interests
    }

    # Pre-parse timestamps and sort event lists by timestamp for binary search
    node_updates_sorted = sorted(
        [
            (parse_ts(u.get("ts")), u)
            for u in node_updates
            if parse_ts(u.get("ts")) is not None
        ],
        key=lambda x: x[0],
    )
    job_released_sorted = sorted(
        [
            (parse_ts(j.get("ts")), j)
            for j in job_released
            if parse_ts(j.get("ts")) is not None
        ],
        key=lambda x: x[0],
    )
    assignment_sorted = sorted(
        [
            (parse_ts(a.get("ts")), a)
            for a in assignment_handled
            if parse_ts(a.get("ts")) is not None
        ],
        key=lambda x: x[0],
    )

    # Extract just timestamps for binary search
    node_update_ts_list = [ts for ts, _ in node_updates_sorted]
    job_release_ts_list = [ts for ts, _ in job_released_sorted]
    assignment_ts_list = [ts for ts, _ in assignment_sorted]

    first_event_ts = parse_ts(sync_events[0].get("ts")) if sync_events else None
    experiment_start_ms = first_event_ts.timestamp() * 1000 if first_event_ts else 0

    def find_events_in_window(ts_list, events_list, sync_ts, window_ms):
        """Find events within window_ms before sync_ts using binary search."""
        sync_ts_ms = sync_ts.timestamp() * 1000
        window_start_ms = sync_ts_ms - window_ms
        # Create offset-aware datetime for comparison
        window_start_dt = datetime.fromtimestamp(
            window_start_ms / 1000, tz=sync_ts.tzinfo
        )
        # Find left bound using binary search
        left = bisect.bisect_left(ts_list, window_start_dt)
        # Iterate only through events in window
        for i in range(left, len(events_list)):
            event_ts = ts_list[i]
            if (sync_ts - event_ts).total_seconds() * 1000 > window_ms:
                break
            yield events_list[i]

    for sync_event in sync_events:
        sync_ts = parse_ts(sync_event.get("ts"))
        if sync_ts is None:
            continue
        sync_ts_ms = sync_ts.timestamp() * 1000

        elapsed_ms = sync_ts_ms - experiment_start_ms

        heartbeat_bucket = round(elapsed_ms / heartbeat_interval)
        expected_heartbeat = heartbeat_bucket * heartbeat_interval
        if abs(elapsed_ms - expected_heartbeat) <= heartbeat_tol:
            categories["heartbeat"].append(sync_event)
            continue

        categorized = False

        # Use binary search to find events in window
        for update_ts, update in find_events_in_window(
            node_update_ts_list, node_updates_sorted, sync_ts, new_cmd_window
        ):
            if update.get("newCommand") or update.get("NewCommand"):
                categories["new_command"].append(sync_event)
                categorized = True
                break

        if not categorized:
            for assign_ts, assign in find_events_in_window(
                assignment_ts_list, assignment_sorted, sync_ts, assign_window
            ):
                categories["assignment"].append(sync_event)
                categorized = True
                break

        if not categorized:
            for release_ts, release in find_events_in_window(
                job_release_ts_list, job_released_sorted, sync_ts, job_release_window
            ):
                categories["job_release"].append(sync_event)
                categorized = True
                break

        if not categorized:
            categories["other"].append(sync_event)

    total = len(sync_events)
    total_commands = metadata.get("total_commands", 1) or 1  # Avoid division by zero

    def calc_stats(count):
        return {
            "count": count,
            "percentage": (count / total * 100) if total > 0 else 0,
            "per_command": count / total_commands,
        }

    breakdown = {
        "total_sync_interests": total,
        "heartbeat": calc_stats(len(categories["heartbeat"])),
        "new_command": calc_stats(len(categories["new_command"])),
        "assignment": calc_stats(len(categories["assignment"])),
        "job_release": calc_stats(len(categories["job_release"])),
        "other": calc_stats(len(categories["other"])),
    }

    return breakdown


def analyze_assignment_conflicts(events: List[Dict], metadata: Dict) -> Dict[str, Any]:
    """Analyze assignment handled events and publication patterns."""
    assignment_events = [e for e in events if e.get("event") == "assignment_handled"]
    decision_events = [e for e in events if e.get("event") == "decision_made"]
    node_update_events = [
        e for e in events if e.get("event") == "node_update" and e.get("jobs")
    ]

    by_reason = defaultdict(int)
    by_action = defaultdict(int)

    for event in assignment_events:
        reason = event.get("reason", "unknown")
        action = event.get("action", "unknown")
        by_reason[reason] += 1
        by_action[action] += 1

    unique_assignments = defaultdict(int)
    for event in assignment_events:
        target = event.get("target", "")
        from_node = event.get("from", "")
        if target and from_node:
            key = (target, from_node)
            unique_assignments[key] += 1

    unique_targets = set()
    for event in assignment_events:
        target = event.get("target", "")
        if target:
            unique_targets.add(target)

    total_node_updates_with_jobs = len(node_update_events)
    unique_node_update_sources = defaultdict(int)
    for event in node_update_events:
        from_node = event.get("from", "")
        if from_node:
            unique_node_update_sources[from_node] += 1

    conflict_window = PHASE_CONFIG["conflict_window_ms"]

    target_assignees = defaultdict(list)
    for event in assignment_events:
        target = event.get("target", "")
        if not target:
            continue
        ts = parse_ts(event.get("ts"))
        if ts is None:
            continue
        assignees = tuple(sorted(event.get("assignees", [])))
        target_assignees[target].append((ts, assignees))

    conflicts = []
    for target, assignments in target_assignees.items():
        assignments.sort(key=lambda x: x[0])
        for i, (ts1, assignees1) in enumerate(assignments):
            for ts2, assignees2 in assignments[i + 1 :]:
                delta_ms = (ts2 - ts1).total_seconds() * 1000
                if delta_ms > conflict_window:
                    break
                if assignees1 != assignees2:
                    conflicts.append(
                        {
                            "target": target,
                            "time_delta_ms": delta_ms,
                            "assignees_1": list(assignees1),
                            "assignees_2": list(assignees2),
                        }
                    )

    selected_candidates = defaultdict(int)
    for event in decision_events:
        selected = event.get("selectedCandidates", [])
        for candidate in selected:
            selected_candidates[candidate] += 1

    reassignment_events = [
        e
        for e in events
        if e.get("event") == "assignment_handled" and e.get("action") == "reassigned"
    ]

    return {
        "total_assignment_handled": len(assignment_events),
        "unique_assignments": len(unique_assignments),
        "unique_targets": len(unique_targets),
        "processing_ratio": len(assignment_events) / len(unique_assignments)
        if unique_assignments
        else 0,
        "by_reason": dict(by_reason),
        "by_action": dict(by_action),
        "total_conflicts": len(conflicts),
        "conflict_examples": conflicts[:10],
        "candidate_selection_counts": dict(selected_candidates),
        "total_reassignments": len(reassignment_events),
        "reassignment_events": reassignment_events[:20],
        "total_node_updates_with_jobs": len(node_update_events),
        "unique_node_update_sources": len(unique_node_update_sources),
    }


def analyze_publication_triggers(events: List[Dict], metadata: Dict) -> Dict[str, Any]:
    """Analyze what triggers group message publications using binary search for efficiency."""
    node_updates = [
        e for e in events if e.get("event") == "node_update" and e.get("jobs")
    ]
    command_received = [e for e in events if e.get("event") == "command_received"]
    command_synced = [e for e in events if e.get("event") == "command_synced"]
    job_claimed = [e for e in events if e.get("event") == "job_claimed"]
    assignment_handled = [e for e in events if e.get("event") == "assignment_handled"]

    triggers = defaultdict(int)

    first_event_ts = None
    for e in events:
        ts = parse_ts(e.get("ts", ""))
        if ts:
            first_event_ts = ts
            break

    if not first_event_ts:
        return {"total": 0, "by_trigger": {}}

    experiment_start_ms = first_event_ts.timestamp() * 1000
    heartbeat_interval = 5000
    heartbeat_tolerance = 500

    # Pre-parse and sort event lists for binary search
    command_received_sorted = sorted(
        [
            (parse_ts(c.get("ts", "")), c)
            for c in command_received
            if parse_ts(c.get("ts", "")) is not None
        ],
        key=lambda x: x[0],
    )
    command_synced_sorted = sorted(
        [
            (parse_ts(c.get("ts", "")), c)
            for c in command_synced
            if parse_ts(c.get("ts", "")) is not None
        ],
        key=lambda x: x[0],
    )
    job_claimed_sorted = sorted(
        [
            (parse_ts(j.get("ts", "")), j)
            for j in job_claimed
            if parse_ts(j.get("ts", "")) is not None
        ],
        key=lambda x: x[0],
    )
    assignment_sorted = sorted(
        [
            (parse_ts(a.get("ts", "")), a)
            for a in assignment_handled
            if parse_ts(a.get("ts", "")) is not None
        ],
        key=lambda x: x[0],
    )

    # Extract just timestamps for binary search
    cr_ts_list = [ts for ts, _ in command_received_sorted]
    cs_ts_list = [ts for ts, _ in command_synced_sorted]
    jc_ts_list = [ts for ts, _ in job_claimed_sorted]
    ah_ts_list = [ts for ts, _ in assignment_sorted]

    def find_events_in_window(ts_list, events_list, update_ts, window_ms):
        """Find events within window_ms before update_ts using binary search."""
        update_ts_ms = update_ts.timestamp() * 1000
        window_start_ms = update_ts_ms - window_ms
        # Create offset-aware datetime for comparison
        window_start_dt = datetime.fromtimestamp(
            window_start_ms / 1000, tz=update_ts.tzinfo
        )
        left = bisect.bisect_left(ts_list, window_start_dt)
        for i in range(left, len(events_list)):
            event_ts = ts_list[i]
            if (update_ts - event_ts).total_seconds() * 1000 > window_ms:
                break
            yield events_list[i]

    for update in node_updates:
        ts = parse_ts(update.get("ts", ""))
        if ts is None:
            continue

        ts_ms = ts.timestamp() * 1000
        elapsed_ms = ts_ms - experiment_start_ms

        trigger = "unknown"

        heartbeat_bucket = round(elapsed_ms / heartbeat_interval)
        expected_heartbeat = heartbeat_bucket * heartbeat_interval
        if abs(elapsed_ms - expected_heartbeat) <= heartbeat_tolerance:
            trigger = "heartbeat"
        else:
            found_trigger = False
            window_ms = 1000  # 1 second window

            # Use binary search for each event type
            for cr in find_events_in_window(
                cr_ts_list, command_received_sorted, ts, window_ms
            ):
                trigger = "new_command"
                found_trigger = True
                break

            if not found_trigger:
                for cs in find_events_in_window(
                    cs_ts_list, command_synced_sorted, ts, window_ms
                ):
                    trigger = "new_command"
                    found_trigger = True
                    break

            if not found_trigger:
                for jc in find_events_in_window(
                    jc_ts_list, job_claimed_sorted, ts, window_ms
                ):
                    trigger = "job_claim"
                    found_trigger = True
                    break

            if not found_trigger:
                for ah in find_events_in_window(
                    ah_ts_list, assignment_sorted, ts, window_ms
                ):
                    trigger = "assignment"
                    found_trigger = True
                    break

        triggers[trigger] += 1

    return {
        "total": len(node_updates),
        "by_trigger": dict(triggers),
    }


def compute_svs_propagation_delays(
    timelines: Dict,
    ground_truth_rtt: GroundTruthRTT = None
) -> Dict[str, Any]:
    """Compute SVS propagation delays for all (publisher, receiver) pairs.

    SVS propagation delay = time from command_published to command_synced
    per (publisher, receiver) pair.
    """
    all_delays = []  # raw delays in ms
    pair_delays = defaultdict(list)  # (pub, recv) -> [delays]
    pair_rtt_normalized = defaultdict(list)  # normalized by ground truth RTT

    for cmd_id, tl in timelines.items():
        pub_ts = tl["command_published_ts"]
        pub_node = tl["command_published_node"]
        if not pub_ts or not pub_node:
            continue

        for sync in tl["svs_syncs"]:
            delay_ms = (sync["synced_ts"] - pub_ts).total_seconds() * 1000
            all_delays.append(delay_ms)
            pair = (sync["publisher"], sync["receiver"])
            pair_delays[pair].append(delay_ms)

            if ground_truth_rtt:
                rtt = ground_truth_rtt.get_rtt(sync["publisher"], sync["receiver"])
                if rtt and rtt > 0:
                    pair_rtt_normalized[pair].append(delay_ms / rtt)

    # Compute summary stats
    def compute_stats(values: List[float]) -> Dict[str, float]:
        if not values:
            return {}
        sorted_values = sorted(values)
        n = len(sorted_values)
        return {
            "count": n,
            "min_ms": min(values),
            "max_ms": max(values),
            "avg_ms": statistics.mean(values),
            "median_ms": statistics.median(values),
            "p95_ms": sorted_values[int(n * 0.95)] if n >= 20 else sorted_values[-1],
            "p99_ms": sorted_values[int(n * 0.99)] if n >= 100 else sorted_values[-1],
        }

    result = {
        "all_delays": all_delays,
        "count": len(all_delays),
        "stats": compute_stats(all_delays) if all_delays else {},
    }

    # Per-pair stats
    if pair_delays:
        pair_stats = {}
        for pair, delays in pair_delays.items():
            pair_key = f"{pair[0]}->{pair[1]}"
            pair_stats[pair_key] = compute_stats(delays)
            if ground_truth_rtt and pair in pair_rtt_normalized:
                norm_values = pair_rtt_normalized[pair]
                if norm_values:
                    pair_stats[pair_key]["rtt_normalized_avg"] = statistics.mean(norm_values)
        result["by_pair"] = pair_stats

    return result


def compute_replication_delays(
    timelines: Dict,
    ground_truth_rtt: GroundTruthRTT = None
) -> Dict[str, Any]:
    """Compute replication delays: RFth_claim_ts - command_received_ts."""
    replication_factor = 3  # from PHASE_CONFIG
    all_delays = []  # ms
    pair_delays = defaultdict(list)

    for cmd_id, tl in timelines.items():
        received_ts = tl["command_received_ts"]
        received_node = tl["command_received_node"]
        all_claims = sorted(zip(tl["all_claims_ts"], tl["all_claim_nodes"]))

        if received_ts and len(all_claims) >= replication_factor:
            rf_claim_ts = all_claims[replication_factor - 1][0]
            rf_claim_node = all_claims[replication_factor - 1][1]
            delay_ms = (rf_claim_ts - received_ts).total_seconds() * 1000
            all_delays.append(delay_ms)
            pair_delays[(received_node, rf_claim_node)].append(delay_ms)

    # Compute summary stats
    def compute_stats(values: List[float]) -> Dict[str, float]:
        if not values:
            return {}
        sorted_values = sorted(values)
        n = len(sorted_values)
        return {
            "count": n,
            "min_ms": min(values),
            "max_ms": max(values),
            "avg_ms": statistics.mean(values),
            "median_ms": statistics.median(values),
            "p95_ms": sorted_values[int(n * 0.95)] if n >= 20 else sorted_values[-1],
            "p99_ms": sorted_values[int(n * 0.99)] if n >= 100 else sorted_values[-1],
        }

    result = {
        "count": len(all_delays),
        "stats": compute_stats(all_delays) if all_delays else {},
    }

    # Per-pair stats
    if pair_delays:
        pair_stats = {}
        for pair, delays in pair_delays.items():
            pair_key = f"{pair[0]}->{pair[1]}"
            pair_stats[pair_key] = compute_stats(delays)
        result["by_pair"] = pair_stats

    return result


def analyze_experiment(
    exp_dir: Path,
    ground_truth_rtt: GroundTruthRTT = None
) -> Dict[str, Any]:
    """Analyze a single experiment directory."""
    print(f"  Parsing events from {exp_dir.name}...")
    events, metadata = parse_events(exp_dir)

    # Detect distribution from experiment directory name (e.g., "partition-hydra-1x8_20260416_071404")
    # The Makefile passes experiment paths like "results/partition-hydra-1x8_TIMESTAMP"
    exp_name = exp_dir.name.lower()
    if "hydra" in exp_name:
        distribution = "hydra"
    elif "auction" in exp_name:
        distribution = "auction"
    else:
        distribution = "unknown"

    # Parse topology for RTT normalization
    topo_path = exp_dir / "topology.conf"
    if not topo_path.exists():
        topo_path = Path("/usr/local/share/testbed_topology.conf")
    direct_delays, median_delay, all_nodes = parse_topology_delays(topo_path)

    # Compute all-pair shortest path delays using Floyd-Warshall
    pair_delays = compute_all_pair_delays(direct_delays, all_nodes, fallback_delay=50.0)

    print(f"  Building command timelines...")
    timelines = build_command_timelines(events, metadata)

    print(f"  Computing phase durations...")
    phases = compute_phase_durations(timelines, metadata, pair_delays)

    print(f"  Categorizing sync interests...")
    sync_breakdown = categorize_sync_interests(events, timelines, metadata)

    print(f"  Analyzing assignment conflicts...")
    conflicts = analyze_assignment_conflicts(events, metadata)

    print(f"  Analyzing publication triggers...")
    pub_triggers = analyze_publication_triggers(events, metadata)

    # Count sync_interest_sent and data_sent events
    total_commands = metadata.get("total_commands", 1) or 1
    sync_interest_count = sum(1 for e in events if e.get("event") == "sync_interest_sent")
    data_sent_count = sum(1 for e in events if e.get("event") == "data_sent")

    # Compute SVS propagation delays
    print(f"  Computing SVS propagation delays...")
    svs_results = compute_svs_propagation_delays(timelines, ground_truth_rtt)

    # Compute replication delays
    print(f"  Computing replication delays...")
    rep_results = compute_replication_delays(timelines, ground_truth_rtt)

    return {
        "name": exp_dir.name,
        "distribution": distribution,
        "metadata": {
            "node_count": metadata.get("node_count", 0),
            "producer_count": metadata.get("producer_count", 0),
            "command_count": metadata.get("command_count", 0),
            "replication_factor": metadata.get("replication_factor", 3),
            "total_duration_seconds": metadata.get("total_duration_seconds", 0),
            "total_commands": metadata.get("total_commands", 0),
        },
        "topology_median_delay_ms": median_delay,
        "events": events,
        "timelines": phases["commands"],
        "phase_summary": phases["summary"],
        "phase_raw": phases["raw_stats"],
        "sync_breakdown": sync_breakdown,
        "conflicts": conflicts,
        "publication_triggers": pub_triggers,
        "sync_interest_count": {
            "total": sync_interest_count,
            "per_command": sync_interest_count / total_commands,
        },
        "data_sent_count": {
            "total": data_sent_count,
            "per_command": data_sent_count / total_commands,
        },
        "svs_propagation": svs_results,
        "replication_delays": rep_results,
    }


def generate_json_output(data: Dict, output_path: Path):
    """Write machine-readable JSON."""
    output = {
        "generated_at": datetime.now().isoformat(),
        "experiments": [],
    }

    for exp in data["experiments"]:
        exp_data = {
            "name": exp["name"],
            "metadata": exp["metadata"],
            "phase_summary": exp["phase_summary"],
            "sync_breakdown": exp["sync_breakdown"],
            "conflicts": exp["conflicts"],
            "publication_triggers": exp.get("publication_triggers", {}),
            "sync_interest_count": exp.get("sync_interest_count", {}),
            "data_sent_count": exp.get("data_sent_count", {}),
            "svs_propagation": exp.get("svs_propagation", {}),
            "replication_delays": exp.get("replication_delays", {}),
        }
        output["experiments"].append(exp_data)

    with open(output_path, "w") as f:
        json.dump(output, f, indent=2)
    print(f"  JSON written to: {output_path}")


def generate_csv_output(data: Dict, output_dir: Path):
    """Write CSV summaries."""

    phase_csv = output_dir / "phase_durations.csv"
    with open(phase_csv, "w") as f:
        # Header with both ms and RTT columns
        f.write(
            "experiment,cmd_id,"
            "ingestion_ms,ingestion_rtt,"
            "propagation_ms,propagation_rtt,"
            "assignment_chain_ms,"
            "replication_ms,replication_rtt,"
            "sync_ms,sync_rtt\n"
        )
        for exp in data["experiments"]:
            exp_name = exp["name"]
            for cmd_id, tl in exp["timelines"].items():
                phases = tl.get("phases", {})
                f.write(f"{exp_name},{cmd_id},")
                # Ingestion
                f.write(
                    f"{phases.get('ingestion_ms', ''):.2f},{phases.get('ingestion_rtt', ''):.2f},"
                    if phases.get("ingestion_ms")
                    else ",,"
                )
                # Propagation
                f.write(
                    f"{phases.get('propagation_ms', ''):.2f},{phases.get('propagation_rtt', ''):.2f},"
                    if phases.get("propagation_ms")
                    else ",,"
                )
                # Assignment chain (ms only, no RTT)
                f.write(
                    f"{phases.get('assignment_chain_ms', ''):.2f},"
                    if phases.get("assignment_chain_ms")
                    else ","
                )
                # Replication
                f.write(
                    f"{phases.get('replication_ms', ''):.2f},{phases.get('replication_rtt', ''):.2f},"
                    if phases.get("replication_ms")
                    else ",,"
                )
                # Sync
                f.write(
                    f"{phases.get('sync_ms', ''):.2f},{phases.get('sync_rtt', ''):.2f}\n"
                    if phases.get("sync_ms")
                    else "\n"
                )
    print(f"  CSV (phases) written to: {phase_csv}")

    sync_csv = output_dir / "sync_interest_breakdown.csv"
    with open(sync_csv, "w") as f:
        f.write(
            "experiment,total,heartbeat_count,heartbeat_pct,new_cmd_count,new_cmd_pct,"
        )
        f.write("assignment_count,assignment_pct,job_release_count,job_release_pct,")
        f.write("other_count,other_pct\n")
        for exp in data["experiments"]:
            sb = exp["sync_breakdown"]
            f.write(f"{exp['name']},{sb['total_sync_interests']},")
            f.write(f"{sb['heartbeat']['count']},{sb['heartbeat']['percentage']:.1f},")
            f.write(
                f"{sb['new_command']['count']},{sb['new_command']['percentage']:.1f},"
            )
            f.write(
                f"{sb['assignment']['count']},{sb['assignment']['percentage']:.1f},"
            )
            f.write(
                f"{sb['job_release']['count']},{sb['job_release']['percentage']:.1f},"
            )
            f.write(f"{sb['other']['count']},{sb['other']['percentage']:.1f}\n")
    print(f"  CSV (sync) written to: {sync_csv}")

    conflict_csv = output_dir / "assignment_conflicts.csv"
    with open(conflict_csv, "w") as f:
        f.write("experiment,total_assignment_handled,total_conflicts")
        f.write(",already_doing_job,not_in_assignees,command_not_received")
        f.write(
            ",skipped,pending,claimed,reassigned,total_reassignments,unique_assignments,processing_ratio,node_updates_with_jobs\n"
        )
        for exp in data["experiments"]:
            c = exp["conflicts"]
            f.write(
                f"{exp['name']},{c['total_assignment_handled']},{c['total_conflicts']}"
            )
            f.write(f",{c['by_reason'].get('already_doing_job', 0)}")
            f.write(f",{c['by_reason'].get('not_in_assignees', 0)}")
            f.write(f",{c['by_reason'].get('command_not_received', 0)}")
            f.write(f",{c['by_action'].get('skipped', 0)}")
            f.write(f",{c['by_action'].get('pending', 0)}")
            f.write(f",{c['by_action'].get('claimed', 0)}")
            f.write(f",{c['by_action'].get('reassigned', 0)}")
            f.write(f",{c.get('total_reassignments', 0)}")
            f.write(f",{c.get('unique_assignments', 0)}")
            f.write(f",{c.get('processing_ratio', 0):.2f}")
            f.write(f",{c.get('total_node_updates_with_jobs', 0)}\n")
    print(f"  CSV (conflicts) written to: {conflict_csv}")

    pub_triggers_csv = output_dir / "publication_triggers.csv"
    with open(pub_triggers_csv, "w") as f:
        f.write(
            "experiment,total_publications,heartbeat,new_command,job_claim,assignment,other\n"
        )
        for exp in data["experiments"]:
            pt = exp.get("publication_triggers", {})
            by_trigger = pt.get("by_trigger", {})
            total = pt.get("total", 0)
            f.write(f"{exp['name']},{total}")
            f.write(f",{by_trigger.get('heartbeat', 0)}")
            f.write(f",{by_trigger.get('new_command', 0)}")
            f.write(f",{by_trigger.get('job_claim', 0)}")
            f.write(f",{by_trigger.get('assignment', 0)}")
            f.write(f",{by_trigger.get('other', 0)}\n")
    print(f"  CSV (pub_triggers) written to: {pub_triggers_csv}")

    summary_csv = output_dir / "experiment_summary.csv"
    with open(summary_csv, "w") as f:
        f.write(
            "experiment,producers,total_commands,node_count,replication_factor,duration_seconds,sync_interests,total_conflicts,total_reassignments\n"
        )
        for exp in data["experiments"]:
            m = exp["metadata"]
            sb = exp["sync_breakdown"]
            c = exp["conflicts"]
            f.write(
                f"{exp['name']},{m['producer_count']},{m['total_commands']},{m['node_count']},{m['replication_factor']},{m.get('total_duration_seconds', 0):.2f},{sb['total_sync_interests']},{c['total_conflicts']},{c.get('total_reassignments', 0)}\n"
            )
    print(f"  CSV (summary) written to: {summary_csv}")

    # SVS propagation delays CSV
    svs_csv = output_dir / "svs_propagation_delays.csv"
    with open(svs_csv, "w") as f:
        f.write("experiment,publisher,receiver,delay_ms,rtt_normalized\n")
        for exp in data["experiments"]:
            svs = exp.get("svs_propagation", {})
            by_pair = svs.get("by_pair", {})
            for pair_key, stats in by_pair.items():
                publisher, receiver = pair_key.split("->")
                rtt_norm = stats.get("rtt_normalized_avg", "")
                f.write(f"{exp['name']},{publisher},{receiver},{stats.get('avg_ms', ''):.2f}")
                if rtt_norm != "":
                    f.write(f",{rtt_norm:.2f}\n")
                else:
                    f.write(",\n")
    print(f"  CSV (SVS propagation) written to: {svs_csv}")

    # Replication delays CSV
    rep_csv = output_dir / "replication_delays.csv"
    with open(rep_csv, "w") as f:
        f.write("experiment,from_node,to_node,delay_ms\n")
        for exp in data["experiments"]:
            rep = exp.get("replication_delays", {})
            by_pair = rep.get("by_pair", {})
            for pair_key, stats in by_pair.items():
                from_node, to_node = pair_key.split("->")
                f.write(f"{exp['name']},{from_node},{to_node},{stats.get('avg_ms', ''):.2f}\n")
    print(f"  CSV (replication delays) written to: {rep_csv}")


def generate_markdown_report(data: Dict, output_path: Path):
    """Write human-readable report with clear hydra/auction separation."""

    # Group experiments by distribution
    hydra_exps = [e for e in data["experiments"] if e.get("distribution") == "hydra"]
    auction_exps = [
        e for e in data["experiments"] if e.get("distribution") == "auction"
    ]
    unknown_exps = [
        e
        for e in data["experiments"]
        if e.get("distribution") not in ("hydra", "auction")
    ]

    # Sort by producer count for consistent ordering
    def sort_key(e):
        return e["metadata"].get("producer_count", 0)

    hydra_exps.sort(key=sort_key)
    auction_exps.sort(key=sort_key)
    unknown_exps.sort(key=sort_key)

    with open(output_path, "w") as f:
        f.write("# NDN Repository Experiment Analysis\n\n")
        f.write(f"**Generated:** {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}\n\n")

        # Overview section
        f.write("## Overview\n\n")
        f.write(f"- **Total Experiments:** {len(data['experiments'])}\n")
        f.write(f"- **Hydra Experiments:** {len(hydra_exps)}\n")
        f.write(f"- **Auction Experiments:** {len(auction_exps)}\n")
        if unknown_exps:
            f.write(f"- **Unknown Distribution:** {len(unknown_exps)}\n")
        f.write("\n")

        # Hydra Section
        if hydra_exps:
            f.write("# HYDRA Results\n\n")
            f.write("## Sync Interest Breakdown\n\n")
            f.write(
                "| Producers | Total Sync | Heartbeat % | Assignment % | Other % |\n"
            )
            f.write(
                "|-----------|------------|-------------|--------------|--------|\n"
            )
            for exp in hydra_exps:
                sb = exp["sync_breakdown"]
                prod = exp["metadata"]["producer_count"]
                f.write(f"| {prod} | {sb['total_sync_interests']:,} ")
                f.write(f"| {sb['heartbeat']['percentage']:.1f}% ")
                f.write(f"| {sb['assignment']['percentage']:.1f}% ")
                f.write(f"| {sb['other']['percentage']:.1f}% |\n")

            f.write("\n## Replication Time (RTT)\n\n")
            f.write("| Producers | Rep Med (RTT) | Rep P95 (RTT) | Rep P99 (RTT) |\n")
            f.write("|-----------|---------------|---------------|---------------|\n")
            for exp in hydra_exps:
                s = exp["phase_summary"]
                prod = exp["metadata"]["producer_count"]
                f.write(f"| {prod} ")
                f.write(f"| {s.get('replication_rtt_median', 0):.2f} ")
                f.write(f"| {s.get('replication_rtt_p95', 0):.2f} ")
                f.write(f"| {s.get('replication_rtt_p99', 0):.2f} |\n")

            f.write("\n## Sync Interests per Command\n\n")
            f.write("| Producers | Sync/Cmd | Heartbeat/cmd | Assignment/cmd |\n")
            f.write("|-----------|----------|---------------|----------------|\n")
            for exp in hydra_exps:
                sb = exp["sync_breakdown"]
                prod = exp["metadata"]["producer_count"]
                f.write(f"| {prod} ")
                f.write(
                    f"| {sb['total_sync_interests'] / max(1, exp['metadata']['total_commands']):.1f} "
                )
                f.write(f"| {sb['heartbeat']['per_command']:.2f} ")
                f.write(f"| {sb['assignment']['per_command']:.2f} |\n")

        # Auction Section
        if auction_exps:
            f.write("\n---\n\n# AUCTION Results\n\n")
            f.write("## Sync Interest Breakdown\n\n")
            f.write(
                "| Producers | Total Sync | Heartbeat % | Assignment % | Other % |\n"
            )
            f.write(
                "|-----------|------------|-------------|--------------|--------|\n"
            )
            for exp in auction_exps:
                sb = exp["sync_breakdown"]
                prod = exp["metadata"]["producer_count"]
                f.write(f"| {prod} | {sb['total_sync_interests']:,} ")
                f.write(f"| {sb['heartbeat']['percentage']:.1f}% ")
                f.write(f"| {sb['assignment']['percentage']:.1f}% ")
                f.write(f"| {sb['other']['percentage']:.1f}% |\n")

            f.write("\n## Replication Time (RTT)\n\n")
            f.write("| Producers | Rep Med (RTT) | Rep P95 (RTT) | Rep P99 (RTT) |\n")
            f.write("|-----------|---------------|---------------|---------------|\n")
            for exp in auction_exps:
                s = exp["phase_summary"]
                prod = exp["metadata"]["producer_count"]
                f.write(f"| {prod} ")
                f.write(f"| {s.get('replication_rtt_median', 0):.2f} ")
                f.write(f"| {s.get('replication_rtt_p95', 0):.2f} ")
                f.write(f"| {s.get('replication_rtt_p99', 0):.2f} |\n")

            f.write("\n## Sync Interests per Command\n\n")
            f.write("| Producers | Sync/Cmd | Heartbeat/cmd | Assignment/cmd |\n")
            f.write("|-----------|----------|---------------|----------------|\n")
            for exp in auction_exps:
                sb = exp["sync_breakdown"]
                prod = exp["metadata"]["producer_count"]
                f.write(f"| {prod} ")
                f.write(
                    f"| {sb['total_sync_interests'] / max(1, exp['metadata']['total_commands']):.1f} "
                )
                f.write(f"| {sb['heartbeat']['per_command']:.2f} ")
                f.write(f"| {sb['assignment']['per_command']:.2f} |\n")

        # Side-by-side Comparison Section
        if hydra_exps and auction_exps:
            f.write("\n---\n\n# HYDRA vs AUCTION Comparison\n\n")
            f.write("## Sync Interests Comparison\n\n")
            f.write(
                "| Producers | Hydra Sync | Auction Sync | Hydra Assign % | Auction Assign % |\n"
            )
            f.write(
                "|-----------|------------|--------------|----------------|------------------|\n"
            )

            hydra_by_prod = {e["metadata"]["producer_count"]: e for e in hydra_exps}
            auction_by_prod = {e["metadata"]["producer_count"]: e for e in auction_exps}

            all_producers = sorted(
                set(hydra_by_prod.keys()) | set(auction_by_prod.keys())
            )
            for prod in all_producers:
                h_exp = hydra_by_prod.get(prod)
                a_exp = auction_by_prod.get(prod)
                h_sb = h_exp["sync_breakdown"] if h_exp else None
                a_sb = a_exp["sync_breakdown"] if a_exp else None

                h_sync = h_sb["total_sync_interests"] if h_sb else "-"
                a_sync = a_sb["total_sync_interests"] if a_sb else "-"
                h_assign = f"{h_sb['assignment']['percentage']:.1f}%" if h_sb else "-"
                a_assign = f"{a_sb['assignment']['percentage']:.1f}%" if a_sb else "-"

                f.write(f"| {prod} | {h_sync} | {a_sync} | {h_assign} | {a_assign} |\n")

            f.write("\n## Replication Time Comparison (Median RTT)\n\n")
            f.write(
                "| Producers | Hydra Rep Med (RTT) | Auction Rep Med (RTT) | Difference |\n"
            )
            f.write(
                "|-----------|---------------------|----------------------|------------|\n"
            )
            for prod in all_producers:
                h_exp = hydra_by_prod.get(prod)
                a_exp = auction_by_prod.get(prod)
                h_s = h_exp["phase_summary"] if h_exp else None
                a_s = a_exp["phase_summary"] if a_exp else None

                h_med = h_s.get("replication_rtt_median", 0) if h_s else None
                a_med = a_s.get("replication_rtt_median", 0) if a_s else None

                if h_med is not None and a_med is not None:
                    diff = a_med - h_med
                    diff_str = f"{diff:+.2f}"
                else:
                    diff_str = "-"

                h_str = f"{h_med:.2f}" if h_med is not None else "-"
                a_str = f"{a_med:.2f}" if a_med is not None else "-"

                f.write(f"| {prod} | {h_str} | {a_str} | {diff_str} |\n")

        # Legacy sections for backward compatibility
        f.write("\n---\n\n# Detailed Analysis\n\n")

        f.write("## Experiments Analyzed\n\n")
        for exp in data["experiments"]:
            m = exp["metadata"]
            dist = exp.get("distribution", "unknown")
            f.write(
                f"- **{exp['name']}** [{dist.upper()}]: {m['producer_count']} producers, "
            )
            f.write(
                f"{m['command_count']} commands/producer, {m['node_count']} nodes, "
            )
            f.write(f"RF={m['replication_factor']}\n")

        f.write("\n## Sync Interest Breakdown (All Experiments)\n\n")
        f.write(
            "| Experiment | Distribution | Total Sync | Heartbeat | New Cmd | Assignment | Job Release | Other |\n"
        )
        f.write(
            "|------------|--------------|------------|-----------|----------|------------|-------------|-------|\n"
        )
        for exp in data["experiments"]:
            sb = exp["sync_breakdown"]
            dist = exp.get("distribution", "unknown")
            f.write(f"| {exp['name']} | {dist} | {sb['total_sync_interests']:,} ")
            f.write(f"| {sb['heartbeat']['percentage']:.1f}% ")
            f.write(f"| {sb['new_command']['percentage']:.1f}% ")
            f.write(f"| {sb['assignment']['percentage']:.1f}% ")
            f.write(f"| {sb['job_release']['percentage']:.1f}% ")
            f.write(f"| {sb['other']['percentage']:.1f}% |\n")

        f.write("\n## Phase Duration Analysis\n\n")
        f.write(
            "| Experiment | Distribution | Ingestion (ms) | Ingestion (RTT) | Assignment Chain (ms) | Assignment Chain (RTT) | Replication (ms) | Replication (RTT) |\n"
        )
        f.write(
            "|------------|--------------|----------------|-----------------|----------------------|----------------------|------------------|-------------------|\n"
        )
        for exp in data["experiments"]:
            s = exp["phase_summary"]
            dist = exp.get("distribution", "unknown")
            f.write(f"| {exp['name']} | {dist} ")
            f.write(f"| {s.get('ingestion_ms_avg', 0):.1f} ")
            f.write(f"| {s.get('ingestion_rtt_avg', 0):.2f} ")
            f.write(f"| {s.get('assignment_chain_ms_avg', 0):.1f} ")
            f.write(f"| {s.get('replication_ms_avg', 0):.1f} ")
            f.write(f"| {s.get('replication_rtt_avg', 0):.2f} |\n")

        f.write("\n## Experiment Duration\n\n")
        f.write(
            "| Experiment | Distribution | Producers | Total Commands | Duration (s) |\n"
        )
        f.write(
            "|------------|--------------|----------|---------------|--------------|\n"
        )
        for exp in data["experiments"]:
            m = exp["metadata"]
            dist = exp.get("distribution", "unknown")
            duration = m.get("total_duration_seconds", 0)
            total_cmds = m.get("total_commands", 0)
            f.write(
                f"| {exp['name']} | {dist} | {m['producer_count']} | {total_cmds} | {duration:.2f} |\n"
            )

        f.write("\n## SVS Propagation Delay\n\n")
        f.write(
            "| Experiment | Distribution | Count | Avg (ms) | Median (ms) | P95 (ms) | P99 (ms) |\n"
        )
        f.write(
            "|------------|--------------|-------|----------|-------------|----------|----------|\n"
        )
        for exp in data["experiments"]:
            svs = exp.get("svs_propagation", {})
            stats = svs.get("stats", {})
            dist = exp.get("distribution", "unknown")
            f.write(f"| {exp['name']} | {dist} ")
            f.write(f"| {stats.get('count', 0):,} ")
            f.write(f"| {stats.get('avg_ms', 0):.2f} ")
            f.write(f"| {stats.get('median_ms', 0):.2f} ")
            f.write(f"| {stats.get('p95_ms', 0):.2f} ")
            f.write(f"| {stats.get('p99_ms', 0):.2f} |\n")

        f.write("\n## Replication Delay (RFth Claim)\n\n")
        f.write(
            "| Experiment | Distribution | Count | Avg (ms) | Median (ms) | P95 (ms) | P99 (ms) |\n"
        )
        f.write(
            "|------------|--------------|-------|----------|-------------|----------|----------|\n"
        )
        for exp in data["experiments"]:
            rep = exp.get("replication_delays", {})
            stats = rep.get("stats", {})
            dist = exp.get("distribution", "unknown")
            f.write(f"| {exp['name']} | {dist} ")
            f.write(f"| {stats.get('count', 0):,} ")
            f.write(f"| {stats.get('avg_ms', 0):.2f} ")
            f.write(f"| {stats.get('median_ms', 0):.2f} ")
            f.write(f"| {stats.get('p95_ms', 0):.2f} ")
            f.write(f"| {stats.get('p99_ms', 0):.2f} |\n")

        f.write("\n## Traffic Statistics\n\n")
        f.write(
            "| Experiment | Distribution | Sync Interests | Sync/Cmd | Data Sent | Data/Cmd |\n"
        )
        f.write(
            "|------------|--------------|---------------|----------|-----------|----------|\n"
        )
        for exp in data["experiments"]:
            sic = exp.get("sync_interest_count", {})
            dsc = exp.get("data_sent_count", {})
            dist = exp.get("distribution", "unknown")
            f.write(f"| {exp['name']} | {dist} ")
            f.write(f"| {sic.get('total', 0):,} ")
            f.write(f"| {sic.get('per_command', 0):.1f} ")
            f.write(f"| {dsc.get('total', 0):,} ")
            f.write(f"| {dsc.get('per_command', 0):.1f} |\n")

        f.write("\n## Assignment Conflict Analysis\n\n")
        f.write(
            "| Experiment | Distribution | Unique Targets | Reassignments | Not In Assignees |\n"
        )
        f.write(
            "|------------|--------------|----------------|---------------|------------------|\n"
        )
        for exp in data["experiments"]:
            c = exp["conflicts"]
            dist = exp.get("distribution", "unknown")
            f.write(f"| {exp['name']} | {dist} ")
            f.write(f"| {c.get('unique_targets', 0):,} ")
            f.write(f"| {c.get('total_reassignments', 0):,} ")
            f.write(f"| {c['by_reason'].get('not_in_assignees', 0):,} |\n")

        f.write("\n## Root Cause Analysis: 4→8 Producer Spike\n\n")

        exp_4 = None
        exp_8 = None
        for exp in data["experiments"]:
            if "producers_4" in exp["name"]:
                exp_4 = exp
            elif "producers_8" in exp["name"]:
                exp_8 = exp

        if exp_4 and exp_8:
            sb_4 = exp_4["sync_breakdown"]
            sb_8 = exp_8["sync_breakdown"]
            c_4 = exp_4["conflicts"]
            c_8 = exp_8["conflicts"]

            sync_increase = (
                sb_8["total_sync_interests"] / sb_4["total_sync_interests"]
                if sb_4["total_sync_interests"] > 0
                else 0
            )
            assign_increase = (
                c_8["total_assignment_handled"] / c_4["total_assignment_handled"]
                if c_4["total_assignment_handled"] > 0
                else 0
            )

            f.write(
                f"**Sync Interest Increase:** {sync_increase:.1f}x (from {sb_4['total_sync_interests']:,} to {sb_8['total_sync_interests']:,})\n\n"
            )
            f.write(
                f"**Assignment Events Increase:** {assign_increase:.1f}x (from {c_4['total_assignment_handled']:,} to {c_8['total_assignment_handled']:,})\n\n"
            )

            f.write("### Key Findings\n\n")
            f.write(
                "1. **Assignment Cascade**: With 8 producers sending simultaneously, nodes receive "
            )
            f.write(
                f"{sb_8['assignment']['count']:,} assignment-related sync interests vs "
            )
            f.write(f"{sb_4['assignment']['count']:,} for 4 producers.\n\n")

            f.write("2. **Conflict Explosion**: Assignment conflicts increased from ")
            f.write(f"{c_4['total_conflicts']:,} to {c_8['total_conflicts']:,} - ")
            f.write("each conflict triggers additional sync to resolve.\n\n")

            f.write(
                "3. **Heartbeat Ratio Dropped**: Heartbeat percentage dropped from "
            )
            f.write(
                f"{sb_4['heartbeat']['percentage']:.1f}% to {sb_8['heartbeat']['percentage']:.1f}% "
            )
            f.write(
                "because the absolute number of event-driven syncs overwhelmed periodic ones.\n\n"
            )

            f.write(
                "4. **Chain Reaction**: More producers → more concurrent commands → "
            )
            f.write(
                "more assignment disagreements → more reassignment cycles → exponential sync growth.\n"
            )

    print(f"  Markdown report written to: {output_path}")


def main():
    parser = argparse.ArgumentParser(
        description="Analyze NDN repository experiment traces"
    )
    parser.add_argument(
        "--experiments",
        nargs="+",
        required=True,
        help="Experiment directories to analyze",
    )
    parser.add_argument("--output-dir", default="./analysis", help="Output directory")
    parser.add_argument(
        "--format",
        nargs="+",
        default=["json", "csv", "markdown"],
        choices=["json", "csv", "markdown"],
        help="Output formats",
    )
    parser.add_argument(
        "--ground-truth-rtt",
        type=Path,
        help="JSON file with pre-measured ground truth RTTs",
    )
    args = parser.parse_args()

    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    # Load ground truth RTTs if provided
    ground_truth_rtt = None
    if args.ground_truth_rtt:
        if args.ground_truth_rtt.exists():
            with open(args.ground_truth_rtt) as f:
                rtt_data = json.load(f)
            ground_truth_rtt = GroundTruthRTT(rtt_data.get("rtts", {}))
            print(f"Loaded ground truth RTTs for {len(rtt_data.get('nodes', []))} nodes")
        else:
            print(f"Warning: Ground truth RTT file not found: {args.ground_truth_rtt}")

    print(f"Analyzing {len(args.experiments)} experiments...\n")

    experiments_data = []

    def analyze_and_collect(exp_dir):
        exp_path = Path(exp_dir)
        if not exp_path.exists():
            print(f"Warning: {exp_dir} does not exist, skipping")
            return None
        return analyze_experiment(exp_path, ground_truth_rtt)

    with ThreadPoolExecutor(max_workers=min(8, len(args.experiments))) as executor:
        futures = {
            executor.submit(analyze_and_collect, exp_dir): exp_dir
            for exp_dir in args.experiments
        }
        for future in as_completed(futures):
            result = future.result()
            if result is not None:
                experiments_data.append(result)

    if not experiments_data:
        print("Error: No valid experiments found")
        sys.exit(1)

    combined = {"experiments": experiments_data}

    print(f"\nGenerating outputs to {output_dir}...\n")

    if "json" in args.format:
        generate_json_output(combined, output_dir / "analysis_results.json")
    if "csv" in args.format:
        generate_csv_output(combined, output_dir)
    if "markdown" in args.format:
        generate_markdown_report(combined, output_dir / "ANALYSIS_REPORT.md")

    print(f"\nAnalysis complete! Results in: {output_dir}")


if __name__ == "__main__":
    main()
