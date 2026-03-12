#!/usr/bin/env python3
"""
Helper utilities for NDN repository experiments.
Provides result collection and calibration analysis.
"""

import argparse
import json
import sys
from datetime import datetime
from pathlib import Path
from typing import Dict, List, Optional, Any
import statistics


def parse_ts(ts: str) -> Optional[datetime]:
    """Parse ISO timestamp from event log."""
    if not ts:
        return None
    ts = ts.replace("Z", "+00:00")
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


def collect_results(
    run_dir: Path,
    csv_path: Optional[Path] = None,
    md_path: Optional[Path] = None,
    show: bool = False,
) -> Dict[str, Any]:
    """
    Collect results from experiment run directory.

    Args:
        run_dir: Directory containing experiment results (nodes_X_producers_Y subdirs)
        csv_path: Path to write CSV summary
        md_path: Path to write RESULTS.md
        show: Print summary to stdout

    Returns:
        Dict with collected results
    """
    results = []

    for exp_dir in sorted(run_dir.iterdir()):
        if not exp_dir.is_dir():
            continue

        metadata_file = exp_dir / "metadata.json"
        if not metadata_file.exists():
            continue

        try:
            with open(metadata_file) as f:
                meta = json.load(f)
        except (json.JSONDecodeError, IOError):
            continue

        total_cmds = meta.get("total_commands", 0)
        sync_interests = meta.get("sync_interests", 0)
        data_packets = meta.get("data_packets", 0)

        result = {
            "nodes": meta.get("node_count", 0),
            "producers": meta.get("producer_count", 0),
            "replicated": meta.get("replicated", False),
            "rep_min_ms": meta.get("replication_time_min_ms") or 0,
            "rep_max_ms": meta.get("replication_time_max_ms") or 0,
            "rep_avg_ms": meta.get("replication_time_avg_ms") or 0,
            "rep_med_ms": meta.get("replication_time_median_ms") or 0,
            "prop_min_ms": meta.get("update_propagation_min_ms") or 0,
            "prop_max_ms": meta.get("update_propagation_max_ms") or 0,
            "prop_avg_ms": meta.get("update_propagation_avg_ms") or 0,
            "prop_med_ms": meta.get("update_propagation_median_ms") or 0,
            "sync_interests": sync_interests,
            "data_packets": data_packets,
            "sync_per_cmd": sync_interests / total_cmds if total_cmds > 0 else 0,
            "data_per_cmd": data_packets / total_cmds if total_cmds > 0 else 0,
            "total_commands": total_cmds,
            "commands_at_rf": meta.get("commands_at_rf", 0),
            "commands_over": meta.get("commands_over", 0),
            "commands_under": meta.get("commands_under", 0),
            "any_ever_over": meta.get("any_ever_over_replicated", False),
            "total_duration_seconds": meta.get("total_duration_seconds", 0),
            "replication_wait_duration_seconds": meta.get(
                "replication_wait_duration_seconds", 0
            ),
            "failure_enabled": meta.get("failure_enabled", False),
            "failure_count": meta.get("failure_count", 0),
            "failure_nodes": meta.get("failure_nodes", []),
            "recovery_achieved": meta.get("recovery_achieved", False),
            "recovery_time_ms": meta.get("recovery_time_ms", 0),
            "pre_failure_commands_at_rf": meta.get("pre_failure_commands_at_rf", 0),
            "pre_failure_commands_under": meta.get("pre_failure_commands_under", 0),
            "post_failure_commands_at_rf": meta.get("post_failure_commands_at_rf", 0),
            "post_failure_commands_under": meta.get("post_failure_commands_under", 0),
        }
        results.append(result)

    # Sort the results primarily by node count, then by producer count
    results.sort(key=lambda r: (r["nodes"], r["producers"]))

    if csv_path:
        write_csv(results, csv_path)

    if md_path:
        write_results_md(results, run_dir, md_path)

    if show:
        print_summary(results)

    return {"results": results}


def write_csv(results: List[Dict], csv_path: Path):
    """Write results to CSV file."""
    with open(csv_path, "w") as f:
        f.write(
            "nodes,producers,replicated,rep_min_ms,rep_max_ms,rep_avg_ms,rep_med_ms,"
        )
        f.write("prop_min_ms,prop_max_ms,prop_avg_ms,prop_med_ms,")
        f.write(
            "sync_interests,data_packets,sync_per_cmd,data_per_cmd,total_commands,commands_at_rf,commands_over,commands_under,any_ever_over,"
        )
        f.write("total_duration_seconds,replication_wait_duration_seconds,")
        f.write(
            "failure_enabled,failure_count,failure_nodes,recovery_achieved,recovery_time_ms,"
        )
        f.write("pre_failure_commands_at_rf,pre_failure_commands_under,")
        f.write("post_failure_commands_at_rf,post_failure_commands_under\n")

        for r in results:
            f.write(f"{r['nodes']},{r['producers']},{str(r['replicated']).lower()},")
            f.write(
                f"{r['rep_min_ms']:.2f},{r['rep_max_ms']:.2f},{r['rep_avg_ms']:.2f},{r['rep_med_ms']:.2f},"
            )
            f.write(
                f"{r['prop_min_ms']:.2f},{r['prop_max_ms']:.2f},{r['prop_avg_ms']:.2f},{r['prop_med_ms']:.2f},"
            )
            f.write(
                f"{r['sync_interests']},{r['data_packets']},{r.get('sync_per_cmd', 0):.2f},{r.get('data_per_cmd', 0):.2f},{r['total_commands']},"
            )
            f.write(
                f"{r['commands_at_rf']},{r['commands_over']},{r['commands_under']},"
            )
            f.write(f"{str(r['any_ever_over']).lower()},")
            f.write(
                f"{r['total_duration_seconds']:.2f},{r['replication_wait_duration_seconds']:.2f},"
            )
            f.write(f"{str(r.get('failure_enabled', False)).lower()},")
            f.write(f"{r.get('failure_count', 0)},")
            f.write(f"'{r.get('failure_nodes', [])}',")
            f.write(f"{str(r.get('recovery_achieved', False)).lower()},")
            f.write(f"{r.get('recovery_time_ms', 0):.2f},")
            f.write(f"{r.get('pre_failure_commands_at_rf', 0)},")
            f.write(f"{r.get('pre_failure_commands_under', 0)},")
            f.write(f"{r.get('post_failure_commands_at_rf', 0)},")
            f.write(f"{r.get('post_failure_commands_under', 0)}\n")


def write_results_md(results: List[Dict], run_dir: Path, md_path: Path):
    """Write RESULTS.md file."""
    with open(md_path, "w") as f:
        f.write("# Experiment Results\n\n")
        f.write(f"**Run:** {datetime.now().strftime('%Y-%m-%d %H:%M:%S')}\n\n")

        has_failure = any(r.get("failure_enabled", False) for r in results)

        if has_failure:
            f.write("## Results (with Failure Simulation)\n\n")
            f.write(
                "| Nodes | Producers | Replicated | Rep Max (ms) | Prop Max (ms) | "
            )
            f.write(
                "Total Cmds | At RF | Over | Under | Sync/Cmd | Data/Cmd | Fail | Killed | Recov | Recov Time (ms) |\n"
            )
            f.write("|-------|-----------|------------|--------------|---------------|")
            f.write(
                "------------|-------|------|-------|----------|----------|------|--------|-------|----------------|\n"
            )

            for r in results:
                f.write(
                    f"| {r['nodes']} | {r['producers']} | {str(r['replicated']).lower()} | "
                )
                f.write(f"{r['rep_max_ms']:.2f} | {r['prop_max_ms']:.2f} | ")
                f.write(
                    f"{r['total_commands']} | {r['commands_at_rf']} | {r['commands_over']} | "
                )
                f.write(f"{r['commands_under']} | ")
                f.write(
                    f"{r.get('sync_per_cmd', 0):.1f} | {r.get('data_per_cmd', 0):.1f} | "
                )
                f.write(f"{str(r.get('failure_enabled', False)).lower()} | ")
                f.write(f"{r.get('failure_count', 0)} | ")
                f.write(f"{str(r.get('recovery_achieved', False)).lower()} | ")
                f.write(f"{r.get('recovery_time_ms', 0):.2f} |\n")
        else:
            f.write("## Results\n\n")
            f.write(
                "| Nodes | Producers | Replicated | Rep Max (ms) | Prop Max (ms) | "
            )
            f.write(
                "Total Cmds | At RF | Over | Under | Sync/Cmd | Data/Cmd | Any Over | Duration (s) |\n"
            )
            f.write("|-------|-----------|------------|--------------|---------------|")
            f.write(
                "------------|-------|------|-------|----------|----------|----------|---------------|\n"
            )

            for r in results:
                f.write(
                    f"| {r['nodes']} | {r['producers']} | {str(r['replicated']).lower()} | "
                )
                f.write(f"{r['rep_max_ms']:.2f} | {r['prop_max_ms']:.2f} | ")
                f.write(
                    f"{r['total_commands']} | {r['commands_at_rf']} | {r['commands_over']} | "
                )
                f.write(f"{r['commands_under']} | ")
                f.write(
                    f"{r.get('sync_per_cmd', 0):.1f} | {r.get('data_per_cmd', 0):.1f} | "
                )
                f.write(f"{str(r.get('any_ever_over', False)).lower()} | ")
                f.write(f"{r['total_duration_seconds']:.2f} |\n")


def print_summary(results: List[Dict]):
    """Print results summary to stdout."""
    if not results:
        print("No results found.")
        return

    has_failure = any(r.get("failure_enabled", False) for r in results)

    if has_failure:
        print("\n=== EXPERIMENT RESULTS (with Failure Simulation) ===\n")
        print(
            f"{'Nodes':<6} {'Prod':<6} {'Repl':<8} {'RepMax':<10} {'PropMax':<10} {'Total':<6} {'AtRF':<5} {'Sync':<8} {'Data':<8} {'S/Cmd':<7} {'D/Cmd':<7} {'Fail':<5} {'Killed':<7} {'Recov':<6} {'RecovTime':<10}"
        )
        print("-" * 126)

        for r in results:
            recov_time = r.get("recovery_time_ms", 0)
            recov_str = f"{recov_time:.0f}ms" if recov_time > 0 else "N/A"
            print(
                f"{r['nodes']:<6} {r['producers']:<6} {str(r['replicated']).lower():<8} "
                f"{r['rep_max_ms']:<10.2f} {r['prop_max_ms']:<10.2f} "
                f"{r['total_commands']:<6} {r['commands_at_rf']:<5} "
                f"{r.get('sync_interests', 0):<8} {r.get('data_packets', 0):<8} "
                f"{r.get('sync_per_cmd', 0):<7.1f} {r.get('data_per_cmd', 0):<7.1f} "
                f"{str(r.get('failure_enabled', False)).lower():<5} "
                f"{r.get('failure_count', 0):<7} "
                f"{str(r.get('recovery_achieved', False)).lower():<6} "
                f"{recov_str:<10}"
            )
    else:
        print("\n=== EXPERIMENT RESULTS ===\n")
        print(
            f"{'Nodes':<6} {'Prod':<6} {'Repl':<8} {'RepMax':<10} {'PropMax':<10} {'Total':<6} {'AtRF':<5} {'Over':<5} {'Under':<6} {'Sync':<8} {'Data':<8} {'S/Cmd':<7} {'D/Cmd':<7}"
        )
        print("-" * 111)

        for r in results:
            print(
                f"{r['nodes']:<6} {r['producers']:<6} {str(r['replicated']).lower():<8} "
                f"{r['rep_max_ms']:<10.2f} {r['prop_max_ms']:<10.2f} "
                f"{r['total_commands']:<6} {r['commands_at_rf']:<5} {r['commands_over']:<5} {r['commands_under']:<6} "
                f"{r.get('sync_interests', 0):<8} {r.get('data_packets', 0):<8} "
                f"{r.get('sync_per_cmd', 0):<7.1f} {r.get('data_per_cmd', 0):<7.1f}"
            )


def analyze_calibration(calibration_dir: Path, node_count: int, distribution: str = "hydra") -> Dict[str, Any]:
    """
    Analyze calibration results from event logs.

    Args:
        calibration_dir: Directory containing iteration subdirectories
        node_count: Expected node count
        distribution: Distribution mechanism (hydra, auction)

    Returns:
        Dict with calibration measurements and recommended timeouts
    """
    svs_times = []
    rep_times = []
    prop_times = []
    prop_rtt_times = []
    meta_values = []

    for iter_dir in sorted(calibration_dir.iterdir()):
        if not iter_dir.is_dir():
            continue

        metadata_file = iter_dir / "metadata.json"
        if metadata_file.exists():
            try:
                with open(metadata_file) as f:
                    meta = json.load(f)
                    meta_values.append(meta)
                rep_time_max = meta.get("replication_time_max_ms")
                if rep_time_max is not None:
                    rep_times.append(rep_time_max)
            except (json.JSONDecodeError, IOError):
                pass

        svs_time = measure_svs_convergence(iter_dir)
        if svs_time > 0:
            svs_times.append(svs_time)

        prop_time, prop_rtt = measure_update_propagation(iter_dir, node_count)
        if prop_time > 0:
            prop_times.append(prop_time)
        if prop_rtt > 0:
            prop_rtt_times.append(prop_rtt)

    max_svs = max(svs_times) if svs_times else 0
    rep_p99 = max(
        [meta.get("replication_time_p99_ms") or 0 for meta in meta_values]
    ) if meta_values else 0
    prop_p99 = max(
        [meta.get("update_propagation_p99_ms") or 0 for meta in meta_values]
    ) if meta_values else 0
    max_rep = max(rep_times) if rep_times else 0
    max_prop = max(prop_times) if prop_times else 0
    max_rtt = max(prop_rtt_times) if prop_rtt_times else 0.0

    rep_p99_safe = rep_p99 * 1.5 if rep_p99 else 0
    prop_p99_safe = prop_p99 * 1.5 if prop_p99 else 0
    if rep_p99_safe and prop_p99_safe:
        safe_interval = rep_p99_safe + prop_p99_safe
    else:
        safe_interval = max_rep + max_prop

    recommended_svs = int((max_svs * 1.25 / 1000) + 1)
    recommended_rep = int((safe_interval / 1000) + 1)

    result = {
        "distribution": distribution,
        "node_count": node_count,
        "iterations": len(rep_times),
        "measurements": {
            "svs_convergence_ms": svs_times,
            "replication_time_ms": rep_times,
            "replication_time_p99_ms": rep_p99,
            "update_propagation_ms": prop_times,
            "update_propagation_rtt": prop_rtt_times,
            "update_propagation_p99_ms": prop_p99,
        },
        "max_values": {
            "svs_convergence_ms": max_svs,
            "replication_time_ms": max_rep,
            "replication_time_p99_ms": rep_p99,
            "update_propagation_ms": max_prop,
            "update_propagation_rtt": max_rtt,
            "update_propagation_p99_ms": prop_p99,
            "safe_interval_ms": safe_interval,
        },
        "recommended_timeouts": {
            "svs_timeout_s": recommended_svs,
            "replication_timeout_s": recommended_rep,
        },
    }

    return result


def measure_svs_convergence(results_dir: Path) -> int:
    """Measure SVS convergence time from event logs."""
    max_svs = 0

    for log_file in results_dir.glob("events-*.jsonl"):
        first_sync = None
        first_update = None

        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        ts = event.get("ts", "")
                        if not ts:
                            continue

                        if event.get("event") == "sync_interest_sent":
                            if first_sync is None:
                                first_sync = ts
                        elif event.get("event") == "node_update":
                            if first_update is None:
                                first_update = ts
                    except json.JSONDecodeError:
                        pass
        except IOError:
            continue

        if first_sync and first_update:
            t1 = parse_ts(first_sync)
            t2 = parse_ts(first_update)
            if t1 and t2:
                delta = (t2 - t1).total_seconds() * 1000
                if delta > max_svs:
                    max_svs = int(delta)

    return max_svs


def parse_topology_delays(topo_path: Path) -> tuple:
    """Parse link delays from topology file.
    Returns: ({(node1_lower, node2_lower): delay_ms}, median_delay_ms)
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


def get_link_delay(node1: str, node2: str, delays: dict, fallback_ms: float) -> float:
    """Get one-way link delay between two nodes.
    Uses direct link if available, otherwise returns fallback.
    """
    key = (node1.lower(), node2.lower())
    return delays.get(key, fallback_ms)


def measure_update_propagation(results_dir: Path, node_count: int, topology_path: str = None) -> tuple:
    """Measure update propagation time from event logs.
    Returns: (max_propagation_ms, list_of_normalized_rtts)
    """
    # Load topology delays
    delays = {}
    fallback_delay = 10.0  # default for generated topologies
    if topology_path:
        topo_path = Path(topology_path)
    else:
        # Try to find topology in results directory
        topo_path = results_dir / 'topology.conf'
        if not topo_path.exists():
            topo_path = Path('/usr/local/share/testbed_topology.conf')
    
    delays, median_delay = parse_topology_delays(topo_path)
    if median_delay:
        fallback_delay = median_delay
    
    all_events = []

    for log_file in results_dir.glob("events-*.jsonl"):
        node_name = log_file.stem.replace("events-", "")
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        event["_node_name"] = node_name
                        all_events.append(event)
                    except json.JSONDecodeError:
                        pass
        except IOError:
            continue

    all_events.sort(key=lambda e: e.get("ts", ""))

    claim_times = {}
    update_received = {}

    for event in all_events:
        ts = event.get("ts")

        if event.get("event") == "job_claimed":
            target = event.get("target", "")
            claiming_node = event.get("_node_name", "")
            if target and claiming_node:
                if target not in claim_times:
                    claim_times[target] = {}
                if claiming_node not in claim_times[target]:
                    claim_times[target][claiming_node] = ts

        elif event.get("event") == "node_update":
            receiving_node = event.get("_node_name", "")
            from_node = event.get("from", "")
            jobs = event.get("jobs", [])

            if from_node and jobs:
                from_node_name = (
                    from_node.split("/")[-1] if "/" in from_node else from_node
                )
                for job in jobs:
                    target = job if isinstance(job, str) else job
                    if target:
                        key = (target, from_node_name)
                        if key not in update_received:
                            update_received[key] = {}
                        if receiving_node not in update_received[key]:
                            update_received[key][receiving_node] = ts

    max_prop_ms = 0
    normalized_rtts = []
    
    for target, claims in claim_times.items():
        for claiming_node, claim_ts in claims.items():
            key = (target, claiming_node)
            if key in update_received:
                received = update_received[key]
                if len(received) >= node_count - 1:
                    latest = None
                    for node, recv_ts in received.items():
                        if node != claiming_node:
                            recv_time = parse_ts(recv_ts)
                            if recv_time and (latest is None or recv_time > latest):
                                latest = recv_time

                    if latest:
                        claim_time = parse_ts(claim_ts)
                        if claim_time:
                            prop_time = (latest - claim_time).total_seconds() * 1000
                            if prop_time > max_prop_ms:
                                max_prop_ms = int(prop_time)
                            
                            # Calculate normalized RTT
                            link_delay = get_link_delay(claiming_node, node, delays, fallback_delay)
                            expected_rtt = 2 * link_delay  # RTT = 2 * one-way delay
                            if expected_rtt > 0:
                                normalized_rtt = prop_time / expected_rtt
                                normalized_rtts.append(normalized_rtt)

    max_normalized = max(normalized_rtts) if normalized_rtts else 0.0
    return max_prop_ms, max_normalized


def cmd_collect(args):
    """Handle 'collect' subcommand."""
    run_dir = Path(args.run_dir)

    if not run_dir.exists():
        print(f"Error: Run directory not found: {run_dir}", file=sys.stderr)
        sys.exit(1)

    csv_path = Path(args.csv) if args.csv else None
    md_path = Path(args.md) if args.md else None

    collect_results(run_dir, csv_path, md_path, show=args.show)


def cmd_calibrate(args):
    """Handle 'calibrate' subcommand."""
    cal_dir = Path(args.calibration_dir)
    distribution = getattr(args, 'distribution', 'hydra')

    if not cal_dir.exists():
        print(f"Error: Calibration directory not found: {cal_dir}", file=sys.stderr)
        sys.exit(1)

    result = analyze_calibration(cal_dir, args.node_count, distribution)

    print(f"\n=== CALIBRATION RESULTS ===\n")
    print(f"Distribution: {result.get('distribution', 'unknown')}")
    print(f"Node count: {result['node_count']}")
    print(f"Iterations: {result['iterations']}")
    print(f"\nMax values:")
    print(f"  SVS convergence: {result['max_values']['svs_convergence_ms']}ms")
    print(f"  Replication time: {result['max_values']['replication_time_ms']}ms")
    print(f"  Update propagation: {result['max_values']['update_propagation_ms']}ms")
    print(f"  Update propagation (RTT): {result['max_values']['update_propagation_rtt']:.2f} RTTs")
    print(f"  Safe interval: {result['max_values']['safe_interval_ms']}ms")
    print(f"\nRecommended timeouts:")
    print(f"  --svs-timeout={result['recommended_timeouts']['svs_timeout_s']}s")
    print(
        f"  --replication-timeout={result['recommended_timeouts']['replication_timeout_s']}s"
    )

    if args.output:
        output_path = Path(args.output)
        output_path.parent.mkdir(parents=True, exist_ok=True)
        with open(output_path, "w") as f:
            json.dump(result, f, indent=2)
        print(f"\nCalibration JSON written to: {output_path}")

        latest_link = output_path.parent / f"latest_calibration_{distribution}.json"
        if output_path.exists():
            latest_link.unlink(missing_ok=True)
            latest_link.symlink_to(output_path.name)
            print(f"Latest symlink updated: {latest_link}")


def main():
    parser = argparse.ArgumentParser(
        description="Helper utilities for NDN repository experiments"
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    collect_parser = subparsers.add_parser(
        "collect", help="Collect results from experiment run"
    )
    collect_parser.add_argument(
        "run_dir", help="Directory containing experiment results"
    )
    collect_parser.add_argument("--csv", help="Path to write CSV summary")
    collect_parser.add_argument("--md", help="Path to write RESULTS.md")
    collect_parser.add_argument(
        "--show", action="store_true", help="Print summary to stdout"
    )
    collect_parser.set_defaults(func=cmd_collect)

    calibrate_parser = subparsers.add_parser(
        "calibrate", help="Analyze calibration results"
    )
    calibrate_parser.add_argument(
        "calibration_dir", help="Directory containing calibration iterations"
    )
    calibrate_parser.add_argument(
        "--node-count", type=int, default=24, help="Expected node count"
    )
    calibrate_parser.add_argument(
        "--distribution", type=str, default="hydra", help="Distribution mechanism (hydra, auction)"
    )
    calibrate_parser.add_argument("--output", help="Path to write calibration JSON")
    calibrate_parser.set_defaults(func=cmd_calibrate)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
