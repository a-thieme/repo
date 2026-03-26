package main

import (
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
)

// if the target is still under-replicated, do it
func (r *Repo) checkPendingAssignment(cmd *tlv.Command) {
	targetStr := cmd.Target.String()
	log.Debug(r, "checkPendingAssignment", "target", targetStr)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pendingAssignments[targetStr]; ok {
		log.Info(r, "checkPendingAssignment_found", "target", targetStr)
		delete(r.pendingAssignments, targetStr)
		if r.countReplication(cmd.Target) < r.rf {
			r.scheduleReevaluationLoop(cmd.Target)
			if r.doCmd(cmd) {
				r.publishUpdateStats(&tlv.NodeUpdate{
					Jobs: r.getMyJobs(),
				})
			}
		}
	}
}

func (r *Repo) processJobAssignment(assignment *tlv.JobAssignment) {
	r.ProcessJobAssignments([]*tlv.JobAssignment{assignment})
}

func (r *Repo) ProcessJobAssignments(assignments []*tlv.JobAssignment) {
	log.Debug(r, "ProcessJobAssignments_enter", "count", len(assignments))
	addedJob := false

	// iterate through assignments,
	for _, assignment := range assignments {
		target := assignment.Target
		targetStr := target.String()
		log.Debug(r, "ProcessJobAssignment_processing", "target", targetStr, "assignees", assignment.Assignees)

		// skip assignment that isn't for us
		if !r.amAssignee(assignment) {
			log.Info(r, "processJobAssignments_skipped", "reason", "not_in_assignees", "target", targetStr)
			r.eventLogger.LogAssignmentHandled(targetStr, "skipped", "not_in_assignees")
			continue
		}

		// Check if command is in r.commands
		cmd := r.getCommand(target)
		if cmd == nil {
			log.Info(r, "processJobAssignments_pending", "target", targetStr)
			r.mu.Lock()
			r.pendingAssignments[targetStr] = assignment
			r.mu.Unlock()
			r.eventLogger.LogAssignmentHandled(targetStr, "pending", "command_not_received")
			continue
		}

		// we were assigned and have the command
		if r.amIDoingJobStr(targetStr) {
			log.Info(r, "processJobAssignments_alreadyDoing", "target", targetStr)
			continue
		}

		// we are assigned and it still needs to be done
		if r.countReplication(target) < r.rf {
			if r.doCmd(cmd) {
				log.Info(r, "processJobAssignments_will_do_job", "target", targetStr, "myNode", r.myNodeName())
				addedJob = true
			}
			r.evaluate(target)
		}
	}

	if addedJob {
		r.publishUpdateStats(&tlv.NodeUpdate{
			Jobs: r.getMyJobs(),
		})
	}
}

// redistMu thread-safe
func (r *Repo) scheduleReevaluationLoop(target enc.Name) {
	targetStr := target.String()
	// NOTE: this should be longer than it takes for a distribution to happen
	// 5 seconds gives enough time for SVS propagation and auction completion
	delay := 5 * time.Second

	r.redistMu.Lock()
	defer r.redistMu.Unlock()

	// if a redistribution is already scheduled, then let it continue
	if _, exists := r.scheduledRedistributions[targetStr]; exists {
		return
	}

	log.Info(r, "scheduleReevaluationLoop_scheduled", "target", targetStr, "delay", delay.String(), "isLeader", r.amILeader())
	r.scheduledRedistributions[targetStr] = time.AfterFunc(delay, func() {
		r.evaluate(target)
	})
}

func (r *Repo) evaluate(target enc.Name) {
	targetStr := target.String()
	log.Debug(r, "evaluate_enter", "target", targetStr)
	r.redistMu.Lock()
	delete(r.scheduledRedistributions, targetStr)
	r.redistMu.Unlock()

	log.Debug(r, "evaluate_checkingReplication", "target", targetStr)
	count := r.countReplication(target)
	log.Debug(r, "evaluate_replicationCount", "target", targetStr, "count", count, "rf", r.rf)
	if count >= r.rf {
		log.Debug(r, "loopCancelled", "target", targetStr)
		return
	}

	isLeader := r.amILeader()
	log.Info(r, "scheduleReevaluationLoop_fired", "target", targetStr, "isLeader", isLeader)
	if !isLeader {
		r.scheduleReevaluationLoop(target)
		return
	}

	cmd := r.getCommand(target)
	if cmd == nil {
		log.Info(r, "scheduleReevaluationLoop_skip_noCommand", "target", targetStr)
		r.scheduleReevaluationLoop(target)
		return
	}

	log.Info(r, "scheduleReevaluationLoop_runningDistribution", "target", targetStr)
	r.distributor.RunDistribution(cmd)
}

// redistMu thread safe
func (r *Repo) cancelReevaluation(target enc.Name) {
	targetStr := target.String()
	r.redistMu.Lock()
	defer r.redistMu.Unlock()
	if t, ok := r.scheduledRedistributions[targetStr]; ok {
		t.Stop()
		delete(r.scheduledRedistributions, targetStr)
	}
}

func (r *Repo) handleNodeDeath(nodeName string) {
	log.Debug(r, "handleNodeDeath", "node", nodeName)
	r.mu.Lock()
	status, exists := r.nodeStatus[nodeName]
	if !exists {
		r.mu.Unlock()
		return
	}
	delete(r.nodeStatus, nodeName)
	r.eventLogger.LogNodeDetectedDead(nodeName, len(status.Jobs))
	r.mu.Unlock()

	r.heartbeatMu.Lock()
	r.deadNodes[nodeName] = true
	r.heartbeatMu.Unlock()

	r.stopHeartbeatTimer(nodeName)
	r.evaluateBatch(status.Jobs)
}

func (r *Repo) evaluateBatch(jobs []enc.Name) {
	log.Debug(r, "evaluateBatch", "jobCount", len(jobs))
	if len(jobs) == 0 {
		return
	}

	r.mu.Lock()
	nodeStatusCopy := make(map[string]NodeStatus, len(r.nodeStatus))
	for k, v := range r.nodeStatus {
		nodeStatusCopy[k] = v
	}
	r.mu.Unlock()

	underReplicated := make([]enc.Name, 0, len(jobs))
	for _, job := range jobs {
		if countReplicationInternal(job, nodeStatusCopy) < r.rf {
			underReplicated = append(underReplicated, job)
		}
	}

	if len(underReplicated) == 0 {
		return
	}

	log.Debug(r, "evaluateBatch_underReplicated", "count", len(underReplicated))

	for _, target := range underReplicated {
		r.scheduleReevaluationLoop(target)
	}

	r.distributor.BatchedDistribution(underReplicated)
}

func (r *Repo) updateNodeStatus(publisher string, update *tlv.NodeUpdate) {
	log.Debug(r, "updateNodeStatus", "publisher", publisher, "jobs", len(update.Jobs), "capacity", update.StorageCapacity, "used", update.StorageUsed)
	r.mu.Lock()
	defer r.mu.Unlock()

	oldStatus, hadOldStatus := r.nodeStatus[publisher]
	r.nodeStatus[publisher] = NodeStatus{
		Capacity:    update.StorageCapacity,
		Used:        update.StorageUsed,
		LastUpdated: time.Now(),
		Jobs:        update.Jobs,
	}

	if hadOldStatus {
		oldJobs := make(map[string]bool)
		for _, job := range oldStatus.Jobs {
			oldJobs[job.String()] = true
		}
		newJobs := make(map[string]bool)
		for _, job := range update.Jobs {
			newJobs[job.String()] = true
		}

		var removedJobs []enc.Name
		for _, job := range oldStatus.Jobs {
			if !newJobs[job.String()] {
				removedJobs = append(removedJobs, job)
			}
		}

		if len(removedJobs) > 0 {
			log.Debug(r, "updateNodeStatus_jobsChanged", "removedJobs", len(removedJobs))
			go r.evaluateBatch(removedJobs)
		}
	}
}
