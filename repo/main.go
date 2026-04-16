package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/a-thieme/repo/repo/util"
	"github.com/named-data/ndnd/std/log"
)

func main() {
	eventLogPath := flag.String("event-log", "events.jsonl", "Path to write machine-readable event log")
	defaultNodePrefix := fmt.Sprintf("/ndn/repo.teame.dev/repo-%d", time.Now().UnixMilli())
	nodePrefix := flag.String("node-prefix", defaultNodePrefix, "Unique node prefix for this repo instance")
	signingIdentity := flag.String("signing-identity", "/ndn/repo.teame.dev/repo", "Signing identity (must match key in keychain)")
	noRelease := flag.Bool("no-release", false, "Disable automatic job release when storage exceeds 75%")
	maxJoinGrowthRate := flag.Uint64("max-join-growth-rate", 10*1024*1024, "Maximum JOIN storage growth per second in bytes")
	heartbeatInterval := flag.Duration("heartbeat-interval", 5*time.Second, "Heartbeat interval for node status updates")
	replicationFactor := flag.Int("replication-factor", 3, "Replication factor for data replication")
	distribution := flag.String("distribution", "hydra", "Distribution mechanism: hydra, auction")
	auctionTimeout := flag.Duration("auction-timeout", 5*time.Second, "Auction timeout for waiting for bid responses")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	if *debug {
		log.Default().SetLevel(log.LevelDebug)
	} else {
		log.Default().SetLevel(log.LevelInfo)
	}

	repo := NewRepo("/ndn/drepo", *nodePrefix, *signingIdentity, *replicationFactor, *noRelease, *maxJoinGrowthRate, *heartbeatInterval, *distribution, nil, *auctionTimeout)

	eventLogger, err := util.NewEventLogger(*eventLogPath, repo.nodePrefix.String())
	if err != nil {
		log.Fatal(nil, "Failed to create event logger", "err", err)
	}
	repo.SetEventLogger(eventLogger)

	if err := repo.Start(); err != nil {
		log.Fatal(nil, "Unable to start repo", "err", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	repo.Close()
	eventLogger.Close()
}
