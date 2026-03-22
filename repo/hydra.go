package main

import (
	"slices"

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

func (h *HydraMechanism) OnCommand(cmd *tlv.Command) *tlv.JobAssignment {
	log.Info(h.repo, "hydra_onCommand", "target", cmd.Target.String(), "node", h.repo.myNodeName())
	winners := DetermineWinners(cmd.Target, h.repo.nodeStatus, h.repo.myNodeName(), h.repo.rf, h.repo.eventLogger)
	log.Info(h.repo, "hydra_onCommand_winners", "target", cmd.Target.String(), "winners", winners)
	if winners != nil {
		myName := h.repo.myNodeName()
		inWinners := slices.Contains(winners, myName)
		log.Info(h.repo, "hydra_onCommand_doTarget_check", "target", cmd.Target.String(), "myName", myName, "inWinners", inWinners)
		if inWinners {
			claimed := h.repo.doTarget(cmd.Target)
			log.Info(h.repo, "hydra_onCommand_claimed", "target", cmd.Target.String(), "claimed", claimed, "node", myName)
			return &tlv.JobAssignment{
				Target:    cmd.Target,
				Assignees: stringNamesToEncNames(winners),
			}
		}
	}
	return nil
}

func (h *HydraMechanism) OnGroupSync(update *tlv.NodeUpdate, publisherName string) {
	log.Info(h.repo, "hydra_onGroupSync", "publisher", publisherName, "hasNewCmd", update.NewCommand != nil, "hasAssignments", len(update.JobAssignments) > 0)
	if update.NewCommand != nil {
		log.Info(h.repo, "hydra_onGroupSync_newCmd", "target", update.NewCommand.Target.String(), "publisher", publisherName)
		h.repo.checkPendingAssignment(update.NewCommand.Target)
	}

	if len(update.JobAssignments) > 0 {
		for _, ja := range update.JobAssignments {
			log.Info(h.repo, "hydra_onGroupSync_assignment", "target", ja.Target.String(), "assignees", encNamesToStrings(ja.Assignees), "publisher", publisherName)
		}
		h.repo.processJobAssignments(update.JobAssignments, publisherName, "hydra")
	}

	if update.NewCommand != nil {
		currentReplication := h.repo.countReplication(update.NewCommand.Target)
		needed := h.repo.rf - currentReplication
		log.Info(h.repo, "hydra_onGroupSync_reevaluate", "target", update.NewCommand.Target.String(), "currentRep", currentReplication, "needed", needed)
		if needed > 0 {
			h.repo.scheduleReevaluationLoop(update.NewCommand.Target)
		}
	}
}

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
	winners := DetermineWinners(cmd.Target, h.repo.nodeStatus, h.repo.myNodeName(), h.repo.rf, h.repo.eventLogger)
	if winners == nil {
		log.Info(h.repo, "hydra_runDistribution_noWinners", "target", cmd.Target.String())
		return
	}
	log.Info(h.repo, "hydra_runDistribution_winners", "target", cmd.Target.String(), "winners", winners)

	myName := h.repo.myNodeName()
	if slices.Contains(winners, myName) {
		claimed := h.repo.doTarget(cmd.Target)
		log.Info(h.repo, "hydra_runDistribution_claimed", "target", cmd.Target.String(), "claimed", claimed, "node", myName)
	}

	assignments := []*tlv.JobAssignment{{
		Target:    cmd.Target,
		Assignees: stringNamesToEncNames(winners),
	}}
	h.repo.publishJobAssignments(assignments)
}

func (h *HydraMechanism) AttachHandlers(client ndn.Client, bidPrefix enc.Name) {
}
