package main

import (
	_ "embed"
	"math/rand/v2"
	"sync"
	"time"

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

// constants
const NOTIFY = "notify"
const HEARTBEAT_TIME = 5 * time.Second

//go:embed testbed-root.decoded
var testbedRootCert []byte

// structs
type Repo struct {
	groupPrefix  enc.Name
	notifyPrefix *enc.Name
	nodePrefix   enc.Name

	engine ndn.Engine
	store  ndn.Store
	client ndn.Client

	groupSync *svs.SvsALO

	mu sync.Mutex

	nodeStatus map[string]NodeStatus
	commands   map[*enc.Name]*tlv.Command

	storageCapacity uint64
	storageUsed     uint64

	rf int
}

type NodeStatus struct {
	Capacity    uint64
	Used        uint64
	LastUpdated time.Time
	Jobs        []enc.Name
}

// utilities
func (r *Repo) String() string {
	return "repo"
}

func (r *Repo) getStorageStats() (capacity uint64, used uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storageCapacity, r.storageUsed
}

func (r *Repo) addCommand(cmd *tlv.Command) {
	r.mu.Lock()
	r.commands[&cmd.Target] = cmd
	r.mu.Unlock()
}

func (r *Repo) calculateReplication(cmd *tlv.Command) int {
	target := cmd.Target
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0
	for _, status := range r.nodeStatus {
		for _, jobTarget := range status.Jobs {
			if jobTarget.Equal(target) {
				count++
				break
			}
		}
	}

	return count
}

func (r *Repo) getMyJobs() []enc.Name {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.nodeStatus["mine"].Jobs
}

func (r *Repo) getCommmandForTarget(target enc.Name) tlv.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return *r.commands[&target]
}

func (r *Repo) getMyCommands() []tlv.Command {
	myJobs := r.getMyJobs()
	commands := make([]tlv.Command, len(myJobs))
	for index, target := range myJobs {
		commands[index] = r.getCommmandForTarget(target)
	}
	return commands
}

func (r *Repo) publishNodeUpdate() {
	r.publishCommand(nil)
}

func (r *Repo) publishCommand(newCmd *tlv.Command) {
	capacity, used := r.getStorageStats()

	myJobs := r.getMyJobs()
	update := &tlv.NodeUpdate{
		Jobs:            myJobs,
		StorageCapacity: capacity,
		StorageUsed:     used,
		NewCommand:      newCmd,
	}

	_, _, err := r.groupSync.Publish(update.Encode())
	if err != nil {
		log.Fatal(r, "node_update_pub_failed", "err", err)
	}
}

// initialization
func NewRepo(groupPrefix string, nodePrefix string, replicationFactor int) *Repo {
	gp, _ := enc.NameFromStr(groupPrefix)
	np, _ := enc.NameFromStr(nodePrefix)
	nf := gp.Append(enc.NewGenericComponent(NOTIFY))

	return &Repo{
		groupPrefix:  gp,
		notifyPrefix: &nf,
		nodePrefix:   np,
		nodeStatus:   make(map[string]NodeStatus),
		rf:           replicationFactor,
	}
}

func (r *Repo) Start() (err error) {
	log.Info(r, "repo_start")

	r.storageCapacity = (10 * 1024 * 1024 * 1024) + uint64(rand.Int64N(5*1024*1024*1024))
	r.storageUsed = uint64(rand.Int64N(100 * 1024 * 1024))

	r.engine = engine.NewBasicEngine(engine.NewDefaultFace())
	if err = r.engine.Start(); err != nil {
		return err
	}

	// TODO: use badger store in the deployed version for persistent storage
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

	r.groupSync, err = svs.NewSvsALO(svs.SvsAloOpts{
		Name: r.nodePrefix,
		Svs: svs.SvSyncOpts{
			Client:       r.client,
			GroupPrefix:  r.groupPrefix,
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

// FIXME: separate the storage ticker from heartbeat ticker
func (r *Repo) runHeartbeat() {
	ticker := time.NewTicker(HEARTBEAT_TIME)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		for _, job := range r.getMyCommands() {
			if job.Type == "JOIN" {
				r.storageUsed += uint64(rand.Int64N(10 * 1024 * 1024))
			}
		}
		if r.storageUsed > r.storageCapacity {
			// TODO: release job
			r.storageUsed = r.storageCapacity
		}
		r.mu.Unlock()

		r.publishNodeUpdate()
	}
}

// handle external actions
func (r *Repo) onCommand(name enc.Name, content enc.Wire, reply func(wire enc.Wire) error) {
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
	r.publishCommand(cmd)
	r.replicate(cmd)
}

func (r *Repo) onGroupSync(pub svs.SvsPub) {
	update, err := tlv.ParseNodeUpdate(enc.NewWireView(pub.Content), false)
	if err != nil {
		log.Warn(r, "node_update_parse_failed", "name", pub.DataName, "err", err)
		return
	}

	publisherName := pub.Publisher.String()

	r.mu.Lock()
	// FIXME: move to utility that updates the node and returns any job that changed from the previous one
	// for now, it can just do the negative deltas, since we are not worried about over-replication
	// then, for each of these negative deltas, call replicate
	r.nodeStatus[publisherName] = NodeStatus{
		Capacity:    update.StorageCapacity,
		Used:        update.StorageUsed,
		LastUpdated: time.Now(),
		Jobs:        update.Jobs,
	}
	r.mu.Unlock()
}

// actions/logic
func (r *Repo) replicate(cmd *tlv.Command) {
	if r.calculateReplication(cmd) >= r.rf { // good or over
		return
	}
	// under-replicated
	if r.shouldClaimJobHydra(cmd) {
		// FIXME: add job to node status and do the below part in that utility
		if cmd.Type == "INSERT" {
			cost := uint64(rand.Int64N(500 * 1024 * 1024))
			r.storageUsed += cost
			if r.storageUsed > r.storageCapacity {
				r.storageUsed = r.storageCapacity
			}
		}
		r.publishNodeUpdate()

	}

}

func (r *Repo) shouldClaimJobHydra(cmd *tlv.Command) bool {
	// FIXME: do this logic:
	// 0) let n be the amount of additional times the command needs to be done to get to r.rl
	// 1) sort all nodes by availability, first being the most available
	// 2) iterate through the first n nodes that aren't doing the command.
	// 2a) if we are any of the nodes listed in (2), return true

	return false
}

// because I didn't write a trust schema
type BasicSchema struct{}

func (s *BasicSchema) Check(pkt enc.Name, cert enc.Name) bool {
	return true
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
