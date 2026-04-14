package main

import (
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
)

type HydraMechanism struct {
	repo   *Repo
	quitCh chan struct{}
}

func NewHydraMechanism(repo *Repo) *HydraMechanism {
	return &HydraMechanism{repo: repo}
}

func (h *HydraMechanism) String() string {
	return "hydra"
}

func (h *HydraMechanism) Mechanism() string {
	return "hydra"
}

func (h *HydraMechanism) Start(client ndn.Client, groupPrefix enc.Name) error {
	h.quitCh = make(chan struct{})
	go h.runHeartbeat()
	return nil
}

func (h *HydraMechanism) runHeartbeat() {
	ticker := time.NewTicker(h.repo.heartbeatInterval)
	defer ticker.Stop()

	h.PublishUpdate(nil)

	for {
		select {
		case <-h.quitCh:
			return
		case <-ticker.C:
			h.PublishUpdate(nil)
		}
	}
}

func (h *HydraMechanism) Stop() {
	if h.quitCh != nil {
		close(h.quitCh)
	}
}

func (h *HydraMechanism) GetAvailability(under []UnderStats) map[string]Availability {
	cpy := h.repo.nodeStatusCopy()
	out := make(map[string]Availability)
	for _, stat := range under {
		for _, nodeName := range stat.Candidates {
			status, exists := cpy[nodeName]
			if exists {
				out[nodeName] = Availability{
					PercentUsed:   status.UsedSpace(),
					TotalCapacity: status.Capacity,
				}
			}

		}
	}
	return out
}

func (h *HydraMechanism) PublishAssignments(assignments []*tlv.JobAssignment) {
	update := &tlv.NodeUpdate{JobAssignments: assignments}
	if h.repo.ProcessJobAssignments(assignments) {
		jobs := h.repo.getMyJobs()
		update.Jobs = make([]*tlv.JobInfo, len(jobs))
		for i, job := range jobs {
			update.Jobs[i] = &tlv.JobInfo{Target: job.Target, StorageSpace: job.Storage}
		}
	}
	h.PublishUpdate(update)
}

// on producer command
func (h *HydraMechanism) OnCommand(cmd *tlv.Command) *tlv.NodeUpdate {
	log.Info(h.repo, "hydra_onCommand", "target", cmd.Target.String(), "node", h.repo.myNodeName())
	// assignment := h.repo.DetermineWinners(cmd.Target)

	update := &tlv.NodeUpdate{}
	// if assignment != nil {
	// 	update.JobAssignments = append(update.JobAssignments, assignment)
	// 	if h.repo.amAssignee(assignment) && h.repo.doTarget(cmd.Target) {
	// 		update.Jobs = h.repo.getMyJobs()
	// 	}
	// }
	update.StorageUsed, update.StorageCapacity = h.repo.getStorageStats()
	return update
}

// func (h *HydraMechanism) RunDistribution(cmd *tlv.Command) {
// 	h.BatchedDistribution([]enc.Name{cmd.Target})
// }

func (h *HydraMechanism) PublishJobs() {
	jobs := h.repo.getMyJobs()
	tlvJobs := make([]*tlv.JobInfo, len(jobs))
	for i, job := range jobs {
		tlvJobs[i] = &tlv.JobInfo{Target: job.Target, StorageSpace: job.Storage}
	}
	h.PublishUpdate(&tlv.NodeUpdate{Jobs: tlvJobs})
}

// publish update with stats attached
func (h *HydraMechanism) PublishUpdate(update *tlv.NodeUpdate) {
	if update == nil {
		update = &tlv.NodeUpdate{}
	}
	update.StorageCapacity, update.StorageUsed = h.repo.getStorageStats()
	h.repo.publishUpdate(update)
}

// func (h *HydraMechanism) BatchedDistribution(jobs []enc.Name) {
// 	log.Info(h.repo, "hydra_batchedDistribution", "jobCount", len(jobs))
//
// 	var assignments []*tlv.JobAssignment
// 	includeJobs := false
// 	for _, target := range jobs {
// 		assignment := h.repo.DetermineWinners(target)
// 		if assignment != nil {
// 			log.Info(h.repo, "hydra_batched_winner", "target", target.String(), "winners", assignment.Assignees)
// 			assignments = append(assignments, assignment)
//
// 			if h.repo.amAssignee(assignment) && h.repo.doTarget(target) {
// 				includeJobs = true
// 			}
// 		}
// 	}
//
// 	if len(assignments) > 0 {
// 		update := &tlv.NodeUpdate{JobAssignments: assignments}
// 		if includeJobs {
// 			update.Jobs = h.repo.getMyJobs()
// 		}
// 		h.PublishUpdate(update)
// 		log.Info(h.repo, "hydra_batched_published", "assignmentCount", len(assignments))
// 	}
// }
