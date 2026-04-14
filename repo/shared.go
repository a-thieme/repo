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
	// Storage will be determined by looking up maximum reported storage across all nodes
	// Only use deterministic fallback if no storage info is available from any node
	r.evaluateBatch([]JobInfo{{Target: target, Storage: 0}}, force)
}

func (r *Repo) getJobStorageSize(target enc.Name) uint64 {
	targetStr := target.String()
	cmd := r.getCommand(target)
	if cmd != nil && cmd.Type == "INSERT" {
		return hashFromString(targetStr) % MaxInsertCost
	}
	// For JOIN jobs, look up from jobStorageUsage if available
	if r.jobStorageUsage != nil {
		if size, exists := r.jobStorageUsage[targetStr]; exists {
			return size
		}
	}
	return 0
}

func (r *Repo) evaluateBatch(jobs []JobInfo, force bool) {
	log.Info(r, "evaluate_batch_start", "jobCount", len(jobs))
	if len(jobs) == 0 {
		return
	}
	allUnder := make([]UnderStats, 0, len(jobs))
	for _, job := range jobs {
		stats := r.checkUnder(job.Target)
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

	// Build a map of job storage sizes - use MAXIMUM reported storage across all nodes
	// This is the actual storage cost that should be used for size-aware calculations
	nodeStatus := r.nodeStatusCopy()
	jobSizes := make(map[string]uint64)
	for _, job := range jobs {
		targetStr := job.Target.String()
		// Start with the storage reported in the job itself (may be from local node)
		maxStorage := job.Storage
		// Look through all nodes to find the maximum reported storage for this job
		for _, status := range nodeStatus {
			for _, nodeJob := range status.Jobs {
				if nodeJob.Target.String() == targetStr && nodeJob.Storage > maxStorage {
					maxStorage = nodeJob.Storage
				}
			}
		}
		jobSizes[targetStr] = maxStorage
		log.Debug(r, "job_size determined", "target", targetStr, "maxStorage", maxStorage)
	}

	// For each job, pick the best candidates considering job sizes
	// We simulate the impact of each assignment on availability
	assignments := []*tlv.JobAssignment{}

	// Track simulated availabilities (we'll use these to make size-aware decisions)
	// Initialize with current availabilities
	simulatedAvail := make(map[string]float64)
	for node, avail := range availabilities {
		simulatedAvail[node] = avail.PercentUsed
	}

	for _, under := range allUnder {
		jobSize := jobSizes[under.Target.String()]
		needed := under.Needed

		// Select winners from candidates, considering job size impact
		winners := r.selectCandidatesWithSizeAwareness(under.Candidates, needed, jobSize, simulatedAvail, availabilities)

		// Log winner selection for this target
		log.Info(r, "winners_selected",
			"target", under.Target.String(),
			"needed", needed,
			"jobSize", jobSize,
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

// selectCandidatesWithSizeAwareness picks the best candidates for a job,
// considering how the job's storage size would impact each candidate's availability.
// It simulates assignments and updates simulatedAvail accordingly.
func (r *Repo) selectCandidatesWithSizeAwareness(
	candidates []string,
	needed int,
	jobSize uint64,
	simulatedAvail map[string]float64,
	actualAvail map[string]Availability,
) []string {
	winners := []string{}

	for len(winners) < needed {
		bestNode := ""
		bestScore := -1.0

		for _, candidate := range candidates {
			// Skip if already selected for this job
			if slices.Contains(winners, candidate) {
				continue
			}
			// Only consider candidates with availability data
			if availData, ok := actualAvail[candidate]; ok && availData.TotalCapacity > 0 {
				avail := simulatedAvail[candidate]
				if avail < bestScore || bestNode == "" {
					bestScore = avail
					bestNode = candidate
				}
			}
		}

		if bestNode == "" {
			break // No more valid candidates
		}

		winners = append(winners, bestNode)
		// Simulate the assignment's impact on this node's availability
		if availData, ok := actualAvail[bestNode]; ok && availData.TotalCapacity > 0 {
			additionalPercent := float64(jobSize) / float64(availData.TotalCapacity)
			simulatedAvail[bestNode] += additionalPercent
		}
	}

	return winners
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

	// Convert tlv.JobInfo to internal JobInfo
	jobs := make([]JobInfo, len(update.Jobs))
	for i, job := range update.Jobs {
		jobs[i] = JobInfo{Target: job.Target, Storage: job.StorageSpace}
	}

	r.nodeStatus[publisher] = NodeStatus{
		Capacity: update.StorageCapacity,
		Used:     update.StorageUsed,
		Jobs:     jobs,
	}

	// Only check for job removals if the update contains an explicit Jobs field
	// A nil Jobs field means "no change to jobs" - not "all jobs removed"
	if hadOldStatus && update.Jobs != nil {
		oldJobs := make(map[string]bool)
		for _, job := range oldStatus.Jobs {
			oldJobs[job.Target.String()] = true
		}
		newJobs := make(map[string]bool)
		for _, job := range update.Jobs {
			newJobs[job.Target.String()] = true
		}

		var removedJobInfos []JobInfo
		for _, job := range oldStatus.Jobs {
			if !newJobs[job.Target.String()] {
				removedJobInfos = append(removedJobInfos, job)
			}
		}

		if len(removedJobInfos) > 0 {
			log.Info(r, "node_status_jobs_removed", "publisher", publisher, "removedJobs", len(removedJobInfos))
			r.evaluateBatch(removedJobInfos, false)
		}
	}
}
