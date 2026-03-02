package main

import (
	_ "embed"
	"fmt"
	"hash/fnv"
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
const HEARTBEAT_TIMEOUT = DEFAULT_HEARTBEAT_INTERVAL*3 + 500*time.Millisecond
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

// utilities
func (ns NodeStatus) UsedSpace() float64 {
	return float64(ns.Used) / float64(ns.Capacity)
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
	return slices.Contains(encNamesToStrings(r.jobs), target.String())
}

func (r *Repo) SetEventLogger(logger util.Logger) {
	r.eventLogger = logger
}

func (r *Repo) GetCountingFace() *util.CountingFace {
	return r.countingFace
}

func hashFromString(s string) uint64 {
	f := fnv.New64()
	f.Write([]byte(s))
	return f.Sum64()
}
func encNamesToStrings(names []enc.Name) []string {
	result := make([]string, len(names))
	for i, n := range names {
		result[i] = n.String()
	}
	return result
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

func (r *Repo) getCommandInternal(target enc.Name) *tlv.Command {
	cmd := r.commands[target.String()]
	if cmd == nil {
		log.Warn(r, "getCommandInternal_nil", "target", target.String())
	}
	return cmd
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

func (r *Repo) publishNodeUpdate(update *tlv.NodeUpdate) {
	if update == nil {
		update = &tlv.NodeUpdate{}
	}
	capacity, used := r.getStorageStats()
	update.Jobs = r.getMyJobs()
	update.StorageCapacity = capacity
	update.StorageUsed = used

	if update.NewCommand != nil {
		r.eventLogger.LogCommandPublished(update.NewCommand.Target.String())
	}

	_, _, err := r.groupSync.Publish(update.Encode())
	if err != nil {
		log.Fatal(r, "node_update_pub_failed", "err", err)
	} else {
		r.updateNodeStatus(r.myNodeName(), update)
	}
}

func (r *Repo) changeStorageUsed(delta uint64) {
	r.storageUsed += delta
	r.eventLogger.LogStorageChanged(r.storageUsed, delta)
}

func (r *Repo) publishCommand(newCmd *tlv.Command, winners []string) {
	var jobAssignments []*tlv.JobAssignment
	if len(winners) > 0 {
		jobAssignments = []*tlv.JobAssignment{{
			Target:    newCmd.Target,
			Assignees: stringNamesToEncNames(winners),
		}}
	}

	update := &tlv.NodeUpdate{
		NewCommand:     newCmd,
		JobAssignments: jobAssignments,
	}

	_, _, err := r.groupSync.Publish(update.Encode())
	if err != nil {
		log.Fatal(r, "node_update_pub_failed", "err", err)
	}

	r.updateNodeStatus(r.myNodeName(), update)
}

func (r *Repo) amAssignee(assignment *tlv.JobAssignment) bool {
	myPrefix := r.myNodeName()
	for _, a := range assignment.Assignees {
		if a.String() == myPrefix {
			return true
		}
	}
	return false
}

func (r *Repo) publishJobAssignments(assignments []*tlv.JobAssignment) {
	r.publishNodeUpdate(&tlv.NodeUpdate{
		JobAssignments: assignments,
	})
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

	// no timer for self
	if publisher == r.myNodeName() {
		return
	}

	// reset node's timer
	if timer, exists := r.nodeTimers[publisher]; exists {
		timer.Reset(HEARTBEAT_TIMEOUT)
	} else {
		r.nodeTimers[publisher] = time.AfterFunc(HEARTBEAT_TIMEOUT, func() {
			r.onHeartbeatTimeoutHydra(publisher)
		})
	}
}

func (r *Repo) onHeartbeatTimeoutHydra(nodeName string) {
	myName := r.myNodeName()
	r.mu.Lock()
	status := r.nodeStatus[nodeName]

	var assignments []*tlv.JobAssignment
	addedJob := false

	for _, target := range status.Jobs {
		cmd := r.getCommandInternal(target)
		winners := r.determineWinnersHydraInternal(cmd)

		if slices.Contains(winners, myName) {
			r.doJob(cmd)
			addedJob = true
		} else if winners != nil { // I'm not in it but there are other winners
			assignments = append(assignments, &tlv.JobAssignment{
				Target:    target,
				Assignees: stringNamesToEncNames(winners),
			})
		}
	}

	delete(r.nodeTimers, nodeName)
	delete(r.nodeStatus, nodeName)
	r.mu.Unlock()

	if addedJob || len(assignments) > 0 {
		r.publishJobAssignments(assignments)
	}
}

// initialization
func NewRepo(groupPrefix string, nodePrefix string, signingIdentity string, replicationFactor int, noRelease bool, maxJoinGrowthRate uint64, heartbeatInterval time.Duration, eventLogger util.Logger) *Repo {
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
		eventLogger:       eventLogger,
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
		// Define the prefix used to identify SVS sync interests
		syncPrefix := r.groupPrefix.Append(enc.NewGenericComponent("group-messages")).Append(enc.NewKeywordComponent("svs")).String()
		r.countingFace = util.NewCountingFace(face, r.eventLogger, syncPrefix)
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
	cmd, err := tlv.ParseCommand(enc.NewWireView(content), false)
	if err != nil {
		log.Warn(r, "command_parse_failed", "err", err)
		return
	}

	r.eventLogger.LogCommandReceived(cmd.Type, cmd.Target.String())
	response := tlv.StatusResponse{
		Target: cmd.Target,
		Status: "received",
	}
	reply(response.Encode())

	r.addCommand(cmd)

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
	}

	addedJob := false
	var reassignments []*tlv.JobAssignment
	for _, assignment := range update.JobAssignments {
		target := assignment.Target
		targetStr := target.String()
		assignees := encNamesToStrings(assignment.Assignees)

		if r.amIDoingJob(target) {
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "skipped", "already_doing_job", assignees)
			continue
		}

		if !slices.Contains(assignees, r.myNodeName()) {
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "skipped", "not_in_assignees", assignees)
			continue
		}

		cmd := r.getCommand(target)
		if cmd == nil {
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "pending", "command_not_received", assignees)
			continue
		}

		// TODO: when we turn on job releases, we need to see if we should do a job (or are able) and then reassign if necessary
		// ideally, the calculation that other nodes do should take into account this limit, so it should ideally be the same
		// algorithm for internal calculation (the hydra one)
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

func (r *Repo) doJob(cmd *tlv.Command) bool {
	c, u := r.getStorageStats()
	if u > 0 && (float64(u)/float64(c) > 0.75) {
		return false
	}
	r.mu.Lock()

	r.jobs = append(r.jobs, cmd.Target)

	if cmd.Type == "INSERT" {
		cost := (hashFromString(cmd.Target.String()) % (500 * 1024 * 1024))
		r.storageUsed += cost

		jobKey := cmd.Target.String()
		if r.jobStorageUsage == nil {
			r.jobStorageUsage = make(map[string]uint64)
		}
		r.jobStorageUsage[jobKey] += cost
	}
	r.mu.Unlock()

	if r.eventLogger != nil {
		r.eventLogger.LogJobClaimed(cmd.Target.String())
	}
	return true
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
	usedSpace := make(map[string]float64)
	capacity := make(map[string]uint64)
	for name, status := range r.nodeStatus {
		isDoing := false
		for _, job := range status.Jobs {
			if job.Equal(cmd.Target) {
				isDoing = true
				break
			}
		}
		if !isDoing {
			us := float64(status.UsedSpace())
			if us < .75 {
				usedSpace[name] = status.UsedSpace()
				candidates = append(candidates, name)
				capacity[name] = status.Capacity
			}
		}
	}

	needed := r.rf - currentReplication

	if needed <= 0 {
		r.eventLogger.LogDecisionMade(
			cmd.Target.String(),
			false,
			"replication_satisfied",
			fmt.Sprintf("current=%d needed=%d", currentReplication, needed),
			currentReplication,
			needed,
			candidates,
			nil,
			nil,
		)
		return nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		if usedSpace[candidates[i]] != usedSpace[candidates[j]] {
			return usedSpace[candidates[i]] > usedSpace[candidates[j]]
		}
		if capacity[candidates[i]] != capacity[candidates[j]] {
			return capacity[candidates[i]] > capacity[candidates[j]]
		}
		return strings.Compare(candidates[i], candidates[j]) < 0
	})

	limit := min(needed, len(candidates))
	selectedCandidates := candidates[:limit]

	candidateScores := make(map[string]int)
	for i, c := range candidates {
		candidateScores[c] = len(candidates) - i
	}

	shouldClaim := false
	reason := "not_selected"
	if slices.Contains(selectedCandidates, myPrefix) {
		shouldClaim = true
		reason = "selected_as_candidate"
	}

	r.eventLogger.LogDecisionMade(
		cmd.Target.String(),
		shouldClaim,
		reason,
		fmt.Sprintf("current=%d needed=%d selected=%v", currentReplication, needed, selectedCandidates),
		currentReplication,
		needed,
		candidates,
		candidateScores,
		selectedCandidates,
	)
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
