package main

import (
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
)

func (r *Repo) checkPendingAssignment(cmd *tlv.Command) {
	targetStr := cmd.Target.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.pendingAssignments[targetStr]; ok {
		log.Info(r, "checkPendingAssignment_found", "target", targetStr)
		cmd := r.commands[targetStr]
		if cmd != nil {
			r.doCmd(cmd)
			r.publishNodeUpdate(&tlv.NodeUpdate{
				Jobs: r.getMyJobs(),
			})
		}
		delete(r.pendingAssignments, targetStr)
	}
}

func (r *Repo) processJobAssignment(assignment *tlv.JobAssignment) {
	r.processJobAssignments([]*tlv.JobAssignment{assignment})
}

func (r *Repo) processJobAssignments(assignments []*tlv.JobAssignment) {
	addedJob := false

	// iterate through assignments,
	for _, assignment := range assignments {
		target := assignment.Target
		targetStr := target.String()
		assignees := encNamesToStrings(assignment.Assignees)

		// skip assignment that isn't for us
		if !r.amAssignee(assignment) {
			log.Info(r, "processJobAssignments_skipped", "reason", "not_in_assignees", "target", targetStr)
			r.eventLogger.LogAssignmentHandled(targetStr, "unknown", "skipped", "not_in_assignees", assignees)
			continue
		}

		// Check if command is in r.commands
		cmd := r.getCommand(target)
		if cmd == nil {
			log.Info(r, "processJobAssignments_pending", "target", targetStr)
			r.mu.Lock()
			r.pendingAssignments[targetStr] = assignment
			r.mu.Unlock()
			r.eventLogger.LogAssignmentHandled(targetStr, "unknown", "pending", "command_not_received", assignees)
			continue
		}

		// we were assigned and have the command
		if r.amIDoingJobStr(targetStr) {
			log.Info(r, "processJobAssignments_alreadyDoing", "target", targetStr)
			continue
		}

		// we are assigned and it still needs to be done
		if r.countReplication(target) < r.rf && r.doCmd(cmd) {
			log.Info(r, "processJobAssignments_will_do_job", "target", targetStr, "myNode", r.myNodeName())
			addedJob = true
			r.countReplication(target) // this will cancel the replication loop if it's no longer needed
		}
	}

	if addedJob {
		r.publishNodeUpdate(&tlv.NodeUpdate{
			Jobs: r.getMyJobs(),
		})
	}
}

// redistMu thread-safe
func (r *Repo) scheduleReevaluationLoop(target enc.Name) {
	targetStr := target.String()
	// NOTE: this should be longer than it takes for a distribution to happen
	delay := 2 * time.Second

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
	r.redistMu.Lock()
	delete(r.scheduledRedistributions, targetStr)
	r.redistMu.Unlock()

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
