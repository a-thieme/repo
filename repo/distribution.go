package main

import (
	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
)

type DistributionMechanism interface {
	Mechanism() string

	Start(client ndn.Client, groupPrefix enc.Name) error

	OnCommand(cmd *tlv.Command) *tlv.NodeUpdate

	RunDistribution(cmd *tlv.Command)

	BatchedDistribution(jobs []enc.Name)
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
