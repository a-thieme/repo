# Distributed NDN Data Repository

## Overview

This project implements a distributed Named Data Networking (NDN) data repository system that provides resilient, decentralized storage and command execution. The repository distributes storage commands across multiple nodes and manages job execution through intelligent replication and node detection mechanisms.

## Architecture

The system consists of three main components:

- **Repo Component**: Main repository that manages storage commands, distributes jobs across nodes, and handles replication
- **Producer Component**: Sends commands to the repository for processing
- **TLV Component**: Defines data structures and protocols for command and node communication

## Core Features

### Storage Management
- Distributed storage management across multiple nodes
- Automatic job distribution based on capacity
- Real-time storage usage tracking
- Deterministic storage simulation for JOIN commands
- Automatic job release when storage exceeds 75% capacity

### Replication System
- Replication factor management (default: 3)
- Intelligent job claiming based on node availability
- Automatic redistribution when nodes go offline

### Node Detection and Resilience
- **Node Detection**: Detects nodes offline after 3 consecutive missed heartbeats
- **Heartbeat Monitoring**: Continuous monitoring of node status through heartbeat mechanism
- **Resilience**: System remains operational when nodes go offline

### Monitoring Mechanisms
- **Heartbeat Monitoring System**: Background monitoring of node status
- **Job Claim Monitoring**: Monitors job claim delays and triggers replication
- **Storage Monitoring**: Continuous storage usage tracking and simulation
- **Re-evaluation**: Re-evaluates replication decisions when needed

## Getting Started

### Basic Usage
The repository receives commands and distributes them as jobs across the node pool. Each node can execute jobs based on its available storage capacity.

### Command Handling
- Commands are received through the command handler
- Jobs are distributed across nodes based on replication factor
- Each node monitors its own storage usage and executes jobs it claims

### Recent Implementation
The system has been enhanced with:
- Node detection mechanism (3 missed heartbeats detection)
- Heartbeat monitoring system for resilience
- Monitoring triggers for re-evaluation
- Automatic job release mechanism (triggers at 75% capacity)
- Deterministic storage simulation for testing

## Components

### Repo Component
Main repository implementation that handles:
- Storage command distribution
- Job execution management
- Replication factor management
- Node status tracking
- Heartbeat monitoring
- Job release mechanism
- Storage simulation

### Producer Component
Command sender that:
- Sends commands to the repository
- Uses TLV protocol for communication
- Implements command sending and validation

### TLV Component
Defines data structures:
- Command structure
- NodeUpdate structure (with JobRelease field)
- StatusResponse structure
- InternalCommand structure (for job release signaling)

## Key Features

1. **Node Detection**: Detects nodes offline after 3 consecutive missed heartbeats
2. **Heartbeat Monitoring**: Continuous monitoring of node status
3. **Automatic Replication**: Intelligent job distribution
4. **Resilience**: System remains operational when nodes go offline
5. **Monitoring**: Multiple monitoring mechanisms for job and node status

## Technical Highlights

### Node Detection Mechanism
- **Offline Detection**: Nodes detected offline after 3 consecutive missed heartbeats
- **Detection Timeout**: Calculated as 3 × heartbeatInterval + 500ms delay
- **Status Tracking**: Real-time node status tracking with heartbeat monitoring
- **Resilience**: Automatic handling of node failures

### Monitoring System
- **Heartbeat Monitoring**: Continuous monitoring of node status
- **Job Claim Monitoring**: Monitors job claim delays and timeouts
- **Re-evaluation Triggers**: Multiple detection mechanisms for re-evaluation

### Storage Management
- **Storage Capacity Tracking**: Real-time storage capacity tracking
- **Usage Monitoring**: Storage usage monitoring across nodes
- **Job Distribution**: Intelligent job distribution based on capacity
- **Capacity Management**: Automatic capacity management and redistribution

## Development Notes

This project implements a distributed NDN data repository with resilient operation. The system automatically handles node failures and maintains operation through intelligent monitoring and re-evaluation mechanisms.

For detailed technical information, see README-TECHNICAL.md.

## Testing

### Unit Tests
```bash
go test -v ./repo/...
```

### Integration Tests (Mini-NDN)
Integration tests run inside Docker using mini-ndn to simulate a distributed network:

```bash
# Quick test (5 nodes, ~3 min)
go test -v ./repo -run TestMiniNDNIntegration -timeout 10m

# Custom node count
NODE_COUNT=10 go test -v ./repo -run TestMiniNDNIntegration_CustomNodeCount -timeout 30m

# Full topology (24 nodes, ~8 min)
RUN_FULL_TOPOLOGY=1 go test -v ./repo -run TestMiniNDNIntegration_FullTopology -timeout 15m

# Multi-node experiments (5, 10, 15, 20, 24 nodes)
./experiments/run_experiments.sh
```

**Requirements:**
- Docker installed and running
- `--privileged` mode for mininet

**Evaluated Metrics:**
| # | Metric |
|---|--------|
| 0 | Command correctly replicated (≥ rf nodes) |
| 1 | Replication time (seconds) |
| 2 | Sync interests sent (sum across nodes) |
| 3 | Data packets sent (sum across nodes) |
| 4 | Max replication reached |
| 5 | Over-replicated (max > rf) |
| 6 | Under-replicated (final < rf) |

See `experiments/README_EXPERIMENT.md` for details.
