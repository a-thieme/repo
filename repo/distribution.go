package main

import (
	"fmt"
	"slices"

	"github.com/a-thieme/repo/repo/util"
	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
)

type DistributionMechanism interface {
	Mechanism() string

	// let the DistributionMechanism decide to add anything NodeUpdate
	OnCommand(cmd *tlv.Command) *tlv.NodeUpdate

	// FIXME: this should be a unified function that repo.go or shared.go does:
	// it should set the nodes to be viewed as dead and then evaluate all the jobs they were doing
	OnNodeDead(nodeName string, jobs []enc.Name)

	OnHeartbeatTick()

	RunDistribution(cmd *tlv.Command)

	AttachHandlers(client ndn.Client, bidPrefix enc.Name)
}

func NewDistributionMechanism(repo *Repo, name string) DistributionMechanism {
	switch name {
	case "auction":
		return NewAuctionMechanism(repo)
	case "hydra":
		return NewHydraMechanism(repo)
	default:
		log.Fatal(nil, "unknown_distribution_mechanism", "mechanism", name)
		return nil
	}
}

func DetermineWinners(target enc.Name, nodeStatus map[string]NodeStatus, myName string, rf int, eventLogger util.Logger) *tlv.JobAssignment {
	currentReplication := countReplicationInternal(target, nodeStatus)

	candidates := make([]string, 0, len(nodeStatus))
	usedPercentage := make(map[string]float64)
	capacity := make(map[string]uint64)

	for name, status := range nodeStatus {
		isDoing := false
		for _, job := range status.Jobs {
			if job.Equal(target) {
				isDoing = true
				break
			}
		}
		if !isDoing {
			up := status.UsedSpace()
			usedPercentage[name] = up
			capacity[name] = status.Capacity
			candidates = append(candidates, name)
		}
	}

	log.Info(nil, "determineWinners_debug", "target", target.String(), "nodeStatus_len", len(nodeStatus), "candidates", candidates)

	needed := rf - currentReplication

	if needed <= 0 {
		if eventLogger != nil {
			eventLogger.LogDecisionMade(
				target.String(),
				false,
				"replication_satisfied",
				fmt.Sprintf("current=%d needed=%d", currentReplication, needed),
				currentReplication,
				needed,
				candidates,
				nil,
				nil,
			)
		}
		return nil
	}

	slices.SortFunc(candidates, func(i, j string) int {
		if usedPercentage[i] != usedPercentage[j] {
			if usedPercentage[i] < usedPercentage[j] {
				return -1
			}
			return 1
		}
		if capacity[i] != capacity[j] {
			if capacity[i] > capacity[j] {
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
		return 0
	})

	limit := min(needed, len(candidates))
	selectedCandidates := candidates[:limit]

	candidateScores := make(map[string]int)
	for i, c := range candidates {
		candidateScores[c] = len(candidates) - i
	}

	shouldClaim := false
	reason := "not_selected"
	if slices.Contains(selectedCandidates, myName) {
		shouldClaim = true
		reason = "selected_as_candidate"
	}

	if eventLogger != nil {
		eventLogger.LogDecisionMade(
			target.String(),
			shouldClaim,
			reason,
			fmt.Sprintf("current=%d needed=%d selected=%v", currentReplication, needed, selectedCandidates),
			currentReplication,
			needed,
			candidates,
			candidateScores,
			selectedCandidates,
		)
	}
	return &tlv.JobAssignment{Target: target, Assignees: stringNamesToEncNames(selectedCandidates)}
}
