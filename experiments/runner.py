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
    args = parser.parse_args()

    sys.argv = [sys.argv[0]]

    signal.signal(signal.SIGINT, signal_handler)
    signal.signal(signal.SIGTERM, signal_handler)

    setLogLevel('info')
    
    nodes = ALL_NODES[:args.node_count]
    results_dir = Path(args.results_dir)
    results_dir.mkdir(parents=True, exist_ok=True)

    info(f'=== Mini-NDN Integration Runner ===\n')
    info(f'Nodes: {nodes}\n')
    info(f'Replication factor: {args.replication_factor}\n')
    info(f'Producers: {args.producer_count}\n')
    info(f'Commands per producer: {args.command_count}\n')
    info(f'Command rate: {args.command_rate} cmds/sec\n')
    info(f'Timeout: {args.timeout}s\n')

    topology_content = create_topology(nodes)
    topo_path = results_dir / 'topology.conf'
    topo_path.write_text(topology_content)
    info(f'Topology written to {topo_path}\n')

    Minindn.cleanUp()
    Minindn.verifyDependencies()
    
    sys.argv = [sys.argv[0], str(topo_path)]
    ndn = Minindn()
    ndn.start()

    info('Starting NFD on nodes...\n')
    nfds = AppManager(ndn, ndn.net.hosts, Nfd)
    
    info('Waiting for NFD to initialize (5s)...\n')
    time.sleep(5)

    info('Setting up routes using NdnRoutingHelper...\n')
    grh = NdnRoutingHelper(ndn.net, "udp", "link-state")
    
    for host in ndn.net.hosts:
        node_prefix = f'/ndn/repo/{host.name}'
        sync_data_prefix = f'/ndn/drepo{node_prefix}'
        grh.addOrigin([host], ["/ndn/drepo", "/ndn/drepo/ndn", node_prefix, sync_data_prefix])
        info(f'  Added origin for {host.name}: /ndn/drepo, /ndn/drepo/ndn, {node_prefix}, {sync_data_prefix}\n')
    
    info('Calculating and installing routes...\n')
    grh.calculateNPossibleRoutes()
    
    info('Setting multicast strategy on /ndn/drepo...\n')
    for host in ndn.net.hosts:
        host.cmd('nfdc strategy set /ndn/drepo /localhost/nfd/strategy/multicast 2>&1')
        host.cmd('nfdc strategy set /ndn/drepo/ndn /localhost/nfd/strategy/multicast 2>&1')

    info('Verifying FIB entries...\n')
    for host in ndn.net.hosts:
        fib = host.cmd('nfdc fib list 2>&1 | head -20')
        info(f'  {host.name} FIB (first 20 lines):\n{fib}\n')

    info('Starting repos on nodes...\n')
    os.environ['SHELL'] = '/bin/bash'
    for host in ndn.net.hosts:
        event_log = results_dir / f'events-{host.name}.jsonl'
        node_prefix = f'/ndn/repo/{host.name}'
        signing_identity = '/ndn/repo.teame.dev/repo'
        cmd = f'/usr/local/bin/repo --event-log {event_log} --node-prefix {node_prefix} --signing-identity {signing_identity} &'
        host.cmd(f'mkdir -p /tmp/{host.name}')
        host.cmd(cmd)
        info(f'  Started repo on {host.name} with prefix {node_prefix}\n')

    node_names = [host.name for host in ndn.net.hosts]
    svs_healthy = wait_for_svs_health(results_dir, node_names, timeout=15)
    
    info('Checking for node updates in event logs...\n')
    for host in ndn.net.hosts:
        event_log = results_dir / f'events-{host.name}.jsonl'
        if event_log.exists():
            with open(event_log) as f:
                lines = f.readlines()
                node_updates = [l for l in lines if 'node_update' in l]
                sync_interests = [l for l in lines if 'sync_interest_sent' in l]
                data_sent = [l for l in lines if 'data_sent' in l]
                rep_checks = [l for l in lines if 'replication_check' in l]
                info(f'  {host.name}: updates_received={len(node_updates)}, sync_sent={len(sync_interests)}, data_sent={len(data_sent)}, rep_checks={len(rep_checks)}\n')

    producer_nodes = ndn.net.hosts[:args.producer_count]
    info(f'Running {args.producer_count} producer(s)...\n')
    for producer_node in producer_nodes:
        info(f'  Starting producer on {producer_node.name}...\n')
        result = producer_node.cmd(f'/usr/local/bin/producer -count {args.command_count} -rate {args.command_rate} 2>&1')
        info(f'  Producer {producer_node.name} output: {result}\n')

    expected_claims = args.producer_count * args.command_count * args.replication_factor
    
    info(f'Waiting for replication (timeout={args.timeout}s, expecting {expected_claims} claims)...\n')
    start_time = time.time()
    replicated = False
    last_claim_count = 0
    
    while time.time() - start_time < args.timeout:
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

    final_claim_count = count_job_claims(results_dir)
    
    sync_interests, data_packets = count_packet_stats(results_dir)
    
    max_replication, final_replication, timeline = build_replication_timeline(results_dir)
    
    over_replicated = max_replication > args.replication_factor
    under_replicated = final_replication < args.replication_factor
    
    command_ts = get_command_timestamp(results_dir)
    last_claim_ts = get_last_claim_timestamp(results_dir)
    
    replication_time_seconds = None
    if command_ts and last_claim_ts:
        try:
            from datetime import datetime
            cmd_time = datetime.fromisoformat(command_ts.replace('Z', '+00:00'))
            claim_time = datetime.fromisoformat(last_claim_ts.replace('Z', '+00:00'))
            replication_time_seconds = (claim_time - cmd_time).total_seconds()
        except:
            replication_time_seconds = replication_time if replicated else None

    actual_nodes = [host.name for host in ndn.net.hosts]
    
    metadata = {
        'nodes': actual_nodes,
        'node_count': args.node_count,
        'replication_factor': args.replication_factor,
        'producer_count': args.producer_count,
        'command_count': args.command_count,
        'command_rate': args.command_rate,
        'expected_claims': expected_claims,
        'timeout': args.timeout,
        'replicated': replicated and final_claim_count >= expected_claims,
        'replication_time_seconds': replication_time_seconds,
        'job_claims': final_claim_count,
        'sync_interests': sync_interests,
        'data_packets': data_packets,
        'max_replication': max_replication,
        'final_replication': final_replication,
        'over_replicated': over_replicated,
        'under_replicated': under_replicated,
        'timeline': timeline
    }
    metadata_path = results_dir / 'metadata.json'
    metadata_path.write_text(json.dumps(metadata, indent=2))
    info(f'Metadata written to {metadata_path}\n')

    info('=== RESULTS ===\n')
    info(f'Replicated: {metadata["replicated"]}\n')
    info(f'Job claims: {final_claim_count}/{expected_claims}\n')
    info(f'Replication time: {replication_time_seconds:.2f}s\n' if replication_time_seconds else 'Replication time: N/A\n')
    info(f'Sync interests: {sync_interests}\n')
    info(f'Data packets: {data_packets}\n')
    info(f'Max replication: {max_replication}\n')
    info(f'Over-replicated: {over_replicated}\n')
    info(f'Under-replicated: {under_replicated}\n')

    cleanup()

    if metadata['replicated']:
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
    """Count distinct nodes that have claimed a job"""
    claim_count = 0
    for log_file in Path(results_dir).glob('events-*.jsonl'):
        try:
            with open(log_file) as f:
                for line in f:
                    try:
                        event = json.loads(line.strip())
                        if event.get('event') == 'job_claimed':
                            claim_count += 1
                            break
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
    Returns: (max_replication, final_replication, timeline)
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
    max_replication = 0
    timeline = []
    
    for event in all_events:
        if event.get('event') == 'job_claimed':
            node = event.get('_node', '')
            target = event.get('target', '')
            if target and node not in claimed.get(target, set()):
                if target not in claimed:
                    claimed[target] = set()
                claimed[target].add(node)
                count = len(claimed[target])
                max_replication = max(max_replication, count)
                timeline.append({
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
                timeline.append({
                    'ts': event.get('ts'),
                    'action': 'release',
                    'node': node,
                    'target': target,
                    'count': count
                })
    
    if claimed:
        target = list(claimed.keys())[0]
        final_replication = len(claimed[target])
    else:
        final_replication = 0
    
    return max_replication, final_replication, timeline

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
