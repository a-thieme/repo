package main

import (
	"slices"
	"sync"
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
)

const BID_SUFFIX = "bid"
const RESULTS_SUFFIX = "results"

type AuctionMechanism struct {
	repo *Repo
}

func NewAuctionMechanism(repo *Repo) *AuctionMechanism {
	return &AuctionMechanism{repo: repo}
}

func (a *AuctionMechanism) AttachHandlers(client ndn.Client, bidPrefix enc.Name) {
	client.AttachCommandHandler(bidPrefix, a.onBidInterest)
}

func (a *AuctionMechanism) Mechanism() string {
	return "auction"
}

func (a *AuctionMechanism) OnCommand(cmd *tlv.Command) *tlv.JobAssignment {
	a.repo.publishNodeUpdate(&tlv.NodeUpdate{NewCommand: cmd})
	go a.RunDistribution(cmd)
	return nil
}

func (a *AuctionMechanism) OnGroupSync(update *tlv.NodeUpdate, publisherName string) {
	if update.NewCommand != nil {
		a.repo.checkPendingAssignment(update.NewCommand.Target)
	}

	if len(update.JobAssignments) > 0 {
		a.repo.processJobAssignments(update.JobAssignments, publisherName, "auction")
	}

	if update.NewCommand != nil {
		a.runAuctionIfNeeded(update.NewCommand.Target)
	}
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
	if a.repo.distributionMechanism != "auction" {
		return
	}

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
	log.Info(a.repo, "runAuction_peers", "target", targetStr, "peers", peers)

	if len(peers) == 0 {
		log.Info(a.repo, "runAuction_no_peers", "target", targetStr)
		a.determineAndPublishWinners(cmd.Target, timestamp, resultsName, nil)
		return
	}

	peerMetrics := make(map[string]struct {
		capacity uint64
		used     uint64
		delay    bool
	})
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
				Timestamp:   timestamp,
				ResultsName: resultsName,
				Auctioneer:  a.repo.nodePrefix,
			}

			log.Info(a.repo, "runAuction_sending_bid_interest", "peer", peer, "bidName", bidName.String())
			a.repo.client.ExpressR(ndn.ExpressRArgs{
				Name: bidName,
				Config: &ndn.InterestConfig{
					CanBePrefix: false,
					MustBeFresh: true,
				},
				AppParam: metricReq.Encode(),
				Retries:  3,
				Callback: func(args ndn.ExpressCallbackArgs) {
					if args.Result != ndn.InterestResultData {
						log.Warn(a.repo, "runAuction_bid_failed", "peer", peer, "result", args.Result)
						return
					}

					log.Info(a.repo, "runAuction_bid_received", "peer", peer, "result", args.Result)

					metricResp, err := tlv.ParseMetricResponse(enc.NewWireView(args.Data.Content()), false)
					if err != nil {
						log.Warn(a.repo, "runAuction_response_parse_failed", "peer", peer, "err", err)
						return
					}

					metricsMu.Lock()
					peerMetrics[peer] = struct {
						capacity uint64
						used     uint64
						delay    bool
					}{
						capacity: metricResp.Capacity,
						used:     metricResp.Used,
						delay:    metricResp.Delay,
					}
					metricsMu.Unlock()

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
			goto determine_winners
		case <-responsesCh:
			receivedCount++
			log.Debug(a.repo, "runAuction_bid_received", "peer", "unknown", "count", receivedCount, "total", len(peers))
		}
	}

determine_winners:
	a.determineAndPublishWinners(cmd.Target, timestamp, resultsName, peerMetrics)
}

func (a *AuctionMechanism) determineAndPublishWinners(target enc.Name, timestamp uint64, resultsName enc.Name, peerMetrics map[string]struct {
	capacity uint64
	used     uint64
	delay    bool
}) {
	targetStr := target.String()
	anyDelay := false
	if peerMetrics != nil {
		for _, metrics := range peerMetrics {
			if metrics.delay {
				anyDelay = true
				break
			}
		}
	}

	if anyDelay {
		log.Info(a.repo, "runAuction_delayed_by_peer", "target", targetStr)
		assignment := &tlv.JobAssignment{
			Target:    target,
			Assignees: nil,
		}
		err := a.repo.client.Store().Put(resultsName, assignment.Encode().Join())
		if err != nil {
			log.Warn(a.repo, "runAuction_delayed_publish_failed", "err", err)
		}
		a.repo.eventLogger.LogAuctionDelayed(targetStr, "delayed_by_peer")

		delayDuration := 2500 * time.Millisecond
		log.Info(a.repo, "runAuction_scheduling_retry", "target", targetStr, "delay", delayDuration.String())
		go func() {
			time.Sleep(delayDuration)
			a.runAuctionIfNeeded(target)
		}()
		return
	}

	needed := a.repo.rf - a.repo.countReplication(target)
	if needed <= 0 {
		log.Info(a.repo, "runAuction_skip_publish", "reason", "replication_satisfied", "target", targetStr)
		return
	}

	nodeStatusCopy := make(map[string]NodeStatus)
	a.repo.mu.Lock()
	for k, v := range a.repo.nodeStatus {
		nodeStatusCopy[k] = v
	}
	a.repo.mu.Unlock()

	a.repo.mu.Lock()
	nodeStatusCopy[a.repo.myNodeName()] = NodeStatus{
		Capacity:    a.repo.storageCapacity,
		Used:        a.repo.storageUsed,
		Jobs:        make([]enc.Name, len(a.repo.jobs)),
		LastUpdated: time.Now(),
	}
	copy(nodeStatusCopy[a.repo.myNodeName()].Jobs, a.repo.jobs)
	a.repo.mu.Unlock()

	if peerMetrics != nil {
		for peer, metrics := range peerMetrics {
			if status, ok := nodeStatusCopy[peer]; ok {
				status.Capacity = metrics.capacity
				status.Used = metrics.used
				nodeStatusCopy[peer] = status
			}
		}
	}

	cmd := a.repo.getCommand(target)
	if cmd == nil {
		return
	}

	winners := DetermineWinners(target, nodeStatusCopy, a.repo.myNodeName(), a.repo.rf, a.repo.eventLogger)

	log.Info(a.repo, "runAuction_winners", "target", targetStr, "winners", winners)

	assignment := &tlv.JobAssignment{
		Target:    target,
		Assignees: stringNamesToEncNames(winners),
	}

	log.Info(a.repo, "runAuction_publishing_results", "target", targetStr, "winners", winners, "resultsName", resultsName.String())

	if slices.Contains(winners, a.repo.myNodeName()) {
		log.Info(a.repo, "runAuction_claiming_job", "target", targetStr, "myNode", a.repo.myNodeName())
		a.repo.doTarget(target)
		a.repo.publishNodeUpdate(&tlv.NodeUpdate{
			Jobs: a.repo.getMyJobs(),
		})
	}

	err := a.repo.client.Store().Put(resultsName, assignment.Encode().Join())
	if err != nil {
		log.Warn(a.repo, "runAuction_publish_failed", "err", err, "resultsName", resultsName.String())
	}

	a.repo.eventLogger.LogAuctionResults(targetStr, resultsName.String(), winners)
}

func (a *AuctionMechanism) runAuctionIfNeeded(target enc.Name) {
	currentReplication := a.repo.countReplication(target)
	needed := a.repo.rf - currentReplication

	if needed <= 0 {
		return
	}

	log.Info(a.repo, "runAuctionIfNeeded", "target", target.String(), "current", currentReplication, "needed", needed)

	cmd := a.repo.getCommand(target)
	if cmd == nil {
		log.Info(a.repo, "runAuctionIfNeeded_skip", "reason", "command_not_received", "target", target.String())
		return
	}

	go a.RunDistribution(cmd)
}

func (a *AuctionMechanism) onBidInterest(name enc.Name, params enc.Wire, reply func(wire enc.Wire) error) {
	log.Info(a.repo, "onBidInterest_received", "name", name.String(), "from", name.Prefix(2).String())

	metricReq, err := tlv.ParseMetricRequest(enc.NewWireView(params), false)
	if err != nil {
		log.Warn(a.repo, "onBidInterest_parse_failed", "err", err)
		return
	}

	log.Info(a.repo, "onBidInterest_processing", "target", metricReq.Target.String(), "timestamp", metricReq.Timestamp)

	incomingTimestamp := metricReq.Timestamp
	resultsName := metricReq.ResultsName
	target := metricReq.Target

	a.repo.mu.Lock()
	ourTimestamp := a.repo.currentAuctionTimestamp
	a.repo.mu.Unlock()

	weDelay := false
	reason := ""

	if incomingTimestamp < ourTimestamp {
		weDelay = true
		reason = "incoming_earlier_than_ours"
	} else if incomingTimestamp > ourTimestamp {
		weDelay = false
		reason = "incoming_later_than_ours"
	} else {
		incomingAuctioneer := metricReq.Auctioneer.String()
		myName := a.repo.myNodeName()
		weDelay = incomingAuctioneer < myName
		if weDelay {
			reason = "equal_timestamp_we_lose_tiebreaker"
		} else {
			reason = "equal_timestamp_we_win_tiebreaker"
		}
	}

	if weDelay {
		log.Info(a.repo, "onBidInterest_delayed", "reason", reason, "incoming", incomingTimestamp, "ours", ourTimestamp)
		a.scheduleDelayedAuction(target)
	}

	capacity, used := a.repo.getStorageStats()
	response := &tlv.MetricResponse{
		Capacity:  capacity,
		Used:      used,
		Timestamp: incomingTimestamp,
		Delay:     !weDelay,
	}

	if a.repo.eventLogger != nil {
		a.repo.eventLogger.LogAuctionBid(target.String(), metricReq.Auctioneer.String(), capacity, used, weDelay)
	}

	log.Debug(a.repo, "replyingToBid", "name", name)
	reply(response.Encode())

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
			a.handleAuctionResults(args.Data, target, resultsName)
		},
	})
}

func (a *AuctionMechanism) scheduleDelayedAuction(target enc.Name) {
	targetStr := target.String()
	delayDuration := 5 * time.Second

	a.repo.mu.Lock()
	if _, exists := a.repo.scheduledAuctions[targetStr]; exists {
		a.repo.mu.Unlock()
		log.Info(a.repo, "scheduleDelayedAuction_skipped", "reason", "already_scheduled", "target", targetStr)
		return
	}

	timer := time.AfterFunc(delayDuration, func() {
		a.repo.mu.Lock()
		delete(a.repo.scheduledAuctions, targetStr)
		a.repo.mu.Unlock()

		a.runAuctionIfNeeded(target)
	})

	a.repo.scheduledAuctions[targetStr] = timer
	a.repo.mu.Unlock()

	log.Info(a.repo, "scheduleDelayedAuction_scheduled", "target", targetStr, "delay", delayDuration.String())
}

func (a *AuctionMechanism) handleAuctionResults(data ndn.Data, target enc.Name, resultsName enc.Name) {
	log.Info(a.repo, "handleAuctionResults", "target", target.String(), "resultsName", resultsName.String())

	assignment, err := tlv.ParseJobAssignment(enc.NewWireView(data.Content()), false)
	if err != nil {
		log.Warn(a.repo, "handleAuctionResults_parse_failed", "err", err)
		return
	}

	targetStr := target.String()
	assignees := encNamesToStrings(assignment.Assignees)

	a.repo.eventLogger.LogAuctionResults(targetStr, resultsName.String(), assignees)

	if len(assignment.Assignees) == 0 {
		log.Info(a.repo, "handleAuctionResults_delayed", "target", targetStr)
		a.repo.eventLogger.LogAuctionDelayed(targetStr, "empty_assignees")
		currentReplication := a.repo.countReplication(target)
		if currentReplication < a.repo.rf {
			a.runAuctionIfNeeded(target)
		}
		return
	}

	if !slices.Contains(assignees, a.repo.myNodeName()) {
		log.Info(a.repo, "handleAuctionResults_not_assignee", "target", targetStr, "myNode", a.repo.myNodeName())
		a.repo.eventLogger.LogAssignmentHandled(targetStr, "auction", "skipped", "not_in_assignees", assignees)
		return
	}

	cmd := a.repo.getCommand(target)
	if cmd == nil {
		log.Info(a.repo, "handleAuctionResults_command_not_received", "target", targetStr)
		a.repo.mu.Lock()
		a.repo.pendingAssignments[targetStr] = assignment
		a.repo.mu.Unlock()
		a.repo.eventLogger.LogAssignmentHandled(targetStr, "auction", "pending", "command_not_received", assignees)
		return
	}

	log.Info(a.repo, "handleAuctionResults_will_do_job", "target", targetStr, "myNode", a.repo.myNodeName())
	if a.repo.doCmd(cmd) {
		a.repo.publishNodeUpdate(&tlv.NodeUpdate{
			Jobs: a.repo.getMyJobs(),
		})
	}

	currentReplication := a.repo.countReplication(target)
	if currentReplication < a.repo.rf {
		a.runAuctionIfNeeded(target)
	}
}
