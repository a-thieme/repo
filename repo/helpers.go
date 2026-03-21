package main

import (
	"fmt"
	"hash/fnv"
	"slices"
	"time"

	"github.com/a-thieme/repo/repo/util"
	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
)

const NOTIFY = "notify"
const DEFAULT_HEARTBEAT_INTERVAL = 5 * time.Second
const HEARTBEAT_TIMEOUT = DEFAULT_HEARTBEAT_INTERVAL*3 + 500*time.Millisecond
const STORAGE_TICK_TIME = 1 * time.Second

const BASE_RETRY_DELAY = 500 * time.Millisecond
const MAX_RETRY_DELAY = 30 * time.Second

const DefaultStorageCapacity = 500 * 1024 * 1024   // 500MB
const MaxInsertCost = DefaultStorageCapacity / 100 // 1% = 5MB

const BID_SUFFIX = "bid"
const RESULTS_SUFFIX = "results"
const HEARTBEAT_SUFFIX = "heartbeat"

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

func (ns NodeStatus) UsedSpace() float64 {
	return float64(ns.Used) / float64(ns.Capacity)
}

func selectLeader(nodeStatus map[string]NodeStatus) string {
	if len(nodeStatus) == 0 {
		return ""
	}
	names := make([]string, 0, len(nodeStatus))
	for name := range nodeStatus {
		names = append(names, name)
	}
	slices.Sort(names)
	return names[0]
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

func (r *Repo) getStorageStats() (capacity uint64, used uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.storageCapacity, r.storageUsed
}

func (r *Repo) getStorageStatsUnsafe() (capacity uint64, used uint64) {
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

func (r *Repo) amAssignee(assignment *tlv.JobAssignment) bool {
	myPrefix := r.myNodeName()
	for _, a := range assignment.Assignees {
		if a.String() == myPrefix {
			return true
		}
	}
	return false
}

func (r *Repo) getOtherNodeNames() []string {
	names := make([]string, 0, len(r.nodeStatus))
	for name := range r.nodeStatus {
		if name != r.myNodeName() {
			names = append(names, name)
		}
	}
	return names
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
	r.mu.Lock()
	for _, job := range r.jobs {
		if job.Equal(target) {
			count++
			break
		}
	}
	r.mu.Unlock()

	log.Debug(r, "countReplication_result", "target", target.String(), "count", count,
		"nodeStatus_len", len(r.nodeStatus),
		"myNodeName", r.myNodeName(),
		"r.jobs_len", len(r.jobs))
	if myStatus, ok := r.nodeStatus[r.myNodeName()]; ok {
		log.Debug(r, "countReplication_self_status", "jobs_len", len(myStatus.Jobs))
	}
	return count
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

	wire := update.Encode()
	log.Info(r, "publishNodeUpdate", "myNode", r.myNodeName(), "jobs", len(update.Jobs))
	name, _, err := r.groupSync.Publish(wire)
	if err != nil {
		log.Fatal(r, "node_update_pub_failed", "err", err)
	} else {
		log.Info(r, "publishNodeUpdate_success", "name", name.String())
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

func (r *Repo) publishJobAssignments(assignments []*tlv.JobAssignment) {
	r.publishNodeUpdate(&tlv.NodeUpdate{
		JobAssignments: assignments,
	})
}

// used when you already have the full command to do
func (r *Repo) doCmd(cmd *tlv.Command) bool {
	c, u := r.getStorageStats()
	return r.doJobWithStats(cmd, c, u)
}

// used when you want to do a target but don't have the command
func (r *Repo) doTarget(target enc.Name) bool {
	c, u := r.getStorageStats()
	cmd := r.getCommand(target)
	if cmd == nil {
		return false
	}
	return r.doJobWithStats(cmd, c, u)
}

func (r *Repo) doJobWithStats(cmd *tlv.Command, capacity, used uint64) bool {
	if capacity > 0 && (float64(used)/float64(capacity) >= 0.75) {
		return false
	}
	r.mu.Lock()

	r.jobs = append(r.jobs, cmd.Target)

	if cmd.Type == "INSERT" {
		cost := (hashFromString(cmd.Target.String()) % MaxInsertCost)
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

type HydraMechanism struct {
	storageThreshold float64
}

func NewHydraMechanism() *HydraMechanism {
	return &HydraMechanism{
		storageThreshold: 0.75,
	}
}

func (h *HydraMechanism) DetermineWinners(target enc.Name, nodeStatus map[string]NodeStatus, myName string, rf int, eventLogger util.Logger) []string {
	currentReplication := countReplicationInternal(target, nodeStatus)

	candidates := make([]string, 0, len(nodeStatus))
	usedSpace := make(map[string]float64)
	capacity := make(map[string]uint64)
	for name, status := range nodeStatus {
		isDoing := false
		for _, job := range status.Jobs {
			if job.Equal(target) {
				isDoing = true
				break
			}
		}
		if !isDoing {
			us := status.UsedSpace()
			if us < h.storageThreshold {
				usedSpace[name] = us
				candidates = append(candidates, name)
				capacity[name] = status.Capacity
			}
		}
	}

	needed := rf - currentReplication

	if needed <= 0 {
		if eventLogger != nil {
			eventLogger.LogDecisionMade(
				target.String(),
				false,
				"replication_satisfied",
				fmt.Sprintf("current=%d needed=%d", currentReplication, needed),
				currentReplication,
				needed,
				candidates,
				nil,
				nil,
			)
		}
		return nil
	}

	slices.SortFunc(candidates, func(i, j string) int {
		if usedSpace[i] != usedSpace[j] {
			if usedSpace[i] < usedSpace[j] {
				return -1
			}
			return 1
		}
		if capacity[i] != capacity[j] {
			if capacity[i] > capacity[j] {
				return -1
			}
			return 1
		}
		if i < j {
			return -1
		}
		if i > j {
			return 1
		}
		return 0
	})

	limit := min(needed, len(candidates))
	selectedCandidates := candidates[:limit]

	candidateScores := make(map[string]int)
	for i, c := range candidates {
		candidateScores[c] = len(candidates) - i
	}

	shouldClaim := false
	reason := "not_selected"
	if slices.Contains(selectedCandidates, myName) {
		shouldClaim = true
		reason = "selected_as_candidate"
	}

	if eventLogger != nil {
		eventLogger.LogDecisionMade(
			target.String(),
			shouldClaim,
			reason,
			fmt.Sprintf("current=%d needed=%d selected=%v", currentReplication, needed, selectedCandidates),
			currentReplication,
			needed,
			candidates,
			candidateScores,
			selectedCandidates,
		)
	}
	return selectedCandidates
}

func countReplicationInternal(target enc.Name, nodeStatus map[string]NodeStatus) int {
	count := 0
	for _, status := range nodeStatus {
		for _, job := range status.Jobs {
			if job.Equal(target) {
				count++
				break
			}
		}
	}
	return count
}

type AuctionMechanism struct{}

func NewAuctionMechanism() *AuctionMechanism {
	return &AuctionMechanism{}
}

func (a *AuctionMechanism) DetermineWinners(target enc.Name, nodeStatus map[string]NodeStatus, myName string, rf int, eventLogger util.Logger) []string {
	currentReplication := countReplicationInternal(target, nodeStatus)

	candidates := make([]string, 0, len(nodeStatus))
	usedPercentage := make(map[string]float64)
	for name, status := range nodeStatus {
		isDoing := false
		for _, job := range status.Jobs {
			if job.Equal(target) {
				isDoing = true
				break
			}
		}
		if !isDoing {
			up := status.UsedSpace()
			usedPercentage[name] = up
			candidates = append(candidates, name)
		}
	}

	needed := rf - currentReplication

	if needed <= 0 {
		if eventLogger != nil {
			eventLogger.LogDecisionMade(
				target.String(),
				false,
				"replication_satisfied",
				fmt.Sprintf("current=%d needed=%d", currentReplication, needed),
				currentReplication,
				needed,
				candidates,
				nil,
				nil,
			)
		}
		return nil
	}

	slices.SortFunc(candidates, func(i, j string) int {
		if usedPercentage[i] != usedPercentage[j] {
			if usedPercentage[i] < usedPercentage[j] {
				return -1
			}
			return 1
		}
		if i < j {
			return -1
		}
		if i > j {
			return 1
		}
		return 0
	})

	limit := min(needed, len(candidates))
	selectedCandidates := candidates[:limit]

	winnerScores := make(map[string]float64)
	for _, c := range selectedCandidates {
		winnerScores[c] = usedPercentage[c]
	}

	shouldClaim := false
	reason := "not_selected"
	if slices.Contains(selectedCandidates, myName) {
		shouldClaim = true
		reason = "selected_as_candidate"
	}

	if eventLogger != nil {
		eventLogger.LogAuctionWinners(
			target.String(),
			candidates,
			winnerScores,
			selectedCandidates,
		)
		eventLogger.LogDecisionMade(
			target.String(),
			shouldClaim,
			reason,
			fmt.Sprintf("current=%d needed=%d selected=%v", currentReplication, needed, selectedCandidates),
			currentReplication,
			needed,
			candidates,
			nil,
			selectedCandidates,
		)
	}
	return selectedCandidates
}

func (r *Repo) advanceRetryDelay(target string) time.Duration {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()

	currentDelay, exists := r.retryDelays[target]
	if !exists {
		r.retryDelays[target] = BASE_RETRY_DELAY
		return BASE_RETRY_DELAY
	}

	newDelay := currentDelay * 2
	if newDelay > MAX_RETRY_DELAY {
		newDelay = MAX_RETRY_DELAY
	}
	r.retryDelays[target] = newDelay
	return newDelay
}

func (r *Repo) clearRetryDelay(target string) {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	delete(r.retryDelays, target)
}
