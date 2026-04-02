package main

import (
	"slices"
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
)

func (r *Repo) ProcessJobAssignment(assignment *tlv.JobAssignment) bool {
	return r.ProcessJobAssignments([]*tlv.JobAssignment{assignment})
}

func (r *Repo) ProcessJobAssignments(assignments []*tlv.JobAssignment) bool {
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
			r.scheduleReevaluationLoop(target)
		}
	}
	return addedJob
}

// redistMu thread-safe
func (r *Repo) scheduleReevaluationLoop(target enc.Name) {
	targetStr := target.String()
	// NOTE: this should be longer than it takes for a distribution to happen
	// 10 seconds gives enough time for SVS propagation and auction completion
	delay := 10 * time.Second

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
	r.evaluateBatch([]enc.Name{target})
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

func (r *Repo) evaluateBatch(jobs []enc.Name) {
	log.Debug(r, "evaluateBatch", "jobCount", len(jobs))
	if len(jobs) == 0 {
		return
	}
	allUnder := make([]UnderStats, 0, len(jobs))
	for _, target := range jobs {
		stats := r.checkUnder(target)
		if stats.Needed > 0 {
			allUnder = append(allUnder, stats)
			r.scheduleReevaluationLoop(target)
		} else {
			r.cancelReevaluation(target)
		}
	}
	if !r.amILeader() {
		return
	}
	availabilities := r.distributor.GetAvailability(allUnder)
	sorted := r.sortCandidates(availabilities)
	// for under in allUnder
	// 	get first needed candidates and make it a job assignment
	assignments := []*tlv.JobAssignment{}
	for _, under := range allUnder {
		added := 0
		winners := []string{}
		for _, candidate := range sorted {
			if slices.Contains(under.Candidates, candidate) {
				winners = append(winners, candidate)
				added += 1
				if added == under.Needed {
					break
				}
			}
		}
		assignments = append(assignments, &tlv.JobAssignment{
			Target:    under.Target,
			Assignees: stringNamesToEncNames(winners),
		})
	}
	r.distributor.PublishAssignments(assignments)
}

func (r *Repo) sortCandidates(abilities map[string]Availability) []string {
	candidates := make([]string, 0, len(abilities))
	for nodeName := range abilities {
		candidates = append(candidates, nodeName)
	}
	// sort by used usedPercentage, total capacity, then name
	slices.SortFunc(candidates, func(i, j string) int {
		ipu := abilities[i].PercentUsed
		jpu := abilities[j].PercentUsed
		if ipu != jpu {
			if ipu < jpu {
				return -1
			}
			return 1
		}
		ic := abilities[i].TotalCapacity
		jc := abilities[j].TotalCapacity
		if ic != jc {
			if ic > jc {
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
		log.Warn(r, "tied", i, j)
		return 0 // shouldn't happen
	})
	return candidates
}

func (r *Repo) updateNodeStatus(publisher string, update *tlv.NodeUpdate) {
	log.Debug(r, "updateNodeStatus", "publisher", publisher, "jobs", len(update.Jobs), "capacity", update.StorageCapacity, "used", update.StorageUsed)
	r.mu.Lock()
	defer r.mu.Unlock()

	oldStatus, hadOldStatus := r.nodeStatus[publisher]
	r.nodeStatus[publisher] = NodeStatus{
		Capacity: update.StorageCapacity,
		Used:     update.StorageUsed,
		Jobs:     update.Jobs,
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
