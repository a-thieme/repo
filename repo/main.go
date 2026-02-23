package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/a-thieme/repo/repo/util"
	"github.com/named-data/ndnd/std/log"
)

func main() {
	eventLogPath := flag.String("event-log", "events.jsonl", "Path to write machine-readable event log")
	nodePrefix := flag.String("node-prefix", "/ndn/repo.teame.dev/repo", "Unique node prefix for this repo instance")
	signingIdentity := flag.String("signing-identity", "/ndn/repo.teame.dev/repo", "Signing identity (must match key in keychain)")
	noRelease := flag.Bool("no-release", false, "Disable automatic job release when storage exceeds 75%")
	maxJoinGrowthRate := flag.Uint64("max-join-growth-rate", 10*1024*1024, "Maximum JOIN storage growth per second in bytes")
	heartbeatInterval := flag.Duration("heartbeat-interval", 5*time.Second, "Heartbeat interval for node status updates")
	debug := flag.Bool("debug", false, "Enable debug logging")
	flag.Parse()

	if *debug {
		log.Default().SetLevel(log.LevelDebug)
	} else {
		log.Default().SetLevel(log.LevelInfo)
	}

	replicationFactor := 3
	repo := NewRepo("/ndn/drepo", *nodePrefix, *signingIdentity, replicationFactor, *noRelease, *maxJoinGrowthRate, *heartbeatInterval)

	eventLogger, err := util.NewEventLogger(*eventLogPath, repo.nodePrefix.String())
	if err != nil {
		log.Fatal(nil, "Failed to create event logger", "err", err)
	}
	defer eventLogger.Close()
	repo.SetEventLogger(eventLogger)

	if err := repo.Start(); err != nil {
		log.Fatal(nil, "Unable to start repo", "err", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}
