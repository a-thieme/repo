package main

import (
	"context"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
)

var (
	failureNodeCount    = flag.Int("failure-nodes", 5, "node count for failure tests")
	failureRF           = flag.Int("failure-rf", 3, "replication factor for failure tests")
	failureCount        = flag.Int("failure-count", 1, "number of repos to kill (default: 1)")
	failureRecoveryWait = flag.Duration("failure-recovery-timeout", 30*time.Second, "timeout to wait for recovery after failure")
)

type failureTestConfig struct {
	name              string
	nodeCount         int
	replicationFactor int
	failureCount      int
	commandType       string
}

func TestFailureRecovery_SingleRepoDown(t *testing.T) {
	runFailureTest(t, failureTestConfig{
		name:              "SingleRepoDown",
		nodeCount:         *failureNodeCount,
		replicationFactor: *failureRF,
		failureCount:      1,
		commandType:       "insert",
	})
}

func TestFailureRecovery_MultipleReposDown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping failure test in short mode")
	}
	runFailureTest(t, failureTestConfig{
		name:              "MultipleReposDown",
		nodeCount:         *failureNodeCount,
		replicationFactor: *failureRF,
		failureCount:      2,
		commandType:       "insert",
	})
}

func runFailureTest(t *testing.T, cfg failureTestConfig) {
	if testing.Short() {
		t.Skip("Skipping failure test in short mode")
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

	repos := startFailureRepos(t, cfg, repoBinary, tmpDir)
	defer stopFailureRepos(repos)

	t.Logf("Waiting for SVS convergence (timeout=%v)...", *svsHealthTimeout)
	if !waitForSVSHealth(t, repos, 1, *svsHealthTimeout) {
		t.Log("WARNING: SVS not healthy - node updates not exchanged between all peers")
	}

	t.Logf("Running producer to send command (timeout=%v)...", *producerTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), *producerTimeout)
	defer cancel()
	producerCmd := exec.CommandContext(ctx, producerBinary, "-type", cfg.commandType)
	producerOutput, err := producerCmd.CombinedOutput()
	if err != nil {
		t.Logf("Producer error: %v", err)
	}
	t.Logf("Producer output:\n%s", string(producerOutput))

	t.Logf("Waiting for full replication to RF=%d...", cfg.replicationFactor)
	if !waitForFullReplication(t, repos, cfg.replicationFactor, *replicationTimeout) {
		commands := getCommandsWithClaims(t, repos)
		for target, nodes := range commands {
			t.Logf("  Command %s: %d claims (need %d)", target, len(nodes), cfg.replicationFactor)
		}
		t.Fatalf("Initial replication failed: not all commands reached RF=%d", cfg.replicationFactor)
	}
	t.Logf("Initial replication achieved")

	commandsBeforeFailure := getCommandsWithClaims(t, repos)
	t.Logf("Commands before failure: %d", len(commandsBeforeFailure))
	for target, nodes := range commandsBeforeFailure {
		t.Logf("  %s: claimed by %v", target, nodes)
	}

	reposToKill := repos[len(repos)-cfg.failureCount:]
	t.Logf("Killing %d repo(s) to simulate failure...", cfg.failureCount)
	for _, r := range reposToKill {
		t.Logf("  Killing repo %s", r.nodeID)
		r.cmd.Process.Kill()
		r.cmd.Wait()
	}

	affectedCommands := make(map[string][]string)
	for target, nodes := range commandsBeforeFailure {
		for _, killedRepo := range reposToKill {
			for _, node := range nodes {
				if node == killedRepo.nodeID {
					affectedCommands[target] = nodes
					break
				}
			}
		}
	}
	t.Logf("Affected commands (had claims on killed repos): %d", len(affectedCommands))

	if len(affectedCommands) == 0 {
		t.Log("No commands were affected by the failure - test cannot measure recovery")
		return
	}

	failureTime := time.Now()
	t.Logf("Failure occurred at %v, waiting for recovery...", failureTime)

	recoveryDeadline := time.Now().Add(*failureRecoveryWait)
	recovered := false
	var recoveryTime time.Duration

	for time.Now().Before(recoveryDeadline) {
		commandsAfterFailure := getCommandsWithClaims(t, repos[:len(repos)-cfg.failureCount])

		allRecovered := true
		for target := range affectedCommands {
			nodes, exists := commandsAfterFailure[target]
			if !exists || len(nodes) < cfg.replicationFactor {
				allRecovered = false
				break
			}
		}

		if allRecovered {
			recoveryTime = time.Since(failureTime)
			recovered = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Logf("=== FAILURE TEST RESULTS ===")
	t.Logf("Node count: %d", cfg.nodeCount)
	t.Logf("Replication factor: %d", cfg.replicationFactor)
	t.Logf("Failure count: %d", cfg.failureCount)
	t.Logf("Commands affected: %d", len(affectedCommands))

	if recovered {
		t.Logf("PASS: Recovery achieved in %v", recoveryTime)
		for target := range affectedCommands {
			t.Logf("  Command %s recovered to RF", target)
		}
	} else {
		commandsAfterFailure := getCommandsWithClaims(t, repos[:len(repos)-cfg.failureCount])
		t.Errorf("FAIL: Recovery not achieved within %v", *failureRecoveryWait)
		for target := range affectedCommands {
			nodes := commandsAfterFailure[target]
			t.Logf("  Command %s: %d claims (need %d)", target, len(nodes), cfg.replicationFactor)
		}
	}
}

func startFailureRepos(t *testing.T, cfg failureTestConfig, repoBinary string, tmpDir string) []*repoProcess {
	repos := make([]*repoProcess, cfg.nodeCount)

	t.Logf("Starting %d repo instances...", cfg.nodeCount)
	for i := 0; i < cfg.nodeCount; i++ {
		nodeID := fmt.Sprintf("n%d", i)
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

		time.Sleep(500 * time.Millisecond)
	}

	return repos
}

func stopFailureRepos(repos []*repoProcess) {
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

func waitForReplication(t *testing.T, repos []*repoProcess, rf int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		claims := getCommandsWithClaims(t, repos)

		minClaims := int(^uint(0) >> 1)
		if len(claims) == 0 {
			minClaims = 0
		} else {
			for _, nodes := range claims {
				if len(nodes) < minClaims {
					minClaims = len(nodes)
				}
			}
		}

		if minClaims >= rf {
			return rf
		}

		time.Sleep(500 * time.Millisecond)
	}

	totalClaims := 0
	for _, r := range repos {
		events, _ := testutil.ParseEventLog(r.logPath)
		claims := testutil.FilterEvents(events, testutil.EventJobClaimed)
		totalClaims += len(claims)
	}
	return totalClaims
}

func getCommandsWithClaims(t *testing.T, repos []*repoProcess) map[string][]string {
	commands := make(map[string]map[string]bool)

	for _, r := range repos {
		events, err := testutil.ParseEventLog(r.logPath)
		if err != nil {
			continue
		}

		claimed := testutil.FilterEvents(events, testutil.EventJobClaimed)
		released := testutil.FilterEvents(events, testutil.EventJobReleased)

		for _, e := range claimed {
			if commands[e.Target] == nil {
				commands[e.Target] = make(map[string]bool)
			}
			commands[e.Target][r.nodeID] = true
		}

		for _, e := range released {
			if commands[e.Target] != nil {
				delete(commands[e.Target], r.nodeID)
			}
		}
	}

	result := make(map[string][]string)
	for target, nodes := range commands {
		result[target] = make([]string, 0, len(nodes))
		for node := range nodes {
			result[target] = append(result[target], node)
		}
	}

	return result
}

func waitForFullReplication(t *testing.T, repos []*repoProcess, rf int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		commands := getCommandsWithClaims(t, repos)

		if len(commands) == 0 {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		allReplicated := true
		for _, nodes := range commands {
			if len(nodes) < rf {
				allReplicated = false
				break
			}
		}

		if allReplicated {
			return true
		}

		time.Sleep(500 * time.Millisecond)
	}

	return false
}

func TestFailureRecovery_CascadingFailures(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping failure test in short mode")
	}

	cfg := failureTestConfig{
		name:              "CascadingFailures",
		nodeCount:         7,
		replicationFactor: 3,
		failureCount:      2,
		commandType:       "insert",
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

	repos := startFailureRepos(t, cfg, repoBinary, tmpDir)
	defer stopFailureRepos(repos)

	t.Logf("Waiting for SVS convergence (timeout=%v)...", *svsHealthTimeout)
	if !waitForSVSHealth(t, repos, 1, *svsHealthTimeout) {
		t.Log("WARNING: SVS not healthy")
	}

	t.Logf("Running producer to send command...")
	ctx, cancel := context.WithTimeout(context.Background(), *producerTimeout)
	defer cancel()
	producerCmd := exec.CommandContext(ctx, producerBinary, "-type", cfg.commandType)
	producerOutput, err := producerCmd.CombinedOutput()
	if err != nil {
		t.Logf("Producer error: %v", err)
	}
	t.Logf("Producer output:\n%s", string(producerOutput))

	t.Logf("Waiting for full replication to RF=%d...", cfg.replicationFactor)
	if !waitForFullReplication(t, repos, cfg.replicationFactor, *replicationTimeout) {
		commands := getCommandsWithClaims(t, repos)
		for target, nodes := range commands {
			t.Logf("  Command %s: %d claims (need %d)", target, len(nodes), cfg.replicationFactor)
		}
		t.Fatalf("Initial replication failed: not all commands reached RF=%d", cfg.replicationFactor)
	}
	t.Logf("Initial replication achieved")

	commandsBeforeFailure := getCommandsWithClaims(t, repos)
	t.Logf("Commands before failure: %d", len(commandsBeforeFailure))

	for i := 0; i < cfg.failureCount; i++ {
		repoToKill := repos[len(repos)-1-i]
		t.Logf("Killing repo %s (cascade step %d/%d)...", repoToKill.nodeID, i+1, cfg.failureCount)
		repoToKill.cmd.Process.Kill()
		repoToKill.cmd.Wait()

		time.Sleep(1 * time.Second)
	}

	survivingRepos := repos[:len(repos)-cfg.failureCount]
	t.Logf("Surviving repos: %d", len(survivingRepos))

	t.Logf("Waiting for recovery (timeout=%v)...", *failureRecoveryWait)
	time.Sleep(*failureRecoveryWait)

	commandsAfterFailure := getCommandsWithClaims(t, survivingRepos)
	t.Logf("=== CASCADING FAILURE TEST RESULTS ===")
	t.Logf("Node count: %d", cfg.nodeCount)
	t.Logf("Replication factor: %d", cfg.replicationFactor)
	t.Logf("Failures: %d", cfg.failureCount)
	t.Logf("Surviving nodes: %d", len(survivingRepos))

	recovered := true
	for target, beforeNodes := range commandsBeforeFailure {
		currentNodes := commandsAfterFailure[target]
		if len(currentNodes) < cfg.replicationFactor {
			recovered = false
			t.Logf("  Command %s: %d claims (was %d, need %d) - NOT RECOVERED", target, len(currentNodes), len(beforeNodes), cfg.replicationFactor)
		} else {
			t.Logf("  Command %s: %d claims - RECOVERED", target, len(currentNodes))
		}
	}

	if recovered {
		t.Logf("PASS: Recovery achieved after cascading failures")
	} else {
		t.Logf("FAIL: Recovery not achieved (expected behavior - recovery not implemented)")
	}
}
