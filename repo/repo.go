package main

import (
	_ "embed"
	"github.com/a-thieme/repo/tlv"
	"time"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/engine"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	"github.com/named-data/ndnd/std/object"
	local_storage "github.com/named-data/ndnd/std/object/storage"
	sec "github.com/named-data/ndnd/std/security"
	"github.com/named-data/ndnd/std/security/keychain"
	"github.com/named-data/ndnd/std/security/signer"
	"github.com/named-data/ndnd/std/sync"
)

const NOTIFY = "notify"
const HEARTBEAT_TIME = 5 * time.Second

var testbedRootName, _ = enc.NameFromStr("/ndn/KEY/%27%C4%B2%2A%9F%7B%81%27/ndn/v=1651246789556")

//go:embed testbed-root.decoded
var testbedRootCert []byte

type NodeStatus struct {
	Capacity    uint64
	Used        uint64
	LastUpdated time.Time
}

type Repo struct {
	groupPrefix  enc.Name
	notifyPrefix *enc.Name
	nodePrefix   enc.Name

	engine ndn.Engine
	store  ndn.Store
	client ndn.Client

	groupSync *sync.SvsALO

	// all commands
	commands []*tlv.Command
	// a node's active jobs
	jobs []*tlv.Command

	nodeStatus map[string]NodeStatus
}

func NewRepo(groupPrefix string, nodePrefix string) *Repo {
	gp, _ := enc.NameFromStr(groupPrefix)
	np, _ := enc.NameFromStr(nodePrefix)
	nf := gp.Append(enc.NewGenericComponent(NOTIFY))

	return &Repo{
		groupPrefix:  gp,
		notifyPrefix: &nf,
		nodePrefix:   np,
		nodeStatus:   make(map[string]NodeStatus),
	}
}

func (r *Repo) String() string {
	return "repo"
}

func (r *Repo) Start() (err error) {
	log.Info(r, "starting")

	log.Debug(r, "make engine")
	r.engine = engine.NewBasicEngine(engine.NewDefaultFace())
	if err = r.engine.Start(); err != nil {
		return err
	}

	// FIXME: use badger store in the deployed version for persistent storage
	// only using memory for testing
	log.Debug(r, "new store")
	r.store = local_storage.NewMemoryStore()

	kc, err := keychain.NewKeyChain("dir:///home/adam/.ndn/keys", r.store)
	if err != nil {
		return err
	}

	// Create Trust Config
	schema := &BasicSchema{}

	// We trust the Testbed Root (bootstrapped above) and our own key
	caData, _, err := r.engine.Spec().ReadData(enc.NewBufferView(testbedRootCert))
	if err != nil {
		return err
	}

	trust, err := sec.NewTrustConfig(kc, schema, []enc.Name{caData.Name()})
	if err != nil {
		return err
	}
	trust.UseDataNameFwHint = true

	// ---------------------------------------------------------
	// Application Start
	// ---------------------------------------------------------

	log.Debug(r, "new client")
	r.client = object.NewClient(r.engine, r.store, trust)

	log.Info(r, "starting sync", "group", r.groupPrefix)
	r.groupSync, err = sync.NewSvsALO(sync.SvsAloOpts{
		Name: r.nodePrefix,
		Svs: sync.SvSyncOpts{
			Client:       r.client,
			GroupPrefix:  r.groupPrefix,
			SyncDataName: r.nodePrefix,
		},
		Snapshot: &sync.SnapshotNull{},
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

	log.Debug(r, "announce", "prefix", r.groupSync.GroupPrefix())
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.groupSync.GroupPrefix(),
		Expose: true,
	})
	log.Debug(r, "announce", "prefix", r.groupSync.DataPrefix())
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.groupSync.DataPrefix(),
		Expose: true,
	})
	// client notification
	log.Debug(r, "announce", "prefix", r.notifyPrefix)
	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.notifyPrefix.Clone(),
		Expose: true,
	})
	log.Debug(r, "attach command handler")
	r.client.AttachCommandHandler(*r.notifyPrefix, r.onCommand)

	err = r.client.Start()
	if err != nil {
		return err
	}
	go r.runHeartbeat()
	return nil

}

func (r *Repo) runHeartbeat() {
	// Publish status every 30 seconds
	ticker := time.NewTicker(HEARTBEAT_TIME)
	defer ticker.Stop()

	for range ticker.C {
		// Send an update with NO new command (just status)
		if err := r.publishNodeUpdate(nil); err != nil {
			log.Warn(r, "heartbeat failed", "err", err)
		}
	}
}

func (r *Repo) onCommand(name enc.Name, content enc.Wire, reply func(wire enc.Wire) error) {
	log.Info(r, "got command")
	cmd, err := tlv.ParseCommand(enc.NewWireView(content), false)
	if err != nil {
		return
	}

	r.commands = append(r.commands, cmd)
	log.Debug(r, "parsed command", "target", cmd.Target)

	response := tlv.StatusResponse{
		Target: cmd.Target,
		Status: "received",
	}

	reply(response.Encode())

	log.Debug(r, "publish to group")
	r.publishNodeUpdate(cmd)
}

func (r *Repo) getStorageStats() (capacity uint64, used uint64) {
	// Example: Mock values (10GB Capacity, 500MB Used)
	return 1024 * 1024 * 1024 * 10, 1024 * 1024 * 500
}

func (r *Repo) publishNodeUpdate(newCmd *tlv.Command) error {
	capacity, used := r.getStorageStats()

	update := &tlv.NodeUpdate{
		Jobs:            r.jobs,
		StorageCapacity: capacity,
		StorageUsed:     used,
	}

	if newCmd != nil {
		update.NewCommands = []*tlv.Command{newCmd}
	}

	log.Info(r, "publishing node update",
		"jobs", len(update.Jobs),
		"new", len(update.NewCommands),
		"storage_used", update.StorageUsed,
		"storage_capacity", update.StorageCapacity,
	)

	_, _, err := r.groupSync.Publish(update.Encode())
	if err != nil {
		log.Error(r, "failed to publish node update", "err", err)
		return err
	}
	return nil
}

func (r *Repo) onGroupSync(pub sync.SvsPub) {
	if len(pub.Content) == 0 {
		return
	}

	update, err := tlv.ParseNodeUpdate(enc.NewWireView(pub.Content), false)
	if err != nil {
		log.Warn(r, "failed to parse node update", "name", pub.DataName, "err", err)
		return
	}

	publisherName := pub.Publisher.String()
	r.nodeStatus[publisherName] = NodeStatus{
		Capacity:    update.StorageCapacity,
		Used:        update.StorageUsed,
		LastUpdated: time.Now(),
	}

	var usagePct float64 = 0
	if update.StorageCapacity > 0 {
		usagePct = (float64(update.StorageUsed) / float64(update.StorageCapacity)) * 100
	}

	log.Info(r, "received node update",
		"from", pub.Publisher,
		"seq", pub.SeqNum,
		"jobs", len(update.Jobs),
		"new_cmds", len(update.NewCommands),
		"storage_pct", usagePct,
	)

	log.Debug(r, "repo status", "known_nodes", len(r.nodeStatus))

	for _, job := range update.Jobs {
		log.Debug(r, "claimed jobs",
			"node", pub.Publisher,
			"target", job.Target,
		)
	}

	for _, cmd := range update.NewCommands {
		log.Info(r, "new command",
			"node", pub.Publisher,
			"target", cmd.Target,
			"type", cmd.Type,
		)
	}
}

// BasicSchema allows all data and suggests the first matching key in the keychain.
type BasicSchema struct{}

func (s *BasicSchema) Check(pkt enc.Name, cert enc.Name) bool {
	return true // Trust everything (matching NullSchema behavior)
}

func (s *BasicSchema) Suggest(name enc.Name, kc ndn.KeyChain) ndn.Signer {
	myname, _ := enc.NameFromStr("/ndn/repo.teame.dev/repo")
	for _, id := range kc.Identities() {
		if id.Name().IsPrefix(myname) {
			if len(id.Keys()) > 0 {
				return id.Keys()[0].Signer()
			}
		}
	}

	return signer.NewSha256Signer()
}
