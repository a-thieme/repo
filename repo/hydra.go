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

	h.repo.publishUpdateStats(nil)

	for {
		select {
		case <-h.quitCh:
			return
		case <-ticker.C:
			h.repo.publishUpdateStats(nil)
		}
	}
}

func (h *HydraMechanism) Stop() {
	if h.quitCh != nil {
		close(h.quitCh)
	}
}

func (h *HydraMechanism) OnCommand(cmd *tlv.Command) *tlv.NodeUpdate {
	log.Debug(h.repo, "hydra_onCommand_enter", "target", cmd.Target.String())
	log.Info(h.repo, "hydra_onCommand", "target", cmd.Target.String(), "node", h.repo.myNodeName())

	nodeStatusCopy := h.repo.nodeStatusCopy()

	assignment := h.repo.DetermineWinners(cmd.Target, nodeStatusCopy)
	log.Info(h.repo, "hydra_onCommand_winners", "target", cmd.Target.String(), "winners", assignment)

	update := &tlv.NodeUpdate{}
	if assignment != nil {
		update.JobAssignments = append(update.JobAssignments, assignment)
		if h.repo.amAssignee(assignment) && h.repo.doTarget(cmd.Target) {
			update.Jobs = h.repo.getMyJobs()
		}
	}
	update.StorageUsed, update.StorageCapacity = h.repo.getStorageStats()
	return update
}

func (h *HydraMechanism) RunDistribution(cmd *tlv.Command) {
	log.Debug(h.repo, "hydra_runDistribution_enter", "target", cmd.Target.String())
	log.Info(h.repo, "hydra_runDistribution", "target", cmd.Target.String(), "node", h.repo.myNodeName())

	nodeStatusCopy := h.repo.nodeStatusCopy()

	log.Debug(h.repo, "hydra_runDistribution_determineWinners", "target", cmd.Target.String())
	assignment := h.repo.DetermineWinners(cmd.Target, nodeStatusCopy)
	log.Debug(h.repo, "hydra_runDistribution_winners", "target", cmd.Target.String(), "assignees", assignment.Assignees)
	log.Info(h.repo, "hydra_onCommand_winners", "target", cmd.Target.String(), "winners", assignment)

	update := &tlv.NodeUpdate{NewCommand: cmd}

	if assignment != nil {
		update.JobAssignments = append(update.JobAssignments, assignment)
		if h.repo.amAssignee(assignment) && h.repo.doTarget(cmd.Target) {
			update.Jobs = h.repo.getMyJobs()
		}
	}
	update.StorageUsed, update.StorageCapacity = h.repo.getStorageStats()
	h.repo.publishUpdateStats(update)
}

func (h *HydraMechanism) BatchedDistribution(jobs []enc.Name) {
	log.Debug(h.repo, "hydra_batchedDistribution_enter", "jobCount", len(jobs))
	log.Info(h.repo, "hydra_batchedDistribution", "jobCount", len(jobs))

	nodeStatusCopy := h.repo.nodeStatusCopy()

	var assignments []*tlv.JobAssignment
	for _, target := range jobs {
		assignment := h.repo.DetermineWinners(target, nodeStatusCopy)
		if assignment != nil {
			log.Info(h.repo, "hydra_batched_winner", "target", target.String(), "winners", assignment.Assignees)
			assignments = append(assignments, assignment)

			if h.repo.amAssignee(assignment) {
				log.Info(h.repo, "hydra_batched_claiming", "target", target.String())
				h.repo.doTarget(target)
			}
		}
	}

	if len(assignments) > 0 {
		update := &tlv.NodeUpdate{
			JobAssignments: assignments,
			NewCommand:     nil,
		}
		update.StorageUsed, update.StorageCapacity = h.repo.getStorageStats()
		h.repo.publishUpdateStats(update)
		log.Info(h.repo, "hydra_batched_published", "assignmentCount", len(assignments))
	}
}
