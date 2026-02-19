package main

import (
	_ "embed"
	"slices"
	"sort"
	"strings"
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

// constants
const NOTIFY = "notify"
const HEARTBEAT_TIME = 5 * time.Second
const STORAGE_TICK_TIME = 1 * time.Second

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

	groupSync *svs.SvsALO

	mu sync.Mutex

	nodeStatus map[string]NodeStatus
	commands   map[string]*tlv.Command

	storageCapacity uint64
	storageUsed     uint64
	jobStorageUsage map[string]uint64

	rf int

	eventLogger  *util.EventLogger
	countingFace *util.CountingFace
}

type NodeStatus struct {
	Capacity          uint64
	Used              uint64
	LastUpdated       time.Time
	Jobs              []enc.Name
	MissingHeartbeats int
}

// utilities
func (ns NodeStatus) FreeSpace() uint64 {
	if ns.Used >= ns.Capacity {
		return 0
	}
	return ns.Capacity - ns.Used
}

func (r *Repo) String() string {
	return "repo"
}

func (r *Repo) myNodeName() string {
	return r.nodePrefix.String()
}

func (r *Repo) SetEventLogger(logger *util.EventLogger) {
	r.eventLogger = logger
}

func (r *Repo) GetCountingFace() *util.CountingFace {
	return r.countingFace
}

func hashFromString(s string) uint64 {
	h := 0
	for _, c := range s {
		h = 31*h + int(c)
	}
	return uint64(h)
}

func (r *Repo) countReplication(target enc.Name) int {
	count := 0
	for _, status := range r.nodeStatus {
		for _, job := range status.Jobs {
			if job.Equal(target) {
				count++
				break
			}
		}
	}
	return count
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

func (r *Repo) getMyJobs() []enc.Name {
	r.mu.Lock()
	defer r.mu.Unlock()
	src := r.nodeStatus[r.myNodeName()].Jobs
	dst := make([]enc.Name, len(src))
	copy(dst, src)
	return dst
}

func (r *Repo) publishNodeUpdate() {
	capacity, used := r.getStorageStats()
	myJobs := r.getMyJobs()

	// Check if we need to release a job (storage > 75%)
	if used > capacity*75/100 {
		r.mu.Lock()
		jobToRelease := r.findJobToRelease()
		if jobToRelease != nil {
			r.mu.Unlock()

			// Remove from local state and update storage
			r.removeJobFromLocal(jobToRelease.Target)

			// Update with JobRelease
			update := &tlv.NodeUpdate{
				Jobs:            myJobs,
				StorageCapacity: capacity,
				StorageUsed:     used,
				NewCommand:      nil,
				JobRelease:      jobToRelease,
			}

			_, _, err := r.groupSync.Publish(update.Encode())
			if err != nil {
				log.Fatal(r, "node_update_pub_failed", "err", err)
			}
		} else {
			r.mu.Unlock()
		}
	} else {
		update := &tlv.NodeUpdate{
			Jobs:            myJobs,
			StorageCapacity: capacity,
			StorageUsed:     used,
			NewCommand:      nil,
		}

		_, _, err := r.groupSync.Publish(update.Encode())
		if err != nil {
			log.Fatal(r, "node_update_pub_failed", "err", err)
		}
	}
}

func (r *Repo) findJobToRelease() *tlv.InternalCommand {
	r.mu.Lock()
	defer r.mu.Unlock()

	myStatus := r.nodeStatus[r.myNodeName()]
	if len(myStatus.Jobs) == 0 {
		return nil
	}

	target := myStatus.Jobs[len(myStatus.Jobs)-1]

	cmd := r.getCommandInternal(target)
	if cmd == nil {
		return nil
	}

	releaseCmd := &tlv.InternalCommand{
		Type:              cmd.Type,
		Target:            target,
		SnapshotThreshold: 0,
	}

	return releaseCmd
}

func (r *Repo) removeJobFromLocal(target enc.Name) {
	r.mu.Lock()
	defer r.mu.Unlock()

	myStatus := r.nodeStatus[r.myNodeName()]

	newJobs := make([]enc.Name, 0)
	for _, job := range myStatus.Jobs {
		if job.String() != target.String() {
			newJobs = append(newJobs, job)
		}
	}
	myStatus.Jobs = newJobs

	jobKey := target.String()
	if r.jobStorageUsage != nil {
		if used, exists := r.jobStorageUsage[jobKey]; exists {
			r.storageUsed -= used
			if r.storageUsed < 0 {
				r.storageUsed = 0
			}
			delete(r.jobStorageUsage, jobKey)
		}
	}

	r.nodeStatus[r.myNodeName()] = myStatus

	if r.eventLogger != nil {
		r.eventLogger.LogJobReleased(target.String())
	}
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

// initialization
func NewRepo(groupPrefix string, nodePrefix string, signingIdentity string, replicationFactor int) *Repo {
	gp, _ := enc.NameFromStr(groupPrefix)
	np, _ := enc.NameFromStr(nodePrefix)
	si, _ := enc.NameFromStr(signingIdentity)
	nf := gp.Append(enc.NewGenericComponent(NOTIFY))

	r := &Repo{
		groupPrefix:     gp,
		notifyPrefix:    &nf,
		nodePrefix:      np,
		signingIdentity: si,
		nodeStatus:      make(map[string]NodeStatus),
		commands:        make(map[string]*tlv.Command),
		jobStorageUsage: make(map[string]uint64),
		rf:              replicationFactor,
	}

	r.nodeStatus[r.nodePrefix.String()] = NodeStatus{
		Jobs: make([]enc.Name, 0),
	}

	return r
}

func (r *Repo) Start() (err error) {
	log.Info(r, "repo_start")

	r.storageCapacity = (10 * 1024 * 1024 * 1024) + (hashFromString(r.nodePrefix.String()) % (5 * 1024 * 1024 * 1024))
	r.storageUsed = (hashFromString(r.nodePrefix.String()) % (100 * 1024 * 1024))

	r.mu.Lock()
	myStatus := r.nodeStatus[r.myNodeName()]
	myStatus.Capacity = r.storageCapacity
	myStatus.Used = r.storageUsed
	r.nodeStatus[r.myNodeName()] = myStatus
	r.mu.Unlock()

	var face ndn.Face = engine.NewDefaultFace()
	if r.eventLogger != nil {
		r.countingFace = util.NewCountingFace(face, r.eventLogger)
		face = r.countingFace
	}

	r.engine = engine.NewBasicEngine(face)
	if err = r.engine.Start(); err != nil {
		return err
	}

	// TODO: use badger store in the deployed version for persistent storage
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
	go r.runHeartbeatMonitor()
	return nil
}

func (r *Repo) runHeartbeat() {
	ticker := time.NewTicker(HEARTBEAT_TIME)
	defer ticker.Stop()

	for range ticker.C {
		r.publishNodeUpdate()
	}
}

func (r *Repo) runHeartbeatMonitor() {
	heartbeatInterval := HEARTBEAT_TIME

	offlineTimeout := 3*heartbeatInterval + 500*time.Millisecond

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		for node, status := range r.nodeStatus {
			timeSinceLastUpdate := time.Since(status.LastUpdated)

			if timeSinceLastUpdate > offlineTimeout {
				if status.MissingHeartbeats < 3 {
					status.MissingHeartbeats++
				}

				r.nodeStatus[node] = status

				if status.MissingHeartbeats == 1 {
					for _, cmd := range r.commands {
						go r.replicate(cmd)
					}
				}
			}
		}
		r.mu.Unlock()
	}
}

func (r *Repo) runStorageSimulation() {
	ticker := time.NewTicker(STORAGE_TICK_TIME)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		myStatus := r.nodeStatus[r.myNodeName()]
		var delta uint64
		for _, target := range myStatus.Jobs {
			cmd := r.getCommandInternal(target)
			if cmd != nil && cmd.Type == "JOIN" {
				jobKey := target.String()
				if r.jobStorageUsage == nil {
					r.jobStorageUsage = make(map[string]uint64)
				}
				growth := (hashFromString(cmd.Target.String()) % (10 * 1024 * 1024))
				r.jobStorageUsage[jobKey] += growth
				delta += growth
			}
		}
		r.storageUsed += delta
		if r.storageUsed > r.storageCapacity {
			r.storageUsed = r.storageCapacity
		}
		r.mu.Unlock()

		if r.eventLogger != nil && delta > 0 {
			r.eventLogger.LogStorageChanged(r.storageUsed, delta)
		}
	}
}

// handle external actions
func (r *Repo) onCommand(name enc.Name, content enc.Wire, reply func(wire enc.Wire) error) {
	cmd, err := tlv.ParseCommand(enc.NewWireView(content), false)
	if err != nil {
		log.Warn(r, "command_parse_failed", "err", err)
		return
	}

	if r.eventLogger != nil {
		r.eventLogger.LogCommandReceived(cmd.Type, cmd.Target.String())
	}

	response := tlv.StatusResponse{
		Target: cmd.Target,
		Status: "received",
	}
	reply(response.Encode())

	r.addCommand(cmd)
	r.publishCommand(cmd)

	go r.replicate(cmd)
}

func (r *Repo) onGroupSync(pub svs.SvsPub) {
	update, err := tlv.ParseNodeUpdate(enc.NewWireView(pub.Content), false)
	if err != nil {
		log.Warn(r, "node_update_parse_failed", "name", pub.DataName, "err", err)
		return
	}

	publisherName := pub.Publisher.String()
	droppedJobs := r.updateNodeStatus(publisherName, update)

	if r.eventLogger != nil {
		jobs := make([]string, len(update.Jobs))
		for i, j := range update.Jobs {
			jobs[i] = j.String()
		}
		r.eventLogger.LogNodeUpdate(publisherName, jobs, update.StorageCapacity, update.StorageUsed)
	}

	if update.NewCommand != nil {
		r.addCommand(update.NewCommand)
		go r.replicate(update.NewCommand)
	}

	if update.JobRelease != nil {
		r.mu.Lock()
		cmd := r.getCommandInternal(update.JobRelease.Target)
		r.mu.Unlock()

		if cmd != nil {
			log.Info(r, "job_released_by_peer", "peer", publisherName, "target", update.JobRelease.Target)
			go r.replicate(cmd)
		}
	}

	for _, target := range droppedJobs {
		r.mu.Lock()
		cmd := r.getCommandInternal(target)
		r.mu.Unlock()

		if cmd != nil {
			log.Info(r, "job_dropped_by_peer_checking_replication", "peer", publisherName, "target", target)
			r.replicate(cmd)
		}
	}
}

// actions/logic
func (r *Repo) replicate(cmd *tlv.Command) {
	if r.shouldClaimJobHydra(cmd) {
		r.claimJob(cmd)
	}
}

func (r *Repo) claimJob(cmd *tlv.Command) {
	r.mu.Lock()

	myStatus := r.nodeStatus[r.myNodeName()]

	myStatus.Jobs = append(myStatus.Jobs, cmd.Target)

	if cmd.Type == "INSERT" {
		cost := (hashFromString(cmd.Target.String()) % (500 * 1024 * 1024))
		r.storageUsed += cost
		if r.storageUsed > r.storageCapacity {
			r.storageUsed = r.storageCapacity
		}
		myStatus.Used = r.storageUsed
		myStatus.Capacity = r.storageCapacity

		jobKey := cmd.Target.String()
		if r.jobStorageUsage == nil {
			r.jobStorageUsage = make(map[string]uint64)
		}
		r.jobStorageUsage[jobKey] += cost
	}

	r.nodeStatus[r.myNodeName()] = myStatus
	currentReplication := r.countReplication(cmd.Target)
	r.mu.Unlock()

	if r.eventLogger != nil {
		r.eventLogger.LogJobClaimed(cmd.Target.String(), currentReplication)
	}

	r.publishNodeUpdate()
}

func (r *Repo) shouldClaimJobHydra(cmd *tlv.Command) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	currentReplication := r.countReplication(cmd.Target)
	myPrefix := r.myNodeName()

	candidates := make([]string, 0, len(r.nodeStatus))
	freeSpace := make(map[string]uint64)

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
			freeSpace[name] = status.FreeSpace()
		}
	}

	needed := r.rf - currentReplication

	if needed <= 0 {
		if r.eventLogger != nil {
			r.eventLogger.LogReplicationDecision(
				cmd.Target.String(),
				false,
				"replication_satisfied",
				currentReplication,
				needed,
				candidates,
				nil,
				freeSpace,
			)
		}
		return false
	}

	sort.Slice(candidates, func(i, j int) bool {
		if freeSpace[candidates[i]] != freeSpace[candidates[j]] {
			return freeSpace[candidates[i]] > freeSpace[candidates[j]]
		}
		return strings.Compare(candidates[i], candidates[j]) > 0
	})

	limit := min(needed, len(candidates))
	selectedCandidates := candidates[:limit]

	shouldClaim := false
	reason := "not_selected"
	for _, c := range selectedCandidates {
		if c == myPrefix {
			shouldClaim = true
			reason = "selected_as_candidate"
			break
		}
	}

	if r.eventLogger != nil {
		r.eventLogger.LogReplicationDecision(
			cmd.Target.String(),
			shouldClaim,
			reason,
			currentReplication,
			needed,
			candidates,
			selectedCandidates,
			freeSpace,
		)
	}
	return shouldClaim
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
