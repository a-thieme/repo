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


def parse_topology_delays(topo_path: Path) -> tuple:
    """Parse link delays from topology file.
    Returns: ({node_pair: delay_ms}, median_delay_ms)
    """
    delays = {}
    median_delays = []

    if not topo_path.exists():
        return delays, None

    try:
        with open(topo_path) as f:
            in_links = False
            for line in f:
                line = line.strip()
                if line == '[links]':
                    in_links = True
                    continue
                if line.startswith('[') and in_links:
                    break
                if in_links and ':' in line and 'delay' in line:
                    parts = line.split()
                    node_part = parts[0]
                    delay_part = [p for p in parts if 'delay' in p][0]

                    nodes = node_part.split(':')
                    if len(nodes) != 2:
                        continue

                    node1, node2 = nodes[0].lower(), nodes[1].lower()
                    delay_str = delay_part.split('=')[1].rstrip('ms')
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
    return delays, median


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
            "all_claims_ts": [],
            "decisions": [],
            "assignments": [],
            "command_received_ts": None,
            "command_synced_ts": None,
            "command_synced_from": [],
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

            elif event_type == "command_synced":
                if tl["command_synced_ts"] is None:
                    tl["command_synced_ts"] = ts
                tl["command_synced_from"].append(event.get("from", "unknown"))

            elif event_type == "job_claimed":
                claim_ts = ts
                if tl["first_claim_ts"] is None:
                    tl["first_claim_ts"] = claim_ts
                tl["all_claims_ts"].append(claim_ts)

            elif event_type == "decision_made":
                tl["decisions"].append(
                    {
                        "ts": ts,
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


def compute_phase_durations(timelines: Dict, metadata: Dict, median_delay_ms: float = None) -> Dict[str, Any]:
    """Calculate duration for each phase per command.

    Args:
        timelines: Command timelines dict
        metadata: Experiment metadata
        median_delay_ms: Median one-way link delay for RTT normalization (if None, skip RTT)
    """
    replication_factor = metadata.get(
        "replication_factor", PHASE_CONFIG["replication_factor"]
    )

    # Use median_delay as fallback for RTT calculation
    fallback_delay = median_delay_ms if median_delay_ms else 10.0
    expected_rtt = 2 * fallback_delay  # RTT = 2 * one-way delay

    phase_stats = {
        "ingestion_ms": [],
        "propagation_ms": [],
        "assignment_chain_ms": [],
        "replication_ms": [],
        "sync_ms": [],
        # RTT-normalized versions
        "ingestion_rtt": [],
        "propagation_rtt": [],
        "assignment_chain_rtt": [],
        "replication_rtt": [],
        "sync_rtt": [],
        "total_commands": len(timelines),
    }

    for cmd_id, tl in timelines.items():
        cmd_phases = {}

        cmd_received = tl["command_received_ts"]
        first_decision = tl["decisions"][0]["ts"] if tl["decisions"] else None
        first_claim = tl["first_claim_ts"]
        all_claims = sorted(tl["all_claims_ts"])
        command_synced = tl["command_synced_ts"]

        if cmd_received and first_decision:
            ingestion_ms = (first_decision - cmd_received).total_seconds() * 1000
            cmd_phases["ingestion_ms"] = ingestion_ms
            phase_stats["ingestion_ms"].append(ingestion_ms)
            if expected_rtt > 0:
                ingestion_rtt = ingestion_ms / expected_rtt
                cmd_phases["ingestion_rtt"] = ingestion_rtt
                phase_stats["ingestion_rtt"].append(ingestion_rtt)
        else:
            cmd_phases["ingestion_ms"] = None
            cmd_phases["ingestion_rtt"] = None

        if first_decision and first_claim:
            propagation_ms = (first_claim - first_decision).total_seconds() * 1000
            cmd_phases["propagation_ms"] = propagation_ms
            phase_stats["propagation_ms"].append(propagation_ms)
            if expected_rtt > 0:
                propagation_rtt = propagation_ms / expected_rtt
                cmd_phases["propagation_rtt"] = propagation_rtt
                phase_stats["propagation_rtt"].append(propagation_rtt)
        else:
            cmd_phases["propagation_ms"] = None
            cmd_phases["propagation_rtt"] = None

        if first_claim and len(all_claims) >= replication_factor:
            rf_claim_time = all_claims[replication_factor - 1]
            assignment_chain_ms = (rf_claim_time - first_claim).total_seconds() * 1000
            cmd_phases["assignment_chain_ms"] = assignment_chain_ms
            phase_stats["assignment_chain_ms"].append(assignment_chain_ms)
            if expected_rtt > 0:
                assignment_chain_rtt = assignment_chain_ms / expected_rtt
                cmd_phases["assignment_chain_rtt"] = assignment_chain_rtt
                phase_stats["assignment_chain_rtt"].append(assignment_chain_rtt)
        else:
            cmd_phases["assignment_chain_ms"] = None
            cmd_phases["assignment_chain_rtt"] = None

        if first_claim and len(all_claims) >= replication_factor:
            rf_claim_time = all_claims[replication_factor - 1]
            replication_ms = (rf_claim_time - first_claim).total_seconds() * 1000
            cmd_phases["replication_ms"] = replication_ms
            phase_stats["replication_ms"].append(replication_ms)
            if expected_rtt > 0:
                replication_rtt = replication_ms / expected_rtt
                cmd_phases["replication_rtt"] = replication_rtt
                phase_stats["replication_rtt"].append(replication_rtt)
        else:
            cmd_phases["replication_ms"] = None
            cmd_phases["replication_rtt"] = None

        if cmd_received and command_synced:
            sync_ms = (command_synced - cmd_received).total_seconds() * 1000
            cmd_phases["sync_ms"] = sync_ms
            phase_stats["sync_ms"].append(sync_ms)
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
            summary[f"{phase}_p95"] = sorted_values[int(n * 0.95)] if n >= 20 else sorted_values[-1]
            summary[f"{phase}_p99"] = sorted_values[int(n * 0.99)] if n >= 100 else sorted_values[-1]
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
        [(parse_ts(u.get("ts")), u) for u in node_updates if parse_ts(u.get("ts")) is not None],
        key=lambda x: x[0]
    )
    job_released_sorted = sorted(
        [(parse_ts(j.get("ts")), j) for j in job_released if parse_ts(j.get("ts")) is not None],
        key=lambda x: x[0]
    )
    assignment_sorted = sorted(
        [(parse_ts(a.get("ts")), a) for a in assignment_handled if parse_ts(a.get("ts")) is not None],
        key=lambda x: x[0]
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
        window_start_dt = datetime.fromtimestamp(window_start_ms / 1000, tz=sync_ts.tzinfo)
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
        [(parse_ts(c.get("ts", "")), c) for c in command_received if parse_ts(c.get("ts", "")) is not None],
        key=lambda x: x[0]
    )
    command_synced_sorted = sorted(
        [(parse_ts(c.get("ts", "")), c) for c in command_synced if parse_ts(c.get("ts", "")) is not None],
        key=lambda x: x[0]
    )
    job_claimed_sorted = sorted(
        [(parse_ts(j.get("ts", "")), j) for j in job_claimed if parse_ts(j.get("ts", "")) is not None],
        key=lambda x: x[0]
    )
    assignment_sorted = sorted(
        [(parse_ts(a.get("ts", "")), a) for a in assignment_handled if parse_ts(a.get("ts", "")) is not None],
        key=lambda x: x[0]
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
        window_start_dt = datetime.fromtimestamp(window_start_ms / 1000, tz=update_ts.tzinfo)
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
            for cr in find_events_in_window(cr_ts_list, command_received_sorted, ts, window_ms):
                trigger = "new_command"
                found_trigger = True
                break

            if not found_trigger:
                for cs in find_events_in_window(cs_ts_list, command_synced_sorted, ts, window_ms):
                    trigger = "new_command"
                    found_trigger = True
                    break

            if not found_trigger:
                for jc in find_events_in_window(jc_ts_list, job_claimed_sorted, ts, window_ms):
                    trigger = "job_claim"
                    found_trigger = True
                    break

            if not found_trigger:
                for ah in find_events_in_window(ah_ts_list, assignment_sorted, ts, window_ms):
                    trigger = "assignment"
                    found_trigger = True
                    break

        triggers[trigger] += 1

    return {
        "total": len(node_updates),
        "by_trigger": dict(triggers),
    }


def analyze_experiment(exp_dir: Path) -> Dict[str, Any]:
    """Analyze a single experiment directory."""
    print(f"  Parsing events from {exp_dir.name}...")
    events, metadata = parse_events(exp_dir)

    # Parse topology for RTT normalization
    topo_path = exp_dir / "topology.conf"
    if not topo_path.exists():
        topo_path = Path("/usr/local/share/testbed_topology.conf")
    _, median_delay = parse_topology_delays(topo_path)

    print(f"  Building command timelines...")
    timelines = build_command_timelines(events, metadata)

    print(f"  Computing phase durations...")
    phases = compute_phase_durations(timelines, metadata, median_delay)

    print(f"  Categorizing sync interests...")
    sync_breakdown = categorize_sync_interests(events, timelines, metadata)

    print(f"  Analyzing assignment conflicts...")
    conflicts = analyze_assignment_conflicts(events, metadata)

    print(f"  Analyzing publication triggers...")
    pub_triggers = analyze_publication_triggers(events, metadata)

    return {
        "name": exp_dir.name,
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
            "assignment_chain_ms,assignment_chain_rtt,"
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
                # Assignment chain
                f.write(
                    f"{phases.get('assignment_chain_ms', ''):.2f},{phases.get('assignment_chain_rtt', ''):.2f},"
                    if phases.get("assignment_chain_ms")
                    else ",,"
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


def generate_markdown_report(data: Dict, output_path: Path):
    """Write human-readable report."""
    with open(output_path, "w") as f:
        f.write("# NDN Repository Experiment Analysis\n\n")
        f.write(f"**Generated:** {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}\n\n")

        f.write("## Experiments Analyzed\n\n")
        for exp in data["experiments"]:
            m = exp["metadata"]
            f.write(f"- **{exp['name']}**: {m['producer_count']} producers, ")
            f.write(
                f"{m['command_count']} commands/producer, {m['node_count']} nodes, "
            )
            f.write(f"RF={m['replication_factor']}\n")

        f.write("\n## Sync Interest Breakdown\n\n")
        f.write(
            "| Experiment | Total Sync | Heartbeat | New Cmd | Assignment | Job Release | Other |\n"
        )
        f.write(
            "|------------|------------|-----------|----------|------------|-------------|-------|\n"
        )
        for exp in data["experiments"]:
            sb = exp["sync_breakdown"]
            f.write(f"| {exp['name']} | {sb['total_sync_interests']:,} ")
            f.write(f"| {sb['heartbeat']['percentage']:.1f}% ")
            f.write(f"| {sb['new_command']['percentage']:.1f}% ")
            f.write(f"| {sb['assignment']['percentage']:.1f}% ")
            f.write(f"| {sb['job_release']['percentage']:.1f}% ")
            f.write(f"| {sb['other']['percentage']:.1f}% |\n")

        f.write("\n### Absolute Counts (per command)\n\n")
        f.write(
            "| Experiment | Heartbeat | Heart/cmd | New Cmd | New/cmd | Assignment | Assign/cmd | Job Release | JobRel/cmd | Other | Other/cmd |\n"
        )
        f.write(
            "|------------|-----------|-----------|---------|---------|------------|------------|-------------|------------|-------|-----------|\n"
        )
        for exp in data["experiments"]:
            sb = exp["sync_breakdown"]
            f.write(f"| {exp['name']} ")
            f.write(f"| {sb['heartbeat']['count']:,} | {sb['heartbeat']['per_command']:.2f} ")
            f.write(f"| {sb['new_command']['count']:,} | {sb['new_command']['per_command']:.2f} ")
            f.write(f"| {sb['assignment']['count']:,} | {sb['assignment']['per_command']:.2f} ")
            f.write(f"| {sb['job_release']['count']:,} | {sb['job_release']['per_command']:.2f} ")
            f.write(f"| {sb['other']['count']:,} | {sb['other']['per_command']:.2f} |\n")

        f.write("\n## Phase Duration Analysis\n\n")
        f.write(
            "| Experiment | Ingestion (ms) | Ingestion (RTT) | Propagation (ms) | Propagation (RTT) | Assignment Chain (ms) | Assignment Chain (RTT) | Replication (ms) | Replication (RTT) |\n"
        )
        f.write(
            "|------------|----------------|-----------------|------------------|-------------------|----------------------|----------------------|------------------|-------------------|\n"
        )
        for exp in data["experiments"]:
            s = exp["phase_summary"]
            f.write(f"| {exp['name']} ")
            f.write(f"| {s.get('ingestion_ms_avg', 0):.1f} ")
            f.write(f"| {s.get('ingestion_rtt_avg', 0):.2f} ")
            f.write(f"| {s.get('propagation_ms_avg', 0):.1f} ")
            f.write(f"| {s.get('propagation_rtt_avg', 0):.2f} ")
            f.write(f"| {s.get('assignment_chain_ms_avg', 0):.1f} ")
            f.write(f"| {s.get('assignment_chain_rtt_avg', 0):.2f} ")
            f.write(f"| {s.get('replication_ms_avg', 0):.1f} ")
            f.write(f"| {s.get('replication_rtt_avg', 0):.2f} |\n")

        # Add RTT summary table
        f.write("\n### Phase Duration RTT Summary (Median, P95, P99)\n\n")
        f.write(
            "| Experiment | Med Ingestion | P95 Ingestion | P99 Ingestion | Med Propagation | P95 Propagation | P99 Propagation | Med Replication | P95 Replication | P99 Replication |\n"
        )
        f.write(
            "|------------|---------------|---------------|---------------|-----------------|-----------------|-----------------|-----------------|-----------------|-----------------|\n"
        )
        for exp in data["experiments"]:
            s = exp["phase_summary"]
            f.write(f"| {exp['name']} ")
            f.write(f"| {s.get('ingestion_rtt_median', 0):.2f} | {s.get('ingestion_rtt_p95', 0):.2f} | {s.get('ingestion_rtt_p99', 0):.2f} ")
            f.write(f"| {s.get('propagation_rtt_median', 0):.2f} | {s.get('propagation_rtt_p95', 0):.2f} | {s.get('propagation_rtt_p99', 0):.2f} ")
            f.write(f"| {s.get('replication_rtt_median', 0):.2f} | {s.get('replication_rtt_p95', 0):.2f} | {s.get('replication_rtt_p99', 0):.2f} |\n")

        # Add topology info
        f.write("\n### Topology Information\n\n")
        f.write(
            "| Experiment | Topology Median Delay (ms) |\n"
        )
        f.write(
            "|------------|--------------------------|\n"
        )
        for exp in data["experiments"]:
            median_delay = exp.get("topology_median_delay_ms", "N/A")
            f.write(f"| {exp['name']} | {median_delay} |\n")

        f.write("\n## Experiment Duration\n\n")
        f.write(
            "| Experiment | Producers | Total Commands | Duration (s) | Commands/s |\n"
        )
        f.write(
            "|------------|----------|---------------|--------------|------------|\n"
        )
        for exp in data["experiments"]:
            m = exp["metadata"]
            duration = m.get("total_duration_seconds", 0)
            total_cmds = m.get("total_commands", 0)
            cmds_per_sec = total_cmds / duration if duration > 0 else 0
            f.write(
                f"| {exp['name']} | {m['producer_count']} | {total_cmds} | {duration:.2f} | {cmds_per_sec:.3f} |\n"
            )

        f.write("\n## Assignment Processing Analysis\n\n")
        f.write(
            "| Experiment | Assignment Events | Unique Assignments | Processing Ratio | Node Updates w/ Jobs |\n"
        )
        f.write(
            "|------------|-------------------|--------------------|--------------------|---------------------|\n"
        )
        for exp in data["experiments"]:
            c = exp["conflicts"]
            f.write(f"| {exp['name']} ")
            f.write(f"| {c['total_assignment_handled']:,} ")
            f.write(f"| {c.get('unique_assignments', 0):,} ")
            f.write(f"| {c.get('processing_ratio', 0):.1f}x ")
            f.write(f"| {c.get('total_node_updates_with_jobs', 0):,} |\n")

        f.write("\n## Assignment Conflict Analysis\n\n")
        f.write("| Experiment | Unique Targets | Reassignments | Not In Assignees |\n")
        f.write("|------------|----------------|---------------|------------------|\n")
        for exp in data["experiments"]:
            c = exp["conflicts"]
            f.write(f"| {exp['name']} ")
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
    args = parser.parse_args()

    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)

    print(f"Analyzing {len(args.experiments)} experiments...\n")

    experiments_data = []

    def analyze_and_collect(exp_dir):
        exp_path = Path(exp_dir)
        if not exp_path.exists():
            print(f"Warning: {exp_dir} does not exist, skipping")
            return None
        return analyze_experiment(exp_path)

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
