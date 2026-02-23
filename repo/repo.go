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
const DEFAULT_HEARTBEAT_INTERVAL = 5 * time.Second
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
	jobs            []enc.Name
	jobStorageUsage map[string]uint64

	rf                int
	noRelease         bool
	maxJoinGrowthRate uint64
	heartbeatInterval time.Duration

	nodeTimers   map[string]*time.Timer
	eventLogger  *util.EventLogger
	countingFace *util.CountingFace
}

type NodeStatus struct {
	Capacity    uint64
	Used        uint64
	LastUpdated time.Time
	Jobs        []enc.Name
	Alive       bool
	TimerID     uint64
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

func (r *Repo) amIDoingJob(target enc.Name) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, job := range r.jobs {
		if job.Equal(target) {
			return true
		}
	}
	return false
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

func stringNamesToEncNames(names []string) []enc.Name {
	result := make([]enc.Name, len(names))
	for i, n := range names {
		result[i], _ = enc.NameFromStr(n)
	}
	return result
}

func (r *Repo) countReplication(target enc.Name) int {
	count := 0
	for _, status := range r.nodeStatus {
		if !status.Alive {
			continue
		}
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

func (r *Repo) getCommand(target enc.Name) *tlv.Command {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getCommandInternal(target)
}

func (r *Repo) getMyJobs() []enc.Name {
	r.mu.Lock()
	defer r.mu.Unlock()
	dst := make([]enc.Name, len(r.jobs))
	copy(dst, r.jobs)
	return dst
}

func (r *Repo) publishNodeUpdate(jobAssignments []*tlv.JobAssignment) {
	capacity, used := r.getStorageStats()
	myJobs := r.getMyJobs()

	if !r.noRelease && used > capacity*75/100 {
		r.mu.Lock()
		jobToRelease := r.findJobToRelease()
		if jobToRelease != nil {
			r.mu.Unlock()

			r.removeJobFromLocal(jobToRelease.Target)

			update := &tlv.NodeUpdate{
				Jobs:            myJobs,
				StorageCapacity: capacity,
				StorageUsed:     used,
				NewCommand:      nil,
				JobRelease:      jobToRelease,
				JobAssignments:  jobAssignments,
			}

			_, _, err := r.groupSync.Publish(update.Encode())
			if err != nil {
				log.Fatal(r, "node_update_pub_failed", "err", err)
			}

			r.mu.Lock()
			r.nodeStatus[r.myNodeName()] = NodeStatus{
				Capacity:    capacity,
				Used:        used,
				Jobs:        myJobs,
				LastUpdated: time.Now(),
				Alive:       true,
			}
			r.mu.Unlock()
		} else {
			r.mu.Unlock()
		}
	} else {
		update := &tlv.NodeUpdate{
			Jobs:            myJobs,
			StorageCapacity: capacity,
			StorageUsed:     used,
			NewCommand:      nil,
			JobAssignments:  jobAssignments,
		}

		_, _, err := r.groupSync.Publish(update.Encode())
		if err != nil {
			log.Fatal(r, "node_update_pub_failed", "err", err)
		}

		r.mu.Lock()
		r.nodeStatus[r.myNodeName()] = NodeStatus{
			Capacity:    capacity,
			Used:        used,
			Jobs:        myJobs,
			LastUpdated: time.Now(),
			Alive:       true,
		}
		r.mu.Unlock()
	}
}

func (r *Repo) findJobToRelease() *tlv.InternalCommand {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.jobs) == 0 {
		return nil
	}

	target := r.jobs[len(r.jobs)-1]

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

	newJobs := make([]enc.Name, 0)
	for _, job := range r.jobs {
		if job.String() != target.String() {
			newJobs = append(newJobs, job)
		}
	}
	r.jobs = newJobs

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

	if r.eventLogger != nil {
		r.eventLogger.LogJobReleased(target.String())
	}
}

func (r *Repo) publishCommand(newCmd *tlv.Command, winners []string) {
	capacity, used := r.getStorageStats()
	myJobs := r.getMyJobs()

	var jobAssignments []*tlv.JobAssignment
	if len(winners) > 0 {
		jobAssignments = []*tlv.JobAssignment{{
			Target:    newCmd.Target,
			Assignees: stringNamesToEncNames(winners),
		}}
	}

	update := &tlv.NodeUpdate{
		Jobs:            myJobs,
		StorageCapacity: capacity,
		StorageUsed:     used,
		NewCommand:      newCmd,
		JobAssignments:  jobAssignments,
	}

	_, _, err := r.groupSync.Publish(update.Encode())
	if err != nil {
		log.Fatal(r, "node_update_pub_failed", "err", err)
	}

	r.mu.Lock()
	r.nodeStatus[r.myNodeName()] = NodeStatus{
		Capacity:    capacity,
		Used:        used,
		Jobs:        myJobs,
		LastUpdated: time.Now(),
		Alive:       true,
	}
	r.mu.Unlock()
}

func (r *Repo) publishJobAssignments(assignments []*tlv.JobAssignment) {
	capacity, used := r.getStorageStats()
	myJobs := r.getMyJobs()

	update := &tlv.NodeUpdate{
		Jobs:            myJobs,
		StorageCapacity: capacity,
		StorageUsed:     used,
		JobAssignments:  assignments,
	}

	_, _, err := r.groupSync.Publish(update.Encode())
	if err != nil {
		log.Fatal(r, "node_update_pub_failed", "err", err)
	}

	r.mu.Lock()
	r.nodeStatus[r.myNodeName()] = NodeStatus{
		Capacity:    capacity,
		Used:        used,
		Jobs:        myJobs,
		LastUpdated: time.Now(),
		Alive:       true,
	}
	r.mu.Unlock()
}

func (r *Repo) handleJobAssignment(assignment *tlv.JobAssignment) {
	myPrefix := r.myNodeName()
	target := assignment.Target

	if r.amIDoingJob(target) {
		return
	}

	amAssignee := false
	for _, a := range assignment.Assignees {
		if a.String() == myPrefix {
			amAssignee = true
			break
		}
	}
	if !amAssignee {
		return
	}

	cmd := r.getCommand(target)
	if cmd == nil {
		return
	}

	winners := r.determineWinnersHydra(cmd)
	if winners == nil {
		return
	}

	amWinner := slices.Contains(winners, myPrefix)
	if amWinner {
		r.claimJob(cmd)
		return
	}

	r.publishJobAssignments([]*tlv.JobAssignment{{
		Target:    target,
		Assignees: stringNamesToEncNames(winners),
	}})
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
		Alive:       true,
	}

	r.resetNodeTimer(publisher)

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

func (r *Repo) resetNodeTimer(nodeName string) {
	if timer, exists := r.nodeTimers[nodeName]; exists {
		timer.Stop()
	}
	status := r.nodeStatus[nodeName]
	status.TimerID++
	r.nodeStatus[nodeName] = status
	timerID := status.TimerID
	timeout := 3*r.heartbeatInterval + 500*time.Millisecond
	r.nodeTimers[nodeName] = time.AfterFunc(timeout, func() {
		r.onNodeTimeout(nodeName, timerID)
	})
}

func (r *Repo) onNodeTimeout(nodeName string, expectedTimerID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	status, exists := r.nodeStatus[nodeName]
	if !exists || !status.Alive || status.TimerID != expectedTimerID {
		return
	}

	status.Alive = false
	r.nodeStatus[nodeName] = status

	var assignments []*tlv.JobAssignment
	for _, target := range status.Jobs {
		cmd := r.getCommandInternal(target)
		if cmd == nil {
			continue
		}
		winners := r.determineWinnersHydraInternal(cmd)
		if winners != nil {
			assignments = append(assignments, &tlv.JobAssignment{
				Target:    target,
				Assignees: stringNamesToEncNames(winners),
			})
		}
	}

	delete(r.nodeTimers, nodeName)

	if len(assignments) > 0 {
		go r.publishJobAssignments(assignments)
	}
}

// initialization
func NewRepo(groupPrefix string, nodePrefix string, signingIdentity string, replicationFactor int, noRelease bool, maxJoinGrowthRate uint64, heartbeatInterval time.Duration) *Repo {
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

	r := &Repo{
		groupPrefix:       gp,
		notifyPrefix:      &nf,
		nodePrefix:        np,
		signingIdentity:   si,
		nodeStatus:        make(map[string]NodeStatus),
		commands:          make(map[string]*tlv.Command),
		jobs:              make([]enc.Name, 0),
		jobStorageUsage:   make(map[string]uint64),
		rf:                replicationFactor,
		noRelease:         noRelease,
		maxJoinGrowthRate: maxJoinGrowthRate,
		heartbeatInterval: heartbeatInterval,
		nodeTimers:        make(map[string]*time.Timer),
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
		Alive:       true,
	}
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
	r.publishNodeUpdate(nil)

	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()

	for range ticker.C {
		r.publishNodeUpdate(nil)
	}
}

func (r *Repo) runStorageSimulation() {
	ticker := time.NewTicker(STORAGE_TICK_TIME)
	defer ticker.Stop()

	for range ticker.C {
		r.mu.Lock()
		var delta uint64
		for _, target := range r.jobs {
			cmd := r.getCommandInternal(target)
			if cmd != nil && cmd.Type == "JOIN" {
				jobKey := target.String()
				if r.jobStorageUsage == nil {
					r.jobStorageUsage = make(map[string]uint64)
				}
				growth := (hashFromString(cmd.Target.String()) % r.maxJoinGrowthRate)
				if growth > 0 {
					r.jobStorageUsage[jobKey] += growth
					delta += growth
				}
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

	winners := r.determineWinnersHydra(cmd)
	r.publishCommand(cmd, winners)

	myPrefix := r.myNodeName()
	if winners != nil {
		for _, w := range winners {
			if w == myPrefix {
				r.claimJob(cmd)
				break
			}
		}
	}
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
		if r.eventLogger != nil {
			r.eventLogger.LogCommandSynced(update.NewCommand.Type, update.NewCommand.Target.String(), publisherName)
		}
	}

	for _, assignment := range update.JobAssignments {
		r.handleJobAssignment(assignment)
	}

	if update.JobRelease != nil {
		r.mu.Lock()
		cmd := r.getCommandInternal(update.JobRelease.Target)
		r.mu.Unlock()

		if cmd != nil {
			log.Info(r, "job_released_by_peer", "peer", publisherName, "target", update.JobRelease.Target)
			winners := r.determineWinnersHydra(cmd)
			if winners != nil {
				r.publishJobAssignments([]*tlv.JobAssignment{{
					Target:    cmd.Target,
					Assignees: stringNamesToEncNames(winners),
				}})
			}
		}
	}

	for _, target := range droppedJobs {
		r.mu.Lock()
		cmd := r.getCommandInternal(target)
		r.mu.Unlock()

		if cmd != nil {
			log.Info(r, "job_dropped_by_peer_checking_replication", "peer", publisherName, "target", target)
			winners := r.determineWinnersHydra(cmd)
			if winners != nil {
				r.publishJobAssignments([]*tlv.JobAssignment{{
					Target:    cmd.Target,
					Assignees: stringNamesToEncNames(winners),
				}})
			}
		}
	}
}

func (r *Repo) claimJob(cmd *tlv.Command) {
	r.mu.Lock()

	r.jobs = append(r.jobs, cmd.Target)

	if cmd.Type == "INSERT" {
		cost := (hashFromString(cmd.Target.String()) % (500 * 1024 * 1024))
		r.storageUsed += cost
		if r.storageUsed > r.storageCapacity {
			r.storageUsed = r.storageCapacity
		}

		jobKey := cmd.Target.String()
		if r.jobStorageUsage == nil {
			r.jobStorageUsage = make(map[string]uint64)
		}
		r.jobStorageUsage[jobKey] += cost
	}

	currentReplication := r.countReplication(cmd.Target)
	r.mu.Unlock()

	if r.eventLogger != nil {
		r.eventLogger.LogJobClaimed(cmd.Target.String(), currentReplication)
	}

	r.publishNodeUpdate(nil)
}

func (r *Repo) determineWinnersHydra(cmd *tlv.Command) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.determineWinnersHydraInternal(cmd)
}

func (r *Repo) determineWinnersHydraInternal(cmd *tlv.Command) []string {
	currentReplication := r.countReplication(cmd.Target)
	myPrefix := r.myNodeName()

	candidates := make([]string, 0, len(r.nodeStatus))
	freeSpace := make(map[string]uint64)

	for name, status := range r.nodeStatus {
		if !status.Alive {
			continue
		}
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
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		if freeSpace[candidates[i]] != freeSpace[candidates[j]] {
			return freeSpace[candidates[i]] > freeSpace[candidates[j]]
		}
		return strings.Compare(candidates[i], candidates[j]) < 0
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
	return selectedCandidates
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
