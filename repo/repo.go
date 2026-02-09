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

	commands []*tlv.Command
	jobs     []*tlv.Command

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
	log.Info(r, "repo_start")

	r.engine = engine.NewBasicEngine(engine.NewDefaultFace())
	if err = r.engine.Start(); err != nil {
		return err
	}

	// FIXME: use badger store in the deployed version for persistent storage
	r.store = local_storage.NewMemoryStore()

	kc, err := keychain.NewKeyChain("dir:///home/adam/.ndn/keys", r.store)
	if err != nil {
		return err
	}

	schema := &BasicSchema{}

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

	r.client.AnnouncePrefix(ndn.Announcement{
		Name:   r.groupSync.GroupPrefix(),
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
	r.client.AttachCommandHandler(*r.notifyPrefix, r.onCommand)

	err = r.client.Start()
	if err != nil {
		return err
	}
	go r.runHeartbeat()
	return nil
}

func (r *Repo) runHeartbeat() {
	ticker := time.NewTicker(HEARTBEAT_TIME)
	defer ticker.Stop()

	for range ticker.C {
		// Send an update with no new command to act as a heartbeat
		if err := r.publishNodeUpdate(nil); err != nil {
			log.Warn(r, "heartbeat_failed", "err", err)
		}
	}
}

func (r *Repo) onCommand(name enc.Name, content enc.Wire, reply func(wire enc.Wire) error) {
	cmd, err := tlv.ParseCommand(enc.NewWireView(content), false)
	if err != nil {
		log.Warn(r, "command_parse_failed", "err", err)
		return
	}

	log.Info(r, "command_recv", "target", cmd.Target, "type", cmd.Type)

	r.commands = append(r.commands, cmd)

	response := tlv.StatusResponse{
		Target: cmd.Target,
		Status: "received",
	}

	reply(response.Encode())

	r.publishNodeUpdate(cmd)
}

func (r *Repo) getStorageStats() (capacity uint64, used uint64) {
	// TODO: Replace with actual syscall.Statfs or store queries
	return 1024 * 1024 * 1024 * 10, 1024 * 1024 * 500 // 500MB/10GB
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

	log.Info(r, "node_update_pub",
		"jobs", len(update.Jobs),
		"new_cmds", len(update.NewCommands),
		"storage_used", update.StorageUsed,
		"storage_capacity", update.StorageCapacity,
	)

	_, _, err := r.groupSync.Publish(update.Encode())
	if err != nil {
		log.Error(r, "node_update_pub_failed", "err", err)
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
		log.Warn(r, "node_update_parse_failed", "name", pub.DataName, "err", err)
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

	log.Info(r, "node_update_recv",
		"from", pub.Publisher,
		"seq", pub.SeqNum,
		"jobs", len(update.Jobs),
		"new_cmds", len(update.NewCommands),
		"storage_pct", usagePct,
	)

	log.Debug(r, "cluster_status", "known_nodes", len(r.nodeStatus))

	for _, job := range update.Jobs {
		log.Debug(r, "peer_job_active",
			"node", pub.Publisher,
			"target", job.Target,
		)
	}

	for _, cmd := range update.NewCommands {
		log.Info(r, "peer_new_cmd",
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

