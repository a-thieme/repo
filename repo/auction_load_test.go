package main

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
)

func TestAuction_Load_2Producers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	restore := setupCSCache(false)
	defer restore()

	out, _ := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s", string(out))
	exec.Command("nfdc", "strategy", "set", "/ndn/drepo/notify", "/localhost/nfd/strategy/best-route").Run()

	out3, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/heartbeat/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set heartbeat (auction): %s (err=%v)", string(out3), err)

	t.Logf("Waiting for routing convergence (%v)...", *routingConvergeWait)
	time.Sleep(*routingConvergeWait)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)
	tmpDir := t.TempDir()

	cfg := integrationTestConfig{
		name:              "AuctionLoad2Producers",
		nodeCount:         5,
		replicationFactor: 3,
		cacheless:         false,
		runProducer:       false,
		debug:             true,
		distribution:      "auction",
	}

	repos := startRepos(t, cfg, repoBinary, tmpDir)
	defer stopRepos(repos)

	t.Logf("Waiting for SVS convergence (timeout=%v)...", *svsHealthTimeout)
	if !waitForSVSHealth(t, repos, 1, *svsHealthTimeout) {
		t.Log("WARNING: SVS not healthy")
	}

	t.Logf("Starting 2 concurrent producers...")
	ctx, cancel := context.WithTimeout(context.Background(), *producerTimeout)
	defer cancel()

	var producerWg sync.WaitGroup
	producerOutputs := make([][]byte, 2)
	producerErrors := make([]error, 2)

	for i := 0; i < 2; i++ {
		producerWg.Add(1)
		go func(idx int) {
			defer producerWg.Done()
			cmd := exec.CommandContext(ctx, producerBinary)
			producerOutputs[idx], producerErrors[idx] = cmd.CombinedOutput()
		}(i)
	}

	producerWg.Wait()

	for i, err := range producerErrors {
		if err != nil {
			t.Logf("Producer %d error: %v", i, err)
		}
		t.Logf("Producer %d output: %s", i, string(producerOutputs[i]))
	}

	t.Logf("Waiting for replication (timeout=%v)...", 30*time.Second)
	deadline := time.Now().Add(30 * time.Second)

	commandsReceived := 0
	var totalClaims int
	for time.Now().Before(deadline) {
		totalClaims = 0
		commandsReceived = 0
		for _, r := range repos {
			events, err := testutil.ParseEventLog(r.logPath)
			if err != nil {
				continue
			}
			cmds := testutil.FilterEvents(events, testutil.EventCommandReceived)
			commandsReceived += len(cmds)
			claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
			if len(claims) > 0 {
				totalClaims++
			}
		}

		if totalClaims >= cfg.replicationFactor*2 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Logf("=== AuctionLoad2Producers RESULTS ===")
	t.Logf("Commands received: %d", commandsReceived)
	t.Logf("Nodes with claims: %d", totalClaims)

	uniqueTargets := make(map[string]int)
	for _, r := range repos {
		events, _ := testutil.ParseEventLog(r.logPath)
		claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
		for _, c := range claims {
			uniqueTargets[c.Target]++
		}
	}

	t.Logf("Unique commands claimed: %d", len(uniqueTargets))
	for target, count := range uniqueTargets {
		t.Logf("  %s: %d claims", target, count)
	}

	if len(uniqueTargets) < 2 {
		t.Errorf("FAIL: Expected 2 unique commands, got %d", len(uniqueTargets))
	}

	allAtRF := true
	for target, count := range uniqueTargets {
		if count < cfg.replicationFactor {
			allAtRF = false
			t.Logf("  Command %s under-replicated: %d < %d", target, count, cfg.replicationFactor)
		}
	}

	if !allAtRF {
		t.Errorf("FAIL: Not all commands achieved RF=%d replication", cfg.replicationFactor)
	} else {
		t.Logf("PASS: All %d concurrent commands replicated to RF=%d", len(uniqueTargets), cfg.replicationFactor)
	}
}

func TestAuction_Load_3Producers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	restore := setupCSCache(false)
	defer restore()

	out, _ := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s", string(out))
	exec.Command("nfdc", "strategy", "set", "/ndn/drepo/notify", "/localhost/nfd/strategy/best-route").Run()

	out3, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/heartbeat/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set heartbeat (auction): %s (err=%v)", string(out3), err)

	t.Logf("Waiting for routing convergence (%v)...", *routingConvergeWait)
	time.Sleep(*routingConvergeWait)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)
	tmpDir := t.TempDir()

	cfg := integrationTestConfig{
		name:              "AuctionLoad3Producers",
		nodeCount:         5,
		replicationFactor: 3,
		cacheless:         false,
		runProducer:       false,
		debug:             false,
		distribution:      "auction",
	}

	repos := startRepos(t, cfg, repoBinary, tmpDir)
	defer stopRepos(repos)

	t.Logf("Waiting for SVS convergence (timeout=%v)...", *svsHealthTimeout)
	if !waitForSVSHealth(t, repos, 1, *svsHealthTimeout) {
		t.Log("WARNING: SVS not healthy")
	}

	t.Logf("Starting 3 concurrent producers...")
	ctx, cancel := context.WithTimeout(context.Background(), *producerTimeout)
	defer cancel()

	var producerWg sync.WaitGroup
	producerOutputs := make([][]byte, 3)
	producerErrors := make([]error, 3)

	for i := 0; i < 3; i++ {
		producerWg.Add(1)
		go func(idx int) {
			defer producerWg.Done()
			cmd := exec.CommandContext(ctx, producerBinary)
			producerOutputs[idx], producerErrors[idx] = cmd.CombinedOutput()
		}(i)
	}

	producerWg.Wait()

	for i, err := range producerErrors {
		if err != nil {
			t.Logf("Producer %d error: %v", i, err)
		}
		t.Logf("Producer %d output: %s", i, string(producerOutputs[i]))
	}

	t.Logf("Waiting for replication (timeout=%v)...", 30*time.Second)
	deadline := time.Now().Add(30 * time.Second)

	commandsReceived := 0
	var totalClaims int
	for time.Now().Before(deadline) {
		totalClaims = 0
		commandsReceived = 0
		for _, r := range repos {
			events, err := testutil.ParseEventLog(r.logPath)
			if err != nil {
				continue
			}
			cmds := testutil.FilterEvents(events, testutil.EventCommandReceived)
			commandsReceived += len(cmds)
			claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
			if len(claims) > 0 {
				totalClaims++
			}
		}

		if totalClaims >= cfg.replicationFactor*3 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Logf("=== AuctionLoad3Producers RESULTS ===")
	t.Logf("Commands received: %d", commandsReceived)
	t.Logf("Nodes with claims: %d", totalClaims)

	uniqueTargets := make(map[string]int)
	for _, r := range repos {
		events, _ := testutil.ParseEventLog(r.logPath)
		claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
		for _, c := range claims {
			uniqueTargets[c.Target]++
		}
	}

	t.Logf("Unique commands claimed: %d", len(uniqueTargets))
	for target, count := range uniqueTargets {
		t.Logf("  %s: %d claims", target, count)
	}

	if len(uniqueTargets) < 3 {
		t.Errorf("FAIL: Expected 3 unique commands, got %d", len(uniqueTargets))
	}

	allAtRF := true
	for target, count := range uniqueTargets {
		if count < cfg.replicationFactor {
			allAtRF = false
			t.Logf("  Command %s under-replicated: %d < %d", target, count, cfg.replicationFactor)
		}
	}

	if !allAtRF {
		t.Errorf("FAIL: Not all commands achieved RF=%d replication", cfg.replicationFactor)
	} else {
		t.Logf("PASS: All %d concurrent commands replicated to RF=%d", len(uniqueTargets), cfg.replicationFactor)
	}
}
