package main

import (
	"fmt"
	"slices"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
)

func (r *Repo) ProcessJobAssignment(assignment *tlv.JobAssignment) bool {
	return r.ProcessJobAssignments([]*tlv.JobAssignment{assignment})
}

func (r *Repo) ProcessJobAssignments(assignments []*tlv.JobAssignment) bool {
	log.Info(r, "process_job_assignments_start", "count", len(assignments))
	addedJob := false

	// iterate through assignments,
	for _, assignment := range assignments {
		target := assignment.Target
		targetStr := target.String()
		log.Info(r, "process_job_assignment", "target", targetStr, "assignees", assignment.Assignees, "correlationID", targetStr)

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
			// Log decision made event
			currentRep := r.countReplication(target)
			shouldClaim := currentRep < r.rf
			reason := ""
			if !shouldClaim {
				reason = "replication_satisfied"
			}
			assignees := make([]string, len(assignment.Assignees))
			for i, a := range assignment.Assignees {
				assignees[i] = a.String()
			}
			r.eventLogger.LogDecisionMade(
				targetStr,
				shouldClaim,
				reason,
				fmt.Sprintf("current=%d rf=%d", currentRep, r.rf),
				currentRep,
				r.rf,
				assignees,
				nil,
				nil,
			)
			if r.doCmd(cmd) {
				log.Info(r, "processJobAssignments_will_do_job", "target", targetStr, "myNode", r.myNodeName())
				addedJob = true
			}
		}
	}
	return addedJob
}

func (r *Repo) evaluate(target enc.Name, force bool) {
	r.evaluateBatch([]enc.Name{target}, force)
}

func (r *Repo) evaluateBatch(jobs []enc.Name, force bool) {
	log.Info(r, "evaluate_batch_start", "jobCount", len(jobs))
	if len(jobs) == 0 {
		return
	}
	allUnder := make([]UnderStats, 0, len(jobs))
	for _, target := range jobs {
		stats := r.checkUnder(target)
		if stats.Needed > 0 {
			allUnder = append(allUnder, stats)
		}
	}
	if !force && !r.amILeader() {
		log.Info(r, "evaluate_batch_not_leader", "jobCount", len(jobs))
		return
	}
	log.Info(r, "evaluate_batch_leader", "jobCount", len(allUnder))
	availabilities := r.distributor.GetAvailability(allUnder)
	sorted := r.sortCandidates(availabilities)
	// for under in allUnder
	// 	get first needed candidates and make it a job assignment
	assignments := []*tlv.JobAssignment{}
	for _, under := range allUnder {
		// Select winners from candidates
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

		// Log winner selection for this target
		log.Info(r, "winners_selected",
			"target", under.Target.String(),
			"needed", under.Needed,
			"winners", winners,
			"candidates", under.Candidates,
			"correlationID", under.Target.String())

		// Log auction winners event
		winnerScores := make(map[string]float64)
		for _, winner := range winners {
			if avail, ok := availabilities[winner]; ok {
				winnerScores[winner] = avail.PercentUsed
			}
		}
		r.eventLogger.LogAuctionWinners(under.Target.String(), under.Candidates, winnerScores, winners)

		assignments = append(assignments, &tlv.JobAssignment{
			Target:    under.Target,
			Assignees: stringNamesToEncNames(winners),
		})

		// Log job assignment event
		r.eventLogger.LogJobAssignment(under.Target.String(), winners)
	}
	log.Info(r, "assignments_published", "count", len(assignments))
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
	log.Info(r, "node_status_updated", "publisher", publisher, "jobs", len(update.Jobs), "capacity", update.StorageCapacity, "used", update.StorageUsed)
	r.mu.Lock()
	defer r.mu.Unlock()

	oldStatus, hadOldStatus := r.nodeStatus[publisher]
	r.nodeStatus[publisher] = NodeStatus{
		Capacity: update.StorageCapacity,
		Used:     update.StorageUsed,
		Jobs:     update.Jobs,
	}

	// Only check for job removals if the update contains an explicit Jobs field
	// A nil Jobs field means "no change to jobs" - not "all jobs removed"
	if hadOldStatus && update.Jobs != nil {
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
			log.Info(r, "node_status_jobs_removed", "publisher", publisher, "removedJobs", len(removedJobs))
			r.evaluateBatch(removedJobs, false)
		}
	}
}
