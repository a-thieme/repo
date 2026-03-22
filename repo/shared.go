package main

import (
	"slices"
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
)

func (r *Repo) checkPendingAssignment(target enc.Name) {
	targetStr := target.String()
	r.mu.Lock()
	if pending, ok := r.pendingAssignments[targetStr]; ok {
		log.Info(r, "checkPendingAssignment_found", "target", targetStr)
		assignees := encNamesToStrings(pending.Assignees)
		if slices.Contains(assignees, r.myNodeName()) {
			cmd := r.getCommand(target)
			if cmd != nil {
				r.doCmd(cmd)
				r.publishNodeUpdate(&tlv.NodeUpdate{
					Jobs: r.getMyJobs(),
				})
			}
		}
		delete(r.pendingAssignments, targetStr)
	}
	r.mu.Unlock()
}

func (r *Repo) processJobAssignments(assignments []*tlv.JobAssignment, publisherName string, mechanism string) {
	addedJob := false

	for _, assignment := range assignments {
		target := assignment.Target
		targetStr := target.String()
		assignees := encNamesToStrings(assignment.Assignees)

		if r.amIDoingJob(target) {
			log.Info(r, "processJobAssignments_skipped", "reason", "already_doing_job", "target", targetStr)
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "skipped", "already_doing_job", assignees)
			r.scheduleReevaluationLoop(target)
			continue
		}

		if !slices.Contains(assignees, r.myNodeName()) {
			log.Info(r, "processJobAssignments_skipped", "reason", "not_in_assignees", "target", targetStr)
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "skipped", "not_in_assignees", assignees)
			continue
		}

		cmd := r.getCommand(target)
		if cmd == nil {
			log.Info(r, "processJobAssignments_pending", "target", targetStr)
			r.mu.Lock()
			r.pendingAssignments[targetStr] = assignment
			r.mu.Unlock()
			r.eventLogger.LogAssignmentHandled(targetStr, publisherName, "pending", "command_not_received", assignees)
			continue
		}

		log.Info(r, "processJobAssignments_will_do_job", "target", targetStr, "myNode", r.myNodeName())
		if r.doTarget(target) {
			addedJob = true
			r.clearRetryDelay(targetStr)
			r.mu.Lock()
			currentReplication := r.countReplication(target)
			if currentReplication >= r.rf {
				r.cancelReevaluation(target)
			}
			r.mu.Unlock()
		} else {
			delay := r.advanceRetryDelay(targetStr)
			log.Info(r, "processJobAssignments_failed_will_retry", "target", targetStr, "delay", delay.String())
			go func(t enc.Name) {
				time.Sleep(delay)
				r.retryJobAssignment(t, mechanism)
			}(target)
		}
	}

	if addedJob {
		r.publishNodeUpdate(&tlv.NodeUpdate{
			Jobs: r.getMyJobs(),
		})
	}
}

func (r *Repo) scheduleReevaluationLoop(target enc.Name) {
	targetStr := target.String()
	delay := DEFAULT_HEARTBEAT_INTERVAL + time.Second

	r.redistMu.Lock()
	defer r.redistMu.Unlock()

	if t, exists := r.scheduledRedistributions[targetStr]; exists {
		t.Stop()
	}

	log.Info(r, "scheduleReevaluationLoop_scheduled", "target", targetStr, "delay", delay.String(), "isLeader", r.amILeader())

	r.scheduledRedistributions[targetStr] = time.AfterFunc(delay, func() {
		r.redistMu.Lock()
		delete(r.scheduledRedistributions, targetStr)
		r.redistMu.Unlock()

		isLeader := r.amILeader()
		log.Info(r, "scheduleReevaluationLoop_fired", "target", targetStr, "isLeader", isLeader)

		if !isLeader {
			r.scheduleReevaluationLoop(target)
			return
		}

		currentReplication := r.countReplication(target)
		log.Info(r, "scheduleReevaluationLoop_check", "target", targetStr, "currentReplication", currentReplication, "rf", r.rf)

		if currentReplication >= r.rf {
			log.Info(r, "scheduleReevaluationLoop_skip_satisfied", "target", targetStr)
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

		r.mu.Lock()
		currentReplication = r.countReplication(target)
		if currentReplication >= r.rf {
			r.cancelReevaluation(target)
		}
		r.mu.Unlock()
	})
}

func (r *Repo) cancelReevaluation(target enc.Name) {
	targetStr := target.String()
	r.redistMu.Lock()
	defer r.redistMu.Unlock()
	if t, ok := r.scheduledRedistributions[targetStr]; ok {
		t.Stop()
		delete(r.scheduledRedistributions, targetStr)
	}
}
