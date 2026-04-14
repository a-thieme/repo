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
	Capacity uint64
	Used     uint64
	Jobs     []enc.Name
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

	log.Info(r, "heartbeat_timer_reset", "node", nodeName, "myNode", r.myNodeName())

	if nodeName == r.myNodeName() {
		return
	}

	r.eventLogger.LogHeartbeatReceived(nodeName)

	// Ensure node exists in nodeStatus with default values if not present
	// This is needed for auction mode where we don't publish storage stats to main SVS
	r.mu.Lock()
	if _, exists := r.nodeStatus[nodeName]; !exists {
		r.nodeStatus[nodeName] = NodeStatus{
			Capacity: 0,
			Used:     0,
			Jobs:     []enc.Name{},
		}
		log.Info(r, "heartbeat_node_added_to_status", "node", nodeName)
	}
	r.mu.Unlock()

	timeout := r.heartbeatInterval*3 + 500*time.Millisecond
	if t, exists := r.heartbeats[nodeName]; exists {
		t.Stop()
		delete(r.heartbeats, nodeName)
	}
	r.heartbeats[nodeName] = time.AfterFunc(timeout, func() {
		r.mu.Lock()
		status, exists := r.nodeStatus[nodeName]
		if !exists {
			r.mu.Unlock()
			return
		}
		delete(r.nodeStatus, nodeName)
		jobsCount := len(status.Jobs)
		r.mu.Unlock()

		log.Warn(r, "node_failure_detected", "deadNode", nodeName, "orphanedJobs", jobsCount)
		r.eventLogger.LogNodeDetectedDead(nodeName, jobsCount)

		r.stopHeartbeatTimer(nodeName)
		r.evaluateBatch(status.Jobs, false)
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

func (r *Repo) amIDoingJobStr(targetStr string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.nodeStatus[r.myNodeName()]
	doing := slices.Contains(encNamesToStrings(status.Jobs), targetStr)
	log.Debug(r, "amIDoingJobStr_check", "target", targetStr, "doing", doing)
	return doing
}

func (r *Repo) SetEventLogger(logger util.UnifiedLogger) {
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
	log.Info(r, "command_added_internal", "target", cmd.Target.String(), "type", cmd.Type, "correlationID", cmd.Target.String())
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

type UnderStats struct {
	Target     enc.Name
	Candidates []string
	Needed     int
}

func (r *Repo) checkUnder(target enc.Name) UnderStats {
	currentReplication := 0
	nodeStatus := r.nodeStatusCopy()
	candidates := make([]string, 0, len(nodeStatus))
	for name, status := range nodeStatus {
		isDoing := false
		for _, job := range status.Jobs {
			if job.Equal(target) {
				isDoing = true
				break
			}
		}
		if isDoing {
			currentReplication += 1
		} else {
			candidates = append(candidates, name)
		}
	}
	needed := r.rf - currentReplication
	return UnderStats{Target: target, Candidates: candidates, Needed: needed}
}

func (r *Repo) countReplication(target enc.Name) int {
	r.mu.Lock()
	count := 0
	nodeStatusDebug := make(map[string]int)
	for nodeName, status := range r.nodeStatus {
		nodeJobCount := 0
		for _, job := range status.Jobs {
			if job.Equal(target) {
				nodeJobCount++
			}
		}
		nodeStatusDebug[nodeName] = nodeJobCount
		if nodeJobCount > 0 {
			count++
		}
	}
	r.mu.Unlock()

	log.Info(r, "countReplication", "target", target.String(), "count", count, "rf", r.rf, "nodeStatus", fmt.Sprintf("%v", nodeStatusDebug), "correlationID", target.String())

	return count
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
	if update.NewCommand != nil {
		r.eventLogger.LogCommandPublished(update.NewCommand.Target.String())
	}
}

// do a command
// make sure to publishNodeUpdate with new jobs if it returns true
func (r *Repo) doCmd(cmd *tlv.Command) bool {
	target := cmd.Target
	targetStr := target.String()

	log.Info(r, "job_claimed", "target", targetStr, "type", cmd.Type, "correlationID", targetStr)

	r.mu.Lock()
	status := r.nodeStatus[r.myNodeName()]
	status.Jobs = append(status.Jobs, target)

	if cmd.Type == "INSERT" {
		cost := (hashFromString(targetStr) % MaxInsertCost)
		status.Used += cost

		jobKey := targetStr
		if r.jobStorageUsage == nil {
			r.jobStorageUsage = make(map[string]uint64)
		}
		r.jobStorageUsage[jobKey] += cost
	}
	r.nodeStatus[r.myNodeName()] = status
	r.mu.Unlock()

	if r.eventLogger != nil {
		r.eventLogger.LogJobClaimed(targetStr)
	}
	return true
}

// do a command but you only have the target
// make sure to publishNodeUpdate with new jobs if it returns true
func (r *Repo) doTarget(target enc.Name) bool {
	cmd := r.getCommand(target)
	if cmd == nil {
		log.Info(r, "doTarget_failed", "target", target.String(), "node", r.myNodeName())
		return false
	}
	result := r.doCmd(cmd)
	return result
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
