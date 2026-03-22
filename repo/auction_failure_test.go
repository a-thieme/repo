package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

var (
	auctionFailureNodeCount    = flag.Int("auction-failure-nodes", 5, "node count for auction failure tests")
	auctionFailureRF           = flag.Int("auction-failure-rf", 3, "replication factor for auction failure tests")
	auctionFailureCount        = flag.Int("auction-failure-count", 1, "number of repos to kill in auction failure tests")
	auctionFailureRecoveryWait = flag.Duration("auction-failure-recovery-timeout", 60*time.Second, "timeout to wait for recovery after failure in auction tests")
	auctionFailureCommandCount = flag.Int("auction-failure-commands", 3, "number of commands to send before failure in auction tests")
)

type auctionFailureTestConfig struct {
	name              string
	nodeCount         int
	replicationFactor int
	failureCount      int
	commandType       string
	commandCount      int
}

func TestAuctionFailure_SingleRepoDown(t *testing.T) {
	runAuctionFailureTest(t, auctionFailureTestConfig{
		name:              "AuctionSingleRepoDown",
		nodeCount:         *auctionFailureNodeCount,
		replicationFactor: *auctionFailureRF,
		failureCount:      1,
		commandType:       "insert",
		commandCount:      *auctionFailureCommandCount,
	})
}

func TestAuctionFailure_MultipleReposDown(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping failure test in short mode")
	}
	runAuctionFailureTest(t, auctionFailureTestConfig{
		name:              "AuctionMultipleReposDown",
		nodeCount:         *auctionFailureNodeCount,
		replicationFactor: *auctionFailureRF,
		failureCount:      2,
		commandType:       "insert",
		commandCount:      *auctionFailureCommandCount,
	})
}

func runAuctionFailureTest(t *testing.T, cfg auctionFailureTestConfig) {
	if testing.Short() {
		t.Skip("Skipping auction failure test in short mode")
	}

	restore := setupCSCache(false)
	defer restore()

	out, _ := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s", string(out))
	exec.Command("nfdc", "strategy", "set", "/ndn/drepo/notify", "/localhost/nfd/strategy/best-route").Run()

	out3, _ := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/heartbeat/v=3", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set heartbeat (auction): %s", string(out3))

	t.Logf("Waiting for routing convergence (%v)...", *routingConvergeWait)
	time.Sleep(*routingConvergeWait)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)
	tmpDir := t.TempDir()

	repos := startAuctionFailureRepos(t, cfg, repoBinary, tmpDir)
	defer stopFailureRepos(repos)

	t.Logf("Waiting for SVS convergence (timeout=%v)...", *svsHealthTimeout)
	if !waitForSVSHealth(t, repos, 1, *svsHealthTimeout) {
		t.Log("WARNING: SVS not healthy - node updates not exchanged between all peers")
	}

	minProducerTime := time.Duration(cfg.commandCount) * 200 * time.Millisecond
	actualProducerTimeout := *producerTimeout
	if actualProducerTimeout < minProducerTime {
		actualProducerTimeout = minProducerTime + 5*time.Second
	}
	t.Logf("Running producer to send %d commands (timeout=%v)...", cfg.commandCount, actualProducerTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), actualProducerTimeout)
	defer cancel()
	producerCmd := exec.CommandContext(ctx, producerBinary, "-type", cfg.commandType, "-count", fmt.Sprintf("%d", cfg.commandCount))
	producerOutput, err := producerCmd.CombinedOutput()
	if err != nil {
		t.Logf("Producer error: %v", err)
	}
	t.Logf("Producer output:\n%s", string(producerOutput))

	t.Logf("Waiting for full replication to RF=%d...", cfg.replicationFactor)
	if !waitForFullReplication(t, repos, cfg.replicationFactor, *replicationTimeout) {
		commands := getCommandsWithClaims(t, repos, nil)
		for target, nodes := range commands {
			t.Logf("  Command %s: %d claims (need %d)", target, len(nodes), cfg.replicationFactor)
		}
		t.Fatalf("Initial replication failed: not all commands reached RF=%d", cfg.replicationFactor)
	}
	t.Logf("Initial replication achieved")

	commandsBeforeFailure := getCommandsWithClaims(t, repos, nil)
	t.Logf("Commands before failure: %d", len(commandsBeforeFailure))
	for target, nodes := range commandsBeforeFailure {
		t.Logf("  %s: claimed by %v", target, nodes)
	}

	nodeClaimCounts := make(map[string]int)
	for _, nodes := range commandsBeforeFailure {
		for _, node := range nodes {
			nodeClaimCounts[node]++
		}
	}

	var reposToKill []*repoProcess
	if cfg.failureCount == 1 {
		maxClaims := 0
		var nodeToKill string
		for node, count := range nodeClaimCounts {
			if count > maxClaims {
				maxClaims = count
				nodeToKill = node
			}
		}
		if nodeToKill != "" {
			for _, r := range repos {
				if r.nodeID == nodeToKill {
					reposToKill = []*repoProcess{r}
					break
				}
			}
		}
	}
	if reposToKill == nil {
		reposToKill = repos[:cfg.failureCount]
	}

	t.Logf("Killing %d repo(s) to simulate failure...", len(reposToKill))
	for _, r := range reposToKill {
		t.Logf("  Killing repo %s", r.nodeID)
		r.cmd.Process.Kill()
		r.cmd.Wait()
	}

	deadNodeIDs := make(map[string]bool)
	for _, r := range reposToKill {
		deadNodeIDs[r.nodeID] = true
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

	recoveryDeadline := time.Now().Add(*auctionFailureRecoveryWait)
	recovered := false
	var recoveryTime time.Duration
	var reactionTime time.Duration

	for time.Now().Before(recoveryDeadline) {
		commandsAfterFailure := getCommandsWithClaims(t, repos, deadNodeIDs)

		allRecovered := true
		for _, nodes := range commandsAfterFailure {
			if len(nodes) < cfg.replicationFactor {
				allRecovered = false
				break
			}
		}

		if allRecovered {
			recoveryTime = time.Since(failureTime)
			reactionTime = recoveryTime - HEARTBEAT_TIMEOUT
			recovered = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	t.Logf("=== AUCTION FAILURE TEST RESULTS ===")
	t.Logf("Node count: %d", cfg.nodeCount)
	t.Logf("Replication factor: %d", cfg.replicationFactor)
	t.Logf("Failures: %d", cfg.failureCount)

	commandsAfterFailure := getCommandsWithClaims(t, repos, deadNodeIDs)

	recoveredCount := 0
	notRecoveredCount := 0
	for target := range affectedCommands {
		currentNodes := commandsAfterFailure[target]
		originalCount := len(affectedCommands[target])
		currentCount := len(currentNodes)

		if currentCount >= cfg.replicationFactor {
			recoveredCount++
			t.Logf("  %s: %d claims - RECOVERED (was %d)", target, currentCount, originalCount)
		} else {
			notRecoveredCount++
			t.Logf("  %s: %d claims (was %d, need %d) - NOT RECOVERED", target, currentCount, originalCount, cfg.replicationFactor)
		}
	}

	if notRecoveredCount > 0 {
		t.Logf("RECOVERY: %d/%d commands recovered in %v", recoveredCount, len(affectedCommands), recoveryTime)
		if !recovered {
			t.Fatalf("Recovery not achieved for %d/%d commands", notRecoveredCount, len(affectedCommands))
		}
	}

	t.Logf("SUCCESS: All %d affected commands fully recovered in %v (reaction time: %v)", recoveredCount, recoveryTime, reactionTime)
}

func startAuctionFailureRepos(t *testing.T, cfg auctionFailureTestConfig, repoBinary string, tmpDir string) []*repoProcess {
	repos := make([]*repoProcess, cfg.nodeCount)

	t.Logf("Starting %d repo instances with Auction distribution...", cfg.nodeCount)
	for i := 0; i < cfg.nodeCount; i++ {
		nodeID := fmt.Sprintf("n%d", i)
		logPath := filepath.Join(tmpDir, "events-"+nodeID+".jsonl")
		nodePrefix := "/ndn/repo/local/" + nodeID

		cmd := exec.Command(repoBinary,
			"--event-log", logPath,
			"--node-prefix", nodePrefix,
			"--signing-identity", "/ndn/repo.teame.dev/repo",
			"--distribution", "auction",
			"--debug",
		)

		stdoutPath := filepath.Join(tmpDir, "stdout-"+nodeID+".log")
		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			t.Fatalf("Failed to create stdout file for %s: %v", nodeID, err)
		}
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

		t.Logf("  Started repo %s with prefix %s (distribution=auction)", nodeID, nodePrefix)

		time.Sleep(500 * time.Millisecond)
	}

	return repos
}
