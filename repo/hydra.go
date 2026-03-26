package main

import (
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
)

type HydraMechanism struct {
	repo *Repo
}

func NewHydraMechanism(repo *Repo) *HydraMechanism {
	return &HydraMechanism{repo: repo}
}

func (h *HydraMechanism) Mechanism() string {
	return "hydra"
}

func (h *HydraMechanism) OnCommand(cmd *tlv.Command) *tlv.NodeUpdate {
	log.Info(h.repo, "hydra_onCommand", "target", cmd.Target.String(), "node", h.repo.myNodeName())

	// Create nodeStatus copy that includes local node's r.jobs
	// FIXME: once we put r.jobs into repo.nodeStatus, we won't need any of this extra logic
	nodeStatusCopy := make(map[string]NodeStatus)
	for k, v := range h.repo.nodeStatus {
		nodeStatusCopy[k] = v
	}
	h.repo.mu.Lock()
	nodeStatusCopy[h.repo.myNodeName()] = NodeStatus{
		Capacity:    h.repo.storageCapacity,
		Used:        h.repo.storageUsed,
		Jobs:        make([]enc.Name, len(h.repo.jobs)),
		LastUpdated: time.Now(),
	}
	copy(nodeStatusCopy[h.repo.myNodeName()].Jobs, h.repo.jobs)
	h.repo.mu.Unlock()

	assignment := DetermineWinners(cmd.Target, nodeStatusCopy, h.repo.myNodeName(), h.repo.rf, h.repo.eventLogger)
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

// FIXME: unify OnNodeDead
func (h *HydraMechanism) OnNodeDead(nodeName string, jobs []enc.Name) {
	h.repo.mu.Lock()
	status, exists := h.repo.nodeStatus[nodeName]
	if !exists {
		h.repo.mu.Unlock()
		return
	}

	delete(h.repo.nodeStatus, nodeName)
	h.repo.eventLogger.LogNodeDetectedDead(nodeName, len(status.Jobs))
	h.repo.mu.Unlock()

	for _, target := range jobs {
		h.repo.scheduleReevaluationLoop(target)
	}
}

func (h *HydraMechanism) OnHeartbeatTick() {
}

func (h *HydraMechanism) RunDistribution(cmd *tlv.Command) {
	log.Info(h.repo, "hydra_runDistribution", "target", cmd.Target.String(), "node", h.repo.myNodeName())

	// Create nodeStatus copy that includes local node's unpublished r.jobs
	// FIXME: we won't need this extra copy logic once r.jobs is merged into repo.nodeStatus
	nodeStatusCopy := make(map[string]NodeStatus)
	for k, v := range h.repo.nodeStatus {
		nodeStatusCopy[k] = v
	}
	h.repo.mu.Lock()
	nodeStatusCopy[h.repo.myNodeName()] = NodeStatus{
		Capacity:    h.repo.storageCapacity,
		Used:        h.repo.storageUsed,
		Jobs:        make([]enc.Name, len(h.repo.jobs)),
		LastUpdated: time.Now(),
	}
	copy(nodeStatusCopy[h.repo.myNodeName()].Jobs, h.repo.jobs)
	h.repo.mu.Unlock()

	assignment := DetermineWinners(cmd.Target, nodeStatusCopy, h.repo.myNodeName(), h.repo.rf, h.repo.eventLogger)
	log.Info(h.repo, "hydra_onCommand_winners", "target", cmd.Target.String(), "winners", assignment)

	update := &tlv.NodeUpdate{NewCommand: cmd}

	if assignment != nil {
		update.JobAssignments = append(update.JobAssignments, assignment)
		if h.repo.amAssignee(assignment) && h.repo.doTarget(cmd.Target) {
			update.Jobs = h.repo.getMyJobs()
		}
	}
	update.StorageUsed, update.StorageCapacity = h.repo.getStorageStats()
	h.repo.publishNodeUpdate(update)
}

// NOTE: placeholder for auction compatability?
func (h *HydraMechanism) AttachHandlers(client ndn.Client, bidPrefix enc.Name) {
}
