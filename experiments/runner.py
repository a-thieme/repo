#!/usr/bin/env python3
"""
Runner script executed inside Docker container.
Orchestrates mini-ndn, repos, and producer. Writes event logs to results dir.
"""

import argparse
import json
import os
import signal
import subprocess
import sys
import threading
import time
from datetime import datetime
from pathlib import Path

from mininet.log import setLogLevel, info
from minindn.minindn import Minindn
from minindn.util import MiniNDNCLI
from minindn.apps.app_manager import AppManager
from minindn.apps.nfd import Nfd
from minindn.helpers.ndn_routing_helper import NdnRoutingHelper

ALL_NODES = [
    "UCLA",
    "NEU",
    "SAVI",
    "OSAKA",
    "AFA",
    "ANYANG",
    "TNO",
    "MEMPHIS",
    "QUB",
    "URJC",
    "WASEDA",
    "UFBA",
    "AVEIRO",
    "MML2",
    "MML1",
    "ARIZONA",
    "IIITH",
    "SINGAPORE",
    "FRANKFURT",
    "SRRU",
    "DELFT",
    "WU",
    "BERN",
    "MINHO",
]

ndn = None
loss_nodes = []
partition_links_list = []


def apply_netem_loss(node_name, loss_rate):
    """Apply tc netem loss to a node's all interfaces.

    Uses netem to simulate packet loss on all interfaces.
    Example: tc qdisc add dev eth0 root netem loss 0.5%
    """
    if loss_rate <= 0:
        return
    for host in ndn.net.hosts:
        if host.name != node_name:
            continue
        info(f"  Applying {loss_rate}% netem loss to {node_name}...\n")
        for intf in host.intfList():
            if intf.name == "lo":
                continue
            # Add netem loss to interface
            host.cmd(f"tc qdisc add dev {intf.name} root netem loss {loss_rate}% 2>/dev/null") or host.cmd(f"tc qdisc change dev {intf.name} root netem loss {loss_rate}% 2>/dev/null")


def remove_netem_loss(node_name):
    """Remove netem loss configuration from a node."""
    for host in ndn.net.hosts:
        if host.name != node_name:
            continue
        info(f"  Removing netem loss from {node_name}...\n")
        for intf in host.intfList():
            if intf.name == "lo":
                continue
            host.cmd(f"tc qdisc del dev {intf.name} root 2>/dev/null")


def apply_link_loss(loss_rate, node_names):
    """Apply netem loss to specified nodes (or all if empty)."""
    if loss_rate <= 0:
        return
    targets = node_names if node_names else [h.name for h in ndn.net.hosts]
    for node_name in targets:
        apply_netem_loss(node_name, loss_rate)


def remove_link_loss(node_names):
    """Remove netem loss from specified nodes."""
    targets = node_names if node_names else loss_nodes
    for node_name in targets:
        remove_netem_loss(node_name)


def apply_link_failure(node1, node2):
    """Simulate link failure between two nodes by dropping all traffic.

    Uses netem to add 100% packet loss AND iptables to block UDP traffic.
    This provides defense-in-depth to ensure no traffic crosses the partition.
    Uses ndn.net.links to find the correct link between nodes.
    """
    # Find the link connecting node1 and node2
    # Use case-insensitive comparison since mini-ndn may use lowercase node names
    node1_lower = node1.lower()
    node2_lower = node2.lower()
    for link in ndn.net.links:
        intf1_node = link.intf1.node.name if link.intf1 and link.intf1.node else None
        intf2_node = link.intf2.node.name if link.intf2 and link.intf2.node else None

        # Check if this link connects node1 and node2 (case-insensitive)
        if (intf1_node and intf1_node.lower() == node1_lower and intf2_node and intf2_node.lower() == node2_lower) or \
           (intf1_node and intf1_node.lower() == node2_lower and intf2_node and intf2_node.lower() == node1_lower):
            node_a = link.intf1.node
            node_b = link.intf2.node
            intf_a = link.intf1
            intf_b = link.intf2

            if intf_a.name == 'lo' or intf_b.name == 'lo':
                continue

            # Get IP addresses
            node_a_ip = intf_a.ip if hasattr(intf_a, 'ip') and intf_a.ip else None
            node_b_ip = intf_b.ip if hasattr(intf_b, 'ip') and intf_b.ip else None

            info(f"  Blocking link {node_a.name} <-> {node_b.name} (IPs: {node_a_ip} <-> {node_b_ip})\n")

            # Apply netem loss on both ends
            node_a.cmd(f"tc qdisc add dev {intf_a.name} root netem loss 100% 2>/dev/null || tc qdisc change dev {intf_a.name} root netem loss 100%")
            node_b.cmd(f"tc qdisc add dev {intf_b.name} root netem loss 100% 2>/dev/null || tc qdisc change dev {intf_b.name} root netem loss 100%")

            # Also use iptables to block UDP traffic between these nodes (defense in depth)
            if node_a_ip and node_b_ip:
                # Block UDP traffic from node_a to node_b
                node_a.cmd(f"iptables -I OUTPUT -d {node_b_ip} -p udp -j DROP 2>/dev/null || true")
                # Block UDP traffic from node_b to node_a
                node_b.cmd(f"iptables -I OUTPUT -d {node_a_ip} -p udp -j DROP 2>/dev/null || true")

            info(f"    netem + iptables applied on both ends\n")

            return  # Link found and failure applied
    info(f"  Warning: No link found between {node1} and {node2}\n")

def capture_nfd_state(node, prefix="ndn", capture_face_scope="local"):
    """Capture NFD FIB and face status for debugging route issues.

    Args:
        node: Mininet host node to query
        prefix: Filter FIB entries by this prefix (empty string = all entries)
        capture_face_scope: 'local' for faces with scope=local, 'all' for everything

    Returns:
        dict with 'fib' (list of FIB entries), 'faces' (list of face entries), 'timestamp'
    """
    import datetime

    result = {
        "timestamp": datetime.datetime.utcnow().isoformat() + "Z",
        "node": node.name,
        "fib": [],
        "faces": [],
    }

    # Capture FIB entries, optionally filtered by prefix
    if prefix:
        fib_output = node.cmd(f"nfdc fib list 2>&1 | grep '{prefix}' || true")
    else:
        fib_output = node.cmd("nfdc fib list 2>&1")

    for line in fib_output.strip().split("\n"):
        if line.strip():
            result["fib"].append(line.strip())

    # Capture face list
    face_output = node.cmd("nfdc face list 2>&1")
    for line in face_output.strip().split("\n"):
        if line.strip():
            # Filter by scope if requested
            if capture_face_scope == "local" and "scope=local" not in line.lower():
                continue
            result["faces"].append(line.strip())

    return result


def capture_nfd_state_all_nodes(prefix="ndn"):
    """Capture NFD state from all nodes. Call this during experiments to diagnose issues.

    Returns:
        dict mapping node name -> NFD state dict
    """
    global ndn
    all_state = {}
    for host in ndn.net.hosts:
        try:
            state = capture_nfd_state(host, prefix=prefix)
            all_state[host.name] = state
        except Exception as e:
            all_state[host.name] = {"error": str(e)}
    return all_state


def log_nfd_state(state, log_prefix="nfd_state"):
    """Log NFD state to stdout in a structured format for analysis.

    Args:
        state: dict from capture_nfd_state or capture_nfd_state_all_nodes
        log_prefix: prefix for log lines
    """
    if isinstance(state, dict) and "node" in state:
        # Single node state
        info(f"  [{log_prefix}] {state['node']} @ {state['timestamp']}\n")
        info(f"    FIB entries ({len(state.get('fib', []))}):\n")
        for entry in state.get('fib', []):
            info(f"      {entry}\n")
        info(f"    Faces ({len(state.get('faces', []))}):\n")
        for face in state.get('faces', []):
            info(f"      {face}\n")
    else:
        # Multiple nodes
        for node_name, node_state in state.items():
            if isinstance(node_state, dict) and "error" not in node_state:
                log_nfd_state(node_state, log_prefix=f"{log_prefix}/{node_name}")


def remove_link_failure(node1, node2):
    """Restore link between two nodes by removing netem and iptables blocks.

    Removes the 100% loss and iptables rules that were applied to simulate link failure.
    """
    node1_lower = node1.lower()
    node2_lower = node2.lower()
    for link in ndn.net.links:
        intf1_node = link.intf1.node.name if link.intf1 and link.intf1.node else None
        intf2_node = link.intf2.node.name if link.intf2 and link.intf2.node else None

        if (intf1_node and intf1_node.lower() == node1_lower and intf2_node and intf2_node.lower() == node2_lower) or \
           (intf1_node and intf1_node.lower() == node2_lower and intf2_node and intf2_node.lower() == node1_lower):
            node_a = link.intf1.node
            node_b = link.intf2.node
            intf_a = link.intf1
            intf_b = link.intf2

            if intf_a.name == 'lo' or intf_b.name == 'lo':
                continue

            # Get IP addresses
            node_a_ip = intf_a.ip if hasattr(intf_a, 'ip') and intf_a.ip else None
            node_b_ip = intf_b.ip if hasattr(intf_b, 'ip') and intf_b.ip else None

            info(f"  Removing block from link {node_a.name}:{intf_a.name} <-> {node_b.name}:{intf_b.name}\n")

            # Remove netem qdisc
            node_a.cmd(f"tc qdisc del dev {intf_a.name} root 2>/dev/null")
            node_b.cmd(f"tc qdisc del dev {intf_b.name} root 2>/dev/null")

            # Remove iptables rules
            if node_a_ip and node_b_ip:
                node_a.cmd(f"iptables -D OUTPUT -d {node_b_ip} -p udp -j DROP 2>/dev/null || true")
                node_b.cmd(f"iptables -D OUTPUT -d {node_a_ip} -p udp -j DROP 2>/dev/null || true")

            return
    info(f"  Warning: No link found between {node1} and {node2} to restore\n")


def create_partition(link_list):
    """Apply failures to list of node pairs to create partition.

    Args:
        link_list: List of tuples [(node1, node2), ...] to sever
    """
    for node1, node2 in link_list:
        apply_link_failure(node1, node2)


def remove_partition(link_list):
    """Restore links for a list of node pairs."""
    for node1, node2 in link_list:
        remove_link_failure(node1, node2)


def cleanup():
    global ndn
    info("\nCleaning up...\n")

    # Remove link loss before stopping ndn
    if loss_nodes:
        info("Removing link loss configurations...\n")
        remove_link_loss(loss_nodes)

    # Remove partition links
    global partition_links_list
    if partition_links_list:
        info("Removing partition links...\n")
        remove_partition(partition_links_list)

    # Use thread with timeout for ndn.stop() - can hang on large experiments
    cleanup_done = threading.Event()
    cleanup_error = [None]

    def run_cleanup():
        try:
            if ndn:
                ndn.stop()
        except Exception as e:
            cleanup_error[0] = e
        finally:
            cleanup_done.set()

    cleanup_thread = threading.Thread(target=run_cleanup)
    cleanup_thread.daemon = True
    cleanup_thread.start()

    # Wait up to 30 seconds for cleanup, then force kill
    if not cleanup_done.wait(timeout=30):
        info("\nCleanup timeout expired, killing processes...\n")
        # Send SIGKILL to entire process group
        os.kill(os.getpid(), signal.SIGKILL)

    try:
        Minindn.cleanUp()
    except:
        pass


def signal_handler(sig, frame):
    cleanup()
    sys.exit(1)


def parse_topology_nodes(topo_path):
    """Extract node names from topology file."""
    nodes = []
    with open(topo_path) as f:
        in_nodes = False
        for line in f:
            line = line.strip()
            if line == "[nodes]":
                in_nodes = True
                continue
            if line.startswith("[") and in_nodes:
                break
            if in_nodes and ":" in line:
                node_name = line.split(":")[0].strip()
                if node_name:
                    nodes.append(node_name)
    return nodes


def main():
    global ndn

    parser = argparse.ArgumentParser()
    parser.add_argument("--node-count", type=int, default=5)
    parser.add_argument("--timeout", type=int, default=60)
    parser.add_argument("--results-dir", default="/results")
    parser.add_argument("--replication-factor", type=int, default=3)
    parser.add_argument("--routing-wait", type=int, default=60)
    parser.add_argument("--producer-count", type=int, default=1)
    parser.add_argument("--command-count", type=int, default=1)
    parser.add_argument("--command-rate", type=int, default=1)
    parser.add_argument(
        "--debug", action="store_true", help="Enable verbose repo logging"
    )
    parser.add_argument(
        "--repo-bin", default="/usr/local/bin/repo", help="Path to repo binary"
    )
    parser.add_argument(
        "--producer-bin",
        default="/usr/local/bin/producer",
        help="Path to producer binary",
    )
    parser.add_argument(
        "--svs-timeout", type=int, default=30, help="SVS health check timeout (seconds)"
    )
    parser.add_argument(
        "--producer-timeout",
        type=int,
        default=1,
        help="Producer command timeout (seconds)",
    )
    parser.add_argument(
        "--sync-start",
        action="store_true",
        help="Synchronize all producers to start at the same time (for simultaneous commands)",
    )
    parser.add_argument(
        "--sync-wait",
        type=int,
        default=5,
        help="Seconds to wait before synchronized start (only used with --sync-start)",
    )
    parser.add_argument(
        "--replication-timeout",
        type=int,
        default=1,
        help="Replication wait timeout (seconds)",
    )
    parser.add_argument(
        "--nfd-wait", type=int, default=3, help="NFD initialization wait (seconds)"
    )
    parser.add_argument(
        "--topology", default="", help="Path to topology file (overrides default)"
    )
    parser.add_argument(
        "--repo-count",
        type=int,
        default=0,
        help="Number of nodes to run repos (0=all nodes)",
    )
    parser.add_argument(
        "--producer-nodes",
        default="",
        help="Comma-separated list of node names to run producers",
    )
    parser.add_argument(
        "--command-type", default="insert", help="Command type: insert, join, or both"
    )
    parser.add_argument(
        "--join-ratio",
        type=float,
        default=0.5,
        help="Ratio of JOIN commands when type is both (0.0-1.0)",
    )
    parser.add_argument(
        "--no-release",
        action="store_true",
        help="Disable automatic job release when storage exceeds 75%%",
    )
    parser.add_argument(
        "--max-join-growth-rate",
        type=int,
        default=10485760,
        help="Maximum JOIN storage growth per second in bytes",
    )
    parser.add_argument(
        "--failure-count",
        type=int,
        default=0,
        help="Number of repos to kill (default: 0, no failure)",
    )
    parser.add_argument(
        "--failure-nodes",
        type=str,
        default="",
        help="Comma-separated node names to kill (default: nodes with most claims)",
    )
    parser.add_argument(
        "--failure-wait",
        type=int,
        default=0,
        help="Seconds to wait after replication before killing (default: 0 = immediately)",
    )
    parser.add_argument(
        "--failure-recovery-timeout",
        type=int,
        default=30,
        help="Timeout for recovery after failure (seconds)",
    )
    parser.add_argument(
        "--distribution", default="hydra", help="Distribution mechanism: hydra, auction"
    )
    parser.add_argument(
        "--link-loss-rate",
        type=float,
        default=0,
        help="Link loss rate percentage (0-100) applied to all nodes",
    )
    parser.add_argument(
        "--link-loss-nodes",
        type=str,
        default="",
        help="Comma-separated node names to apply loss to (default: all nodes)",
    )
    parser.add_argument(
        "--partition-links",
        type=str,
        default="",
        help="Comma-separated node1:node2 pairs to sever (e.g., 'UCLA:WASEDA,OSAKA:DELFT')",
    )
    parser.add_argument(
        "--partition-after",
        type=float,
        default=0,
        help="Seconds to wait after commands complete before creating partition (default: 0)",
    )
    parser.add_argument(
        "--partition-timeout",
        type=float,
        default=60,
        help="Seconds to wait after partition before collecting results (default: 60)",
    )
    args = parser.parse_args()

    sys.argv = [sys.argv[0]]

    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    setLogLevel("info")

    experiment_start_time = time.time()

    results_dir = Path(args.results_dir)
    results_dir.mkdir(parents=True, exist_ok=True)

    testbed_default = "/usr/local/share/testbed_topology.conf"
    topo_path = None
    nodes = None

    if args.topology:
        topo_path = Path(args.topology)
        if not topo_path.exists():
            info(f"ERROR: Topology file not found: {topo_path}\n")
            sys.exit(1)
        nodes = parse_topology_nodes(topo_path)
        info(f"Using user-specified topology: {topo_path} ({len(nodes)} nodes)\n")
        if args.node_count != len(nodes):
            info(
                f"ERROR: --node-count ({args.node_count}) does not match topology node count ({len(nodes)})\n"
            )
            sys.exit(1)
    elif Path(testbed_default).exists():
        testbed_nodes = parse_topology_nodes(Path(testbed_default))
        if args.node_count == len(testbed_nodes):
            topo_path = Path(testbed_default)
            nodes = testbed_nodes
            info(f"Using default testbed topology: {topo_path} ({len(nodes)} nodes)\n")
            # Copy topology to results for analysis
            topo_content = Path(testbed_default).read_text()
            (results_dir / "topology.conf").write_text(topo_content)
        else:
            nodes = ALL_NODES[: args.node_count]
            topology_content = create_topology(nodes)
            topo_path = results_dir / "topology.conf"
            topo_path.write_text(topology_content)
            info(
                f"Using generated full-mesh topology for {len(nodes)} nodes (testbed has {len(testbed_nodes)} nodes)\n"
            )
    else:
        nodes = ALL_NODES[: args.node_count]
        topology_content = create_topology(nodes)
        topo_path = results_dir / "topology.conf"
        topo_path.write_text(topology_content)
        info(f"Using generated full-mesh topology for {len(nodes)} nodes\n")

    info(f"=== Mini-NDN Integration Runner ===\n")
    info(f"Nodes: {nodes}\n")
    info(f"Replication factor: {args.replication_factor}\n")
    info(f"Producers: {args.producer_count}\n")
    info(f"Commands per producer: {args.command_count}\n")
    info(f"Command rate: {args.command_rate} cmds/sec\n")
    info(f"Timeout: {args.timeout}s\n")

    min_producer_time = (args.command_count / args.command_rate) + 10
    if args.producer_timeout < min_producer_time:
        args.producer_timeout = int(min_producer_time) + 5
        info(
            f"Auto-adjusted producer timeout to {args.producer_timeout}s for {args.command_count} commands at {args.command_rate} cmds/sec\n"
        )

    Minindn.cleanUp()
    Minindn.verifyDependencies()

    sys.argv = [sys.argv[0], str(topo_path)]
    ndn = Minindn()
    ndn.start()

    info("Starting NFD on nodes...\n")
    nfds = AppManager(ndn, ndn.net.hosts, Nfd)

    info(f"Waiting for NFD to initialize ({args.nfd_wait}s)...\n")
    time.sleep(args.nfd_wait)

    info("Setting up routes using NdnRoutingHelper...\n")
    grh = NdnRoutingHelper(ndn.net, "udp", "link-state")

    for host in ndn.net.hosts:
        node_prefix = f"/ndn/repo/{host.name}"
        sync_data_prefix = f"/ndn/drepo/group-messages{node_prefix}"
        results_prefix = f"{node_prefix}/results"
        heartbeat_prefix = "/ndn/drepo/heartbeat"
        bid_prefix = f"{node_prefix}/bid"
        grh.addOrigin(
            [host],
            [
                "/ndn/drepo/group-messages/32=svs",
                "/ndn/drepo/heartbeat",
                node_prefix,
                sync_data_prefix,
                results_prefix,
                bid_prefix,
            ],
        )
        info(
            f"  Added origin for {host.name}: /ndn/drepo/group-messages/32=svs, /ndn/drepo/heartbeat, {node_prefix}, {sync_data_prefix}, {results_prefix}, {bid_prefix}\n"
        )

    info("Calculating and installing routes...\n")
    grh.calculateNPossibleRoutes()

    # Force sync RIB to FIB to ensure all routes are installed
    info("Syncing RIB to FIB...\n")
    for host in ndn.net.hosts:
        host.cmd("nfdc rib announce 2>&1")

    # Wait for routes to propagate
    info("Waiting for routes to propagate (5s)...\n")
    time.sleep(5)

    info("Setting multicast strategy for SVS sync interests and bid prefixes...\n")
    for host in ndn.net.hosts:
        host.cmd(
            "nfdc strategy set /ndn/drepo/group-messages/32=svs /localhost/nfd/strategy/multicast 2>&1"
        )
        host.cmd(
            "nfdc strategy set /ndn/drepo/heartbeat /localhost/nfd/strategy/multicast 2>&1"
        )
        # Set multicast strategy for each node's bid prefix explicitly (not regex)
        for other_host in ndn.net.hosts:
            bid_prefix = f"/ndn/repo/{other_host.name}/bid"
            host.cmd(
                f"nfdc strategy set {bid_prefix} /localhost/nfd/strategy/multicast 2>&1"
            )
        host.cmd(
            "nfdc strategy set /ndn/drepo/notify /localhost/nfd/strategy/best-route 2>&1"
        )

    info("Verifying multicast strategy is active on all nodes...\n")
    strategy_timeout = 10
    for host in ndn.net.hosts:
        prefixes_to_verify = [
            "/ndn/drepo/group-messages/32=svs",
            "/ndn/drepo/heartbeat",
        ]
        # Add bid prefixes for verification
        for other_host in ndn.net.hosts:
            prefixes_to_verify.append(f"/ndn/repo/{other_host.name}/bid")

        for prefix in prefixes_to_verify:
            deadline = time.time() + strategy_timeout
            result = ""
            while time.time() < deadline:
                result = host.cmd(f"nfdc strategy show {prefix} 2>&1")
                if "/localhost/nfd/strategy/multicast" in result:
                    break
                host.cmd(
                    f"nfdc strategy set {prefix} /localhost/nfd/strategy/multicast 2>&1"
                )
                time.sleep(0.1)
            else:
                info(
                    f"  ERROR: Failed to set multicast strategy on {host.name} for {prefix} after {strategy_timeout}s\n"
                )
                info(f"    Last output: {result}\n")
    info("Strategy verification complete.\n")

    info("Verifying FIB entries...\n")
    for host in ndn.net.hosts:
        fib = host.cmd("nfdc fib list 2>&1 | head -20")
        info(f"  {host.name} FIB (first 20 lines):\n{fib}\n")

    repo_count = args.repo_count if args.repo_count > 0 else len(ndn.net.hosts)
    repo_hosts = ndn.net.hosts[:repo_count]

    info(f"Starting repos on {repo_count} of {len(ndn.net.hosts)} nodes...\n")
    os.environ["SHELL"] = "/bin/bash"
    for host in repo_hosts:
        event_log = results_dir / f"events-{host.name}.jsonl"
        stdout_log = results_dir / f"stdout-{host.name}.log"
        node_prefix = f"/ndn/repo/{host.name}"
        signing_identity = "/ndn/repo.teame.dev/repo"
        debug_flag = " --debug" if args.debug else ""
        no_release_flag = " --no-release" if args.no_release else ""
        storage_flags = (
            f"{no_release_flag} --max-join-growth-rate {args.max_join_growth_rate}"
        )
        dist_flag = f" --distribution {args.distribution}"
        auction_timeout_flag = (
            " --auction-timeout 5s" if args.distribution == "auction" else ""
        )
        cmd = f"{args.repo_bin} --event-log {event_log} --node-prefix {node_prefix} --signing-identity {signing_identity}{debug_flag}{storage_flags}{dist_flag}{auction_timeout_flag} > {stdout_log} 2>&1 &"
        host.cmd(f"mkdir -p /tmp/{host.name}")
        host.cmd(cmd)
        info(f"  Started repo on {host.name} with prefix {node_prefix}\n")

    repo_node_names = [host.name for host in repo_hosts]
    svs_healthy = wait_for_svs_health(
        results_dir, repo_node_names, timeout=args.svs_timeout
    )

    info("Checking for node updates in event logs...\n")
    for host in repo_hosts:
        event_log = results_dir / f"events-{host.name}.jsonl"
        if event_log.exists():
            with open(event_log) as f:
                lines = f.readlines()
                node_updates = [l for l in lines if "node_update" in l]
                sync_interests = [l for l in lines if "sync_interest_sent" in l]
                data_sent = [l for l in lines if "data_sent" in l]
                rep_checks = [l for l in lines if "replication_check" in l]
                info(
                    f"  {host.name}: updates_received={len(node_updates)}, sync_sent={len(sync_interests)}, data_sent={len(data_sent)}, rep_checks={len(rep_checks)}\n"
                )

    if args.producer_nodes:
        producer_node_names = [n.strip() for n in args.producer_nodes.split(",")]
        producer_nodes = [h for h in repo_hosts if h.name in producer_node_names]
        if len(producer_nodes) != len(producer_node_names):
            found = {h.name for h in producer_nodes}
            missing = [n for n in producer_node_names if n not in found]
            info(f"ERROR: Producer nodes not found in repo hosts: {missing}\n")
            sys.exit(1)
        if len(producer_nodes) != args.producer_count:
            info(
                f"ERROR: --producer-nodes specifies {len(producer_nodes)} nodes but --producer-count is {args.producer_count}\n"
            )
            sys.exit(1)
    else:
        producer_nodes = repo_hosts[: args.producer_count]

    expected_commands = len(producer_nodes) * args.command_count
    min_replication_time = (expected_commands / args.command_rate) * 1.5

    # Auction mode needs more time due to bidding round
    auction_multiplier = 10 if args.distribution == "auction" else 5

    calculated_timeout = int(min_replication_time * auction_multiplier) + 10
    if args.distribution == "auction" and calculated_timeout < 30:
        calculated_timeout = 30

    if args.replication_timeout < calculated_timeout:
        args.replication_timeout = calculated_timeout
        info(
            f"Auto-adjusted replication timeout to {args.replication_timeout}s ({auction_multiplier}x multiplier) for {expected_commands} commands\n"
        )

    # Apply link loss if specified
    loss_nodes_list = [n.strip() for n in args.link_loss_nodes.split(",") if n.strip()] if args.link_loss_nodes else []
    if args.link_loss_rate > 0:
        info(f"Applying {args.link_loss_rate}% link loss to {len(loss_nodes_list) if loss_nodes_list else 'all'} nodes...\n")
        apply_link_loss(args.link_loss_rate, loss_nodes_list)
        loss_nodes.extend(loss_nodes_list if loss_nodes_list else [h.name for h in ndn.net.hosts])

    info(f"Running {len(producer_nodes)} producer(s)...\n")

    # Capture baseline NFD state before commands start
    info("=== NFD STATE: BASELINE (before commands) ===\n")
    baseline_state = capture_nfd_state_all_nodes(prefix="/ndn/drepo")
    for node_name, state in baseline_state.items():
        if isinstance(state, dict) and "error" not in state:
            log_nfd_state(state, log_prefix=f"baseline/{node_name}")

    # Also capture with all faces (not just local) for debugging
    info("=== NFD STATE: BASELINE ALL FACES (before commands) ===\n")
    baseline_all_faces = {}
    for host in ndn.net.hosts:
        try:
            state = capture_nfd_state(host, prefix="/ndn/drepo", capture_face_scope="all")
            baseline_all_faces[host.name] = state
        except Exception as e:
            baseline_all_faces[host.name] = {"error": str(e)}
    for node_name, state in baseline_all_faces.items():
        if isinstance(state, dict) and "error" not in state:
            log_nfd_state(state, log_prefix=f"baseline_faces/{node_name}")

    def run_producer_with_nfd_monitoring(node, sync_time=None):
        """Run producer on a node with concurrent NFD state monitoring during execution.

        Uses threading to run producer in background while monitoring NFD state.
        This approach avoids the 'face is not running' issue that occurs with popen().
        """
        info(f"  Starting producer on {node.name} with NFD monitoring...\n")
        cmd_type_flag = f" -type {args.command_type}"
        join_ratio_flag = (
            f" -join-ratio {args.join_ratio}" if args.command_type == "both" else ""
        )
        sync_start_flag = f" -sync-start {sync_time}" if sync_time else ""

        # Use threading to run producer in background
        producer_cmd = f"timeout {args.producer_timeout}s {args.producer_bin} -count {args.command_count} -rate {args.command_rate}{cmd_type_flag}{join_ratio_flag}{sync_start_flag}"
        producer_output_file = results_dir / f"producer-{node.name}.log"
        producer_result = [None]  # Use list to capture result from thread

        def run_producer_thread():
            # Use node.cmd() which properly maintains NFD face connectivity
            result = node.cmd(f"{producer_cmd} > {producer_output_file} 2>&1")
            producer_result[0] = result

        # Start producer in a thread
        prod_thread = threading.Thread(target=run_producer_thread)
        prod_thread.daemon = True
        prod_thread.start()

        # Give the thread a moment to start
        time.sleep(0.1)

        # Monitor NFD state during producer execution
        monitoring_interval = 0.5  # Capture state every 500ms
        max_wait = args.command_count / args.command_rate + 10  # Expected runtime + buffer

        start_time = time.time()
        nfd_states_during = []

        # Capture initial state
        try:
            state = capture_nfd_state(node, prefix="/ndn/drepo", capture_face_scope="all")
            state["elapsed_ms"] = 0
            nfd_states_during.append(state)
        except Exception as e:
            info(f"  Warning: Initial NFD state capture failed: {e}\n")

        # Monitor during execution
        while prod_thread.is_alive() and (time.time() - start_time) < max_wait:
            time.sleep(monitoring_interval)

            # Capture NFD state
            try:
                state = capture_nfd_state(node, prefix="/ndn/drepo", capture_face_scope="all")
                state["elapsed_ms"] = int((time.time() - start_time) * 1000)
                nfd_states_during.append(state)

                # Check for local unix faces (on-demand faces that may go idle)
                for face_line in state.get("faces", []):
                    if "unix" in face_line.lower() and "on-demand" in face_line.lower():
                        info(f"  [NFD MONITOR] {node.name} face @ {state['elapsed_ms']}ms: {face_line[:80]}...\n")
            except Exception as e:
                info(f"  Warning: NFD state capture failed during monitoring: {e}\n")

            # Also check for any Nack patterns in producer output so far
            try:
                if producer_output_file.exists():
                    content = producer_output_file.read_text()
                    if "no_route" in content.lower():
                        info(f"  [NFD MONITOR] *** NO_ROUTE detected in producer output at {state['elapsed_ms']}ms ***\n")
                        # Capture full state immediately when Nack detected
                        try:
                            immediate_state = capture_nfd_state(node, prefix="/ndn/drepo", capture_face_scope="all")
                            immediate_state["elapsed_ms"] = int((time.time() - start_time) * 1000)
                            immediate_state["note"] = "CAPTURED_AT_NACK"
                            nfd_states_during.append(immediate_state)
                            info(f"  [NFD MONITOR] Immediate face list at failure:\n")
                            for face in immediate_state.get("faces", []):
                                info(f"    {face}\n")
                        except Exception as e2:
                            info(f"  Warning: Immediate NFD capture failed: {e2}\n")
            except Exception:
                pass

        # Wait for producer thread to complete
        prod_thread.join(timeout=max_wait)
        result = producer_output_file.read_text() if producer_output_file.exists() else ""

        # Log captured states
        if nfd_states_during:
            nfd_monitoring_file = results_dir / f"nfd-monitoring-{node.name}.json"
            with open(nfd_monitoring_file, "w") as f:
                json.dump(nfd_states_during, f, indent=2)
            info(f"  NFD monitoring data written to {nfd_monitoring_file} ({len(nfd_states_during)} snapshots)\n")

        info(f"  Producer {node.name} output: {result[:500]}...\n")
        return result

    def run_producer(node, sync_time=None):
        """Run producer on a node, optionally with synchronized start."""
        info(f"  Starting producer on {node.name}...\n")
        cmd_type_flag = f" -type {args.command_type}"
        join_ratio_flag = (
            f" -join-ratio {args.join_ratio}" if args.command_type == "both" else ""
        )
        sync_start_flag = f" -sync-start {sync_time}" if sync_time else ""
        result = node.cmd(
            f"timeout {args.producer_timeout}s {args.producer_bin} -count {args.command_count} -rate {args.command_rate}{cmd_type_flag}{join_ratio_flag}{sync_start_flag} 2>&1"
        )
        info(f"  Producer {node.name} output: {result}\n")
        return result

    if args.sync_start and len(producer_nodes) > 1:
        # Synchronized start: all producers send at the same moment
        sync_time = int(time.time()) + args.sync_wait
        info(f"=== SYNCHRONIZED START: all producers will send at Unix timestamp {sync_time} ===\n")
        threads = []
        for producer_node in producer_nodes:
            t = threading.Thread(target=lambda pn=producer_node: run_producer_with_nfd_monitoring(pn, sync_time))
            threads.append(t)
            t.start()
        for t in threads:
            t.join()
    else:
        # Sequential start (original behavior)
        for producer_node in producer_nodes:
            run_producer(producer_node)

    # Capture NFD state after commands complete (before replication wait)
    info("=== NFD STATE: AFTER COMMANDS (before replication) ===\n")
    after_commands_state = capture_nfd_state_all_nodes(prefix="/ndn/drepo")
    for node_name, state in after_commands_state.items():
        if isinstance(state, dict) and "error" not in state:
            log_nfd_state(state, log_prefix=f"after_cmds/{node_name}")

    # Also capture with all faces (not just local) for debugging
    info("=== NFD STATE: AFTER COMMANDS ALL FACES (before replication) ===\n")
    after_all_faces = {}
    for host in ndn.net.hosts:
        try:
            state = capture_nfd_state(host, prefix="/ndn/drepo", capture_face_scope="all")
            after_all_faces[host.name] = state
        except Exception as e:
            after_all_faces[host.name] = {"error": str(e)}
    for node_name, state in after_all_faces.items():
        if isinstance(state, dict) and "error" not in state:
            log_nfd_state(state, log_prefix=f"after_cmds_faces/{node_name}")

    expected_claims = expected_commands * args.replication_factor

    info(
        f"Waiting for replication (timeout={args.replication_timeout}s, expecting {expected_commands} commands, {expected_claims} claims)...\n"
    )
    start_time = time.time()
    replicated = False
    last_claim_count = 0
    last_progress_time = time.time()

    while time.time() - start_time < args.replication_timeout:
        claim_count = count_job_claims(results_dir)
        if claim_count != last_claim_count:
            info(f"  Job claims: {claim_count}/{expected_claims}\n")
            last_claim_count = claim_count
            last_progress_time = time.time()

        # Check if we've achieved full replication
        commands = build_replication_timeline(results_dir)
        commands_at_rf = sum(
            1
            for cmd in commands.values()
            if cmd["final_replication"] == args.replication_factor
        )
        commands_over = sum(
            1
            for cmd in commands.values()
            if cmd["final_replication"] > args.replication_factor
        )
        commands_under = sum(
            1
            for cmd in commands.values()
            if cmd["final_replication"] < args.replication_factor
        )

        if commands_under == 0 and len(commands) > 0:
            # Allow over-replication - more copies than RF is okay for partition testing
            # What matters is that no command is under-replicated
            replicated = True
            info(
                f"  All {expected_commands} commands achieved at least RF={args.replication_factor} ({commands_at_rf} at RF, {commands_over} over)\n"
            )
            break

        # If no progress for 30s, break to avoid infinite loop
        if time.time() - last_progress_time > 30:
            info(
                f"  No progress for 30s, stopping at {commands_at_rf}/{expected_commands} commands at RF\n"
            )
            break

        time.sleep(1)

    # Rebuild commands to get final counts
    commands = build_replication_timeline(results_dir)

    replication_time = time.time() - start_time

    # Apply partition if specified
    # Partition happens AFTER replication completes, then nodes detect failures and re-replicate
    partition_links = []
    partition_created = False
    if args.partition_links:
        link_pairs = args.partition_links.split(",")
        partition_links = []
        for pair in link_pairs:
            parts = pair.strip().split(":")
            if len(parts) == 2:
                partition_links.append((parts[0].strip(), parts[1].strip()))
        if partition_links and replicated:
            info(f"=== NETWORK PARTITION ===\n")
            info(f"Partition links: {partition_links}\n")
            if args.partition_after > 0:
                info(f"Waiting {args.partition_after}s before partition...\n")
                time.sleep(args.partition_after)
            info("Creating partition...\n")
            global partition_links_list
            partition_links_list = partition_links
            create_partition(partition_links)
            partition_created = True
            info("Partition created. Monitoring behavior...\n")
            # Give nodes time to detect partition and react
            info(f"Waiting {args.partition_timeout}s for heartbeat detection and re-replication...\n")
            time.sleep(args.partition_timeout)

            # Rebuild command timeline after partition to get post-partition replication counts
            commands_after_partition = build_replication_timeline(results_dir)
            expected_commands = args.command_count * len(producer_nodes)
            commands_at_2rf = sum(
                1
                for cmd in commands_after_partition.values()
                if cmd["final_replication"] == 2 * args.replication_factor
            )
            commands_under_2rf = sum(
                1
                for cmd in commands_after_partition.values()
                if cmd["final_replication"] < 2 * args.replication_factor
            )
            info(f"Post-partition: {commands_at_2rf}/{expected_commands} commands at 2*RF={2*args.replication_factor}\n")
            if commands_under_2rf > 0:
                info(f"  WARNING: {commands_under_2rf} commands not at 2*RF\n")
                # For partition test, require 2*RF for success
                replicated = False

    failure_metadata = {"failure_enabled": False}

    if args.failure_count > 0 and replicated:
        info(f"=== FAILURE SIMULATION ===\n")
        info(f"Failure count: {args.failure_count}\n")

        commands_before_failure = build_replication_timeline(results_dir)

        node_claim_counts = {}
        claimed = {}

        for log_file in Path(results_dir).glob("events-*.jsonl"):
            node_name = log_file.stem.replace("events-", "")
            try:
                with open(log_file) as f:
                    for line in f:
                        try:
                            event = json.loads(line.strip())
                            if event.get("event") == "job_claimed":
                                target = event.get("target", "")
                                if target:
                                    if target not in claimed:
                                        claimed[target] = set()
                                    claimed[target].add(node_name)
                        except json.JSONDecodeError:
                            pass
            except FileNotFoundError:
                pass

        for target, nodes in claimed.items():
            for node in nodes:
                node_claim_counts[node] = node_claim_counts.get(node, 0) + 1

        info(f"Node claim counts: {node_claim_counts}\n")

        nodes_to_kill = []
        if args.failure_nodes:
            nodes_to_kill = [n.strip() for n in args.failure_nodes.split(",")]
            info(f"Specified nodes to kill: {nodes_to_kill}\n")
        else:
            sorted_nodes = sorted(
                node_claim_counts.items(), key=lambda x: x[1], reverse=True
            )
            nodes_to_kill = [n[0] for n in sorted_nodes[: args.failure_count]]
            info(f"Auto-selected nodes to kill (by claim count): {nodes_to_kill}\n")

        affected_commands = {}
        for target, nodes in claimed.items():
            for node in nodes_to_kill:
                if node in nodes:
                    affected_commands[target] = commands_before_failure.get(target, {})
                    break

        info(f"Commands affected by failure: {len(affected_commands)}\n")

        pre_failure_data = {
            "commands_at_rf": sum(
                1
                for c in commands_before_failure.values()
                if c["final_replication"] == args.replication_factor
            ),
            "commands_under": sum(
                1
                for c in commands_before_failure.values()
                if c["final_replication"] < args.replication_factor
            ),
            "affected_commands": list(affected_commands.keys()),
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }

        if args.failure_wait > 0:
            info(f"Waiting {args.failure_wait}s before killing nodes...\n")
            time.sleep(args.failure_wait)

        info(f"Killing repos on nodes: {nodes_to_kill}\n")
        failure_timestamp = datetime.utcnow().isoformat() + "Z"
        for node_name in nodes_to_kill:
            for host in repo_hosts:
                if host.name == node_name:
                    host.cmd('pkill -9 -f "repo --event-log" 2>/dev/null || true')
                    info(f"  Killed repo on {node_name}\n")
                    break

        failure_time = time.time()
        info(f"  Failure timestamp: {failure_timestamp}\n")

        info(f"Waiting for recovery (timeout={args.failure_recovery_timeout}s)...\n")
        recovery_deadline = time.time() + args.failure_recovery_timeout
        recovered = False
        recovery_time_ms = 0

        while time.time() < recovery_deadline:
            commands_after = build_replication_timeline(
                results_dir, after_timestamp=failure_timestamp
            )

            all_recovered = True
            for target in affected_commands:
                if target in commands_after:
                    if (
                        commands_after[target]["final_replication"]
                        < args.replication_factor
                    ):
                        all_recovered = False
                        break
                else:
                    all_recovered = False
                    break

            if all_recovered and len(affected_commands) > 0:
                recovery_time_ms = (time.time() - failure_time) * 1000
                recovered = True
                info(f"Recovery achieved in {recovery_time_ms:.2f}ms\n")
                break

            time.sleep(0.5)

        # Calculate detection and recovery metrics
        detection_timestamp = find_failure_detection_timestamp(
            results_dir, nodes_to_kill
        )
        timing_metrics = calculate_failure_metrics(
            failure_timestamp, detection_timestamp, recovery_time_ms
        )

        commands_after_failure = build_replication_timeline(
            results_dir, after_timestamp=failure_timestamp
        )

        post_failure_commands_at_rf = sum(
            1
            for c in commands_after_failure.values()
            if c["final_replication"] == args.replication_factor
        )
        post_failure_commands_under = sum(
            1
            for c in commands_after_failure.values()
            if c["final_replication"] < args.replication_factor
        )

        commands_recovered = 0
        commands_lost = 0
        for target in affected_commands:
            if target in commands_after_failure:
                if (
                    commands_after_failure[target]["final_replication"]
                    >= args.replication_factor
                ):
                    commands_recovered += 1
                else:
                    commands_lost += 1
            else:
                commands_lost += 1

        recovery_data = {
            "achieved": recovered,
            "recovery_time_ms": recovery_time_ms,
            "detection_time_ms": timing_metrics.get("detection_time_ms"),
            "recovery_time_after_detection_ms": timing_metrics.get(
                "recovery_time_after_detection_ms"
            ),
            "detection_timestamp": detection_timestamp,
            "commands_recovered": commands_recovered,
            "commands_lost": commands_lost,
            "timeout": args.failure_recovery_timeout,
        }

        post_failure_data = {
            "commands_at_rf": post_failure_commands_at_rf,
            "commands_under": post_failure_commands_under,
            "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }

        failure_metadata = {
            "failure_enabled": True,
            "failure_count": args.failure_count,
            "failure_nodes": nodes_to_kill,
            "failure_timestamp": failure_timestamp,
            "failure_wait_seconds": args.failure_wait,
            "pre_failure": pre_failure_data,
            "recovery": recovery_data,
            "post_failure": post_failure_data,
        }

        info(f"=== FAILURE RESULTS ===\n")
        info(f"Recovery achieved: {recovered}\n")
        info(f"Failure timestamp: {failure_timestamp}\n")
        info(f"Detection timestamp: {detection_timestamp}\n")
        detection_time = timing_metrics.get("detection_time_ms")
        info(
            f"Detection time: {detection_time:.2f}ms\n"
            if detection_time
            else "Detection time: N/A\n"
        )
        info(f"Recovery time: {recovery_time_ms:.2f}ms\n")
        recovery_after_det = timing_metrics.get("recovery_time_after_detection_ms")
        info(
            f"Recovery time after detection: {recovery_after_det:.2f}ms\n"
            if recovery_after_det
            else "Recovery time after detection: N/A\n"
        )
        info(f"Commands recovered: {commands_recovered}\n")
        info(f"Commands lost: {commands_lost}\n")

        commands = commands_after_failure

    info("Waiting for event logs to flush (2s)...\n")
    time.sleep(2)

    sync_interests, data_packets = count_packet_stats(results_dir)

    commands = build_replication_timeline(results_dir)
    command_count = build_command_timelines(results_dir)
    info(f"Built {command_count} command timelines\n")

    commands_at_rf = 0
    commands_over = 0
    commands_under = 0
    any_over_replicated = False
    all_final_at_rf = True

    for target, cmd_data in commands.items():
        final_rep = cmd_data["final_replication"]
        max_rep = cmd_data["max_replication"]
        cmd_data["was_ever_over_replicated"] = max_rep > args.replication_factor

        if final_rep == args.replication_factor:
            commands_at_rf += 1
        elif final_rep > args.replication_factor:
            commands_over += 1
        else:
            commands_under += 1

        if cmd_data["was_ever_over_replicated"]:
            any_over_replicated = True
        if final_rep < args.replication_factor:
            all_final_at_rf = False

    total_commands = len(commands)
    success = replicated and commands_under == 0

    rep_stats = calculate_replication_time(results_dir, args.replication_factor)
    actual_nodes = [host.name for host in repo_hosts]
    prop_stats = calculate_update_propagation_time(results_dir, len(actual_nodes))
    topology_source = str(topo_path) if topo_path else "generated"
    producer_node_names = [h.name for h in producer_nodes]

    total_duration = time.time() - experiment_start_time

    metadata = {
        "nodes": actual_nodes,
        "node_count": args.node_count,
        "repo_count": repo_count,
        "topology_source": topology_source,
        "replication_factor": args.replication_factor,
        "producer_count": len(producer_nodes),
        "producer_nodes": producer_node_names,
        "command_count": args.command_count,
        "command_rate": args.command_rate,
        "command_type": args.command_type,
        "join_ratio": args.join_ratio,
        "no_release": args.no_release,
        "max_join_growth_rate": args.max_join_growth_rate,
        "timeout": args.timeout,
        "replicated": success,
        "replication_time_min_ms": rep_stats["min"] * 1000 if rep_stats else None,
        "replication_time_max_ms": rep_stats["max"] * 1000 if rep_stats else None,
        "replication_time_avg_ms": rep_stats["avg"] * 1000 if rep_stats else None,
        "replication_time_median_ms": rep_stats["median"] * 1000 if rep_stats else None,
        "replication_time_p95_ms": rep_stats["p95"] * 1000
        if rep_stats and rep_stats.get("p95")
        else None,
        "replication_time_p99_ms": rep_stats["p99"] * 1000
        if rep_stats and rep_stats.get("p99")
        else None,
        "update_propagation_min_ms": prop_stats["min"] * 1000 if prop_stats else None,
        "update_propagation_max_ms": prop_stats["max"] * 1000 if prop_stats else None,
        "update_propagation_avg_ms": prop_stats["avg"] * 1000 if prop_stats else None,
        "update_propagation_median_ms": prop_stats["median"] * 1000
        if prop_stats
        else None,
        "update_propagation_p95_ms": prop_stats["p95"] * 1000
        if prop_stats and prop_stats.get("p95")
        else None,
        "update_propagation_p99_ms": prop_stats["p99"] * 1000
        if prop_stats and prop_stats.get("p99")
        else None,
        "sync_interests": sync_interests,
        "data_packets": data_packets,
        "total_commands": total_commands,
        "commands_at_rf": commands_at_rf,
        "commands_over": commands_over,
        "commands_under": commands_under,
        "any_ever_over_replicated": any_over_replicated,
        "total_duration_seconds": total_duration,
        "replication_wait_duration_seconds": replication_time,
        "failure_enabled": failure_metadata.get("failure_enabled", False),
        "failure_count": failure_metadata.get("failure_count", 0),
        "failure_nodes": failure_metadata.get("failure_nodes", []),
        "failure_wait_seconds": failure_metadata.get("failure_wait_seconds", 0),
        "pre_failure_commands_at_rf": failure_metadata.get("pre_failure", {}).get(
            "commands_at_rf", 0
        ),
        "pre_failure_commands_under": failure_metadata.get("pre_failure", {}).get(
            "commands_under", 0
        ),
        "pre_failure_affected_commands": failure_metadata.get("pre_failure", {}).get(
            "affected_commands", []
        ),
        "recovery_achieved": failure_metadata.get("recovery", {}).get(
            "achieved", False
        ),
        "recovery_time_ms": failure_metadata.get("recovery", {}).get(
            "recovery_time_ms", 0
        ),
        "recovery_commands_recovered": failure_metadata.get("recovery", {}).get(
            "commands_recovered", 0
        ),
        "recovery_commands_lost": failure_metadata.get("recovery", {}).get(
            "commands_lost", 0
        ),
        "post_failure_commands_at_rf": failure_metadata.get("post_failure", {}).get(
            "commands_at_rf", 0
        ),
        "post_failure_commands_under": failure_metadata.get("post_failure", {}).get(
            "commands_under", 0
        ),
        "commands": commands,
    }
    metadata_path = results_dir / "metadata.json"
    metadata_path.write_text(json.dumps(metadata, indent=2))
    info(f"Metadata written to {metadata_path}\n")

    info("=== RESULTS ===\n")
    info(f"Success: {success}\n")
    info(
        f"Commands: {total_commands} total, {commands_at_rf} at rf={args.replication_factor}, {commands_over} over, {commands_under} under\n"
    )
    if rep_stats:
        info(
            f"Replication time: max={rep_stats['max'] * 1000:.2f}ms, avg={rep_stats['avg'] * 1000:.2f}ms, median={rep_stats['median'] * 1000:.2f}ms, p95={rep_stats['p95'] * 1000:.2f}ms, p99={rep_stats['p99'] * 1000:.2f}ms\n"
        )
    else:
        info("Replication time: N/A\n")
    if prop_stats:
        info(
            f"Update propagation: max={prop_stats['max'] * 1000:.2f}ms, avg={prop_stats['avg'] * 1000:.2f}ms, median={prop_stats['median'] * 1000:.2f}ms, p95={prop_stats['p95'] * 1000:.2f}ms, p99={prop_stats['p99'] * 1000:.2f}ms\n"
        )
    else:
        info("Update propagation: N/A\n")
    info(f"Sync interests: {sync_interests}\n")
    info(f"Data packets: {data_packets}\n")
    info(f"Any ever over-replicated: {any_over_replicated}\n")
    info(f"Total experiment duration: {total_duration:.2f}s\n")

    cleanup()

    if success:
        info("TEST PASSED\n")
        sys.exit(0)
    else:
        info("TEST FAILED: Replication not achieved\n")
        sys.exit(2)


def create_topology(nodes):
    """Generate topology file for subset of nodes with mesh connectivity"""
    lines = ["[nodes]"]
    for node in nodes:
        lines.append(f"{node}: _")
    lines.append("[switches]")
    lines.append("[links]")

    for i in range(len(nodes)):
        for j in range(i + 1, len(nodes)):
            lines.append(f"{nodes[i]}:{nodes[j]} delay=10ms")

    return "\n".join(lines) + "\n"


def count_unique_peer_updates(log_file, my_prefix=None):
    """Count unique peers from which we've received node updates"""
    peers = set()
    try:
        with open(log_file) as f:
            for line in f:
                try:
                    event = json.loads(line.strip())
                    if event.get("event") == "node_update":
                        from_node = event.get("from", "")
                        if from_node and from_node != my_prefix:
                            peers.add(from_node)
                except json.JSONDecodeError:
                    pass
    except FileNotFoundError:
        pass
    return len(peers)


def wait_for_svs_health(results_dir, node_names, timeout=15):
    """Wait until all nodes have received updates from all peers.
    Returns True if healthy, False if timed out.
    """
    expected_peers = len(node_names) - 1
    if expected_peers == 0:
        return True

    deadline = time.time() + timeout
    node_list = list(node_names)

    info(
        f"Waiting for SVS health (expecting {expected_peers} peers per node, timeout={timeout}s)...\n"
    )

    while time.time() < deadline:
        all_healthy = True
        healthy_count = 0

        for node in node_list:
            log_file = results_dir / f"events-{node}.jsonl"
            peer_count = count_unique_peer_updates(log_file)
            if peer_count >= expected_peers:
                healthy_count += 1
            else:
                all_healthy = False

        if all_healthy:
            info(
                f"  SVS healthy: all {len(node_list)} nodes see {expected_peers} peers\n"
            )
            return True

        elapsed = time.time() - deadline + timeout
        info(
            f"  SVS progress: {healthy_count}/{len(node_list)} nodes healthy ({elapsed:.1f}s elapsed)\n"
        )
        time.sleep(0.5)

    info(f"  SVS health check timed out after {timeout}s\n")
    return False


def count_job_claims(results_dir):
    """Count total job claims across all nodes"""
    claim_count = 0
    for log_file in Path(results_dir).glob("events-*.jsonl"):
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get("event") == "job_claimed":
                            claim_count += 1
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass
    return claim_count


def count_packet_stats(results_dir):
    """Get total sync interests and data packets across all nodes (sum, not max)"""
    total_sync = 0
    total_data = 0

    for log_file in Path(results_dir).glob("events-*.jsonl"):
        try:
            with open(log_file) as f:
                node_sync = 0
                node_data = 0
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get("event") == "sync_interest_sent":
                            node_sync = max(node_sync, event.get("total", 0))
                        elif event.get("event") == "data_sent":
                            node_data = max(node_data, event.get("total", 0))
                    except json.JSONDecodeError:
                        pass
                total_sync += node_sync
                total_data += node_data
        except FileNotFoundError:
            pass

    return total_sync, total_data


def build_replication_timeline(results_dir, after_timestamp=None):
    """Build timeline of replication state from all event logs.
    Args:
        results_dir: directory containing event logs
        after_timestamp: optional ISO8601 timestamp string - only include events after this time
    Returns: dict mapping target -> {final_replication, max_replication, was_ever_over_replicated, timeline}
    """
    all_events = []

    for log_file in Path(results_dir).glob("events-*.jsonl"):
        node_name = log_file.stem.replace("events-", "")
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        # Filter by timestamp if specified
                        if after_timestamp and event.get("ts", "") < after_timestamp:
                            continue
                        event["_node"] = node_name
                        all_events.append(event)
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass

    all_events.sort(key=lambda e: e.get("ts", ""))

    claimed = {}
    commands = {}

    for event in all_events:
        if event.get("event") == "job_claimed":
            node = event.get("_node", "")
            target = event.get("target", "")
            if target and node not in claimed.get(target, set()):
                if target not in claimed:
                    claimed[target] = set()
                    commands[target] = {"timeline": []}
                claimed[target].add(node)
                count = len(claimed[target])
                commands[target]["timeline"].append(
                    {
                        "ts": event.get("ts"),
                        "action": "claim",
                        "node": node,
                        "target": target,
                        "count": count,
                    }
                )
        elif event.get("event") == "job_released":
            node = event.get("_node", "")
            target = event.get("target", "")
            if target in claimed and node in claimed[target]:
                claimed[target].discard(node)
                count = len(claimed[target])
                commands[target]["timeline"].append(
                    {
                        "ts": event.get("ts"),
                        "action": "release",
                        "node": node,
                        "target": target,
                        "count": count,
                    }
                )

    for target, nodes in claimed.items():
        final_rep = len(nodes)
        max_rep = 0
        for entry in commands[target]["timeline"]:
            max_rep = max(max_rep, entry["count"])
        commands[target]["final_replication"] = final_rep
        commands[target]["max_replication"] = max_rep
        commands[target]["was_ever_over_replicated"] = None

    return commands


def find_failure_detection_timestamp(results_dir, killed_nodes):
    """Find the first node_detected_dead event timestamp for any killed node.
    Returns: ISO8601 timestamp string or None if not found.
    """
    if not killed_nodes:
        return None

    killed_set = set(killed_nodes)
    earliest_ts = None

    for log_file in Path(results_dir).glob("events-*.jsonl"):
        with open(log_file) as f:
            for line in f:
                try:
                    event = json.loads(line.strip())
                    if event.get("event") == "node_detected_dead":
                        node_name = event.get("name", "")
                        # The node name in event is like /ndn/repo/neu, extract just 'neu'
                        if node_name.startswith("/ndn/repo/"):
                            node_name = node_name.replace("/ndn/repo/", "")
                        if node_name in killed_set:
                            ts = event.get("ts")
                            if ts and (earliest_ts is None or ts < earliest_ts):
                                earliest_ts = ts
                except json.JSONDecodeError:
                    pass

    return earliest_ts


def calculate_failure_metrics(failure_timestamp, detection_timestamp, recovery_time_ms):
    """Calculate detection and recovery metrics.

    Args:
        failure_timestamp: ISO8601 timestamp when node was killed
        detection_timestamp: ISO8601 timestamp when failure was detected (node_detected_dead event)
        recovery_time_ms: total time from failure to recovery in milliseconds

    Returns:
        dict with detection_time_ms, recovery_time_after_detection_ms
    """
    from datetime import datetime

    if not failure_timestamp or not detection_timestamp:
        return {
            "detection_time_ms": None,
            "recovery_time_after_detection_ms": None,
        }

    def parse_ts(ts):
        if not ts:
            return None
        ts = ts.replace("Z", "+00:00")
        # Truncate nanoseconds to microseconds before parsing
        # Python's fromisoformat only supports 6 decimal places
        if "." in ts:
            # Handle timestamp with timezone
            if "+" in ts:
                main_tz = ts.rsplit("+", 1)
                main = main_tz[0]
                tz = "+" + main_tz[1]
            elif "-" in ts[10:]:  # Date separator is -, timezone separator is also -
                # Find the last - that separates date from time+tz
                idx = ts.rfind("-", 10)  # Start after YYYY-MM-DD
                main = ts[:idx]
                tz = ts[idx:]
            else:
                main = ts
                tz = ""
            if "." in main:
                main, frac = main.rsplit(".", 1)
                frac = frac[:6]  # Truncate to microseconds
                ts = main + "." + frac + tz
        return datetime.fromisoformat(ts)

    failure_dt = parse_ts(failure_timestamp)
    detection_dt = parse_ts(detection_timestamp)

    if not failure_dt or not detection_dt:
        return {
            "detection_time_ms": None,
            "recovery_time_after_detection_ms": None,
        }

    # Detection time: failure -> detected
    detection_time_ms = (detection_dt - failure_dt).total_seconds() * 1000

    # Recovery time after detection: detected -> recovered
    recovery_time_after_detection_ms = recovery_time_ms - detection_time_ms

    return {
        "detection_time_ms": detection_time_ms,
        "recovery_time_after_detection_ms": max(0, recovery_time_after_detection_ms),
    }


def calculate_replication_time(results_dir, replication_factor):
    """Calculate replication time: from first command received to when it reaches rf claims.
    Returns dict with max, avg, median times in seconds, or None if no data.
    """
    from datetime import datetime
    import statistics

    def parse_ts(ts):
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
        return datetime.fromisoformat(ts)

    all_events = []

    for log_file in Path(results_dir).glob("events-*.jsonl"):
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
        except FileNotFoundError:
            pass

    all_events.sort(key=lambda e: e.get("ts", ""))

    command_times = {}
    claim_counts = {}

    for event in all_events:
        ts = event.get("ts")
        if event.get("event") == "command_received":
            target = event.get("target", "")
            if target and target not in command_times:
                command_times[target] = {"start": ts, "rf_reached": None}
                claim_counts[target] = set()

        elif event.get("event") == "job_claimed":
            target = event.get("target", "")
            node = event.get("_node_name", "")
            if target in claim_counts and node not in claim_counts[target]:
                claim_counts[target].add(node)
                count = len(claim_counts[target])
                if (
                    count == replication_factor
                    and command_times[target]["rf_reached"] is None
                ):
                    command_times[target]["rf_reached"] = ts

    rep_times = []
    for target, times in command_times.items():
        if times["rf_reached"]:
            try:
                start = parse_ts(times["start"])
                end = parse_ts(times["rf_reached"])
                if start and end:
                    rep_times.append((end - start).total_seconds())
            except:
                pass

    if not rep_times:
        return None

    sorted_times = sorted(rep_times)
    n = len(sorted_times)

    def percentile(p):
        if n == 0:
            return None
        idx = int(n * p / 100)
        if idx >= n:
            idx = n - 1
        return sorted_times[idx]

    return {
        "min": min(rep_times),
        "max": max(rep_times),
        "avg": statistics.mean(rep_times),
        "median": statistics.median(rep_times),
        "p95": percentile(95) if n >= 20 else sorted_times[-1],
        "p99": percentile(99) if n >= 100 else sorted_times[-1],
    }


def calculate_update_propagation_time(results_dir, total_nodes):
    """Calculate update propagation time: from when a node claims a job to when all other
    nodes receive an update containing that job from the claiming node.
    Returns the maximum propagation time across all claims.
    """
    from datetime import datetime

    def parse_ts(ts):
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
        return datetime.fromisoformat(ts)

    all_events = []
    all_node_names = set()

    for log_file in Path(results_dir).glob("events-*.jsonl"):
        node_name = log_file.stem.replace("events-", "")
        all_node_names.add(node_name)
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        event["_node_name"] = node_name
                        all_events.append(event)
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass

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

    prop_times = []
    for target, claims in claim_times.items():
        for claiming_node, claim_ts in claims.items():
            key = (target, claiming_node)
            if key in update_received:
                received = update_received[key]
                if len(received) >= total_nodes - 1:
                    latest = None
                    for node, recv_ts in received.items():
                        if node != claiming_node:
                            try:
                                recv_time = parse_ts(recv_ts)
                                if latest is None or recv_time > latest:
                                    latest = recv_time
                            except:
                                pass
                    if latest:
                        try:
                            claim_time = parse_ts(claim_ts)
                            if claim_time:
                                prop_times.append((latest - claim_time).total_seconds())
                        except:
                            pass

    if not prop_times:
        return None

    import statistics

    sorted_times = sorted(prop_times)
    n = len(sorted_times)

    def percentile(p):
        if n == 0:
            return None
        idx = int(n * p / 100)
        if idx >= n:
            idx = n - 1
        return sorted_times[idx]

    return {
        "min": min(prop_times),
        "max": max(prop_times),
        "avg": statistics.mean(prop_times),
        "median": statistics.median(prop_times),
        "p95": percentile(95) if n >= 20 else sorted_times[-1],
        "p99": percentile(99) if n >= 100 else sorted_times[-1],
    }


def build_command_timelines(results_dir):
    """Extract all events grouped by command target, sorted by timestamp.
    Outputs JSON files to results/command_timelines/{sanitized_target}.json
    """
    import re

    all_events = []

    for log_file in Path(results_dir).glob("events-*.jsonl"):
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
        except FileNotFoundError:
            pass

    all_events.sort(key=lambda e: e.get("ts", ""))

    command_events = {}

    for event in all_events:
        cmd_id = event.get("cmdId") or event.get("target", "")
        if not cmd_id:
            continue

        if cmd_id not in command_events:
            command_events[cmd_id] = []
        command_events[cmd_id].append(event)

    timelines_dir = Path(results_dir) / "command_timelines"
    timelines_dir.mkdir(exist_ok=True)

    def sanitize_filename(name):
        return re.sub(r"[^a-zA-Z0-9]", "_", name)

    for cmd_id, events in command_events.items():
        timeline = {"commandId": cmd_id, "eventCount": len(events), "events": events}

        filename = sanitize_filename(cmd_id) + ".json"
        output_path = timelines_dir / filename
        output_path.write_text(json.dumps(timeline, indent=2))

    return len(command_events)


def get_command_timestamp(results_dir):
    """Get the timestamp of the first command received"""
    for log_file in Path(results_dir).glob("events-*.jsonl"):
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get("event") == "command_received":
                            return event.get("ts")
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass
    return None


def get_last_claim_timestamp(results_dir):
    """Get the timestamp of the last job_claimed event (for replication time calculation)"""
    last_ts = None
    for log_file in Path(results_dir).glob("events-*.jsonl"):
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get("event") == "job_claimed":
                            ts = event.get("ts")
                            if ts:
                                if last_ts is None or ts > last_ts:
                                    last_ts = ts
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass
    return last_ts


if __name__ == "__main__":
    main()
