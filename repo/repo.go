package main

import (
	_ "embed"
	"slices"
	"sync"
	"time"

	"github.com/a-thieme/repo/repo/util"
	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/engine"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	"github.com/named-data/ndnd/std/object"
	local_storage "github.com/named-data/ndnd/std/object/storage"
	sec "github.com/named-data/ndnd/std/security"
	"github.com/named-data/ndnd/std/security/keychain"
	"github.com/named-data/ndnd/std/security/signer"
	svs "github.com/named-data/ndnd/std/sync"
)

//go:embed testbed-root.decoded
var testbedRootCert []byte

// structs
type Repo struct {
	groupPrefix     enc.Name
	notifyPrefix    *enc.Name
	nodePrefix      enc.Name
	signingIdentity enc.Name

	engine ndn.Engine
	store  ndn.Store
	client ndn.Client

	groupSync     *svs.SvsALO
	heartbeatSync *svs.SvsALO

	mu sync.Mutex

	nodeStatus map[string]NodeStatus
	commands   map[string]*tlv.Command

	storageCapacity uint64
	storageUsed     uint64
	jobs            []enc.Name
	jobStorageUsage map[string]uint64

	rf                int
	noRelease         bool
	maxJoinGrowthRate uint64
	heartbeatInterval time.Duration

	distributionMechanism string
	distributor           DistributionMechanism

	auctionTimeout time.Duration

	currentAuctionTimestamp uint64
	pendingAssignments      map[string]*tlv.JobAssignment
	scheduledAuctions       map[string]*time.Timer

	eventLogger  util.Logger
	countingFace *util.CountingFace
}

type NodeStatus struct {
	Capacity    uint64
	Used        uint64
	LastUpdated time.Time
	Jobs        []enc.Name
	TimerID     uint64
}

type DistributionMechanism interface {
	DetermineWinners(cmd *tlv.Command, nodeStatus map[string]NodeStatus, myName string, rf int, eventLogger util.Logger) []string
}

// initialization
func NewRepo(groupPrefix string, nodePrefix string, signingIdentity string, replicationFactor int, noRelease bool, maxJoinGrowthRate uint64, heartbeatInterval time.Duration, distributionMechanism string, eventLogger util.Logger, auctionTimeout time.Duration) *Repo {
	gp, _ := enc.NameFromStr(groupPrefix)
	np, _ := enc.NameFromStr(nodePrefix)
	si, _ := enc.NameFromStr(signingIdentity)
	nf := gp.Append(enc.NewGenericComponent(NOTIFY))

	if maxJoinGrowthRate == 0 {
		maxJoinGrowthRate = 10 * 1024 * 1024
	}

	if heartbeatInterval == 0 {
		heartbeatInterval = DEFAULT_HEARTBEAT_INTERVAL
	}

	if eventLogger == nil {
		eventLogger = &util.NullEventLogger{}
	}

	var distributor DistributionMechanism
	switch distributionMechanism {
	case "auction":
		distributor = NewAuctionMechanism()
	case "hydra":
		distributor = NewHydraMechanism()
	default:
		log.Warn(nil, "unknown_distribution_mechanism", "mechanism", distributionMechanism, "defaulting", "hydra")
		distributor = NewHydraMechanism()
	}

	r := &Repo{
		groupPrefix:           gp,
		notifyPrefix:          &nf,
		nodePrefix:            np,
		signingIdentity:       si,
		nodeStatus:            make(map[string]NodeStatus),
		commands:              make(map[string]*tlv.Command),
		jobs:                  make([]enc.Name, 0),
		jobStorageUsage:       make(map[string]uint64),
		rf:                    replicationFactor,
		noRelease:             noRelease,
		maxJoinGrowthRate:     maxJoinGrowthRate,
		heartbeatInterval:     heartbeatInterval,
		distributionMechanism: distributionMechanism,
		distributor:           distributor,
		eventLogger:           eventLogger,
		auctionTimeout:        auctionTimeout,
		pendingAssignments:    make(map[string]*tlv.JobAssignment),
		scheduledAuctions:     make(map[string]*time.Timer),
	}

	return r
}

func (r *Repo) Start() (err error) {
	log.Info(r, "repo_start")

	r.storageCapacity = (10 * 1024 * 1024 * 1024) + (hashFromString(r.nodePrefix.String()) % (5 * 1024 * 1024 * 1024))
	r.storageUsed = (hashFromString(r.nodePrefix.String()) % (100 * 1024 * 1024))

	r.mu.Lock()
	r.nodeStatus[r.myNodeName()] = NodeStatus{
		Capacity:    r.storageCapacity,
		Used:        r.storageUsed,
		Jobs:        r.jobs,
		LastUpdated: time.Now(),
	}
	r.mu.Unlock()

	var face ndn.Face = engine.NewDefaultFace()

	if r.eventLogger != nil {
		syncPrefix := r.groupPrefix.Append(enc.NewGenericComponent("group-messages")).Append(enc.NewKeywordComponent("svs")).String()
		r.countingFace = util.NewCountingFace(face, r.eventLogger, syncPrefix)
		face = r.countingFace
	}

	r.engine = engine.NewBasicEngine(face)
	if err = r.engine.Start(); err != nil {
		return err
	}

	r.store = local_storage.NewMemoryStore()

	kc, err := keychain.NewKeyChain("dir:///home/adam/.ndn/keys", r.store)
	if err != nil {
		return err
	}

	schema := &BasicSchema{signingIdentity: r.signingIdentity}

	caData, _, err := r.engine.Spec().ReadData(enc.NewBufferView(testbedRootCert))
	if err != nil {
		return err
	}

	trust, err := sec.NewTrustConfig(kc, schema, []enc.Name{caData.Name()})
	if err != nil {
		return err
	}
	trust.UseDataNameFwHint = true

	r.client = object.NewClient(r.engine, r.store, trust)

	r.groupSync, err = svs.NewSvsALO(svs.SvsAloOpts{
		Name: r.nodePrefix,
		Svs: svs.SvSyncOpts{
			Client:       r.client,
			GroupPrefix:  r.groupPrefix.Append(enc.NewGenericComponent("group-messages")),
			SyncDataName: r.nodePrefix,
		},
		Snapshot: &svs.SnapshotNull{},
	})
	if err != nil {
		return err
	}
	err = r.groupSync.SubscribePublisher(enc.Name{}, r.onGroupSync)
	if err != nil {
		return err
	}

	err = r.groupSync.Start()
	if err != nil {
		return err
	}

	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.groupSync.SyncPrefix(),
		Expose: true,
	})
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.groupSync.DataPrefix(),
		Expose: true,
	})
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.notifyPrefix.Clone(),
		Expose: true,
	})

	bidPrefix := r.nodePrefix.Append(enc.NewGenericComponent(BID_SUFFIX))
	resultsPrefix := r.nodePrefix.Append(enc.NewGenericComponent(RESULTS_SUFFIX))

	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   bidPrefix,
		Expose: true,
	})
	r.client.AttachCommandHandler(bidPrefix, r.onBidInterest)

	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   resultsPrefix,
		Expose: true,
	})

	if r.distributionMechanism == "auction" {
		heartbeatGroupPrefix := r.groupPrefix.Append(enc.NewGenericComponent(HEARTBEAT_SUFFIX))
		r.heartbeatSync, err = svs.NewSvsALO(svs.SvsAloOpts{
			Name: r.nodePrefix,
			Svs: svs.SvSyncOpts{
				Client:       r.client,
				GroupPrefix:  heartbeatGroupPrefix,
				SyncDataName: r.nodePrefix,
			},
			Snapshot: &svs.SnapshotNull{},
		})
		if err != nil {
			return err
		}
		err = r.heartbeatSync.Start()
		if err != nil {
			return err
		}
		r.client.AnnouncePrefix(ndn.Announcement{
			Name:   r.heartbeatSync.SyncPrefix(),
			Expose: true,
		})
	}

	r.client.AttachCommandHandler(*r.notifyPrefix, r.onCommand)

	err = r.client.Start()
	if err != nil {
		return err
	}
	go r.runHeartbeat()
	go r.runStorageSimulation()
	return nil
}

func (r *Repo) runHeartbeat() {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()

	r.publishNodeUpdate(nil)

	if r.distributionMechanism == "auction" && r.heartbeatSync != nil {
		r.heartbeatSync.Publish(nil)
	}

	for range ticker.C {
		r.publishNodeUpdate(nil)
		r.checkStaleNodes()

		if r.distributionMechanism == "auction" && r.heartbeatSync != nil {
			r.heartbeatSync.Publish(nil)
		}
	}
}

func (r *Repo) checkStaleNodes() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for nodeName, status := range r.nodeStatus {
		if nodeName == r.myNodeName() {
			continue
		}
		if now.Sub(status.LastUpdated) > HEARTBEAT_TIMEOUT {
			log.Info(r, "stale_node_detected", "node", nodeName)
			if r.distributionMechanism == "auction" {
				go r.onHeartbeatTimeoutAuction(nodeName, status.Jobs)
			} else {
				go r.onHeartbeatTimeoutHydra(nodeName)
			}
		}
	}
}

func (r *Repo) runStorageSimulation() {
	ticker := time.NewTicker(STORAGE_TICK_TIME)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		for _, target := range r.jobs {
			cmd := r.getCommandInternal(target)
			if cmd != nil && cmd.Type == "JOIN" {
				jobKey := target.String()
				if r.jobStorageUsage == nil {
					r.jobStorageUsage = make(map[string]uint64)
				}
				growth := (hashFromString(cmd.Target.String()) % r.maxJoinGrowthRate)
				r.jobStorageUsage[jobKey] += growth
				r.changeStorageUsed(growth)
			}
		}
		r.mu.Unlock()
	}
}

func (r *Repo) onCommand(name enc.Name, content enc.Wire, reply func(wire enc.Wire) error) {
	log.Info(r, "onCommand_received", "distribution", r.distributionMechanism)

	cmd, err := tlv.ParseCommand(enc.NewWireView(content), false)
	if err != nil {
		log.Warn(r, "command_parse_failed", "err", err)
		return
	}

	response := tlv.StatusResponse{
		Target: cmd.Target,
		Status: "received",
	}
	reply(response.Encode())

	r.addCommand(cmd)
	r.eventLogger.LogCommandReceived(cmd.Type, cmd.Target.String())

	if r.distributionMechanism == "auction" {
		targetStr := cmd.Target.String()

		r.mu.Lock()
		if pending, ok := r.pendingAssignments[targetStr]; ok {
			log.Info(r, "onCommand_pending_assignment_found", "target", targetStr)
			assignees := encNamesToStrings(pending.Assignees)
			if slices.Contains(assignees, r.myNodeName()) {
				r.doJob(cmd)
				r.publishNodeUpdate(&tlv.NodeUpdate{
					Jobs: r.getMyJobs(),
				})
			}
			delete(r.pendingAssignments, targetStr)
		}
		r.mu.Unlock()

		currentReplication := r.countReplication(cmd.Target)
		needed := r.rf - currentReplication
		log.Info(r, "onCommand_auction_trigger", "target", targetStr, "current", currentReplication, "needed", needed, "distribution", r.distributionMechanism)
		if needed > 0 {
			log.Info(r, "onCommand_checking_replication", "target", targetStr, "current", currentReplication, "needed", needed)
			r.publishNodeUpdate(&tlv.NodeUpdate{
				NewCommand: cmd,
			})
			go r.runAuction(cmd)
		}
		return
	}

	myPrefix := r.myNodeName()
	winners := r.determineWinnersHydra(cmd)
	if slices.Contains(winners, myPrefix) {
		r.doJob(cmd)
	}

	r.publishNodeUpdate(&tlv.NodeUpdate{
		NewCommand: cmd,
		JobAssignments: []*tlv.JobAssignment{{
			Target:    cmd.Target,
			Assignees: stringNamesToEncNames(winners),
		}},
	})
}

func (r *Repo) onGroupSync(pub svs.SvsPub) {
	update, err := tlv.ParseNodeUpdate(enc.NewWireView(pub.Content), false)
	if err != nil {
		log.Warn(r, "node_update_parse_failed", "name", pub.DataName, "err", err)
		return
	}

	publisherName := pub.Publisher.String()
	r.updateNodeStatus(publisherName, update)
	r.eventLogger.LogNodeUpdate(publisherName, update.Jobs, update.StorageCapacity, update.StorageUsed)

	if update.NewCommand != nil {
		r.addCommand(update.NewCommand)
		r.eventLogger.LogCommandSynced(update.NewCommand.Type, update.NewCommand.Target.String(), publisherName)

		if r.distributionMechanism == "auction" {
			targetStr := update.NewCommand.Target.String()
			r.mu.Lock()
			if pending, ok := r.pendingAssignments[targetStr]; ok {
				log.Info(r, "onGroupSync_pending_assignment_found", "target", targetStr)
				assignees := encNamesToStrings(pending.Assignees)
				if slices.Contains(assignees, r.myNodeName()) {
					r.doJob(update.NewCommand)
					r.publishNodeUpdate(&tlv.NodeUpdate{
						Jobs: r.getMyJobs(),
					})
				}
				delete(r.pendingAssignments, targetStr)
			}
			r.mu.Unlock()

			currentReplication := r.countReplication(update.NewCommand.Target)
			needed := r.rf - currentReplication
			if needed > 0 {
				go r.checkReplicationAndRunAuction(update.NewCommand)
			}
		}
	}

	if len(update.JobAssignments) > 0 {
		log.Debug(r, "onGroupSync_assignments", "from", publisherName, "count", len(update.JobAssignments))
	}

	if r.distributionMechanism == "auction" {
		r.handleAuctionJobAssignments(update.JobAssignments, publisherName)
		return
	}

	addedJob := false
	var reassignments []*tlv.JobAssignment
	log.Debug(r, "onGroupSync_job_assignments", "count", len(update.JobAssignments), "from", publisherName)
	for _, assignment := range update.JobAssignments {
		target := assignment.Target
		targetStr := target.String()
		assignees := encNamesToStrings(assignment.Assignees)

		if r.amIDoingJob(target) {
			log.Info(r, "onGroupSync_assignment_skipped", "reason", "already_doing_job", "target", targetStr)
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "skipped", "already_doing_job", assignees)
			continue
		}

		if !slices.Contains(assignees, r.myNodeName()) {
			log.Info(r, "onGroupSync_assignment_skipped", "reason", "not_in_assignees", "target", targetStr, "assignees", assignees, "myNode", r.myNodeName())
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "skipped", "not_in_assignees", assignees)
			continue
		}

		cmd := r.getCommand(target)
		log.Debug(r, "onGroupSync_got_command", "target", targetStr, "cmd", cmd)
		if cmd == nil {
			log.Info(r, "onGroupSync_assignment_pending", "reason", "command_not_received", "target", targetStr)
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "pending", "command_not_received", assignees)
			continue
		}

		log.Info(r, "onGroupSync_will_do_job", "target", targetStr, "myNode", r.myNodeName())
		if r.doJob(cmd) {
			addedJob = true
		} else {
			winners := r.determineWinnersHydra(cmd)
			reassignments = append(reassignments, &tlv.JobAssignment{Target: cmd.Target, Assignees: stringNamesToEncNames(winners)})
		}
	}
	if addedJob || len(reassignments) > 0 {
		r.publishNodeUpdate(&tlv.NodeUpdate{
			JobAssignments: reassignments,
		})
	}
}

func (r *Repo) handleAuctionJobAssignments(assignments []*tlv.JobAssignment, publisherName string) {
	for _, assignment := range assignments {
		target := assignment.Target
		targetStr := target.String()
		assignees := encNamesToStrings(assignment.Assignees)

		if len(assignees) == 0 {
			log.Info(r, "handleAuctionJobAssignments_delayed", "target", targetStr, "from", publisherName)
			r.eventLogger.LogAuctionDelayed(targetStr, "delayed_by_peer")
			currentReplication := r.countReplication(target)
			if currentReplication < r.rf {
				cmd := r.getCommand(target)
				if cmd != nil {
					go r.checkReplicationAndRunAuction(cmd)
				}
			}
			continue
		}

		if r.amIDoingJob(target) {
			log.Info(r, "handleAuctionJobAssignments_skipped", "reason", "already_doing_job", "target", targetStr)
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "skipped", "already_doing_job", assignees)
			continue
		}

		if !slices.Contains(assignees, r.myNodeName()) {
			log.Info(r, "handleAuctionJobAssignments_skipped", "reason", "not_in_assignees", "target", targetStr)
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "skipped", "not_in_assignees", assignees)
			continue
		}

		cmd := r.getCommand(target)
		if cmd == nil {
			log.Info(r, "handleAuctionJobAssignments_pending", "target", targetStr)
			r.mu.Lock()
			r.pendingAssignments[targetStr] = assignment
			r.mu.Unlock()
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "pending", "command_not_received", assignees)
			continue
		}

		log.Info(r, "handleAuctionJobAssignments_will_do_job", "target", targetStr, "myNode", r.myNodeName())
		if r.doJob(cmd) {
			r.publishNodeUpdate(&tlv.NodeUpdate{
				Jobs: r.getMyJobs(),
			})
		}

		currentReplication := r.countReplication(target)
		if currentReplication < r.rf {
			go r.checkReplicationAndRunAuction(cmd)
		}
	}
}

func (r *Repo) determineWinnersHydra(cmd *tlv.Command) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.distributor.DetermineWinners(cmd, r.nodeStatus, r.myNodeName(), r.rf, r.eventLogger)
}

func (r *Repo) updateNodeStatus(publisher string, update *tlv.NodeUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nodeStatus[publisher] = NodeStatus{
		Capacity:    update.StorageCapacity,
		Used:        update.StorageUsed,
		LastUpdated: time.Now(),
		Jobs:        update.Jobs,
	}
}

func (r *Repo) onHeartbeatTimeoutHydra(nodeName string) {
	log.Info(r, "heartbeat_timeout_triggered", "node", nodeName)
	myName := r.myNodeName()
	r.mu.Lock()
	defer r.mu.Unlock()
	status, exists := r.nodeStatus[nodeName]
	if !exists {
		log.Warn(r, "heartbeat_timeout_node_gone", "node", nodeName)
		return
	}

	r.eventLogger.LogNodeDetectedDead(nodeName, len(status.Jobs))

	delete(r.nodeStatus, nodeName)

	var assignments []*tlv.JobAssignment
	addedJob := false

	for _, target := range status.Jobs {
		cmd := r.getCommandInternal(target)
		if cmd == nil {
			continue
		}

		currentReplication := countReplicationInternal(target, r.nodeStatus)
		if currentReplication >= r.rf {
			continue
		}

		winners := r.determineWinnersHydra(cmd)

		if slices.Contains(winners, myName) {
			r.doJob(cmd)
			addedJob = true
		} else if winners != nil {
			assignments = append(assignments, &tlv.JobAssignment{
				Target:    target,
				Assignees: stringNamesToEncNames(winners),
			})
		}
	}

	r.mu.Unlock()

	if addedJob || len(assignments) > 0 {
		r.publishJobAssignments(assignments)
	}
}

func (r *Repo) onHeartbeatTimeoutAuction(nodeName string, jobs []enc.Name) {
	log.Info(r, "heartbeat_timeout_auction_triggered", "node", nodeName)
	r.mu.Lock()
	r.eventLogger.LogNodeDetectedDead(nodeName, len(jobs))
	delete(r.nodeStatus, nodeName)
	r.mu.Unlock()

	for _, target := range jobs {
		cmd := r.getCommand(target)
		if cmd == nil {
			continue
		}

		currentReplication := r.countReplication(target)
		needed := r.rf - currentReplication
		if needed > 0 {
			log.Info(r, "heartbeat_timeout_auction_rerun", "target", target.String(), "current", currentReplication, "needed", needed)
			r.checkReplicationAndRunAuction(cmd)
		}
	}
}

// BasicSchema allows all data and suggests the first matching key in the keychain.
type BasicSchema struct {
	signingIdentity enc.Name
}

func (s *BasicSchema) Check(pkt enc.Name, cert enc.Name) bool {
	return true
}

func (s *BasicSchema) Suggest(name enc.Name, kc ndn.KeyChain) ndn.Signer {
	for _, id := range kc.Identities() {
		if id.Name().IsPrefix(s.signingIdentity) {
			if len(id.Keys()) > 0 {
				return id.Keys()[0].Signer()
			}
		}
	}
	return signer.NewSha256Signer()
}

func (r *Repo) onBidInterest(name enc.Name, params enc.Wire, reply func(wire enc.Wire) error) {
	log.Debug(r, "onBidInterest_received", "name", name.String())

	metricReq, err := tlv.ParseMetricRequest(enc.NewWireView(params), false)
	if err != nil {
		log.Warn(r, "onBidInterest_parse_failed", "err", err)
		return
	}

	log.Info(r, "onBidInterest_processing", "target", metricReq.Target.String(), "timestamp", metricReq.Timestamp)

	incomingTimestamp := metricReq.Timestamp
	resultsName := metricReq.ResultsName
	target := metricReq.Target

	r.mu.Lock()
	ourTimestamp := r.currentAuctionTimestamp
	r.mu.Unlock()

	delay := false
	reason := ""

	if incomingTimestamp < ourTimestamp {
		delay = true
		reason = "incoming_earlier_than_ours"
	} else if incomingTimestamp > ourTimestamp {
		delay = false
		reason = "incoming_later_than_ours"
	} else {
		incomingAuctioneer := name.Prefix(2).String()
		myName := r.myNodeName()
		delay = incomingAuctioneer > myName
		if delay {
			reason = "equal_timestamp_we_win_tiebreaker"
		} else {
			reason = "equal_timestamp_we_lose_tiebreaker"
		}
	}

	if delay {
		log.Info(r, "onBidInterest_delayed", "reason", reason, "incoming", incomingTimestamp, "ours", ourTimestamp)
		r.scheduleDelayedAuction(target)
	}

	capacity, used := r.getStorageStats()
	response := &tlv.MetricResponse{
		Capacity:  capacity,
		Used:      used,
		Timestamp: incomingTimestamp,
		Delay:     delay,
	}

	if r.eventLogger != nil {
		r.eventLogger.LogAuctionBid(target.String(), name.Prefix(2).String(), capacity, used, delay)
	}

	reply(response.Encode())

	r.subscribeToResults(resultsName, target)
}

func (r *Repo) subscribeToResults(resultsName enc.Name, target enc.Name) {
	log.Debug(r, "subscribeToResults", "resultsName", resultsName.String(), "target", target.String())

	r.client.ExpressR(ndn.ExpressRArgs{
		Name: resultsName,
		Callback: func(args ndn.ExpressCallbackArgs) {
			if args.Result != ndn.InterestResultData {
				log.Warn(r, "subscribeToResults_failed", "result", args.Result)
				return
			}
			r.handleAuctionResults(args.Data, target, resultsName)
		},
	})
}

func (r *Repo) scheduleDelayedAuction(target enc.Name) {
	targetStr := target.String()
	delayDuration := 5 * time.Second

	r.mu.Lock()
	if _, exists := r.scheduledAuctions[targetStr]; exists {
		r.mu.Unlock()
		log.Info(r, "scheduleDelayedAuction_skipped", "reason", "already_scheduled", "target", targetStr)
		return
	}

	timer := time.AfterFunc(delayDuration, func() {
		r.mu.Lock()
		delete(r.scheduledAuctions, targetStr)
		r.mu.Unlock()

		currentRep := r.countReplication(target)
		log.Info(r, "scheduleDelayedAuction_executing", "target", targetStr, "delay", delayDuration.String(), "current_replication", currentRep, "rf", r.rf)

		r.runAuctionIfNeeded(target)
	})

	r.scheduledAuctions[targetStr] = timer
	r.mu.Unlock()

	log.Info(r, "scheduleDelayedAuction_scheduled", "target", targetStr, "delay", delayDuration.String())
}

func (r *Repo) handleAuctionResults(data ndn.Data, target enc.Name, resultsName enc.Name) error {
	log.Info(r, "handleAuctionResults", "target", target.String(), "resultsName", resultsName.String())

	assignment, err := tlv.ParseJobAssignment(enc.NewWireView(data.Content()), false)
	if err != nil {
		log.Warn(r, "handleAuctionResults_parse_failed", "err", err)
		return nil
	}

	targetStr := target.String()
	assignees := encNamesToStrings(assignment.Assignees)

	r.eventLogger.LogAuctionResults(targetStr, resultsName.String(), assignees)

	if len(assignment.Assignees) == 0 {
		log.Info(r, "handleAuctionResults_delayed", "target", targetStr)
		r.eventLogger.LogAuctionDelayed(targetStr, "empty_assignees")
		currentReplication := r.countReplication(target)
		if currentReplication < r.rf {
			r.checkReplicationAndRunAuction(r.getCommand(target))
		}
		return nil
	}

	if !slices.Contains(assignees, r.myNodeName()) {
		log.Info(r, "handleAuctionResults_not_assignee", "target", targetStr, "myNode", r.myNodeName())
		r.eventLogger.LogAssignmentHandled(targetStr, "auction", "skipped", "not_in_assignees", assignees)
		return nil
	}

	cmd := r.getCommand(target)
	if cmd == nil {
		log.Info(r, "handleAuctionResults_command_not_received", "target", targetStr)
		r.mu.Lock()
		r.pendingAssignments[targetStr] = assignment
		r.mu.Unlock()
		r.eventLogger.LogAssignmentHandled(targetStr, "auction", "pending", "command_not_received", assignees)
		return nil
	}

	log.Info(r, "handleAuctionResults_will_do_job", "target", targetStr, "myNode", r.myNodeName())
	if r.doJob(cmd) {
		r.publishNodeUpdate(&tlv.NodeUpdate{
			Jobs: r.getMyJobs(),
		})
	}

	currentReplication := r.countReplication(target)
	if currentReplication < r.rf {
		r.checkReplicationAndRunAuction(cmd)
	}

	return nil
}

func (r *Repo) runAuction(cmd *tlv.Command) {
	if r.distributionMechanism != "auction" {
		return
	}

	targetStr := cmd.Target.String()
	currentReplication := r.countReplication(cmd.Target)
	needed := r.rf - currentReplication

	if needed <= 0 {
		log.Info(r, "runAuction_skip", "reason", "replication_satisfied", "target", targetStr, "current", currentReplication)
		return
	}

	timestamp := uint64(time.Now().UnixNano())
	resultsName := r.nodePrefix.Append(enc.NewGenericComponent(RESULTS_SUFFIX))
	resultsName = resultsName.Append(enc.NewTimestampComponent(timestamp))

	log.Info(r, "runAuction_started", "target", targetStr, "timestamp", timestamp, "current", currentReplication, "needed", needed)

	r.mu.Lock()
	r.currentAuctionTimestamp = timestamp
	r.mu.Unlock()

	if r.eventLogger != nil {
		r.eventLogger.LogAuctionStarted(targetStr, currentReplication, needed, timestamp)
	}

	peers := r.getOtherNodeNames()
	log.Info(r, "runAuction_peers", "target", targetStr, "peers", peers)

	if len(peers) == 0 {
		log.Info(r, "runAuction_no_peers", "target", targetStr)
		r.determineAndPublishAuctionWinners(cmd, timestamp, resultsName, nil)
		return
	}

	peerMetrics := make(map[string]struct {
		capacity uint64
		used     uint64
		delay    bool
	})
	var metricsMu sync.Mutex
	responsesCh := make(chan string, len(peers))

	for _, peer := range peers {
		peerPrefix, _ := enc.NameFromStr(peer)
		bidName := peerPrefix.Append(enc.NewGenericComponent(BID_SUFFIX))
		bidName = bidName.Append(enc.NewTimestampComponent(timestamp))

		metricReq := &tlv.MetricRequest{
			Target:      cmd.Target,
			Timestamp:   timestamp,
			ResultsName: resultsName,
		}

		peerCopy := peer
		r.client.ExpressR(ndn.ExpressRArgs{
			Name: bidName,
			Config: &ndn.InterestConfig{
				CanBePrefix: false,
				MustBeFresh: true,
			},
			AppParam: metricReq.Encode(),
			Retries:  3,
			Callback: func(args ndn.ExpressCallbackArgs) {
				if args.Result != ndn.InterestResultData {
					log.Warn(r, "runAuction_bid_failed", "peer", peerCopy, "result", args.Result)
					return
				}

				metricResp, err := tlv.ParseMetricResponse(enc.NewWireView(args.Data.Content()), false)
				if err != nil {
					log.Warn(r, "runAuction_response_parse_failed", "peer", peerCopy, "err", err)
					return
				}

				metricsMu.Lock()
				peerMetrics[peerCopy] = struct {
					capacity uint64
					used     uint64
					delay    bool
				}{
					capacity: metricResp.Capacity,
					used:     metricResp.Used,
					delay:    metricResp.Delay,
				}
				metricsMu.Unlock()

				r.mu.Lock()
				if status, ok := r.nodeStatus[peerCopy]; ok {
					status.LastUpdated = time.Now()
					r.nodeStatus[peerCopy] = status
				}
				r.mu.Unlock()

				select {
				case responsesCh <- peerCopy:
				default:
				}
			},
		})
	}

	auctionTimer := time.NewTimer(r.auctionTimeout)
	receivedCount := 0
	for {
		select {
		case <-auctionTimer.C:
			log.Info(r, "runAuction_timeout", "target", targetStr, "received", receivedCount, "total_peers", len(peers))
			goto determine_winners
		case <-responsesCh:
			receivedCount++
			if receivedCount >= len(peers) {
				log.Info(r, "runAuction_all_received", "target", targetStr, "received", receivedCount)
				goto determine_winners
			}
		}
	}

determine_winners:
	r.determineAndPublishAuctionWinners(cmd, timestamp, resultsName, peerMetrics)
}

func (r *Repo) determineAndPublishAuctionWinners(cmd *tlv.Command, timestamp uint64, resultsName enc.Name, peerMetrics map[string]struct {
	capacity uint64
	used     uint64
	delay    bool
}) {
	anyDelay := false
	if peerMetrics != nil {
		for _, metrics := range peerMetrics {
			if metrics.delay {
				anyDelay = true
				break
			}
		}
	}

	if anyDelay {
		log.Info(r, "runAuction_delayed_by_peer", "target", cmd.Target.String())
		assignment := &tlv.JobAssignment{
			Target:    cmd.Target,
			Assignees: nil,
		}
		err := r.client.Store().Put(resultsName, assignment.Encode().Join())
		if err != nil {
			log.Warn(r, "runAuction_delayed_publish_failed", "err", err)
		}
		r.eventLogger.LogAuctionDelayed(cmd.Target.String(), "delayed_by_peer")

		delayDuration := 2500 * time.Millisecond
		log.Info(r, "runAuction_scheduling_retry", "target", cmd.Target.String(), "delay", delayDuration.String())
		go func() {
			time.Sleep(delayDuration)
			r.checkReplicationAndRunAuction(cmd)
		}()
		return
	}

	needed := r.rf - r.countReplication(cmd.Target)
	if needed <= 0 {
		log.Info(r, "runAuction_skip_publish", "reason", "replication_satisfied", "target", cmd.Target.String())
		return
	}

	nodeStatusCopy := make(map[string]NodeStatus)
	r.mu.Lock()
	for k, v := range r.nodeStatus {
		nodeStatusCopy[k] = v
	}
	r.mu.Unlock()

	r.mu.Lock()
	nodeStatusCopy[r.myNodeName()] = NodeStatus{
		Capacity:    r.storageCapacity,
		Used:        r.storageUsed,
		Jobs:        make([]enc.Name, len(r.jobs)),
		LastUpdated: time.Now(),
	}
	copy(nodeStatusCopy[r.myNodeName()].Jobs, r.jobs)
	r.mu.Unlock()

	if peerMetrics != nil {
		for peer, metrics := range peerMetrics {
			if status, ok := nodeStatusCopy[peer]; ok {
				status.Capacity = metrics.capacity
				status.Used = metrics.used
				nodeStatusCopy[peer] = status
			}
		}
	}

	winners := r.determineWinnersAuction(cmd, nodeStatusCopy, r.myNodeName(), r.rf)

	log.Info(r, "runAuction_winners", "target", cmd.Target.String(), "winners", winners)

	assignment := &tlv.JobAssignment{
		Target:    cmd.Target,
		Assignees: stringNamesToEncNames(winners),
	}

	log.Info(r, "runAuction_publishing_results", "target", cmd.Target.String(), "winners", winners, "resultsName", resultsName.String())

	if slices.Contains(winners, r.myNodeName()) {
		log.Info(r, "runAuction_claiming_job", "target", cmd.Target.String(), "myNode", r.myNodeName())
		r.doJob(cmd)
	}

	err := r.client.Store().Put(resultsName, assignment.Encode().Join())
	if err != nil {
		log.Warn(r, "runAuction_publish_failed", "err", err, "resultsName", resultsName.String())
	}

	r.eventLogger.LogAuctionResults(cmd.Target.String(), resultsName.String(), winners)
}

func (r *Repo) determineWinnersAuction(cmd *tlv.Command, nodeStatus map[string]NodeStatus, myName string, rf int) []string {
	return r.distributor.DetermineWinners(cmd, nodeStatus, myName, rf, r.eventLogger)
}

func (r *Repo) checkReplicationAndRunAuction(cmd *tlv.Command) {
	if r.distributionMechanism != "auction" {
		return
	}

	if cmd == nil {
		return
	}

	r.runAuctionIfNeeded(cmd.Target)
}

func (r *Repo) runAuctionIfNeeded(target enc.Name) {
	currentReplication := r.countReplication(target)
	needed := r.rf - currentReplication

	if needed <= 0 {
		return
	}

	log.Info(r, "runAuctionIfNeeded", "target", target.String(), "current", currentReplication, "needed", needed)

	cmd := r.getCommand(target)
	if cmd == nil {
		log.Info(r, "runAuctionIfNeeded_skip", "reason", "command_not_received", "target", target.String())
		return
	}

	go r.runAuction(cmd)
}
