package main

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
	"github.com/a-thieme/repo/repo/util"
	"github.com/a-thieme/repo/tlv"
	enc "github.com/named-data/ndnd/std/encoding"
)

type integrationTestConfig struct {
	name              string
	nodeCount         int
	replicationFactor int
	cacheless         bool
	runProducer       bool
	debug             bool
}

func buildRepoBinary(t *testing.T) string {
	binaryPath := filepath.Join(t.TempDir(), "repo")
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = filepath.Join("..", "repo")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build repo binary: %v\n%s", err, output)
	}
	return binaryPath
}

func buildProducerBinary(t *testing.T) string {
	binaryPath := filepath.Join(t.TempDir(), "producer")
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = filepath.Join("..", "producer")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build producer binary: %v\n%s", err, output)
	}
	return binaryPath
}

type repoProcess struct {
	cmd     *exec.Cmd
	logPath string
	nodeID  string
	prefix  string
}

func countUniquePeerUpdates(events []testutil.Event, myPrefix string) map[string]int {
	peers := make(map[string]int)
	for _, e := range events {
		if e.EventType == testutil.EventNodeUpdate && e.From != "" && e.From != myPrefix {
			peers[e.From]++
		}
	}
	return peers
}

func waitForSVSHealth(t *testing.T, repos []*repoProcess, minUpdatesPerPeer int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allHealthy := true
		for _, r := range repos {
			events, err := testutil.ParseEventLog(r.logPath)
			if err != nil {
				allHealthy = false
				continue
			}
			peers := countUniquePeerUpdates(events, r.prefix)
			expectedPeers := len(repos) - 1
			healthyPeers := 0
			for _, count := range peers {
				if count >= minUpdatesPerPeer {
					healthyPeers++
				}
			}
			if healthyPeers < expectedPeers {
				allHealthy = false
			}
		}
		if allHealthy {
			return true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return false
}

func setupCSCache(cacheless bool) func() {
	if !cacheless {
		return func() {}
	}
	exec.Command("nfdc", "cs", "config", "capacity", "1").Run()
	exec.Command("nfdc", "cs", "erase").Run()
	return func() {
		exec.Command("nfdc", "cs", "config", "capacity", "65536", "admit", "on", "serve", "on").Run()
		exec.Command("nfdc", "cs", "erase").Run()
	}
}

func startRepos(t *testing.T, cfg integrationTestConfig, repoBinary string, tmpDir string) []*repoProcess {
	repos := make([]*repoProcess, cfg.nodeCount)

	t.Logf("Starting %d repo instances...", cfg.nodeCount)
	for i := 0; i < cfg.nodeCount; i++ {
		nodeID := string(rune('a' + i))
		logPath := filepath.Join(tmpDir, "events-"+nodeID+".jsonl")
		nodePrefix := "/ndn/repo/local/" + nodeID

		cmd := exec.Command(repoBinary,
			"--event-log", logPath,
			"--node-prefix", nodePrefix,
			"--signing-identity", "/ndn/repo.teame.dev/repo",
		)

		if cfg.debug {
			stdoutPath := filepath.Join(tmpDir, "stdout-"+nodeID+".log")
			stdoutFile, err := os.Create(stdoutPath)
			if err != nil {
				t.Fatalf("Failed to create stdout file for %s: %v", nodeID, err)
			}
			cmd.Stdout = stdoutFile
			cmd.Stderr = stdoutFile
			cmd.Args = append(cmd.Args, "--debug")
		} else {
			cmd.Stdout = nil
			cmd.Stderr = nil
		}

		if err := cmd.Start(); err != nil {
			t.Fatalf("Failed to start repo %s: %v", nodeID, err)
		}

		repos[i] = &repoProcess{
			cmd:     cmd,
			logPath: logPath,
			nodeID:  nodeID,
			prefix:  nodePrefix,
		}
		t.Logf("  Started repo %s with prefix %s", nodeID, nodePrefix)

		time.Sleep(500 * time.Millisecond)
	}

	return repos
}

func stopRepos(repos []*repoProcess) {
	for _, r := range repos {
		if r.cmd.Process == nil {
			continue
		}
		r.cmd.Process.Signal(syscall.SIGTERM)
	}

	done := make(chan struct{}, len(repos))
	for _, r := range repos {
		go func(r *repoProcess) {
			if r.cmd.Process != nil {
				r.cmd.Wait()
			}
			done <- struct{}{}
		}(r)
	}

	timeout := time.After(2 * time.Second)
	for i := 0; i < len(repos); i++ {
		select {
		case <-done:
		case <-timeout:
			for _, r := range repos {
				if r.cmd.Process != nil {
					r.cmd.Process.Kill()
					r.cmd.Wait()
				}
			}
			return
		}
	}
}

func runReplicationTest(t *testing.T, cfg integrationTestConfig) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	restore := setupCSCache(cfg.cacheless)
	defer restore()

	out, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)
	out2, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/notify", "/localhost/nfd/strategy/best-route").CombinedOutput()
	t.Logf("nfdc strategy set notify: %s (err=%v)", string(out2), err)
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)

	t.Logf("Waiting for routing convergence (%v)...", *routingConvergeWait)
	time.Sleep(*routingConvergeWait)

	repoBinary := buildRepoBinary(t)
	var producerBinary string
	if cfg.runProducer {
		producerBinary = buildProducerBinary(t)
	}
	tmpDir := t.TempDir()

	repos := startRepos(t, cfg, repoBinary, tmpDir)
	defer stopRepos(repos)

	t.Logf("Waiting for SVS convergence (timeout=%v)...", *svsHealthTimeout)
	if !waitForSVSHealth(t, repos, 1, *svsHealthTimeout) {
		t.Log("WARNING: SVS not healthy - node updates not exchanged between all peers")
	}

	if cfg.runProducer {
		t.Logf("Running producer to send command (timeout=%v)...", *producerTimeout)
		ctx, cancel := context.WithTimeout(context.Background(), *producerTimeout)
		defer cancel()
		producerCmd := exec.CommandContext(ctx, producerBinary)
		producerOutput, err := producerCmd.CombinedOutput()
		if err != nil {
			t.Logf("Producer error: %v", err)
		}
		t.Logf("Producer output:\n%s", string(producerOutput))
	}

	t.Logf("Waiting for replication (timeout=%v)...", *replicationTimeout)
	deadline := time.Now().Add(*replicationTimeout)

	var totalClaims int
	for time.Now().Before(deadline) {
		totalClaims = 0
		for _, r := range repos {
			events, err := testutil.ParseEventLog(r.logPath)
			if err != nil {
				continue
			}
			claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
			if len(claims) > 0 {
				totalClaims++
			}
		}

		if totalClaims >= cfg.replicationFactor {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Logf("=== %s RESULTS (cacheless=%v) ===", cfg.name, cfg.cacheless)
	totalDataPackets := 0
	for _, r := range repos {
		events, err := testutil.ParseEventLog(r.logPath)
		if err != nil {
			t.Logf("  %s: error parsing log: %v", r.nodeID, err)
			continue
		}
		cmds := testutil.FilterEvents(events, testutil.EventCommandReceived)
		claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
		syncs := testutil.FilterEvents(events, testutil.EventSyncInterestSent)
		updates := testutil.FilterEvents(events, testutil.EventNodeUpdate)
		repChecks := testutil.FilterEvents(events, testutil.EventReplicationCheck)
		dataSent := testutil.FilterEvents(events, testutil.EventDataSent)
		totalDataPackets += len(dataSent)
		t.Logf("  %s: commands=%d, claims=%d, syncs=%d, data=%d, updates=%d, repChecks=%d",
			r.nodeID, len(cmds), len(claims), len(syncs), len(dataSent), len(updates), len(repChecks))
		for _, rc := range repChecks {
			t.Logf("    repCheck: shouldClaim=%v reason=%s", rc.ShouldClaim, rc.Reason)
		}
	}
	t.Logf("Total data packets sent: %d", totalDataPackets)

	if totalClaims < cfg.replicationFactor {
		t.Errorf("FAIL: Expected %d claims, got %d", cfg.replicationFactor, totalClaims)
	} else {
		t.Logf("PASS: Replication achieved with %d claims", totalClaims)
	}

	if cfg.debug {
		t.Log("=== REPO STDOUT FILES ===")
		for _, r := range repos {
			stdoutPath := filepath.Join(tmpDir, "stdout-"+r.nodeID+".log")
			content, err := os.ReadFile(stdoutPath)
			if err != nil {
				t.Logf("  %s: could not read stdout file", r.nodeID)
				continue
			}
			t.Logf("  %s stdout:\n%s", r.nodeID, string(content))
		}
	}
}

func TestLocalReplication_FiveNodes(t *testing.T) {
	runReplicationTest(t, integrationTestConfig{
		name:              "FiveNodes",
		nodeCount:         5,
		replicationFactor: 3,
		cacheless:         false,
		runProducer:       true,
	})
}

func TestLocalReplication_FiveNodes_NoCache(t *testing.T) {
	runReplicationTest(t, integrationTestConfig{
		name:              "FiveNodes_NoCache",
		nodeCount:         5,
		replicationFactor: 3,
		cacheless:         true,
		runProducer:       true,
	})
}

func TestCommandPropagation(t *testing.T) {
	runReplicationTest(t, integrationTestConfig{
		name:              "CommandPropagation",
		nodeCount:         3,
		replicationFactor: 2,
		cacheless:         false,
		runProducer:       true,
	})
}

func TestEventLog_JobReleaseFlow(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "release.jsonl")

	logger, err := util.NewEventLogger(logPath, "/ndn/repo/test")
	if err != nil {
		t.Fatalf("Failed to create event logger: %v", err)
	}

	logger.LogCommandReceived("INSERT", "/ndn/target/release-test")
	logger.LogJobClaimed("/ndn/target/release-test", 1)
	logger.LogStorageChanged(800000000, 100000000)
	logger.LogStorageChanged(900000000, 100000000)
	logger.LogJobReleased("/ndn/target/release-test")

	logger.Close()

	events, err := testutil.ParseEventLog(logPath)
	if err != nil {
		t.Fatalf("Failed to parse event log: %v", err)
	}

	releaseEvents := testutil.FilterEvents(events, testutil.EventJobReleased)
	if len(releaseEvents) != 1 {
		t.Errorf("Expected 1 job release event, got %d", len(releaseEvents))
	}

	storageEvents := testutil.FilterEvents(events, testutil.EventStorageChanged)
	if len(storageEvents) != 2 {
		t.Errorf("Expected 2 storage change events, got %d", len(storageEvents))
	}
}

func TestEventLog_NodeUpdateFlow(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "update.jsonl")

	logger, err := util.NewEventLogger(logPath, "/ndn/repo/node-a")
	if err != nil {
		t.Fatalf("Failed to create event logger: %v", err)
	}

	logger.LogNodeUpdate("/ndn/repo/node-b", []string{"/ndn/job/1", "/ndn/job/2"}, 1e9, 5e8)
	logger.LogNodeUpdate("/ndn/repo/node-c", []string{"/ndn/job/1"}, 2e9, 1e9)

	logger.Close()

	events, err := testutil.ParseEventLog(logPath)
	if err != nil {
		t.Fatalf("Failed to parse event log: %v", err)
	}

	nodeUpdates := testutil.FilterEvents(events, testutil.EventNodeUpdate)
	if len(nodeUpdates) != 2 {
		t.Errorf("Expected 2 node update events, got %d", len(nodeUpdates))
	}

	fromNodeB := make([]testutil.Event, 0)
	for _, e := range nodeUpdates {
		if e.From == "/ndn/repo/node-b" {
			fromNodeB = append(fromNodeB, e)
		}
	}
	if len(fromNodeB) != 1 {
		t.Errorf("Expected 1 update from node-b, got %d", len(fromNodeB))
	}
	if len(fromNodeB[0].Jobs) != 2 {
		t.Errorf("Expected 2 jobs from node-b, got %d", len(fromNodeB[0].Jobs))
	}
}

func TestTLV_EncodingDecoding(t *testing.T) {
	target, _ := enc.NameFromStr("/ndn/target/test")

	cmd := tlv.Command{
		Type:              "INSERT",
		Target:            target,
		SnapshotThreshold: 100,
	}

	encoded := cmd.Encode()
	if len(encoded) == 0 {
		t.Error("Encoded command is empty")
	}

	decoded, err := tlv.ParseCommand(enc.NewWireView(encoded), false)
	if err != nil {
		t.Fatalf("Failed to decode command: %v", err)
	}

	if decoded.Type != cmd.Type {
		t.Errorf("Type mismatch: expected %s, got %s", cmd.Type, decoded.Type)
	}
	if !decoded.Target.Equal(cmd.Target) {
		t.Errorf("Target mismatch: expected %s, got %s", cmd.Target, decoded.Target)
	}
}

func TestEventLog_JSONFormat(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "format.jsonl")

	logger, err := util.NewEventLogger(logPath, "/ndn/repo/test")
	if err != nil {
		t.Fatalf("Failed to create event logger: %v", err)
	}

	logger.LogCommandReceived("INSERT", "/ndn/target/test")
	logger.Close()

	f, err := os.Open(logPath)
	if err != nil {
		t.Fatalf("Failed to open log file: %v", err)
	}
	defer f.Close()

	var raw map[string]interface{}
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatal("Log file is empty")
	}

	if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	requiredFields := []string{"ts", "event", "node", "type", "target"}
	for _, field := range requiredFields {
		if _, ok := raw[field]; !ok {
			t.Errorf("Missing required field: %s", field)
		}
	}

	if raw["event"] != "command_received" {
		t.Errorf("Expected event type 'command_received', got %v", raw["event"])
	}
}

func TestConcurrentCommands_TwoProducers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	restore := setupCSCache(false)
	defer restore()

	out, _ := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s", string(out))
	exec.Command("nfdc", "strategy", "set", "/ndn/drepo/notify", "/localhost/nfd/strategy/best-route").Run()

	t.Logf("Waiting for routing convergence (%v)...", *routingConvergeWait)
	time.Sleep(*routingConvergeWait)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)
	tmpDir := t.TempDir()

	cfg := integrationTestConfig{
		name:              "ConcurrentCommands",
		nodeCount:         5,
		replicationFactor: 3,
		cacheless:         false,
		runProducer:       false,
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

	t.Logf("Waiting for replication (timeout=%v)...", *replicationTimeout)
	deadline := time.Now().Add(*replicationTimeout)

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

	t.Logf("=== ConcurrentCommands RESULTS ===")
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

func TestMultipleCommands_Sequential(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	restore := setupCSCache(false)
	defer restore()

	out, _ := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s", string(out))
	exec.Command("nfdc", "strategy", "set", "/ndn/drepo/notify", "/localhost/nfd/strategy/best-route").Run()

	t.Logf("Waiting for routing convergence (%v)...", *routingConvergeWait)
	time.Sleep(*routingConvergeWait)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)
	tmpDir := t.TempDir()

	cfg := integrationTestConfig{
		name:              "MultipleCommands",
		nodeCount:         3,
		replicationFactor: 2,
		cacheless:         false,
		runProducer:       false,
	}

	repos := startRepos(t, cfg, repoBinary, tmpDir)
	defer stopRepos(repos)

	t.Logf("Waiting for SVS convergence (timeout=%v)...", *svsHealthTimeout)
	if !waitForSVSHealth(t, repos, 1, *svsHealthTimeout) {
		t.Log("WARNING: SVS not healthy")
	}

	t.Logf("Sending 10 sequential commands...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i := 0; i < 10; i++ {
		cmd := exec.CommandContext(ctx, producerBinary)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("Command %d error: %v", i, err)
		} else {
			t.Logf("Command %d: sent", i)
		}
		_ = output
		time.Sleep(200 * time.Millisecond)
	}

	t.Logf("Waiting for replication (timeout=%v)...", *replicationTimeout)
	time.Sleep(*replicationTimeout)

	t.Logf("=== MultipleCommands RESULTS ===")
	uniqueTargets := make(map[string]int)
	for _, r := range repos {
		events, _ := testutil.ParseEventLog(r.logPath)
		claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
		for _, c := range claims {
			uniqueTargets[c.Target]++
		}
	}

	t.Logf("Unique commands claimed: %d", len(uniqueTargets))
	atRF := 0
	for target, count := range uniqueTargets {
		if count >= cfg.replicationFactor {
			atRF++
		}
		t.Logf("  %s: %d claims", target, count)
	}

	if atRF < 10 {
		t.Errorf("FAIL: Expected 10 commands at RF=%d, got %d", cfg.replicationFactor, atRF)
	} else {
		t.Logf("PASS: All 10 commands replicated to RF=%d", cfg.replicationFactor)
	}
}

func TestReplicationFactor_EdgeCases(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	t.Run("RF1", func(t *testing.T) {
		runReplicationTest(t, integrationTestConfig{
			name:              "RF1_EdgeCase",
			nodeCount:         3,
			replicationFactor: 1,
			cacheless:         false,
			runProducer:       true,
		})
	})

	t.Run("RFEqualsNodes", func(t *testing.T) {
		runReplicationTest(t, integrationTestConfig{
			name:              "RFEqualsNodes",
			nodeCount:         3,
			replicationFactor: 3,
			cacheless:         false,
			runProducer:       true,
		})
	})
}

func TestNodeJoin_MidStream(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	restore := setupCSCache(false)
	defer restore()

	out, _ := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s", string(out))
	exec.Command("nfdc", "strategy", "set", "/ndn/drepo/notify", "/localhost/nfd/strategy/best-route").Run()

	t.Logf("Waiting for routing convergence (%v)...", *routingConvergeWait)
	time.Sleep(*routingConvergeWait)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)
	tmpDir := t.TempDir()

	cfg := integrationTestConfig{
		name:              "NodeJoin",
		nodeCount:         3,
		replicationFactor: 2,
		cacheless:         false,
		runProducer:       false,
	}

	repos := startRepos(t, cfg, repoBinary, tmpDir)
	defer stopRepos(repos)

	t.Logf("Waiting for SVS convergence (timeout=%v)...", *svsHealthTimeout)
	if !waitForSVSHealth(t, repos, 1, *svsHealthTimeout) {
		t.Log("WARNING: SVS not healthy")
	}

	t.Logf("Sending command with %d nodes...", cfg.nodeCount)
	ctx, cancel := context.WithTimeout(context.Background(), *producerTimeout)
	defer cancel()
	producerCmd := exec.CommandContext(ctx, producerBinary)
	producerOutput, err := producerCmd.CombinedOutput()
	if err != nil {
		t.Logf("Producer error: %v", err)
	}
	t.Logf("Producer output:\n%s", string(producerOutput))

	t.Logf("Waiting for initial replication...")
	time.Sleep(5 * time.Second)

	t.Logf("Starting new node (node %d)...", cfg.nodeCount+1)
	newNodeID := string(rune('a' + cfg.nodeCount))
	newLogPath := filepath.Join(tmpDir, "events-"+newNodeID+".jsonl")
	newNodePrefix := "/ndn/repo/local/" + newNodeID

	newCmd := exec.Command(repoBinary,
		"--event-log", newLogPath,
		"--node-prefix", newNodePrefix,
		"--signing-identity", "/ndn/repo.teame.dev/repo",
	)
	newCmd.Stdout = nil
	newCmd.Stderr = nil

	if err := newCmd.Start(); err != nil {
		t.Fatalf("Failed to start new repo: %v", err)
	}
	defer func() {
		newCmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(500 * time.Millisecond)
		newCmd.Process.Kill()
		newCmd.Wait()
	}()

	t.Logf("Waiting for new node to join SVS group...")
	time.Sleep(10 * time.Second)

	t.Logf("=== NODE JOIN RESULTS ===")
	for i, r := range repos {
		events, _ := testutil.ParseEventLog(r.logPath)
		claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
		t.Logf("  Original node %d (%s): claims=%d", i, r.nodeID, len(claims))
	}

	newEvents, err := testutil.ParseEventLog(newLogPath)
	if err != nil {
		t.Logf("  New node: could not parse log: %v", err)
	} else {
		updates := testutil.FilterEvents(newEvents, testutil.EventNodeUpdate)
		claims := testutil.FilterEvents(newEvents, testutil.EventJobClaimed)
		t.Logf("  New node (%s): updates=%d, claims=%d", newNodeID, len(updates), len(claims))
	}

	t.Logf("PASS: Node join test completed")
}
