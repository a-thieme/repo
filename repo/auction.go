package main

import (
	"sync"
	"time"

	"github.com/a-thieme/repo/tlv"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
	"github.com/named-data/ndnd/std/ndn"
	spec "github.com/named-data/ndnd/std/ndn/spec_2022"
	svs "github.com/named-data/ndnd/std/sync"
)

const (
	HEARTBEAT_SUFFIX = "heartbeat"
	BID_SUFFIX       = "bid"
	RESULTS_SUFFIX   = "results"
)

type AuctionMechanism struct {
	repo            *Repo
	heartbeatSvSync *svs.SvSync
	quitCh          chan struct{}
}

func NewAuctionMechanism(repo *Repo) *AuctionMechanism {
	return &AuctionMechanism{repo: repo}
}

func (a *AuctionMechanism) String() string {
	return "auction"
}

func (a *AuctionMechanism) Start(client ndn.Client, groupPrefix enc.Name) error {
	a.quitCh = make(chan struct{})
	heartbeatGroupPrefix := groupPrefix.Append(enc.NewGenericComponent(HEARTBEAT_SUFFIX))
	a.heartbeatSvSync = svs.NewSvSync(svs.SvSyncOpts{
		Client:      client,
		GroupPrefix: heartbeatGroupPrefix,
		OnUpdate: func(update svs.SvSyncUpdate) {
			a.HandleHeartbeatUpdate(update)
		},
		SyncDataName: a.repo.nodePrefix,
	})
	client.AnnouncePrefix(ndn.Announcement{
		Name:   heartbeatGroupPrefix,
		Expose: true,
	})
	if err := a.heartbeatSvSync.Start(); err != nil {
		return err
	}
	go a.runHeartbeatTick()

	bidPrefix := a.repo.nodePrefix.Append(enc.NewGenericComponent(BID_SUFFIX))
	if err := client.Engine().AttachHandler(bidPrefix, a.onBidInterest); err != nil {
		log.Error(a, "AttachHandler", "bidPrefix", bidPrefix, "err", err)
	}
	a.repo.client.AnnouncePrefix(ndn.Announcement{
		Name:   bidPrefix,
		Expose: true,
	})

	resultsPrefix := a.repo.nodePrefix.Append(enc.NewGenericComponent(RESULTS_SUFFIX))
	a.repo.client.AnnouncePrefix(ndn.Announcement{
		Name:   resultsPrefix,
		Expose: true,
	})

	return nil
}

func (a *AuctionMechanism) runHeartbeatTick() {
	ticker := time.NewTicker(a.repo.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.quitCh:
			return
		case <-ticker.C:
			a.heartbeatSvSync.IncrSeqNo(a.repo.nodePrefix)
		}
	}
}

func (a *AuctionMechanism) HandleHeartbeatUpdate(update svs.SvSyncUpdate) {
	nodeName := update.Name.String()
	a.repo.mu.Lock()
	if status, exists := a.repo.nodeStatus[nodeName]; exists {
		status.LastUpdated = time.Now()
		a.repo.nodeStatus[nodeName] = status
	} else {
		a.repo.nodeStatus[nodeName] = NodeStatus{
			LastUpdated: time.Now(),
		}
	}
	a.repo.mu.Unlock()
	a.repo.resetHeartbeatTimer(nodeName)
}

func (a *AuctionMechanism) Stop() {
	if a.quitCh != nil {
		close(a.quitCh)
	}
	if a.heartbeatSvSync != nil {
		a.heartbeatSvSync.Stop()
	}
}

func (a *AuctionMechanism) Mechanism() string {
	return "auction"
}

func (a *AuctionMechanism) OnCommand(cmd *tlv.Command) *tlv.NodeUpdate {
	go a.RunDistribution(cmd)
	return nil
}

func (a *AuctionMechanism) BatchedDistribution(jobs []enc.Name) {
	peers := a.repo.getOtherNodeNames()
	if len(peers) == 0 {
		log.Info(a.repo, "runAuctionBatched_no_peers", "jobCount", len(jobs))
		return
	}

	timestamp := uint64(time.Now().UnixNano())
	resultsName := a.repo.nodePrefix.Append(enc.NewGenericComponent(RESULTS_SUFFIX))
	resultsName = resultsName.Append(enc.NewTimestampComponent(timestamp))

	peerMetrics := a.collectPeerMetrics(peers, jobs, resultsName)
	if len(peerMetrics) == 0 {
		log.Info(a.repo, "runAuctionBatched_no_metrics", "jobCount", len(jobs))
		return
	}

	var assignments []*tlv.JobAssignment
	shouldPublishJobs := false
	for _, target := range jobs {
		targetStr := target.String()
		a.repo.scheduleReevaluationLoop(target)

		a.repo.mu.Lock()
		status := a.repo.nodeStatus[a.repo.myNodeName()]
		peerMetrics[a.repo.myNodeName()] = &tlv.NodeUpdate{
			StorageCapacity: status.Capacity,
			StorageUsed:     status.Used,
			Jobs:            status.Jobs,
		}

		nodeStatus := make(map[string]NodeStatus)
		for name, metric := range peerMetrics {
			nodeStatus[name] = NodeStatus{
				Capacity: metric.StorageCapacity,
				Used:     metric.StorageUsed,
				Jobs:     metric.Jobs,
			}
		}
		a.repo.mu.Unlock()

		assignment := a.repo.DetermineWinners(target, nodeStatus)
		isAssignee := a.repo.amAssignee(assignment)
		if assignment != nil {
			log.Info(a.repo, "runAuctionBatched_winner", "target", targetStr, "winners", assignment.Assignees)
			assignments = append(assignments, assignment)

			if isAssignee {
				log.Info(a.repo, "runAuctionBatched_claiming", "target", targetStr)
				if a.repo.doTarget(target) {
					shouldPublishJobs = true
				}
			}
		}
	}

	batched := &tlv.JobAssignmentBatch{JobAssignments: assignments}
	log.Info(a.repo, "runAuctionBatched_published", "assignmentCount", len(assignments))
	err := a.repo.client.Store().Put(resultsName, batched.Bytes())
	if err != nil {
		log.Warn(a.repo, "runAuctionBatched_put_failed", "err", err, "resultsName", resultsName.String())
	}
	if shouldPublishJobs {
		a.repo.publishJobs()
	}
}

func (a *AuctionMechanism) collectPeerMetrics(peers []string, jobs []enc.Name, resultsName enc.Name) map[string]*tlv.NodeUpdate {
	peerMetrics := make(map[string]*tlv.NodeUpdate)
	var metricsMu sync.Mutex
	responsesCh := make(chan string, len(peers))

	timestamp := uint64(time.Now().UnixNano())

	var wg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		go func(peer string) {
			defer wg.Done()

			peerPrefix, _ := enc.NameFromStr(peer)
			bidName := peerPrefix.Append(enc.NewGenericComponent(BID_SUFFIX))
			bidName = bidName.Append(enc.NewTimestampComponent(timestamp))

			metricReq := &tlv.MetricRequest{
				Target:      jobs[0],
				ResultsName: resultsName,
				Auctioneer:  a.repo.nodePrefix,
			}

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
						return
					}

					metricResp, err := tlv.ParseNodeUpdate(enc.NewWireView(args.Data.Content()), false)
					if err != nil {
						return
					}

					metricsMu.Lock()
					peerMetrics[peer] = metricResp
					metricsMu.Unlock()

					a.repo.resetHeartbeatTimer(peer)

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
			return peerMetrics
		case <-responsesCh:
			receivedCount++
			if receivedCount == len(peers) {
				return peerMetrics
			}
		}
	}
}

func (a *AuctionMechanism) RunDistribution(cmd *tlv.Command) {
	targetStr := cmd.Target.String()
	a.repo.scheduleReevaluationLoop(cmd.Target)
	currentReplication := a.repo.countReplication(cmd.Target)
	needed := a.repo.rf - currentReplication

	if needed <= 0 {
		log.Info(a.repo, "runAuction_skip", "reason", "replication_satisfied", "target", targetStr, "current", currentReplication)
		if a.repo.eventLogger != nil {
			a.repo.eventLogger.LogAuctionDelayed(targetStr, "replication_satisfied")
		}
		return
	}

	timestamp := uint64(time.Now().UnixNano())
	resultsName := a.repo.nodePrefix.Append(enc.NewGenericComponent(RESULTS_SUFFIX))
	resultsName = resultsName.Append(enc.NewTimestampComponent(timestamp))

	log.Info(a.repo, "runAuction_started", "target", targetStr, "timestamp", timestamp, "current", currentReplication, "needed", needed)

	if a.repo.eventLogger != nil {
		a.repo.eventLogger.LogAuctionStarted(targetStr, currentReplication, needed, timestamp)
	}

	peers := a.repo.getOtherNodeNamesNotDoing(cmd.Target)
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

					a.repo.resetHeartbeatTimer(peer)

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
			a.determineAndPublishWinners(cmd.Target, resultsName, peerMetrics)
			return
		case <-responsesCh:
			receivedCount++
			log.Debug(a.repo, "runAuction_bid_received", "peer", "unknown", "count", receivedCount, "total", len(peers))
			if receivedCount == len(peers) {
				a.determineAndPublishWinners(cmd.Target, resultsName, peerMetrics)
				return
			}
		}
	}
}

func (a *AuctionMechanism) determineAndPublishWinners(target enc.Name, resultsName enc.Name, peerMetrics map[string]*tlv.NodeUpdate,
) {
	targetStr := target.String()

	a.repo.mu.Lock()
	status := a.repo.nodeStatus[a.repo.myNodeName()]
	peerMetrics[a.repo.myNodeName()] = &tlv.NodeUpdate{
		StorageCapacity: status.Capacity,
		StorageUsed:     status.Used,
		Jobs:            status.Jobs,
	}

	nodeStatus := make(map[string]NodeStatus)
	for name, metric := range peerMetrics {
		nodeStatus[name] = NodeStatus{
			Capacity: metric.StorageCapacity,
			Used:     metric.StorageUsed,
			Jobs:     metric.Jobs,
		}
	}
	a.repo.mu.Unlock()

	cmd := a.repo.getCommand(target)
	if cmd == nil {
		return
	}

	assignment := a.repo.DetermineWinners(target, nodeStatus)
	log.Info(a.repo, "runAuction_winners", "target", targetStr, "winners", assignment.Assignees)

	if a.repo.eventLogger != nil {
		candidates := make([]string, 0, len(nodeStatus))
		for name, status := range nodeStatus {
			isDoing := false
			for _, job := range status.Jobs {
				if job.Equal(target) {
					isDoing = true
					break
				}
			}
			if !isDoing {
				candidates = append(candidates, name)
			}
		}
		winners := make([]string, len(assignment.Assignees))
		for i, a := range assignment.Assignees {
			winners[i] = a.String()
		}
		a.repo.eventLogger.LogAuctionWinners(targetStr, candidates, nil, winners)
	}

	batched := &tlv.JobAssignmentBatch{
		JobAssignments: []*tlv.JobAssignment{assignment},
	}
	err := a.repo.client.Store().Put(resultsName, batched.Bytes())
	if err != nil {
		log.Warn(a.repo, "runAuction_publish_failed", "err", err, "resultsName", resultsName.String())
	}

	if a.repo.amAssignee(assignment) {
		log.Info(a.repo, "runAuction_claiming_job", "target", targetStr, "myNode", a.repo.myNodeName())
		a.repo.doTarget(target)
		a.repo.publishJobs()
	}

	a.repo.eventLogger.LogAuctionResults(targetStr, resultsName.String(), encNamesToStrings(assignment.Assignees))
}

func (a *AuctionMechanism) onBidInterest(args ndn.InterestHandlerArgs) {
	interest := args.Interest
	log.Debug(a.repo, "onBidInterest_received", "name", interest.Name().String())

	appParam := interest.AppParam()
	if len(appParam) == 0 {
		log.Warn(a.repo, "onBidInterest_no_app_param", "name", interest.Name().String())
		return
	}

	metricReq, err := tlv.ParseMetricRequest(enc.NewWireView(appParam), false)
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

	signer := a.repo.client.SuggestSigner(interest.Name())
	if signer == nil {
		log.Error(a, "onBidInterest_no_signer")
		return
	}

	resName := interest.Name()
	data, err := spec.Spec{}.MakeData(resName, &ndn.DataConfig{}, response.Encode(), signer)
	if err != nil {
		log.Error(a, "onBidInterest_make_data_failed", "err", err)
		return
	}

	log.Debug(a.repo, "replyingToBid", "name", resName.String())
	if err := args.Reply(data.Wire); err != nil {
		log.Error(a, "bidReply", "failed", resName.String(), "err", err)
	}

	resultsName := metricReq.ResultsName
	target := metricReq.Target
	a.repo.eventLogger.LogAuctionBid(target.String(), metricReq.Auctioneer.String(), capacity, used)

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

			assignments, err := tlv.ParseJobAssignmentBatch(enc.NewWireView(args.Data.Content()), false)
			if err != nil {
				log.Error(a, "ParseJobAssignmentBatch", "failed", target)
				return
			}
			a.repo.ProcessJobAssignments(assignments.JobAssignments)
		},
	})
}
