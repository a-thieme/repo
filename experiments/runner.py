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
import time
from pathlib import Path

from mininet.log import setLogLevel, info
from minindn.minindn import Minindn
from minindn.util import MiniNDNCLI
from minindn.apps.app_manager import AppManager
from minindn.apps.nfd import Nfd
from minindn.helpers.ndn_routing_helper import NdnRoutingHelper

ALL_NODES = [
    "UCLA", "NEU", "SAVI", "OSAKA", "AFA", "ANYANG", "TNO", "MEMPHIS",
    "QUB", "URJC", "WASEDA", "UFBA", "AVEIRO", "MML2", "MML1", "ARIZONA",
    "IIITH", "SINGAPORE", "FRANKFURT", "SRRU", "DELFT", "WU", "BERN", "MINHO"
]

ndn = None

def cleanup():
    global ndn
    info('\nCleaning up...\n')
    if ndn:
        try:
            ndn.stop()
        except:
            pass
    Minindn.cleanUp()

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
            if line == '[nodes]':
                in_nodes = True
                continue
            if line.startswith('[') and in_nodes:
                break
            if in_nodes and ':' in line:
                node_name = line.split(':')[0].strip()
                if node_name:
                    nodes.append(node_name)
    return nodes

def main():
    global ndn

    parser = argparse.ArgumentParser()
    parser.add_argument('--node-count', type=int, default=5)
    parser.add_argument('--timeout', type=int, default=60)
    parser.add_argument('--results-dir', default='/results')
    parser.add_argument('--replication-factor', type=int, default=3)
    parser.add_argument('--routing-wait', type=int, default=60)
    parser.add_argument('--producer-count', type=int, default=1)
    parser.add_argument('--command-count', type=int, default=1)
    parser.add_argument('--command-rate', type=int, default=1)
    parser.add_argument('--debug', action='store_true', help='Enable verbose repo logging')
    parser.add_argument('--repo-bin', default='/usr/local/bin/repo', help='Path to repo binary')
    parser.add_argument('--producer-bin', default='/usr/local/bin/producer', help='Path to producer binary')
    parser.add_argument('--svs-timeout', type=int, default=7, help='SVS health check timeout (seconds)')
    parser.add_argument('--producer-timeout', type=int, default=5, help='Producer command timeout (seconds)')
    parser.add_argument('--replication-timeout', type=int, default=30, help='Replication wait timeout (seconds)')
    parser.add_argument('--nfd-wait', type=int, default=3, help='NFD initialization wait (seconds)')
    parser.add_argument('--topology', default='', help='Path to topology file (overrides default)')
    parser.add_argument('--repo-count', type=int, default=0, help='Number of nodes to run repos (0=all nodes)')
    parser.add_argument('--producer-nodes', default='', help='Comma-separated list of node names to run producers')
    parser.add_argument('--command-type', default='insert', help='Command type: insert, join, or both')
    parser.add_argument('--join-ratio', type=float, default=0.5, help='Ratio of JOIN commands when type is both (0.0-1.0)')
    parser.add_argument('--no-release', action='store_true', help='Disable automatic job release when storage exceeds 75%')
    parser.add_argument('--max-join-growth-rate', type=int, default=10485760, help='Maximum JOIN storage growth per second in bytes')
    args = parser.parse_args()

    sys.argv = [sys.argv[0]]

    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    setLogLevel('info')
    
    results_dir = Path(args.results_dir)
    results_dir.mkdir(parents=True, exist_ok=True)

    testbed_default = '/usr/local/share/testbed_topology.conf'
    topo_path = None
    nodes = None

    if args.topology:
        topo_path = Path(args.topology)
        if not topo_path.exists():
            info(f'ERROR: Topology file not found: {topo_path}\n')
            sys.exit(1)
        nodes = parse_topology_nodes(topo_path)
        info(f'Using user-specified topology: {topo_path} ({len(nodes)} nodes)\n')
        if args.node_count != len(nodes):
            info(f'ERROR: --node-count ({args.node_count}) does not match topology node count ({len(nodes)})\n')
            sys.exit(1)
    elif Path(testbed_default).exists():
        testbed_nodes = parse_topology_nodes(Path(testbed_default))
        if args.node_count == len(testbed_nodes):
            topo_path = Path(testbed_default)
            nodes = testbed_nodes
            info(f'Using default testbed topology: {topo_path} ({len(nodes)} nodes)\n')
        else:
            nodes = ALL_NODES[:args.node_count]
            topology_content = create_topology(nodes)
            topo_path = results_dir / 'topology.conf'
            topo_path.write_text(topology_content)
            info(f'Using generated full-mesh topology for {len(nodes)} nodes (testbed has {len(testbed_nodes)} nodes)\n')
    else:
        nodes = ALL_NODES[:args.node_count]
        topology_content = create_topology(nodes)
        topo_path = results_dir / 'topology.conf'
        topo_path.write_text(topology_content)
        info(f'Using generated full-mesh topology for {len(nodes)} nodes\n')

    info(f'=== Mini-NDN Integration Runner ===\n')
    info(f'Nodes: {nodes}\n')
    info(f'Replication factor: {args.replication_factor}\n')
    info(f'Producers: {args.producer_count}\n')
    info(f'Commands per producer: {args.command_count}\n')
    info(f'Command rate: {args.command_rate} cmds/sec\n')
    info(f'Timeout: {args.timeout}s\n')
    
    min_producer_time = (args.command_count / args.command_rate) + 10
    if args.producer_timeout < min_producer_time:
        args.producer_timeout = int(min_producer_time) + 5
        info(f'Auto-adjusted producer timeout to {args.producer_timeout}s for {args.command_count} commands at {args.command_rate} cmds/sec\n')

    Minindn.cleanUp()
    Minindn.verifyDependencies()
    
    sys.argv = [sys.argv[0], str(topo_path)]
    ndn = Minindn()
    ndn.start()

    info('Starting NFD on nodes...\n')
    nfds = AppManager(ndn, ndn.net.hosts, Nfd)
    
    info(f'Waiting for NFD to initialize ({args.nfd_wait}s)...\n')
    time.sleep(args.nfd_wait)

    info('Setting up routes using NdnRoutingHelper...\n')
    grh = NdnRoutingHelper(ndn.net, "udp", "link-state")
    
    for host in ndn.net.hosts:
        node_prefix = f'/ndn/repo/{host.name}'
        sync_data_prefix = f'/ndn/drepo/group-messages{node_prefix}'
        grh.addOrigin([host], ["/ndn/drepo/group-messages/32=svs", node_prefix, sync_data_prefix])
        info(f'  Added origin for {host.name}: /ndn/drepo/group-messages/32=svs, {node_prefix}, {sync_data_prefix}\n')
    
    info('Calculating and installing routes...\n')
    grh.calculateNPossibleRoutes()
    
    info('Setting multicast strategy for SVS sync interests only...\n')
    for host in ndn.net.hosts:
        host.cmd('nfdc strategy set /ndn/drepo/group-messages/32=svs /localhost/nfd/strategy/multicast 2>&1')
        host.cmd('nfdc strategy set /ndn/drepo/notify /localhost/nfd/strategy/best-route 2>&1')

    info('Verifying multicast strategy is active on all nodes...\n')
    strategy_timeout = 10
    for host in ndn.net.hosts:
        for prefix in ['/ndn/drepo/group-messages/32=svs']:
            deadline = time.time() + strategy_timeout
            result = ''
            while time.time() < deadline:
                result = host.cmd(f'nfdc strategy show {prefix} 2>&1')
                if '/localhost/nfd/strategy/multicast' in result:
                    break
                host.cmd(f'nfdc strategy set {prefix} /localhost/nfd/strategy/multicast 2>&1')
                time.sleep(0.1)
            else:
                info(f'  ERROR: Failed to set multicast strategy on {host.name} for {prefix} after {strategy_timeout}s\n')
                info(f'    Last output: {result}\n')
    info('Strategy verification complete.\n')

    info('Verifying FIB entries...\n')
    for host in ndn.net.hosts:
        fib = host.cmd('nfdc fib list 2>&1 | head -20')
        info(f'  {host.name} FIB (first 20 lines):\n{fib}\n')

    repo_count = args.repo_count if args.repo_count > 0 else len(ndn.net.hosts)
    repo_hosts = ndn.net.hosts[:repo_count]

    info(f'Starting repos on {repo_count} of {len(ndn.net.hosts)} nodes...\n')
    os.environ['SHELL'] = '/bin/bash'
    for host in repo_hosts:
        event_log = results_dir / f'events-{host.name}.jsonl'
        stdout_log = results_dir / f'stdout-{host.name}.log'
        node_prefix = f'/ndn/repo/{host.name}'
        signing_identity = '/ndn/repo.teame.dev/repo'
        debug_flag = ' --debug' if args.debug else ''
        no_release_flag = ' --no-release' if args.no_release else ''
        storage_flags = f'{no_release_flag} --max-join-growth-rate {args.max_join_growth_rate}'
        cmd = f'{args.repo_bin} --event-log {event_log} --node-prefix {node_prefix} --signing-identity {signing_identity}{debug_flag}{storage_flags} > {stdout_log} 2>&1 &'
        host.cmd(f'mkdir -p /tmp/{host.name}')
        host.cmd(cmd)
        info(f'  Started repo on {host.name} with prefix {node_prefix}\n')

    repo_node_names = [host.name for host in repo_hosts]
    svs_healthy = wait_for_svs_health(results_dir, repo_node_names, timeout=args.svs_timeout)
    
    info('Checking for node updates in event logs...\n')
    for host in repo_hosts:
        event_log = results_dir / f'events-{host.name}.jsonl'
        if event_log.exists():
            with open(event_log) as f:
                lines = f.readlines()
                node_updates = [l for l in lines if 'node_update' in l]
                sync_interests = [l for l in lines if 'sync_interest_sent' in l]
                data_sent = [l for l in lines if 'data_sent' in l]
                rep_checks = [l for l in lines if 'replication_check' in l]
                info(f'  {host.name}: updates_received={len(node_updates)}, sync_sent={len(sync_interests)}, data_sent={len(data_sent)}, rep_checks={len(rep_checks)}\n')

    if args.producer_nodes:
        producer_node_names = [n.strip() for n in args.producer_nodes.split(',')]
        producer_nodes = [h for h in repo_hosts if h.name in producer_node_names]
        if len(producer_nodes) != len(producer_node_names):
            found = {h.name for h in producer_nodes}
            missing = [n for n in producer_node_names if n not in found]
            info(f'ERROR: Producer nodes not found in repo hosts: {missing}\n')
            sys.exit(1)
        if len(producer_nodes) != args.producer_count:
            info(f'ERROR: --producer-nodes specifies {len(producer_nodes)} nodes but --producer-count is {args.producer_count}\n')
            sys.exit(1)
    else:
        producer_nodes = repo_hosts[:args.producer_count]

    info(f'Running {len(producer_nodes)} producer(s)...\n')
    for producer_node in producer_nodes:
        info(f'  Starting producer on {producer_node.name}...\n')
        cmd_type_flag = f' -type {args.command_type}'
        join_ratio_flag = f' -join-ratio {args.join_ratio}' if args.command_type == 'both' else ''
        result = producer_node.cmd(f'timeout {args.producer_timeout}s {args.producer_bin} -count {args.command_count} -rate {args.command_rate}{cmd_type_flag}{join_ratio_flag} 2>&1')
        info(f'  Producer {producer_node.name} output: {result}\n')

    expected_commands = len(producer_nodes) * args.command_count
    expected_claims = expected_commands * args.replication_factor
    
    info(f'Waiting for replication (timeout={args.replication_timeout}s, expecting {expected_commands} commands, {expected_claims} claims)...\n')
    start_time = time.time()
    replicated = False
    last_claim_count = 0
    
    while time.time() - start_time < args.replication_timeout:
        claim_count = count_job_claims(results_dir)
        if claim_count != last_claim_count:
            info(f'  Job claims: {claim_count}/{expected_claims}\n')
            last_claim_count = claim_count
        
        if claim_count >= expected_claims:
            replicated = True
            break
        time.sleep(1)

    replication_time = time.time() - start_time

    info('Waiting for event logs to flush (2s)...\n')
    time.sleep(2)

    sync_interests, data_packets = count_packet_stats(results_dir)
    
    commands = build_replication_timeline(results_dir)
    
    commands_at_rf = 0
    commands_over = 0
    commands_under = 0
    any_over_replicated = False
    all_final_at_rf = True
    
    for target, cmd_data in commands.items():
        final_rep = cmd_data['final_replication']
        max_rep = cmd_data['max_replication']
        cmd_data['was_ever_over_replicated'] = max_rep > args.replication_factor
        
        if final_rep == args.replication_factor:
            commands_at_rf += 1
        elif final_rep > args.replication_factor:
            commands_over += 1
        else:
            commands_under += 1
        
        if cmd_data['was_ever_over_replicated']:
            any_over_replicated = True
        if final_rep < args.replication_factor:
            all_final_at_rf = False
    
    total_commands = len(commands)
    success = replicated and commands_under == 0
    
    rep_stats = calculate_replication_time(results_dir, args.replication_factor)
    actual_nodes = [host.name for host in repo_hosts]
    prop_stats = calculate_update_propagation_time(results_dir, len(actual_nodes))
    topology_source = str(topo_path) if topo_path else 'generated'
    producer_node_names = [h.name for h in producer_nodes]
    
    metadata = {
        'nodes': actual_nodes,
        'node_count': args.node_count,
        'repo_count': repo_count,
        'topology_source': topology_source,
        'replication_factor': args.replication_factor,
        'producer_count': len(producer_nodes),
        'producer_nodes': producer_node_names,
        'command_count': args.command_count,
        'command_rate': args.command_rate,
        'command_type': args.command_type,
        'join_ratio': args.join_ratio,
        'no_release': args.no_release,
        'max_join_growth_rate': args.max_join_growth_rate,
        'timeout': args.timeout,
        'replicated': success,
        'replication_time_min_ms': rep_stats['min'] * 1000 if rep_stats else None,
        'replication_time_max_ms': rep_stats['max'] * 1000 if rep_stats else None,
        'replication_time_avg_ms': rep_stats['avg'] * 1000 if rep_stats else None,
        'replication_time_median_ms': rep_stats['median'] * 1000 if rep_stats else None,
        'update_propagation_min_ms': prop_stats['min'] * 1000 if prop_stats else None,
        'update_propagation_max_ms': prop_stats['max'] * 1000 if prop_stats else None,
        'update_propagation_avg_ms': prop_stats['avg'] * 1000 if prop_stats else None,
        'update_propagation_median_ms': prop_stats['median'] * 1000 if prop_stats else None,
        'sync_interests': sync_interests,
        'data_packets': data_packets,
        'total_commands': total_commands,
        'commands_at_rf': commands_at_rf,
        'commands_over': commands_over,
        'commands_under': commands_under,
        'any_ever_over_replicated': any_over_replicated,
        'commands': commands
    }
    metadata_path = results_dir / 'metadata.json'
    metadata_path.write_text(json.dumps(metadata, indent=2))
    info(f'Metadata written to {metadata_path}\n')

    info('=== RESULTS ===\n')
    info(f'Success: {success}\n')
    info(f'Commands: {total_commands} total, {commands_at_rf} at rf={args.replication_factor}, {commands_over} over, {commands_under} under\n')
    if rep_stats:
        info(f'Replication time: max={rep_stats["max"]*1000:.2f}ms, avg={rep_stats["avg"]*1000:.2f}ms, median={rep_stats["median"]*1000:.2f}ms\n')
    else:
        info('Replication time: N/A\n')
    if prop_stats:
        info(f'Update propagation: max={prop_stats["max"]*1000:.2f}ms, avg={prop_stats["avg"]*1000:.2f}ms, median={prop_stats["median"]*1000:.2f}ms\n')
    else:
        info('Update propagation: N/A\n')
    info(f'Sync interests: {sync_interests}\n')
    info(f'Data packets: {data_packets}\n')
    info(f'Any ever over-replicated: {any_over_replicated}\n')

    cleanup()

    if success:
        info('TEST PASSED\n')
        sys.exit(0)
    else:
        info('TEST FAILED: Replication not achieved\n')
        sys.exit(2)

def create_topology(nodes):
    """Generate topology file for subset of nodes with mesh connectivity"""
    lines = ['[nodes]']
    for node in nodes:
        lines.append(f'{node}: _')
    lines.append('[switches]')
    lines.append('[links]')
    
    for i in range(len(nodes)):
        for j in range(i + 1, len(nodes)):
            lines.append(f'{nodes[i]}:{nodes[j]} delay=10ms')
    
    return '\n'.join(lines) + '\n'

def count_unique_peer_updates(log_file, my_prefix=None):
    """Count unique peers from which we've received node updates"""
    peers = set()
    try:
        with open(log_file) as f:
            for line in f:
                try:
                    event = json.loads(line.strip())
                    if event.get('event') == 'node_update':
                        from_node = event.get('from', '')
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
    
    info(f'Waiting for SVS health (expecting {expected_peers} peers per node, timeout={timeout}s)...\n')
    
    while time.time() < deadline:
        all_healthy = True
        healthy_count = 0
        
        for node in node_list:
            log_file = results_dir / f'events-{node}.jsonl'
            peer_count = count_unique_peer_updates(log_file)
            if peer_count >= expected_peers:
                healthy_count += 1
            else:
                all_healthy = False
        
        if all_healthy:
            info(f'  SVS healthy: all {len(node_list)} nodes see {expected_peers} peers\n')
            return True
        
        elapsed = time.time() - deadline + timeout
        info(f'  SVS progress: {healthy_count}/{len(node_list)} nodes healthy ({elapsed:.1f}s elapsed)\n')
        time.sleep(0.5)
    
    info(f'  SVS health check timed out after {timeout}s\n')
    return False

def count_job_claims(results_dir):
    """Count total job claims across all nodes"""
    claim_count = 0
    for log_file in Path(results_dir).glob('events-*.jsonl'):
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get('event') == 'job_claimed':
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
    
    for log_file in Path(results_dir).glob('events-*.jsonl'):
        try:
            with open(log_file) as f:
                node_sync = 0
                node_data = 0
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get('event') == 'sync_interest_sent':
                            node_sync = max(node_sync, event.get('total', 0))
                        elif event.get('event') == 'data_sent':
                            node_data = max(node_data, event.get('total', 0))
                    except json.JSONDecodeError:
                        pass
                total_sync += node_sync
                total_data += node_data
        except FileNotFoundError:
            pass
    
    return total_sync, total_data

def build_replication_timeline(results_dir):
    """Build timeline of replication state from all event logs.
    Returns: dict mapping target -> {final_replication, max_replication, was_ever_over_replicated, timeline}
    """
    all_events = []
    
    for log_file in Path(results_dir).glob('events-*.jsonl'):
        node_name = log_file.stem.replace('events-', '')
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        event['_node'] = node_name
                        all_events.append(event)
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass
    
    all_events.sort(key=lambda e: e.get('ts', ''))
    
    claimed = {}
    commands = {}
    
    for event in all_events:
        if event.get('event') == 'job_claimed':
            node = event.get('_node', '')
            target = event.get('target', '')
            if target and node not in claimed.get(target, set()):
                if target not in claimed:
                    claimed[target] = set()
                    commands[target] = {'timeline': []}
                claimed[target].add(node)
                count = len(claimed[target])
                commands[target]['timeline'].append({
                    'ts': event.get('ts'),
                    'action': 'claim',
                    'node': node,
                    'target': target,
                    'count': count
                })
        elif event.get('event') == 'job_released':
            node = event.get('_node', '')
            target = event.get('target', '')
            if target in claimed and node in claimed[target]:
                claimed[target].discard(node)
                count = len(claimed[target])
                commands[target]['timeline'].append({
                    'ts': event.get('ts'),
                    'action': 'release',
                    'node': node,
                    'target': target,
                    'count': count
                })
    
    for target, nodes in claimed.items():
        final_rep = len(nodes)
        max_rep = 0
        for entry in commands[target]['timeline']:
            max_rep = max(max_rep, entry['count'])
        commands[target]['final_replication'] = final_rep
        commands[target]['max_replication'] = max_rep
        commands[target]['was_ever_over_replicated'] = None
    
    return commands

def calculate_replication_time(results_dir, replication_factor):
    """Calculate replication time: from first command received to when it reaches rf claims.
    Returns dict with max, avg, median times in seconds, or None if no data.
    """
    from datetime import datetime
    import statistics
    
    def parse_ts(ts):
        if not ts:
            return None
        ts = ts.replace('Z', '+00:00')
        if '.' in ts:
            parts = ts.split('.')
            frac_and_tz = parts[1]
            if '+' in frac_and_tz:
                frac, tz = frac_and_tz.split('+')
                frac = frac[:6]
                ts = f"{parts[0]}.{frac}+{tz}"
            elif '-' in frac_and_tz[1:]:
                idx = frac_and_tz.rfind('-')
                frac = frac_and_tz[:idx][:6]
                tz = frac_and_tz[idx:]
                ts = f"{parts[0]}.{frac}{tz}"
        return datetime.fromisoformat(ts)
    
    all_events = []
    
    for log_file in Path(results_dir).glob('events-*.jsonl'):
        node_name = log_file.stem.replace('events-', '')
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        event['_node_name'] = node_name
                        all_events.append(event)
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass
    
    all_events.sort(key=lambda e: e.get('ts', ''))
    
    command_times = {}
    claim_counts = {}
    
    for event in all_events:
        ts = event.get('ts')
        if event.get('event') == 'command_received':
            target = event.get('target', '')
            if target and target not in command_times:
                command_times[target] = {'start': ts, 'rf_reached': None}
                claim_counts[target] = set()
        
        elif event.get('event') == 'job_claimed':
            target = event.get('target', '')
            node = event.get('_node_name', '')
            if target in claim_counts and node not in claim_counts[target]:
                claim_counts[target].add(node)
                count = len(claim_counts[target])
                if count == replication_factor and command_times[target]['rf_reached'] is None:
                    command_times[target]['rf_reached'] = ts
    
    rep_times = []
    for target, times in command_times.items():
        if times['rf_reached']:
            try:
                start = parse_ts(times['start'])
                end = parse_ts(times['rf_reached'])
                if start and end:
                    rep_times.append((end - start).total_seconds())
            except:
                pass
    
    if not rep_times:
        return None
    
    return {
        'min': min(rep_times),
        'max': max(rep_times),
        'avg': statistics.mean(rep_times),
        'median': statistics.median(rep_times)
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
        ts = ts.replace('Z', '+00:00')
        if '.' in ts:
            parts = ts.split('.')
            frac_and_tz = parts[1]
            if '+' in frac_and_tz:
                frac, tz = frac_and_tz.split('+')
                frac = frac[:6]
                ts = f"{parts[0]}.{frac}+{tz}"
            elif '-' in frac_and_tz[1:]:
                idx = frac_and_tz.rfind('-')
                frac = frac_and_tz[:idx][:6]
                tz = frac_and_tz[idx:]
                ts = f"{parts[0]}.{frac}{tz}"
        return datetime.fromisoformat(ts)
    
    all_events = []
    all_node_names = set()
    
    for log_file in Path(results_dir).glob('events-*.jsonl'):
        node_name = log_file.stem.replace('events-', '')
        all_node_names.add(node_name)
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        event['_node_name'] = node_name
                        all_events.append(event)
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass
    
    all_events.sort(key=lambda e: e.get('ts', ''))
    
    claim_times = {}
    update_received = {}
    
    for event in all_events:
        ts = event.get('ts')
        
        if event.get('event') == 'job_claimed':
            target = event.get('target', '')
            claiming_node = event.get('_node_name', '')
            if target and claiming_node:
                if target not in claim_times:
                    claim_times[target] = {}
                if claiming_node not in claim_times[target]:
                    claim_times[target][claiming_node] = ts
        
        elif event.get('event') == 'node_update':
            receiving_node = event.get('_node_name', '')
            from_node = event.get('from', '')
            jobs = event.get('jobs', [])
            
            if from_node and jobs:
                from_node_name = from_node.split('/')[-1] if '/' in from_node else from_node
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
    return {
        'min': min(prop_times),
        'max': max(prop_times),
        'avg': statistics.mean(prop_times),
        'median': statistics.median(prop_times)
    }

def get_command_timestamp(results_dir):
    """Get the timestamp of the first command received"""
    for log_file in Path(results_dir).glob('events-*.jsonl'):
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get('event') == 'command_received':
                            return event.get('ts')
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass
    return None

def get_last_claim_timestamp(results_dir):
    """Get the timestamp of the last job_claimed event (for replication time calculation)"""
    last_ts = None
    for log_file in Path(results_dir).glob('events-*.jsonl'):
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get('event') == 'job_claimed':
                            ts = event.get('ts')
                            if ts:
                                if last_ts is None or ts > last_ts:
                                    last_ts = ts
                    except json.JSONDecodeError:
                        pass
        except FileNotFoundError:
            pass
    return last_ts

if __name__ == '__main__':
    main()
