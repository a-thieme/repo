package main

import (
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
)

func isNFDRunning() bool {
	cmd := exec.Command("pgrep", "-x", "nfd")
	err := cmd.Run()
	return err == nil
}

type heartbeatIntegrationTestConfig struct {
	name              string
	nodeCount         int
	distribution      string
	heartbeatInterval time.Duration
	killOne           bool
	waitForHeartbeats int
}

func buildRepoBinaryForHeartbeat(t *testing.T) string {
	binaryPath := filepath.Join(t.TempDir(), "repo")
	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = filepath.Join("..", "repo")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build repo binary: %v\n%s", err, output)
	}
	return binaryPath
}

type heartbeatRepoProcess struct {
	cmd     *exec.Cmd
	logPath string
	nodeID  string
	prefix  string
}

func startHeartbeatRepos(t *testing.T, cfg heartbeatIntegrationTestConfig, repoBinary string, tmpDir string) []*heartbeatRepoProcess {
	repos := make([]*heartbeatRepoProcess, cfg.nodeCount)

	t.Logf("Starting %d %s repo instances with %v heartbeat interval...", cfg.nodeCount, cfg.distribution, cfg.heartbeatInterval)
	for i := 0; i < cfg.nodeCount; i++ {
		nodeID := string(rune('a' + i))
		logPath := filepath.Join(tmpDir, "events-"+nodeID+".jsonl")
		nodePrefix := "/ndn/repo/local/hb-" + cfg.distribution + "/" + nodeID

		cmd := exec.Command(repoBinary,
			"--event-log", logPath,
			"--node-prefix", nodePrefix,
			"--signing-identity", "/ndn/repo.teame.dev/repo",
			"--distribution", cfg.distribution,
			"--heartbeat-interval", cfg.heartbeatInterval.String(),
		)
		cmd.Stdout = nil
		cmd.Stderr = nil

		if err := cmd.Start(); err != nil {
			t.Fatalf("Failed to start repo %s: %v", nodeID, err)
		}

		repos[i] = &heartbeatRepoProcess{
			cmd:     cmd,
			logPath: logPath,
			nodeID:  nodeID,
			prefix:  nodePrefix,
		}
		t.Logf("  Started repo %s with prefix %s", nodeID, nodePrefix)

		time.Sleep(200 * time.Millisecond)
	}

	return repos
}

func stopHeartbeatRepos(repos []*heartbeatRepoProcess) {
	for _, r := range repos {
		if r.cmd.Process == nil {
			continue
		}
		r.cmd.Process.Signal(syscall.SIGTERM)
	}

	done := make(chan struct{}, len(repos))
	for _, r := range repos {
		go func(r *heartbeatRepoProcess) {
			if r.cmd.Process != nil {
				r.cmd.Wait()
			}
			done <- struct{}{}
		}(r)
	}

	timeout := time.After(5 * time.Second)
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

func waitForHeartbeatConvergence(t *testing.T, repos []*heartbeatRepoProcess, minHeartbeats int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		allHealthy := true
		for _, r := range repos {
			events, err := testutil.ParseEventLog(r.logPath)
			if err != nil {
				allHealthy = false
				continue
			}

			nodeUpdates := testutil.FilterEvents(events, testutil.EventHeartbeatReceived)
			if len(nodeUpdates) < minHeartbeats {
				allHealthy = false
				continue
			}

			otherNodes := make(map[string]bool)
			for _, e := range events {
				if e.EventType == testutil.EventHeartbeatReceived && e.From != "" && e.From != r.prefix {
					otherNodes[e.From] = true
				}
			}

			if len(otherNodes) < len(repos)-1 {
				allHealthy = false
			}
		}
		if allHealthy {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
}

func countHeartbeats(events []testutil.Event, prefix string) int {
	uniqueNodes := make(map[string]bool)
	for _, e := range events {
		if e.EventType == testutil.EventHeartbeatReceived && e.From != "" && e.From != prefix {
			uniqueNodes[e.From] = true
		}
	}
	return len(uniqueNodes)
}

func TestAuctionHeartbeat_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if !isNFDRunning() {
		t.Skip("NFD is not running - skipping integration test")
	}

	out, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)
	out, err = exec.Command("nfdc", "strategy", "set", "/ndn/drepo/heartbeat", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set heartbeat: %s (err=%v)", string(out), err)

	cfg := heartbeatIntegrationTestConfig{
		name:              "auction heartbeat",
		nodeCount:         3,
		distribution:      "auction",
		heartbeatInterval: 500 * time.Millisecond,
		waitForHeartbeats: 2,
	}

	tmpDir := t.TempDir()
	repoBinary := buildRepoBinaryForHeartbeat(t)
	repos := startHeartbeatRepos(t, cfg, repoBinary, tmpDir)
	defer stopHeartbeatRepos(repos)

	t.Logf("Waiting for heartbeat convergence (%v)...", 3*time.Second)
	if !waitForHeartbeatConvergence(t, repos, cfg.waitForHeartbeats, 3*time.Second) {
		for _, r := range repos {
			events, _ := testutil.ParseEventLog(r.logPath)
			t.Logf("Node %s received %d node updates", r.nodeID, len(testutil.FilterEvents(events, testutil.EventNodeUpdate)))
		}
		t.Fatal("Heartbeats did not converge within timeout")
	}

	for _, r := range repos {
		events, err := testutil.ParseEventLog(r.logPath)
		if err != nil {
			t.Fatalf("Failed to parse event log for %s: %v", r.nodeID, err)
		}

		heartbeats := countHeartbeats(events, r.prefix)
		if heartbeats < cfg.waitForHeartbeats {
			t.Errorf("Node %s: expected at least %d heartbeats, got %d", r.nodeID, cfg.waitForHeartbeats, heartbeats)
		}

		otherNodes := make(map[string]bool)
		for _, e := range events {
			if e.EventType == testutil.EventHeartbeatReceived && e.From != "" && e.From != r.prefix {
				otherNodes[e.From] = true
			}
		}
		if len(otherNodes) != cfg.nodeCount-1 {
			t.Errorf("Node %s: expected %d other nodes, got %d", r.nodeID, cfg.nodeCount-1, len(otherNodes))
		}

		t.Logf("Node %s: %d heartbeats published, %d other nodes visible", r.nodeID, heartbeats, len(otherNodes))
	}
}

func TestHydraHeartbeat_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if !isNFDRunning() {
		t.Skip("NFD is not running - skipping integration test")
	}

	out, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)

	cfg := heartbeatIntegrationTestConfig{
		name:              "hydra heartbeat",
		nodeCount:         3,
		distribution:      "hydra",
		heartbeatInterval: 500 * time.Millisecond,
		waitForHeartbeats: 2,
	}

	tmpDir := t.TempDir()
	repoBinary := buildRepoBinaryForHeartbeat(t)
	repos := startHeartbeatRepos(t, cfg, repoBinary, tmpDir)
	defer stopHeartbeatRepos(repos)

	t.Logf("Waiting for heartbeat convergence (%v)...", 3*time.Second)
	if !waitForHeartbeatConvergence(t, repos, cfg.waitForHeartbeats, 3*time.Second) {
		for _, r := range repos {
			events, _ := testutil.ParseEventLog(r.logPath)
			t.Logf("Node %s received %d node updates", r.nodeID, len(testutil.FilterEvents(events, testutil.EventNodeUpdate)))
		}
		t.Fatal("Heartbeats did not converge within timeout")
	}

	for _, r := range repos {
		events, err := testutil.ParseEventLog(r.logPath)
		if err != nil {
			t.Fatalf("Failed to parse event log for %s: %v", r.nodeID, err)
		}

		heartbeats := countHeartbeats(events, r.prefix)
		if heartbeats < cfg.waitForHeartbeats {
			t.Errorf("Node %s: expected at least %d heartbeats, got %d", r.nodeID, cfg.waitForHeartbeats, heartbeats)
		}

		otherNodes := make(map[string]bool)
		for _, e := range events {
			if e.EventType == testutil.EventNodeUpdate && e.From != "" && e.From != r.prefix {
				otherNodes[e.From] = true
			}
		}
		if len(otherNodes) != cfg.nodeCount-1 {
			t.Errorf("Node %s: expected %d other nodes, got %d", r.nodeID, cfg.nodeCount-1, len(otherNodes))
		}

		t.Logf("Node %s: %d heartbeats published, %d other nodes visible", r.nodeID, heartbeats, len(otherNodes))
	}
}

func TestAuctionHeartbeatTimeout_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if !isNFDRunning() {
		t.Skip("NFD is not running - skipping integration test")
	}

	out, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)
	out, err = exec.Command("nfdc", "strategy", "set", "/ndn/drepo/heartbeat", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set heartbeat: %s (err=%v)", string(out), err)

	cfg := heartbeatIntegrationTestConfig{
		name:              "auction heartbeat timeout",
		nodeCount:         3,
		distribution:      "auction",
		heartbeatInterval: 500 * time.Millisecond,
		waitForHeartbeats: 3,
	}

	tmpDir := t.TempDir()
	repoBinary := buildRepoBinaryForHeartbeat(t)
	repos := startHeartbeatRepos(t, cfg, repoBinary, tmpDir)
	defer stopHeartbeatRepos(repos)

	t.Logf("Waiting for initial heartbeat convergence...")
	if !waitForHeartbeatConvergence(t, repos, cfg.waitForHeartbeats, 3*time.Second) {
		t.Fatal("Initial heartbeat convergence failed")
	}

	deadNode := repos[0]
	t.Logf("Killing node %s (%s) to simulate failure...", deadNode.nodeID, deadNode.prefix)
	deadNode.cmd.Process.Signal(syscall.SIGKILL)

	waitTime := 4*time.Second + 500*time.Millisecond
	t.Logf("Waiting %v for heartbeat timeout detection...", waitTime)
	time.Sleep(waitTime)

	for _, r := range repos[1:] {
		events, err := testutil.ParseEventLog(r.logPath)
		if err != nil {
			t.Fatalf("Failed to parse event log for %s: %v", r.nodeID, err)
		}

		nodeUpdates := testutil.FilterEvents(events, testutil.EventNodeUpdate)
		deadNodeUpdates := 0
		for _, e := range nodeUpdates {
			if e.From == deadNode.prefix {
				deadNodeUpdates++
			}
		}

		if deadNodeUpdates > 0 {
			t.Errorf("Node %s: expected 0 updates from dead node %s after timeout, got %d", r.nodeID, deadNode.nodeID, deadNodeUpdates)
		}

		t.Logf("Node %s: no updates from dead node %s after timeout (good)", r.nodeID, deadNode.nodeID)
	}
}

func TestHydraHeartbeatTimeout_Integration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if !isNFDRunning() {
		t.Skip("NFD is not running - skipping integration test")
	}

	out, err := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s (err=%v)", string(out), err)

	cfg := heartbeatIntegrationTestConfig{
		name:              "hydra heartbeat timeout",
		nodeCount:         3,
		distribution:      "hydra",
		heartbeatInterval: 500 * time.Millisecond,
		waitForHeartbeats: 3,
	}

	tmpDir := t.TempDir()
	repoBinary := buildRepoBinaryForHeartbeat(t)
	repos := startHeartbeatRepos(t, cfg, repoBinary, tmpDir)
	defer stopHeartbeatRepos(repos)

	t.Logf("Waiting for initial heartbeat convergence...")
	if !waitForHeartbeatConvergence(t, repos, cfg.waitForHeartbeats, 3*time.Second) {
		t.Fatal("Initial heartbeat convergence failed")
	}

	deadNode := repos[0]
	t.Logf("Killing node %s (%s) to simulate failure...", deadNode.nodeID, deadNode.prefix)
	deadNode.cmd.Process.Signal(syscall.SIGKILL)

	waitTime := 4*time.Second + 500*time.Millisecond
	t.Logf("Waiting %v for heartbeat timeout detection...", waitTime)
	time.Sleep(waitTime)

	for _, r := range repos[1:] {
		events, err := testutil.ParseEventLog(r.logPath)
		if err != nil {
			t.Fatalf("Failed to parse event log for %s: %v", r.nodeID, err)
		}

		nodeUpdates := testutil.FilterEvents(events, testutil.EventNodeUpdate)
		deadNodeUpdates := 0
		for _, e := range nodeUpdates {
			if e.From == deadNode.prefix {
				deadNodeUpdates++
			}
		}

		if deadNodeUpdates > 0 {
			t.Errorf("Node %s: expected 0 updates from dead node %s after timeout, got %d", r.nodeID, deadNode.nodeID, deadNodeUpdates)
		}

		t.Logf("Node %s: no updates from dead node %s after timeout (good)", r.nodeID, deadNode.nodeID)
	}
}
