package main

import (
	"slices"
	"sync"
	"sync/atomic"
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
	auctionSeq      uint64
	interestMap     map[string]ndn.InterestHandlerArgs
	interestMu      sync.Mutex
	resultsLookup   map[string]enc.Name
}

func NewAuctionMechanism(repo *Repo) *AuctionMechanism {
	return &AuctionMechanism{repo: repo, auctionSeq: 0}
}

func (a *AuctionMechanism) String() string {
	return "auction"
}

// publish update with nothing attached
func (a *AuctionMechanism) PublishUpdate(update *tlv.NodeUpdate) {
	a.repo.publishUpdate(update)
}

func (h *AuctionMechanism) PublishJobs() {
	h.PublishUpdate(&tlv.NodeUpdate{Jobs: h.repo.getMyJobs()})
}

func (a *AuctionMechanism) Start(client ndn.Client, groupPrefix enc.Name) error {
	a.quitCh = make(chan struct{})
	a.interestMap = make(map[string]ndn.InterestHandlerArgs)
	a.resultsLookup = make(map[string]enc.Name)
	heartbeatGroupPrefix := groupPrefix.Append(enc.NewGenericComponent(HEARTBEAT_SUFFIX))
	a.heartbeatSvSync = svs.NewSvSync(svs.SvSyncOpts{
		Client:      client,
		GroupPrefix: heartbeatGroupPrefix,
		OnUpdate: func(update svs.SvSyncUpdate) {
			nodeName := update.Name.String()
			log.Debug(a, "heartbeat", "node", nodeName)
			a.repo.resetHeartbeatTimer(nodeName)
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
	if err := client.Engine().AttachHandler(resultsPrefix, a.onResultsInterest); err != nil {
		log.Error(a, "AttachHandler", "bidPrefix", resultsPrefix, "err", err)
	}
	a.repo.client.AnnouncePrefix(ndn.Announcement{
		Name:   resultsPrefix,
		Expose: true,
	})

	return nil
}

func (a *AuctionMechanism) onResultsInterest(args ndn.InterestHandlerArgs) {
	iname := args.Interest.Name().String()
	log.Debug(a, "resultsInterest", "name", iname)
	a.interestMu.Lock()
	a.interestMap[iname] = args
	a.interestMu.Unlock()
}

func (a *AuctionMechanism) publishAssignment(resultsName enc.Name, assignment *tlv.JobAssignment) {
	a.PublishAssignments([]*tlv.JobAssignment{assignment})
}

func (a *AuctionMechanism) PublishAssignments(assignments []*tlv.JobAssignment) {
	// NOTE: every target will have all the same assignments, so just go through all of them, publishing for each resultsName
	for _, assignment := range assignments {
		targetStr := assignment.Target.String()
		resultsName, exists := a.resultsLookup[targetStr]

		// only do this once per resultsName
		if exists {
			delete(a.resultsLookup, targetStr)
		} else {
			continue
		}

		batched := &tlv.JobAssignmentBatch{JobAssignments: assignments}
		signer := a.repo.client.SuggestSigner(resultsName)
		if signer == nil {
			log.Error(a, "onBidInterest_no_signer")
			return
		}
		data, err := spec.Spec{}.MakeData(resultsName, &ndn.DataConfig{}, batched.Encode(), signer)
		if err != nil {
			log.Warn(a.repo, "MakeData", "failed", err, "resultsName", resultsName.String())
		}
		nStr := resultsName.String()
		err = a.repo.client.Store().Put(resultsName, data.Wire.Join())
		if err != nil {
			log.Warn(a.repo, "PutData", "failed", err, "resultsName", nStr)
		}
		a.interestMu.Lock()
		args, exists := a.interestMap[nStr]
		if exists {
			delete(a.interestMap, nStr)
		}
		a.interestMu.Unlock()
		if exists {
			if args.Reply(data.Wire) == nil {
				log.Debug(a, "repliedToBuffered", "name", nStr)
			}
		}
		a.repo.store.Put(resultsName, data.Wire.Join())
		log.Info(a.repo, "runAuctionBatched_published", "assignmentCount", len(batched.JobAssignments))
	}
	if a.repo.ProcessJobAssignments(assignments) {
		a.PublishJobs()
	}
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
	return &tlv.NodeUpdate{NewCommand: cmd}
}

func (a *AuctionMechanism) GetAvailability(under []UnderStats) map[string]Availability {
	auctionID := atomic.AddUint64(&a.auctionSeq, 1)
	resultsName := a.repo.nodePrefix.Append(enc.NewGenericComponent(RESULTS_SUFFIX))
	resultsName = resultsName.Append(enc.NewTimestampComponent(auctionID))
	log.Info(a.repo, "runAuction_started", "auctionID", auctionID)

	peerMetrics := make(map[string]Availability)
	myPrefix := a.repo.nodePrefix.String()
	// get all the candidates from under and request their availability
	target := under[0].Target
	peers := []string{}
	for _, stat := range under {
		for _, nodeName := range stat.Candidates {
			if !slices.Contains(peers, nodeName) {
				// NOTE: we don't need to fetch our own metrics, so if we are a candidate, we add it manually
				if nodeName == myPrefix {
					mine := a.repo.nodeStatusCopy()[myPrefix]
					peerMetrics[myPrefix] = Availability{
						PercentUsed:   mine.UsedSpace(),
						TotalCapacity: mine.Capacity,
					}
				} else {
					peers = append(peers, nodeName)
				}
			}
		}
		a.resultsLookup[stat.Target.String()] = resultsName
	}

	var metricsMu sync.Mutex
	responsesCh := make(chan string, len(peers))

	auctionTimer := time.NewTimer(a.repo.auctionTimeout)

	var wg sync.WaitGroup
	var callbackWg sync.WaitGroup
	for _, peer := range peers {
		wg.Add(1)
		callbackWg.Add(1)
		go func(peer string) {
			defer wg.Done()

			peerPrefix, _ := enc.NameFromStr(peer)
			bidName := peerPrefix.Append(enc.NewGenericComponent(BID_SUFFIX))
			bidName = bidName.Append(enc.NewTimestampComponent(auctionID))

			metricReq := &tlv.MetricRequest{
				Target:      target,
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
					defer callbackWg.Done()

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
					peerMetrics[peer] = Availability{
						PercentUsed:   float64(metricResp.StorageUsed) / float64(metricResp.StorageCapacity),
						TotalCapacity: metricResp.StorageCapacity,
					}
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

	// Wait for goroutines to finish calling ExpressR and for all callbacks to fire
	wg.Wait()
	callbackWg.Wait()

	receivedCount := 0
	for {
		select {
		case <-auctionTimer.C:
			log.Info(a.repo, "runAuction_timeout", "target", target.String(), "received", receivedCount, "total_peers", len(peers))
			return peerMetrics
		case <-responsesCh:
			receivedCount++
			log.Debug(a.repo, "runAuction_bid_received", "peer", "unknown", "count", receivedCount, "total", len(peers))
			if receivedCount == len(peers) {
				return peerMetrics
			}
		}
	}
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
			if a.repo.ProcessJobAssignments(assignments.JobAssignments) {
				a.PublishJobs()
			}
		},
	})
}
