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

const (
	NOTIFY                     = "notify"
	DEFAULT_HEARTBEAT_INTERVAL = 5 * time.Second
	HEARTBEAT_TIMEOUT          = DEFAULT_HEARTBEAT_INTERVAL*3 + 500*time.Millisecond
	STORAGE_TICK_TIME          = 1 * time.Second
)

const (
	DefaultStorageCapacity = 500 * 1024 * 1024            // 500MB
	MaxInsertCost          = DefaultStorageCapacity / 100 // 1% = 5MB
)

type NodeStatus struct {
	Capacity    uint64
	Used        uint64
	LastUpdated time.Time
	Jobs        []enc.Name
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

func (r *Repo) resetHeartbeatTimer(nodeName string) {
	r.heartbeatMu.Lock()
	defer r.heartbeatMu.Unlock()

	log.Debug(r, "resetHeartbeatTimer", "node", nodeName)

	if nodeName == r.myNodeName() {
		return
	}

	r.eventLogger.LogHeartbeatReceived(nodeName)

	timeout := r.heartbeatInterval*3 + 500*time.Millisecond
	if t, exists := r.heartbeats[nodeName]; exists {
		t.Stop()
		delete(r.heartbeats, nodeName)
	}
	r.heartbeats[nodeName] = time.AfterFunc(timeout, func() {
		r.handleNodeDeath(nodeName)
	})
}

func (r *Repo) stopHeartbeatTimer(nodeName string) {
	r.heartbeatMu.Lock()
	defer r.heartbeatMu.Unlock()

	if t, exists := r.heartbeats[nodeName]; exists {
		t.Stop()
		delete(r.heartbeats, nodeName)
	}
}

func (r *Repo) amILeader() bool {
	return r.myNodeName() == selectLeader(r.nodeStatus)
}

func (r *Repo) String() string {
	return "repo"
}

func (r *Repo) myNodeName() string {
	return r.nodePrefix.String()
}

func (r *Repo) amIDoingJob(target enc.Name) bool {
	return r.amIDoingJobStr(target.String())
}

func (r *Repo) amIDoingJobStr(targetStr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.nodeStatus[r.myNodeName()]
	doing := slices.Contains(encNamesToStrings(status.Jobs), targetStr)
	log.Debug(r, "amIDoingJobStr_check", "target", targetStr, "doing", doing)
	return doing
}

func (r *Repo) SetEventLogger(logger util.Logger) {
	r.eventLogger = logger
}

func (r *Repo) getStorageStats() (capacity uint64, used uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.nodeStatus[r.myNodeName()]
	return status.Capacity, status.Used
}

func (r *Repo) addCommand(cmd *tlv.Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	log.Debug(r, "addCommand", "target", cmd.Target.String(), "type", cmd.Type)
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
	status := r.nodeStatus[r.myNodeName()]
	dst := make([]enc.Name, len(status.Jobs))
	copy(dst, status.Jobs)
	return dst
}

func (r *Repo) amAssignee(assignment *tlv.JobAssignment) bool {
	myPrefix := r.myNodeName()
	for _, a := range assignment.Assignees {
		if a.String() == myPrefix {
			log.Debug(r, "amAssignee_check", "assignees", assignment.Assignees, "myNode", myPrefix, "isAssignee", true)
			return true
		}
	}
	log.Debug(r, "amAssignee_check", "assignees", assignment.Assignees, "myNode", myPrefix, "isAssignee", false)
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

func (r *Repo) getOtherNodeNamesNotDoing(target enc.Name) []string {
	names := make([]string, 0, len(r.nodeStatus))
	for name, status := range r.nodeStatus {
		if name == r.myNodeName() {
			continue
		}
		isDoing := false
		for _, job := range status.Jobs {
			if job.Equal(target) {
				isDoing = true
				break
			}
		}
		if !isDoing {
			names = append(names, name)
		}
	}
	return names
}

func (r *Repo) countReplication(target enc.Name) int {
	r.mu.Lock()
	count := 0
	for _, status := range r.nodeStatus {
		for _, job := range status.Jobs {
			if job.Equal(target) {
				count++
				break
			}
		}
	}
	r.mu.Unlock()

	log.Debug(r, "countReplication", "target", target.String(), "count", count, "rf", r.rf)

	if count >= r.rf {
		r.cancelReevaluation(target)
	}
	return count
}

func (r *Repo) publishUpdateStats(update *tlv.NodeUpdate) {
	if update == nil {
		update = &tlv.NodeUpdate{}
	}
	capacity, used := r.getStorageStats()
	update.StorageCapacity = capacity
	update.StorageUsed = used

	if update.NewCommand != nil {
		r.eventLogger.LogCommandPublished(update.NewCommand.Target.String())
	}
	r.publishUpdate(update)
}

func (r *Repo) publishUpdate(update *tlv.NodeUpdate) {
	log.Info(r, "publishNodeUpdate", "myNode", r.myNodeName(), "jobs", len(update.Jobs))
	wire := update.Encode()
	name, _, err := r.groupSync.Publish(wire)
	if err != nil {
		log.Fatal(r, "node_update_pub_failed", "err", err)
		return
	}
	log.Info(r, "publishNodeUpdate_success", "name", name.String())
}

func (r *Repo) publishJobs() {
	r.publishUpdate(&tlv.NodeUpdate{
		Jobs: r.getMyJobs(),
	})
}

// used when you already have the full command to do
// make sure to publishNodeUpdate with new jobs if it returns true
func (r *Repo) doCmd(cmd *tlv.Command) bool {
	log.Debug(r, "doCmd_enter", "target", cmd.Target.String(), "type", cmd.Type)
	c, u := r.getStorageStats()
	return r.doJobWithStats(cmd, c, u)
}

// used when you want to do a target but don't have the command
// make sure to publishNodeUpdate with new jobs if it returns true
func (r *Repo) doTarget(target enc.Name) bool {
	log.Info(r, "doTarget_called", "target", target.String(), "node", r.myNodeName())
	log.Debug(r, "doTarget_enter", "target", target.String())
	cmd := r.getCommand(target)
	if cmd == nil {
		return false
	}
	result := r.doCmd(cmd)
	return result
}

func (r *Repo) doJobWithStats(cmd *tlv.Command, capacity, used uint64) bool {
	log.Debug(r, "doJobWithStats", "target", cmd.Target.String(), "capacity", capacity, "used", used)
	r.mu.Lock()
	status := r.nodeStatus[r.myNodeName()]
	status.Jobs = append(status.Jobs, cmd.Target)

	if cmd.Type == "INSERT" {
		cost := (hashFromString(cmd.Target.String()) % MaxInsertCost)
		status.Used += cost

		jobKey := cmd.Target.String()
		if r.jobStorageUsage == nil {
			r.jobStorageUsage = make(map[string]uint64)
		}
		r.jobStorageUsage[jobKey] += cost
	}
	r.nodeStatus[r.myNodeName()] = status
	r.mu.Unlock()

	if r.eventLogger != nil {
		r.eventLogger.LogJobClaimed(cmd.Target.String())
	}
	return true
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

func (r *Repo) nodeStatusCopy() map[string]NodeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := make(map[string]NodeStatus, len(r.nodeStatus))
	for k, v := range r.nodeStatus {
		copy[k] = v
	}
	return copy
}

func (r *Repo) DetermineWinners(target enc.Name, nodeStatus map[string]NodeStatus) *tlv.JobAssignment {
	currentReplication := countReplicationInternal(target, nodeStatus)

	candidates := make([]string, 0, len(nodeStatus))
	usedPercentage := make(map[string]float64)
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
			up := status.UsedSpace()
			usedPercentage[name] = up
			capacity[name] = status.Capacity
			candidates = append(candidates, name)
		}
	}

	log.Info(nil, "determineWinners_debug", "target", target.String(), "nodeStatus_len", len(nodeStatus), "candidates", candidates)

	needed := r.rf - currentReplication

	if needed <= 0 {
		if r.eventLogger != nil {
			r.eventLogger.LogDecisionMade(
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
	if slices.Contains(selectedCandidates, r.myNodeName()) {
		shouldClaim = true
		reason = "selected_as_candidate"
	}

	if r.eventLogger != nil {
		r.eventLogger.LogDecisionMade(
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
	return &tlv.JobAssignment{Target: target, Assignees: stringNamesToEncNames(selectedCandidates)}
}
