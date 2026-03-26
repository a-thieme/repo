package main

import (
	_ "embed"
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

type Repo struct {
	groupPrefix     enc.Name
	notifyPrefix    *enc.Name
	nodePrefix      enc.Name
	signingIdentity enc.Name

	engine ndn.Engine
	store  ndn.Store
	client ndn.Client

	groupSync              *svs.SvsALO
	auctionHeartbeatSvSync *svs.SvSync

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

	auctionTimeout          time.Duration
	currentAuctionTimestamp uint64

	pendingAssignments map[string]*tlv.JobAssignment

	scheduledRedistributions map[string]*time.Timer
	redistMu                 sync.Mutex

	retryDelays map[string]time.Duration
	retryMu     sync.Mutex

	eventLogger  util.Logger
	countingFace *util.CountingFace
}

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

	r := &Repo{
		groupPrefix:              gp,
		notifyPrefix:             &nf,
		nodePrefix:               np,
		signingIdentity:          si,
		nodeStatus:               make(map[string]NodeStatus),
		commands:                 make(map[string]*tlv.Command),
		jobs:                     make([]enc.Name, 0), // FIXME: remove this, and use nodeStatus[myNodeName].jobs instead; keep things together
		jobStorageUsage:          make(map[string]uint64),
		rf:                       replicationFactor,
		noRelease:                noRelease,
		maxJoinGrowthRate:        maxJoinGrowthRate,
		heartbeatInterval:        heartbeatInterval,
		distributionMechanism:    distributionMechanism,
		eventLogger:              eventLogger,
		pendingAssignments:       make(map[string]*tlv.JobAssignment),
		scheduledRedistributions: make(map[string]*time.Timer),
		retryDelays:              make(map[string]time.Duration),
	}

	r.distributor = NewDistributionMechanism(r, distributionMechanism)

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

	face := engine.NewDefaultFace()

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
	r.distributor.AttachHandlers(r.client, bidPrefix)

	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   resultsPrefix,
		Expose: true,
	})

	// FIXME: put this inside of auction.go in a distributor.Start() function
	if r.distributionMechanism == "auction" {
		heartbeatGroupPrefix := r.groupPrefix.Append(enc.NewGenericComponent(HEARTBEAT_SUFFIX))
		r.auctionHeartbeatSvSync = svs.NewSvSync(svs.SvSyncOpts{
			Client:      r.client,
			GroupPrefix: heartbeatGroupPrefix,
			OnUpdate: func(update svs.SvSyncUpdate) {
				r.handleAuctionHeartbeatSvSyncUpdate(update)
			},
			SyncDataName: r.nodePrefix,
		})
		r.client.AnnouncePrefix(ndn.Announcement{
			Name:   heartbeatGroupPrefix,
			Expose: true,
		})
		if err := r.auctionHeartbeatSvSync.Start(); err != nil {
			return err
		}
	}

	if r.client.AttachCommandHandler(*r.notifyPrefix, r.onCommand) == nil {
		log.Error(r, "AttachCommandHandler", "failed", r.notifyPrefix)
	}

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
	r.distributor.OnHeartbeatTick()

	for range ticker.C {
		r.publishNodeUpdate(nil)
		r.checkStaleNodes()
		r.distributor.OnHeartbeatTick()
	}
}

// FIXME: this shouldn't work this way. each node should have its own timer set to
// HEARTBEAT_TIMEOUT and we call r.heartbeats[node].Reset() when we see an update for it.
// the reset should call those OnNodeDead functions
// after this, we can remove the LastUpdated field
func (r *Repo) checkStaleNodes() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	for nodeName, status := range r.nodeStatus {
		if nodeName == r.myNodeName() {
			continue
		}
		elapsed := now.Sub(status.LastUpdated)
		if elapsed > HEARTBEAT_TIMEOUT {
			log.Info(r, "stale_node_detected", "node", nodeName, "elapsed", elapsed.String(), "jobs", len(status.Jobs))
			r.eventLogger.LogNodeDetectedDead(nodeName, len(status.Jobs))
			// FIXME: this should either remove the node from r.nodeStatus or we should have an "alive" field in
			// r.nodeStatus so we don't count its jobs. we likely don't need the nodeName in the distributor's onNodeDead() call
			r.distributor.OnNodeDead(nodeName, status.Jobs)
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
	log.Info(r, "onCommand_received")

	cmd, err := tlv.ParseCommand(enc.NewWireView(content), false)
	if err != nil {
		log.Warn(r, "command_parse_failed", "err", err)
		return
	}

	response := tlv.StatusResponse{
		Target: cmd.Target,
		Status: "received",
	}
	if reply(response.Encode()) == nil {
		log.Error(r, "commandReply", "failed", name)
	}

	r.addCommand(cmd)
	r.eventLogger.LogCommandReceived(cmd.Type, cmd.Target.String())

	nodeUpdate := r.distributor.OnCommand(cmd)
	nodeUpdate.NewCommand = cmd
	r.publishNodeUpdate(nodeUpdate)
}

func (r *Repo) onGroupSync(pub svs.SvsPub) {
	update, err := tlv.ParseNodeUpdate(enc.NewWireView(pub.Content), false)
	if err != nil {
		log.Warn(r, "node_update_parse_failed", "name", pub.DataName, "err", err)
		return
	}

	publisherName := pub.Publisher.String()
	log.Info(r, "onGroupSync_received", "publisher", publisherName, "jobs", len(update.Jobs), "newCmd", update.NewCommand != nil)
	r.updateNodeStatus(publisherName, update)
	r.eventLogger.LogNodeUpdate(publisherName, update.Jobs, update.StorageCapacity, update.StorageUsed)

	if update.NewCommand != nil {
		cmd := update.NewCommand
		r.addCommand(cmd)
		r.eventLogger.LogCommandSynced(update.NewCommand.Type, update.NewCommand.Target.String(), publisherName)
		r.checkPendingAssignment(cmd)
		r.scheduleReevaluationLoop(update.NewCommand.Target)
	}
	if len(update.JobAssignments) > 0 {
		r.processJobAssignments(update.JobAssignments)
	}
}

// FIXME: this can be moved to shared or helper
func (r *Repo) updateNodeStatus(publisher string, update *tlv.NodeUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nodeStatus[publisher] = NodeStatus{
		Capacity:    update.StorageCapacity,
		Used:        update.StorageUsed,
		LastUpdated: time.Now(),
		Jobs:        update.Jobs,
	}
	// FIXME: should evaluate any jobs that change from the previous node status to now
	// for example, if Jobs was A,B,C before and now B,C,D, it should check both A and D
}

func (r *Repo) handleHeartbeatUpdate(update svs.SvSyncUpdate) {
	nodeName := update.Name.String()
	r.mu.Lock()
	defer r.mu.Unlock()

	if status, exists := r.nodeStatus[nodeName]; exists {
		status.LastUpdated = time.Now()
		r.nodeStatus[nodeName] = status
	} else {
		r.nodeStatus[nodeName] = NodeStatus{
			LastUpdated: time.Now(),
		}
	}
}

// FIXME: this should be handled inside auction
func (r *Repo) handleAuctionHeartbeatSvSyncUpdate(update svs.SvSyncUpdate) {
	r.handleHeartbeatUpdate(update)
}

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
