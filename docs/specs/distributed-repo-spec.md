================================================================================
Distributed Repository Specification
================================================================================

1. Introduction

This document specifies the message formats, processing rules, and shared
semantics for the distributed repository. The system supports two distribution
mechanisms:

- Auction: An asynchronous multi-step protocol using bidding
- Hydra: A synchronous single-step protocol embedding assignments in NodeUpdate

Both mechanisms share common semantics for heartbeats, replication checking,
command processing, and winner determination, as specified in Section 5.

2. Terminology

2.1 Core Concepts

Job Target (Target)
	The identifier for a job to be performed. This is the command target name
	from the producer (e.g., /ndn/producer/mytarget/t=123). Nodes "perform"
	the job by executing the command (simulated INSERT or JOIN operation).

	Appears in: Command, JobAssignment, NodeUpdate

JobAssignment
	A TLV structure notifying nodes of job assignments. Contains:
	- Target: enc.Name (job identifier)
	- Assignees: []enc.Name (list of node names assigned to perform the job)

NodeUpdate
	An SVS-published structure containing node state. Contains:
	- Jobs: []enc.Name (list of job targets currently being performed)
	- NewCommand: *Command (optional new command from producer)
	- StorageCapacity: uint64 (node's total storage capacity in bytes)
	- StorageUsed: uint64 (node's current storage usage in bytes)
	- JobRelease: []*InternalCommand (optional job release signals)
	- JobAssignments: []*JobAssignment (optional redistribution assignments)

Command
	A command received from the producer. Contains:
	- Type: string (INSERT or JOIN)
	- Target: enc.Name (job identifier)
	- SnapshotThreshold: uint64

2.2 Auction-Specific

ResultsName
	The Interest name where auction results are published. Format:
	/<auctioneer-node-prefix>/results/v=<timestamp>

Bid Interest Name
	The Interest name on which MetricRequest is sent to peers. Format:
	/<peer-node-prefix>/bid/v=<timestamp>

Auction Timestamp
	A 64-bit nanosecond timestamp that, when combined with the node name,
	uniquely identifies an auction instance and provides ordering for collision
	resolution.

3. Constants

3.1 Timing

HEARTBEAT_INTERVAL
	Time between heartbeat publications. Configurable via --heartbeat-interval.
	Default: 5 seconds.

HEARTBEAT_TIMEOUT
	Time before considering a node offline. RECOMMENDED: 3 × HEARTBEAT_INTERVAL + buffer
	where buffer allows for network jitter and processing delay.

3.2 Storage

STORAGE_THRESHOLD
	Maximum storage utilization for job claiming. Nodes with storage usage
	exceeding this threshold MUST NOT claim new jobs. Operator-defined,
	RECOMMEND 75%.

REPLICATION_FACTOR
	Number of nodes that must perform each job. Operator-defined, MUST be
	greater than 0.

4. TLV Definitions

4.1 Command (received from producer)

	Type			string		(INSERT or JOIN)
	Target			enc.Name		(job identifier)
	SnapshotThreshold	uint64

4.2 NodeUpdate (SVS-published)

	0x290:	Jobs			[]enc.Name		(sequence of job targets)
	0x291:	NewCommand		*Command		(optional new command)
	0x292:	StorageCapacity		uint64			(node's total capacity)
	0x293:	StorageUsed		uint64			(node's used storage)
	0x294:	JobRelease		[]*InternalCommand	(job release signals)
	0x297:	JobAssignments		[]*JobAssignment	(redistribution assignments)

4.3 JobAssignment

	0x295:	Target			enc.Name		(job identifier)
	0x296:	Assignees		[]enc.Name		(sequence of node names)

4.4 Auction Messages

MetricRequest (carried in Bid Interest application parameters)

	0x298:	Target		enc.Name	(Job target identifier)
	0x29A:	Timestamp	uint64		(64-bit nanosecond timestamp)
	0x299:	ResultsName	enc.Name	(Results Interest Name)

MetricResponse (sent as Data in response to Bid Interest)

	0x292:	Capacity	uint64		(Node's storage capacity in bytes)
	0x293:	Used		uint64		(Node's used storage in bytes)
	0x29B:	Delay		bool		(True if peer requests delay)

Note: TLV types 0x292 and 0x293 are reused in MetricResponse with the same
semantic meaning as in NodeUpdate (storage capacity and usage).

5. Shared Semantics

The following sections specify semantics common to all distribution mechanisms.

5.1 Heartbeat Mechanism

Each node MUST publish heartbeats at regular intervals to maintain presence
in the group.

5.1.1 Publication

The node MUST increase its SVS sequence number by 1 every HEARTBEAT_INTERVAL
and publish a NodeUpdate containing its current Jobs list and storage metrics.

5.1.2 Reception

Upon receiving a heartbeat (NodeUpdate) from another node, the node MUST
reset that peer's timeout timer to HEARTBEAT_TIMEOUT.

5.1.3 Timeout

When a peer's heartbeat times out:

	1. Retrieve the offline node's last Jobs state
	2. For each job, check if replication is satisfied
	3. If any job is under-replicated, invoke the distribution mechanism

5.2 Replication Check

The system MUST verify that each job has sufficient replication.

5.2.1 Triggers

Replication checks are performed:

	1. On command receipt from producer
	2. On Jobs Update from another node
	3. On heartbeat timeout (when a node is detected offline)

5.2.2 Algorithm

For each known job target:

	1. Count the number of nodes currently performing the job
	2. If count < ReplicationFactor, the job is under-replicated
	3. Under-replicated jobs MUST trigger the distribution mechanism

5.3 Command Processing

When a node receives a command from the producer, it MUST:

	1. Parse the command (Type: INSERT or JOIN, Target: enc.Name)
	2. Send StatusResponse to producer with status "received"
	3. Store the command internally
	4. Publish NodeUpdate to group sync with NewCommand set to the command
	5. Perform replication check per Section 5.2
	6. If under-replicated, invoke the distribution mechanism

5.4 Winner Determination

When a node determines that a job is under-replicated, it MUST compute winners
as follows:

5.4.1 Filter

Exclude any node that:
	- Is already assigned the job
	- Has storage utilization exceeding STORAGE_THRESHOLD

5.4.2 Sort

Sort remaining candidates by:
	Primary:   Storage utilization percentage ascending
	Secondary: Capacity descending			(larger capacity first)
	Tertiary:  Node name ascending		(lexicographic)

5.4.3 Select

Select top N candidates where N = ReplicationFactor - CurrentReplicationCount

5.5 Ordering Semantics

JobAssignment and command NewCommand may arrive in any order via SVS. A node
MUST NOT claim a job before receiving the corresponding command. If
JobAssignment arrives first, the node MUST buffer the assignment to process
upon receiving the command.

6. Auction Distribution Mechanism

The auction mechanism is an asynchronous multi-step protocol where nodes
bid for the right to perform jobs.

6.1 Auction Initiation

When a node determines that a job is under-replicated, it initiates an auction
by generating a 64-bit nanosecond timestamp representing the current time. This
timestamp, combined with the node name, uniquely identifies the auction instance
and provides canonical ordering for collision resolution.

6.2 Collision Resolution

When multiple nodes initiate auctions simultaneously, the following rules ensure
exactly one auction proceeds.

6.2.1 Timestamp Ordering

When a node receives a MetricRequest:

	If the Target matches a locally-initiated auction:

		If incoming timestamp < local timestamp:
			The incoming auction is EARLIER. Cancel and reschedule local auction.

		If incoming timestamp > local timestamp:
			The incoming auction is LATER. Set Delay=true in MetricResponse.

		If timestamps are equal:
			Use lexicographic comparison of node names. The node with the lesser
			name wins. Set Delay=(auctioneer_name > local_node_name). If false,
			cancel and reschedule local auction.

	If targets differ:
		No timestamp comparison needed. Proceed with local auction and set
		Delay=false in MetricResponse.

6.2.2 Delay Flag Semantics

A Delay value of true RECOMMENDS that the receiving auctioneer cancel and
reschedule its auction. Upon receiving any MetricResponse with Delay=true, the
auctioneer MUST:

	1. Cancel its current auction
	2. Publish JobAssignment with empty Assignees
	3. Reschedule the auction

6.2.3 Rescheduled Auctions

Rescheduled auctions SHOULD allow time for ongoing auctions to complete and for
NodeUpdates to propagate. The node MUST check for under-replication when the
reschedule timer expires and MAY cancel earlier if the target is no longer
under-replicated.

6.3 Processing Rules

6.3.1 Auctioneer

The auctioneer MUST:

	1. Generate a unique timestamp and construct the ResultsName
	2. Send MetricRequest to all peers excluding self and nodes already assigned
	3. Wait for responses or timeout
	4. If any response has Delay=true, publish empty JobAssignment and cancel
	5. Otherwise determine winners per Section 5.4 and publish JobAssignment
	6. Schedule a follow-up auction (cancelled if target is no longer
	   under-replicated)

6.3.2 Peer

Upon receiving a MetricRequest, a node MUST:

	1. Determine Delay flag per Section 6.2
	2. Send MetricResponse with local storage metrics and computed Delay value
	3. Subscribe to ResultsName

6.3.3 Results Handling

Upon receiving JobAssignment:

	- If Assignees is empty: should schedule a new auction if still
	  under-replicated
	- If node is in Assignees and has received command: claim job
	- If target remains under-replicated: should schedule an additional auction

7. Hydra Distribution Mechanism

Hydra is a synchronous single-step protocol that distributes jobs by embedding
job assignments directly in SVS-published NodeUpdate messages.

7.1 Command Processing

When a node receives a command from the producer and determines the job is
under-replicated:

	1. Compute winners per Section 5.4
	2. If this node is in winners, claim the job
	3. Publish NodeUpdate to SVS group containing:
	   - NewCommand: the command
	   - JobAssignments: [{ Target: target, Assignees: winners }]

7.2 Job Assignment Processing

Upon receiving a NodeUpdate containing JobAssignments:

	1. For each JobAssignment:
	   a. If this node is already performing the job, skip
	   b. If this node's name is in Assignees and the command is available,
	      claim the job

	2. See Section 5.5 for ordering semantics.

7.3 Failure Recovery

When a node's heartbeat times out (per Section 5.1.3):

	1. Retrieve the offline node's last Jobs state
	2. For each job the offline node was performing:
	   a. Check current replication count
	   b. If count < ReplicationFactor, compute winners per Section 5.4
	3. If this node is in winners, claim the job
	4. Publish NodeUpdate with JobAssignments for redistribution
