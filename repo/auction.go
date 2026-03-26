package main

import (
	"sync"
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
)

const (
	BID_SUFFIX     = "bid"
	RESULTS_SUFFIX = "results"
)

type AuctionMechanism struct {
	repo *Repo
}

func NewAuctionMechanism(repo *Repo) *AuctionMechanism {
	return &AuctionMechanism{repo: repo}
}

func (a *AuctionMechanism) String() string {
	return "auction"
}

func (a *AuctionMechanism) AttachHandlers(client ndn.Client, bidPrefix enc.Name) {
	if client.AttachCommandHandler(bidPrefix, a.onBidInterest) == nil {
		log.Error(a, "AttachCommandHandler", "bidPrefix", bidPrefix)
	}
}

func (a *AuctionMechanism) Mechanism() string {
	return "auction"
}

func (a *AuctionMechanism) OnCommand(cmd *tlv.Command) *tlv.NodeUpdate {
	go a.RunDistribution(cmd)
	return nil
}

func (a *AuctionMechanism) OnNodeDead(nodeName string, jobs []enc.Name) {
	for _, target := range jobs {
		a.repo.scheduleReevaluationLoop(target)
	}
}

func (a *AuctionMechanism) OnHeartbeatTick() {
	a.repo.auctionHeartbeatSvSync.IncrSeqNo(a.repo.nodePrefix)
}

func (a *AuctionMechanism) RunDistribution(cmd *tlv.Command) {
	targetStr := cmd.Target.String()
	currentReplication := a.repo.countReplication(cmd.Target)
	needed := a.repo.rf - currentReplication

	if needed <= 0 {
		log.Info(a.repo, "runAuction_skip", "reason", "replication_satisfied", "target", targetStr, "current", currentReplication)
		return
	}

	timestamp := uint64(time.Now().UnixNano())
	resultsName := a.repo.nodePrefix.Append(enc.NewGenericComponent(RESULTS_SUFFIX))
	resultsName = resultsName.Append(enc.NewTimestampComponent(timestamp))

	log.Info(a.repo, "runAuction_started", "target", targetStr, "timestamp", timestamp, "current", currentReplication, "needed", needed)

	a.repo.mu.Lock()
	a.repo.currentAuctionTimestamp = timestamp
	a.repo.mu.Unlock()

	if a.repo.eventLogger != nil {
		a.repo.eventLogger.LogAuctionStarted(targetStr, currentReplication, needed, timestamp)
	}

	peers := a.repo.getOtherNodeNames()
	// FIXME: this should only list peers who are not currently doing the target as a job (may need another helper function)
	log.Info(a.repo, "runAuction_peers", "target", targetStr, "peers", peers)

	// edge case where we get a command before seeing peers
	if len(peers) == 0 {
		log.Info(a.repo, "runAuction_no_peers", "target", targetStr)
		a.determineAndPublishWinners(cmd.Target, resultsName, nil)
		return
	}

	peerMetrics := make(map[string]*tlv.NodeUpdate)
	var metricsMu sync.Mutex
	responsesCh := make(chan string, len(peers))

	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(peer string) {
			defer wg.Done()

			peerPrefix, _ := enc.NameFromStr(peer)
			bidName := peerPrefix.Append(enc.NewGenericComponent(BID_SUFFIX))
			bidName = bidName.Append(enc.NewTimestampComponent(timestamp))

			metricReq := &tlv.MetricRequest{
				Target:      cmd.Target,
				ResultsName: resultsName,
				Auctioneer:  a.repo.nodePrefix,
			}

			log.Info(a.repo, "runAuction_sending_bid_interest", "peer", peer, "bidName", bidName.String())
			a.repo.client.ExpressR(ndn.ExpressRArgs{
				Name: bidName,
				Config: &ndn.InterestConfig{
					CanBePrefix: false,
					MustBeFresh: true, // TODO: see if this is needed
				},
				AppParam: metricReq.Encode(),
				Retries:  3,
				Callback: func(args ndn.ExpressCallbackArgs) {
					if args.Result != ndn.InterestResultData {
						log.Warn(a.repo, "runAuction_bid_failed", "peer", peer, "result", args.Result)
						return
					}

					log.Info(a.repo, "runAuction_bid_received", "peer", peer, "result", args.Result)
					metricResp, err := tlv.ParseNodeUpdate(enc.NewWireView(args.Data.Content()), false)
					if err != nil {
						log.Warn(a.repo, "runAuction_response_parse_failed", "peer", peer, "err", err)
						return
					}

					// save response
					metricsMu.Lock()
					peerMetrics[peer] = metricResp
					metricsMu.Unlock()

					// FIXME: this should just reset the heartbeat timeout timer for this node
					a.repo.mu.Lock()
					if status, ok := a.repo.nodeStatus[peer]; ok {
						status.LastUpdated = time.Now()
						a.repo.nodeStatus[peer] = status
					}
					a.repo.mu.Unlock()

					select {
					case responsesCh <- peer:
					default:
					}
				},
			})
		}(peer)
	}
	wg.Wait()

	auctionTimer := time.NewTimer(a.repo.auctionTimeout)
	receivedCount := 0
	for {
		select {
		case <-auctionTimer.C:
			log.Info(a.repo, "runAuction_timeout", "target", targetStr, "received", receivedCount, "total_peers", len(peers))
			goto determine_winners // FIXME: why do we use goto instead of break here?
		case <-responsesCh:
			receivedCount++
			log.Debug(a.repo, "runAuction_bid_received", "peer", "unknown", "count", receivedCount, "total", len(peers))
			if receivedCount == len(peers) {
				goto determine_winners // FIXME: why do we use goto instead of break here?
			}
		}
	}

determine_winners:
	a.determineAndPublishWinners(cmd.Target, resultsName, peerMetrics)
}

func (a *AuctionMechanism) determineAndPublishWinners(target enc.Name, resultsName enc.Name, peerMetrics map[string]*tlv.NodeUpdate,
) {
	targetStr := target.String()

	a.repo.mu.Lock()
	// FIXME: verify that this is fine for thread safety
	peerMetrics[a.repo.myNodeName()] = &tlv.NodeUpdate{
		StorageCapacity: a.repo.storageCapacity,
		StorageUsed:     a.repo.storageUsed,
		Jobs:            a.repo.jobs,
	}
	a.repo.mu.Unlock()

	cmd := a.repo.getCommand(target)
	if cmd == nil {
		return
	}

	// FIXME: determine whether to use NodeUpdate or NodeStatus here for this DetermineWinners call, and update accordingly
	assignment := DetermineWinners(target, peerMetrics, a.repo.myNodeName(), a.repo.rf, a.repo.eventLogger)
	log.Info(a.repo, "runAuction_winners", "target", targetStr, "winners", assignment.Assignees)
	err := a.repo.client.Store().Put(resultsName, assignment.Encode().Join())
	if err != nil {
		log.Warn(a.repo, "runAuction_publish_failed", "err", err, "resultsName", resultsName.String())
	}

	if a.repo.amAssignee(assignment) {
		log.Info(a.repo, "runAuction_claiming_job", "target", targetStr, "myNode", a.repo.myNodeName())
		a.repo.doTarget(target)
		a.repo.publishNodeUpdate(&tlv.NodeUpdate{
			Jobs: a.repo.getMyJobs(),
		})
	}

	a.repo.eventLogger.LogAuctionResults(targetStr, resultsName.String(), encNamesToStrings(assignment.Assignees))
}

func (a *AuctionMechanism) onBidInterest(name enc.Name, params enc.Wire, reply func(wire enc.Wire) error) {
	log.Debug(a.repo, "onBidInterest_received", "name", name.String())

	metricReq, err := tlv.ParseMetricRequest(enc.NewWireView(params), false)
	if err != nil {
		log.Warn(a.repo, "onBidInterest_parse_failed", "err", err)
		return
	}

	log.Info(a.repo, "onBidInterest_processing", "target", metricReq.Target.String(), "auctioneer", metricReq.Auctioneer)
	capacity, used := a.repo.getStorageStats()
	response := &tlv.NodeUpdate{
		StorageCapacity: capacity,
		StorageUsed:     used,
	}

	log.Debug(a.repo, "replyingToBid", "name", name)
	reply(response.Encode())

	resultsName := metricReq.ResultsName
	target := metricReq.Target
	a.repo.eventLogger.LogAuctionBid(target.String(), metricReq.Auctioneer.String(), capacity, used, false)

	log.Debug(a.repo, "subscribeToResults", "resultsName", resultsName.String(), "target", target.String())
	a.repo.client.ExpressR(ndn.ExpressRArgs{
		Name: resultsName,
		Config: &ndn.InterestConfig{
			CanBePrefix: false,
			MustBeFresh: true,
		},
		Retries: 3,
		Callback: func(args ndn.ExpressCallbackArgs) {
			if args.Result != ndn.InterestResultData {
				log.Warn(a.repo, "subscribeToResults_failed", "result", args.Result)
				return
			}

			assignment, err := tlv.ParseJobAssignment(enc.NewWireView(args.Data.Content()), false)
			if err != nil {
				log.Error(a, "ParseJobAssignment", "failed", target)
				return
			}
			a.repo.processJobAssignment(assignment)
		},
	})
}
