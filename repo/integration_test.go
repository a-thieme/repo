package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
	"github.com/a-thieme/repo/repo/util"
	"github.com/a-thieme/repo/tlv"
	enc "github.com/named-data/ndnd/std/encoding"
)

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

func TestLocalReplication_FiveNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	out, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)

	t.Log("Waiting for routing convergence (3s)...")
	time.Sleep(3 * time.Second)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)
	tmpDir := t.TempDir()

	nodeCount := 5
	replicationFactor := 3

	repos := make([]*repoProcess, nodeCount)

	t.Log("Starting 5 repo instances...")
	for i := 0; i < nodeCount; i++ {
		nodeID := string(rune('a' + i))
		logPath := filepath.Join(tmpDir, "events-"+nodeID+".jsonl")
		nodePrefix := "/ndn/repo/local/" + nodeID

		cmd := exec.Command(repoBinary,
			"--event-log", logPath,
			"--node-prefix", nodePrefix,
			"--signing-identity", "/ndn/repo.teame.dev/repo",
		)
		cmd.Stdout = nil
		cmd.Stderr = nil

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

	defer func() {
		for _, r := range repos {
			r.cmd.Process.Kill()
			r.cmd.Wait()
		}
	}()

	t.Log("Waiting for repos to initialize and sync...")
	time.Sleep(20 * time.Second)

	t.Log("Checking SVS health (node updates exchanged)...")
	if !waitForSVSHealth(t, repos, 1, 10*time.Second) {
		t.Log("WARNING: SVS not healthy - node updates not exchanged between all peers")
	}

	t.Log("Running producer to send command...")
	producerCmd := exec.Command(producerBinary)
	producerOutput, err := producerCmd.CombinedOutput()
	if err != nil {
		t.Logf("Producer error: %v", err)
	}
	t.Logf("Producer output:\n%s", string(producerOutput))

	t.Log("Waiting for replication...")
	timeout := 30 * time.Second
	deadline := time.Now().Add(timeout)

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

		if totalClaims >= replicationFactor {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Log("Final event analysis:")
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
		t.Logf("  %s: commands=%d, claims=%d, syncs=%d, updates=%d, repChecks=%d",
			r.nodeID, len(cmds), len(claims), len(syncs), len(updates), len(repChecks))
		for _, rc := range repChecks {
			t.Logf("    repCheck: shouldClaim=%v reason=%s", rc.ShouldClaim, rc.Reason)
		}
	}

	if totalClaims < replicationFactor {
		t.Errorf("FAIL: Expected %d claims, got %d", replicationFactor, totalClaims)
	} else {
		t.Logf("PASS: Replication achieved with %d claims", totalClaims)
	}
}

func TestNodeUpdateExchange(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	out, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)

	t.Log("Waiting for routing convergence (3s)...")
	time.Sleep(3 * time.Second)

	repoBinary := buildRepoBinary(t)
	tmpDir := t.TempDir()

	nodeCount := 3
	repos := make([]*repoProcess, nodeCount)

	t.Logf("Starting %d repo instances...", nodeCount)
	for i := 0; i < nodeCount; i++ {
		nodeID := string(rune('a' + i))
		logPath := filepath.Join(tmpDir, "events-"+nodeID+".jsonl")
		nodePrefix := "/ndn/repo/local/" + nodeID
		stdoutPath := filepath.Join(tmpDir, "stdout-"+nodeID+".log")

		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			t.Fatalf("Failed to create stdout file for %s: %v", nodeID, err)
		}

		cmd := exec.Command(repoBinary,
			"--event-log", logPath,
			"--node-prefix", nodePrefix,
			"--signing-identity", "/ndn/repo.teame.dev/repo",
			"--debug",
		)
		cmd.Stdout = stdoutFile
		cmd.Stderr = stdoutFile

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

	defer func() {
		for _, r := range repos {
			r.cmd.Process.Kill()
			r.cmd.Wait()
		}
	}()

	t.Log("Waiting for heartbeat exchange (20s)...")
	time.Sleep(20 * time.Second)

	t.Log("=== FIB STATE ===")
	fibOut, _ := exec.Command("nfdc", "fib", "list").CombinedOutput()
	t.Logf("%s", string(fibOut))

	t.Log("=== FACES (unix socket) ===")
	faceOut, _ := exec.Command("sh", "-c", "nfdc face list | grep unix").CombinedOutput()
	t.Logf("%s", string(faceOut))

	t.Log("=== NODE UPDATE EXCHANGE ANALYSIS ===")
	allHealthy := true
	for _, r := range repos {
		events, err := testutil.ParseEventLog(r.logPath)
		if err != nil {
			t.Logf("  %s: error parsing log: %v", r.nodeID, err)
			allHealthy = false
			continue
		}
		peers := countUniquePeerUpdates(events, r.prefix)
		syncs := testutil.FilterEvents(events, testutil.EventSyncInterestSent)
		t.Logf("  %s: syncs=%d, peers=%v", r.nodeID, len(syncs), peers)

		expectedPeers := nodeCount - 1
		if len(peers) < expectedPeers {
			t.Logf("    FAIL: expected updates from %d peers, got %d", expectedPeers, len(peers))
			allHealthy = false
		}
	}

	if !allHealthy {
		t.Errorf("FAIL: Not all nodes received updates from all peers")
	} else {
		t.Log("PASS: All nodes received updates from all peers")
	}

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

func TestCommandPropagation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	out, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)

	t.Log("Waiting for routing convergence (3s)...")
	time.Sleep(3 * time.Second)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)
	tmpDir := t.TempDir()

	nodeCount := 3
	replicationFactor := 2
	repos := make([]*repoProcess, nodeCount)

	t.Logf("Starting %d repo instances...", nodeCount)
	for i := 0; i < nodeCount; i++ {
		nodeID := string(rune('a' + i))
		logPath := filepath.Join(tmpDir, "events-"+nodeID+".jsonl")
		nodePrefix := "/ndn/repo/local/" + nodeID

		cmd := exec.Command(repoBinary,
			"--event-log", logPath,
			"--node-prefix", nodePrefix,
			"--signing-identity", "/ndn/repo.teame.dev/repo",
		)
		cmd.Stdout = nil
		cmd.Stderr = nil

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

	defer func() {
		for _, r := range repos {
			r.cmd.Process.Kill()
			r.cmd.Wait()
		}
	}()

	t.Log("Waiting for SVS health (20s)...")
	time.Sleep(20 * time.Second)

	t.Log("Checking SVS health before command propagation test...")
	if !waitForSVSHealth(t, repos, 1, 5*time.Second) {
		t.Skip("SVS not healthy - cannot test command propagation")
	}
	t.Log("SVS health check passed")

	t.Log("Running producer to send command...")
	producerCmd := exec.Command(producerBinary)
	producerOutput, err := producerCmd.CombinedOutput()
	if err != nil {
		t.Logf("Producer error: %v", err)
	}
	t.Logf("Producer output:\n%s", string(producerOutput))

	t.Log("Waiting for command propagation (10s)...")
	time.Sleep(10 * time.Second)

	t.Log("=== COMMAND PROPAGATION ANALYSIS ===")
	nodesWithCommand := 0
	nodesWithClaim := 0
	for _, r := range repos {
		events, err := testutil.ParseEventLog(r.logPath)
		if err != nil {
			t.Logf("  %s: error parsing log: %v", r.nodeID, err)
			continue
		}
		cmds := testutil.FilterEvents(events, testutil.EventCommandReceived)
		claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
		t.Logf("  %s: commands=%d, claims=%d", r.nodeID, len(cmds), len(claims))

		if len(cmds) > 0 {
			nodesWithCommand++
		}
		if len(claims) > 0 {
			nodesWithClaim++
		}
	}

	if nodesWithCommand < nodeCount {
		t.Errorf("FAIL: Expected all %d nodes to receive command, got %d", nodeCount, nodesWithCommand)
	} else {
		t.Logf("PASS: All %d nodes received command", nodesWithCommand)
	}

	if nodesWithClaim != replicationFactor {
		t.Errorf("FAIL: Expected %d nodes to claim job, got %d", replicationFactor, nodesWithClaim)
	} else {
		t.Logf("PASS: Exactly %d nodes claimed job (replication factor)", nodesWithClaim)
	}
}

func TestRepoIntegration_SingleProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	binaryPath := buildRepoBinary(t)
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "events.jsonl")

	cmd := exec.Command(binaryPath, "--event-log", logPath)
	cmd.Env = append(os.Environ(), "NLSR_CONFIG=disabled")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("Failed to create stdout pipe: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("Failed to start repo: %v", err)
	}

	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			t.Logf("repo stdout: %s", scanner.Text())
		}
	}()

	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()

	time.Sleep(2 * time.Second)

	events, err := testutil.WaitForEvents(logPath, 1, 5*time.Second)
	if err != nil {
		t.Logf("Event log contents after waiting: %d events", len(events))
	}

	syncEvents := testutil.FilterEvents(events, testutil.EventSyncInterestSent)
	t.Logf("Sync interest events: %d", len(syncEvents))

	if len(syncEvents) > 0 {
		t.Logf("Latest sync interest count: %d", syncEvents[len(syncEvents)-1].Total)
	}
}

func TestEventLog_MultiNodeReplication(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	tmpDir := t.TempDir()

	nodeCount := 3
	logPaths := make([]string, nodeCount)
	for i := 0; i < nodeCount; i++ {
		logPaths[i] = filepath.Join(tmpDir, "events-"+string(rune('a'+i))+".jsonl")
	}

	events := make([][]testutil.Event, nodeCount)
	for i := 0; i < nodeCount; i++ {
		logger, err := util.NewEventLogger(logPaths[i], "/ndn/repo/node-"+string(rune('a'+i)))
		if err != nil {
			t.Fatalf("Failed to create event logger %d: %v", i, err)
		}

		logger.LogCommandReceived("INSERT", "/ndn/target/test")
		logger.LogReplicationDecision(
			"/ndn/target/test",
			true,
			"selected_as_candidate",
			0,
			3,
			[]string{"/ndn/repo/node-a", "/ndn/repo/node-b", "/ndn/repo/node-c"},
			[]string{"/ndn/repo/node-a", "/ndn/repo/node-b", "/ndn/repo/node-c"},
			map[string]uint64{
				"/ndn/repo/node-a": 1e9,
				"/ndn/repo/node-b": 1e9,
				"/ndn/repo/node-c": 1e9,
			},
		)
		logger.LogJobClaimed("/ndn/target/test", i+1)
		logger.LogSyncInterestSent(uint64(i + 1))
		logger.LogDataSent("/ndn/sync/data", uint64(i+1))

		logger.Close()

		events[i], err = testutil.ParseEventLog(logPaths[i])
		if err != nil {
			t.Fatalf("Failed to parse event log %d: %v", i, err)
		}
	}

	totalClaims := 0
	for i := range events {
		claims := testutil.FilterEvents(events[i], testutil.EventJobClaimed)
		totalClaims += len(claims)
	}

	if totalClaims != nodeCount {
		t.Errorf("Expected %d total job claims across all nodes, got %d", nodeCount, totalClaims)
	}

	nodesWithJob := testutil.GetNodesWithJob(append(events[0], append(events[1], events[2]...)...), "/ndn/target/test")
	if len(nodesWithJob) != nodeCount {
		t.Errorf("Expected %d nodes to have claimed the job, got %d", nodeCount, len(nodesWithJob))
	}
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
