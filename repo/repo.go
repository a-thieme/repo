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
	notifyPrefix    enc.Name
	nodePrefix      enc.Name
	signingIdentity enc.Name

	engine ndn.Engine
	store  ndn.Store
	client ndn.Client

	groupSync *svs.SvsALO

	mu sync.Mutex

	nodeStatus map[string]NodeStatus
	commands   map[string]*tlv.Command

	jobStorageUsage map[string]uint64

	rf                int
	maxJoinGrowthRate uint64
	heartbeatInterval time.Duration

	distributionMechanism string
	distributor           DistributionMechanism

	auctionTimeout time.Duration

	pendingAssignments map[string]*tlv.JobAssignment

	scheduledRedistributions map[string]*time.Timer
	redistMu                 sync.Mutex

	heartbeats  map[string]*time.Timer
	heartbeatMu sync.Mutex

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
		notifyPrefix:             nf,
		nodePrefix:               np,
		signingIdentity:          si,
		nodeStatus:               make(map[string]NodeStatus),
		commands:                 make(map[string]*tlv.Command),
		jobStorageUsage:          make(map[string]uint64),
		rf:                       replicationFactor,
		maxJoinGrowthRate:        maxJoinGrowthRate,
		heartbeatInterval:        heartbeatInterval,
		auctionTimeout:           auctionTimeout,
		distributionMechanism:    distributionMechanism,
		eventLogger:              eventLogger,
		pendingAssignments:       make(map[string]*tlv.JobAssignment),
		scheduledRedistributions: make(map[string]*time.Timer),
		heartbeats:               make(map[string]*time.Timer),
	}

	r.distributor = NewDistributionMechanism(r, distributionMechanism)

	return r
}

func (r *Repo) Start() (err error) {
	log.Info(r, "repo_start")

	storageCapacity := (10 * 1024 * 1024 * 1024) + (hashFromString(r.nodePrefix.String()) % (5 * 1024 * 1024 * 1024))
	storageUsed := (hashFromString(r.nodePrefix.String()) % (100 * 1024 * 1024))

	r.mu.Lock()
	r.nodeStatus[r.myNodeName()] = NodeStatus{
		Capacity:    storageCapacity,
		Used:        storageUsed,
		Jobs:        []enc.Name{},
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

	if err := r.distributor.Start(r.client, r.groupPrefix); err != nil {
		return err
	}

	if r.client.AttachCommandHandler(r.notifyPrefix, r.onNewCommandFromProducer) != nil {
		log.Error(r, "AttachCommandHandler", "failed", r.notifyPrefix)
	}

	err = r.client.Start()
	if err != nil {
		return err
	}

	go r.runStorageSimulation()
	return nil
}

func (r *Repo) runStorageSimulation() {
	ticker := time.NewTicker(STORAGE_TICK_TIME)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		status := r.nodeStatus[r.myNodeName()]
		for _, target := range status.Jobs {
			cmd := r.getCommandInternal(target)
			if cmd != nil && cmd.Type == "JOIN" {
				jobKey := target.String()
				if r.jobStorageUsage == nil {
					r.jobStorageUsage = make(map[string]uint64)
				}
				growth := (hashFromString(cmd.Target.String()) % r.maxJoinGrowthRate)
				r.jobStorageUsage[jobKey] += growth
				status.Used += growth
				r.nodeStatus[r.myNodeName()] = status
				r.eventLogger.LogStorageChanged(status.Used, growth)
			}
		}
		r.mu.Unlock()
	}
}

func (r *Repo) onNewCommandFromProducer(name enc.Name, content enc.Wire, reply func(wire enc.Wire) error) {
	log.Info(r, "onCommand_received")

	cmd, err := tlv.ParseCommand(enc.NewWireView(content), false)
	if err != nil {
		log.Warn(r, "command_parse_failed", "err", err)
		return
	}

	log.Debug(r, "command_parsed", "type", cmd.Type, "target", cmd.Target.String())

	response := tlv.StatusResponse{
		Target: cmd.Target,
		Status: "received",
	}
	if reply(response.Encode()) != nil {
		log.Error(r, "commandReply", "failed", name)
	}

	r.addCommand(cmd)
	log.Debug(r, "command_added", "target", cmd.Target.String())
	r.eventLogger.LogCommandReceived(cmd.Type, cmd.Target.String())

	nodeUpdate := r.distributor.OnCommand(cmd)
	if nodeUpdate == nil {
		nodeUpdate = &tlv.NodeUpdate{}
	}
	nodeUpdate.NewCommand = cmd
	r.publishUpdate(nodeUpdate)
}

func (r *Repo) onGroupSync(pub svs.SvsPub) {
	update, err := tlv.ParseNodeUpdate(enc.NewWireView(pub.Content), false)
	if err != nil {
		log.Warn(r, "node_update_parse_failed", "name", pub.DataName, "err", err)
		return
	}

	publisherName := pub.Publisher.String()
	log.Info(r, "onGroupSync_received", "publisher", publisherName, "jobs", len(update.Jobs), "newCmd", update.NewCommand != nil)
	log.Debug(r, "groupSync_update_received", "publisher", publisherName, "jobs", len(update.Jobs))

	r.updateNodeStatus(publisherName, update)
	r.resetHeartbeatTimer(publisherName)
	r.eventLogger.LogNodeUpdate(publisherName, update.Jobs, update.StorageCapacity, update.StorageUsed)

	if update.NewCommand != nil {
		cmd := update.NewCommand
		log.Debug(r, "groupSync_processing_newCommand", "target", update.NewCommand.Target.String())
		r.addCommand(cmd)
		r.eventLogger.LogCommandSynced(update.NewCommand.Type, update.NewCommand.Target.String(), publisherName)
		r.checkPendingAssignment(cmd)
		r.scheduleReevaluationLoop(update.NewCommand.Target)
	}
	if len(update.JobAssignments) > 0 {
		log.Debug(r, "groupSync_processing_assignments", "count", len(update.JobAssignments))
		r.ProcessJobAssignments(update.JobAssignments)
	}
}

func (r *Repo) Close() error {
	r.heartbeatMu.Lock()
	for name, t := range r.heartbeats {
		t.Stop()
		delete(r.heartbeats, name)
	}
	r.heartbeatMu.Unlock()

	r.redistMu.Lock()
	for target, t := range r.scheduledRedistributions {
		t.Stop()
		delete(r.scheduledRedistributions, target)
	}
	r.redistMu.Unlock()

	if r.distributor != nil {
		r.distributor.Stop()
	}

	if r.groupSync != nil {
		r.groupSync.Stop()
	}

	if r.client != nil {
		r.client.Stop()
	}

	if r.engine != nil {
		r.engine.Stop()
	}

	if r.countingFace != nil {
		r.countingFace.Close()
	}

	return nil
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
