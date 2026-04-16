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

// JobInfo pairs a job target with its allocated storage space
type JobInfo struct {
	Target  enc.Name
	Storage uint64
}

type NodeStatus struct {
	Capacity uint64
	Used     uint64
	Jobs     []JobInfo
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
			Jobs:     []JobInfo{},
		}
		log.Info(r, "heartbeat_node_added_to_status", "node", nodeName)
	}
	r.mu.Unlock()

	timeout := r.heartbeatInterval*3 + 500*time.Millisecond

	// Close the old done channel to invalidate any in-flight callback
	if done, exists := r.heartbeatDone[nodeName]; exists {
		close(done)
		delete(r.heartbeatDone, nodeName)
	}

	// Stop and remove old timer if exists
	if t, exists := r.heartbeats[nodeName]; exists {
		t.Stop()
		delete(r.heartbeats, nodeName)
	}

	// Create new done channel for this timer
	done := make(chan struct{})
	r.heartbeatDone[nodeName] = done

	r.heartbeats[nodeName] = time.AfterFunc(timeout, func() {
		// Check if this timer was invalidated by a newer heartbeat
		select {
		case <-done:
			// Timer was invalidated, skip
			return
		default:
			// Continue with callback
		}

		// Copy the jobs we need to evaluate before releasing the lock
		var jobsToEvaluate []JobInfo
		r.mu.Lock()
		status, exists := r.nodeStatus[nodeName]
		if !exists {
			r.mu.Unlock()
			return
		}
		delete(r.nodeStatus, nodeName)
		jobsCount := len(status.Jobs)
		if jobsCount > 0 {
			jobsToEvaluate = make([]JobInfo, jobsCount)
			copy(jobsToEvaluate, status.Jobs)
		}
		r.mu.Unlock()

		log.Warn(r, "node_failure_detected", "deadNode", nodeName, "orphanedJobs", jobsCount)
		r.eventLogger.LogNodeDetectedDead(nodeName, jobsCount)

		// Only check the dead node's jobs - the perpetual fallback mechanism
		// handles re-checking these until RF is satisfied.
		// NOTE: evaluateBatch is called WITHOUT holding r.mu to avoid deadlock
		// since evaluateBatch itself acquires r.mu internally.
		if len(jobsToEvaluate) > 0 {
			r.evaluateBatch(jobsToEvaluate, false)
		}
	})
}

// scheduleFallback schedules a perpetual fallback for a target.
// When the fallback fires, it re-evaluates the target. If still under-replicated,
// it reschedules another fallback. This ensures targets eventually reach RF
// even if the leader fails or distribution is delayed.
func (r *Repo) scheduleFallback(target enc.Name) {
	key := target.String()

	r.heartbeatMu.Lock()
	// Cancel existing fallback for this target
	if t, exists := r.fallbackTimers[key]; exists {
		t.Stop()
		delete(r.fallbackTimers, key)
	}

	// Schedule new fallback with delay long enough for distribution to complete
	delay := r.heartbeatInterval*3 + 500*time.Millisecond*2
	r.fallbackTimers[key] = time.AfterFunc(delay, func() {
		r.heartbeatMu.Lock()
		delete(r.fallbackTimers, key)
		r.heartbeatMu.Unlock()

		log.Info(r, "fallback_fired", "target", key, "leader", r.myNodeName())

		// Evaluate the target - if not the leader, this returns early but we still
		// reschedule the fallback for next time in case we became leader
		r.evaluateBatch([]JobInfo{{Target: target, Storage: 0}}, false)

		// Perpetually reschedule fallback if target is still under-replicated
		// This applies to ALL nodes (leader or not) to handle leader changes
		if r.countReplication(target) < r.rf {
			r.scheduleFallback(target)
		}
	})
	r.heartbeatMu.Unlock()
}

// cancelFallback cancels any scheduled fallback for the given target
func (r *Repo) cancelFallback(target enc.Name) {
	key := target.String()
	r.heartbeatMu.Lock()
	if t, exists := r.fallbackTimers[key]; exists {
		t.Stop()
		delete(r.fallbackTimers, key)
	}
	r.heartbeatMu.Unlock()
}

// scheduleEvalIfNotExists schedules an initial evaluation with a delay long enough
// for distribution to complete. This prevents over-replication when multiple nodes
// evaluate simultaneously during initial distribution.
func (r *Repo) scheduleEvalIfNotExists(target enc.Name) {
	key := target.String()
	r.heartbeatMu.Lock()
	defer r.heartbeatMu.Unlock()
	if _, exists := r.evalTimers[key]; exists {
		log.Debug(r, "scheduleEvalIfNotExists_skip", "target", key, "reason", "already_scheduled")
		return
	}
	// Delay to allow initial distribution to complete before evaluating
	delay := 5 * time.Second
	r.evalTimers[key] = time.AfterFunc(delay, func() {
		r.heartbeatMu.Lock()
		delete(r.evalTimers, key)
		r.heartbeatMu.Unlock()
		log.Info(r, "eval_timer_fired", "target", key, "leader", r.myNodeName())
		r.evaluateBatch([]JobInfo{{Target: target, Storage: 0}}, false)
	})
}

// cancelEval cancels any scheduled evaluation for the given target
func (r *Repo) cancelEval(target enc.Name) {
	key := target.String()
	r.heartbeatMu.Lock()
	if t, exists := r.evalTimers[key]; exists {
		t.Stop()
		delete(r.evalTimers, key)
	}
	r.heartbeatMu.Unlock()
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
	jobNames := make([]enc.Name, len(status.Jobs))
	for i, job := range status.Jobs {
		jobNames[i] = job.Target
	}
	doing := slices.Contains(encNamesToStrings(jobNames), targetStr)
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

func (r *Repo) getMyJobs() []JobInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.nodeStatus[r.myNodeName()]
	dst := make([]JobInfo, len(status.Jobs))
	copy(dst, status.Jobs)
	return dst
}

// jobInfosToTLV converts internal JobInfo slice to TLV JobInfo slice
func jobInfosToTLV(jobs []JobInfo) []*tlv.JobInfo {
	if len(jobs) == 0 {
		return nil
	}
	result := make([]*tlv.JobInfo, len(jobs))
	for i, job := range jobs {
		result[i] = &tlv.JobInfo{Target: job.Target, StorageSpace: job.Storage}
	}
	return result
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
			if job.Target.Equal(target) {
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
			if job.Target.Equal(target) {
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

	if cmd.Type == "INSERT" {
		cost := (hashFromString(targetStr) % MaxInsertCost)
		status.Used += cost

		jobKey := targetStr
		if r.jobStorageUsage == nil {
			r.jobStorageUsage = make(map[string]uint64)
		}
		r.jobStorageUsage[jobKey] += cost

		status.Jobs = append(status.Jobs, JobInfo{Target: target, Storage: cost})
	} else {
		// JOIN jobs - storage grows over time, start with 0
		status.Jobs = append(status.Jobs, JobInfo{Target: target, Storage: 0})
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
			if job.Target.Equal(target) {
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
