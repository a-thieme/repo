package main

import (
	_ "embed"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
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
const STORAGE_TICK_TIME = 1 * time.Second

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
	commands   map[string]*tlv.Command

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
	defer r.mu.Unlock()
	r.commands[cmd.Target.String()] = cmd
}

// Internal helper: assumes lock is held
func (r *Repo) getCommandInternal(target enc.Name) *tlv.Command {
	return r.commands[target.String()]
}

// Internal helper: assumes lock is held
func (r *Repo) calculateReplication(target enc.Name) int {
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
	src := r.nodeStatus["mine"].Jobs
	dst := make([]enc.Name, len(src))
	copy(dst, src)
	return dst
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

	r := &Repo{
		groupPrefix:  gp,
		notifyPrefix: &nf,
		nodePrefix:   np,
		nodeStatus:   make(map[string]NodeStatus),
		commands:     make(map[string]*tlv.Command),
		rf:           replicationFactor,
	}

	r.nodeStatus["mine"] = NodeStatus{
		Jobs: make([]enc.Name, 0),
	}

	return r
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
	go r.runStorageSimulation()
	return nil
}

func (r *Repo) runHeartbeat() {
	ticker := time.NewTicker(HEARTBEAT_TIME)
	defer ticker.Stop()

	for range ticker.C {
		r.publishNodeUpdate()
	}
}

func (r *Repo) runStorageSimulation() {
	ticker := time.NewTicker(STORAGE_TICK_TIME)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		myStatus := r.nodeStatus["mine"]
		for _, target := range myStatus.Jobs {
			cmd := r.getCommandInternal(target)
			if cmd != nil && cmd.Type == "JOIN" {
				r.storageUsed += uint64(rand.Int64N(10 * 1024 * 1024))
			}
		}
		r.mu.Unlock()
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
	droppedJobs := r.updateNodeStatus(publisherName, update)

	for _, target := range droppedJobs {
		// We need the full command object to replicate
		r.mu.Lock()
		cmd := r.getCommandInternal(target)
		r.mu.Unlock()

		if cmd != nil {
			log.Info(r, "job_dropped_by_peer_checking_replication", "peer", publisherName, "target", target)
			r.replicate(cmd)
		}
	}
}

// Utility to update node and return negative deltas (dropped jobs)
func (r *Repo) updateNodeStatus(publisher string, update *tlv.NodeUpdate) []enc.Name {
	r.mu.Lock()
	defer r.mu.Unlock()

	oldStatus, exists := r.nodeStatus[publisher]
	var oldJobs []enc.Name
	if exists {
		oldJobs = oldStatus.Jobs
	}

	r.nodeStatus[publisher] = NodeStatus{
		Capacity:    update.StorageCapacity,
		Used:        update.StorageUsed,
		LastUpdated: time.Now(),
		Jobs:        update.Jobs,
	}

	// Calculate negative deltas (jobs present in old but missing in new)
	dropped := make([]enc.Name, 0)
	for _, oldJob := range oldJobs {
		stillExists := slices.ContainsFunc(update.Jobs, func(n enc.Name) bool {
			return oldJob.Equal(n)
		})
		if !stillExists {
			dropped = append(dropped, oldJob)
		}
	}

	return dropped
}

// actions/logic
func (r *Repo) replicate(cmd *tlv.Command) {
	if r.calculateReplication(cmd.Target) >= r.rf {
		return
	}

	// under-replicated
	if r.shouldClaimJobHydra(cmd) {
		r.claimJob(cmd)
	}
}

func (r *Repo) claimJob(cmd *tlv.Command) {
	r.mu.Lock()

	myStatus := r.nodeStatus["mine"]

	// Add job
	myStatus.Jobs = append(myStatus.Jobs, cmd.Target)

	// Apply immediate cost
	if cmd.Type == "INSERT" {
		cost := uint64(rand.Int64N(500 * 1024 * 1024))
		r.storageUsed += cost
		if r.storageUsed > r.storageCapacity {
			r.storageUsed = r.storageCapacity
		}
		myStatus.Used = r.storageUsed
		myStatus.Capacity = r.storageCapacity
	}

	r.nodeStatus["mine"] = myStatus
	r.mu.Unlock()
	r.publishNodeUpdate()
}

func (r *Repo) shouldClaimJobHydra(cmd *tlv.Command) bool {

	currentReplication := r.calculateReplication(cmd.Target)

	needed := r.rf - currentReplication
	if needed <= 0 {
		return false
	}

	r.mu.Lock()
	candidates := make([]string, 0, len(r.nodeStatus))

	for name, status := range r.nodeStatus {
		isDoing := false
		for _, job := range status.Jobs {
			if job.Equal(cmd.Target) {
				isDoing = true
				break
			}
		}

		if !isDoing {
			candidates = append(candidates, name)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		// Calculate free space for i
		// FIXME: create a utility for getting the free space of a node so we don't have to keep doing the same calculation over and over
		statusI := r.nodeStatus[candidates[i]]
		freeI := statusI.Capacity - statusI.Used

		// Calculate free space for j
		statusJ := r.nodeStatus[candidates[j]]
		freeJ := statusJ.Capacity - statusJ.Used

		if freeI != freeJ {
			return freeI > freeJ
		}
		return strings.Compare(candidates[i], candidates[j]) > 0
	})

	r.mu.Unlock()

	limit := min(needed, len(candidates))

	// FIXME: for loop can be modernized using range over int
	for i := 0; i < limit; i++ {
		if candidates[i] == "mine" {
			return true
		}
	}

	return false
}

// BasicSchema allows all data and suggests the first matching key in the keychain.
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

