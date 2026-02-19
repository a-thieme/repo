package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
)

const dockerImage = "mini-ndn-integration"

type IntegrationConfig struct {
	NodeCount         int
	ReplicationFactor int
	Timeout           int
	RoutingWait       int
}

type Metadata struct {
	Nodes               []string        `json:"nodes"`
	NodeCount           int             `json:"node_count"`
	ReplicationFactor   int             `json:"replication_factor"`
	Timeout             int             `json:"timeout"`
	Replicated          bool            `json:"replicated"`
	ReplicationTimeSecs *float64        `json:"replication_time_seconds"`
	JobClaims           int             `json:"job_claims"`
	SyncInterests       uint64          `json:"sync_interests"`
	DataPackets         uint64          `json:"data_packets"`
	MaxReplication      int             `json:"max_replication"`
	FinalReplication    int             `json:"final_replication"`
	OverReplicated      bool            `json:"over_replicated"`
	UnderReplicated     bool            `json:"under_replicated"`
	Timeline            []TimelineEntry `json:"timeline"`
}

type TimelineEntry struct {
	Ts     string `json:"ts"`
	Action string `json:"action"`
	Node   string `json:"node"`
	Target string `json:"target"`
	Count  int    `json:"count"`
}

type TestResults struct {
	Replicated       bool    `json:"replicated"`
	ReplicationTime  float64 `json:"replication_time_seconds"`
	SyncInterests    uint64  `json:"sync_interests"`
	DataPackets      uint64  `json:"data_packets"`
	NodesWithJob     int     `json:"nodes_with_job"`
	ExpectedReplicas int     `json:"expected_replicas"`
	CommandTarget    string  `json:"command_target"`
	MaxReplication   int     `json:"max_replication"`
	OverReplicated   bool    `json:"over_replicated"`
	UnderReplicated  bool    `json:"under_replicated"`
}

func TestMiniNDNIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker not available, skipping integration test")
	}

	config := IntegrationConfig{
		NodeCount:         5,
		ReplicationFactor: 3,
		Timeout:           60,
		RoutingWait:       30,
	}

	repoDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("Failed to get repo directory: %v", err)
	}

	t.Log("Building Go binaries...")
	binDir := filepath.Join(repoDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("Failed to create bin directory: %v", err)
	}

	repoBuild := exec.Command("go", "build", "-o", filepath.Join(binDir, "repo"), ".")
	repoBuild.Dir = filepath.Join(repoDir, "repo")
	if output, err := repoBuild.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build repo: %v\n%s", err, output)
	}

	producerBuild := exec.Command("go", "build", "-o", filepath.Join(binDir, "producer"), ".")
	producerBuild.Dir = filepath.Join(repoDir, "producer")
	if output, err := producerBuild.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build producer: %v\n%s", err, output)
	}
	t.Log("Binaries built successfully")

	t.Log("Copying NDN keys...")
	keysDir := filepath.Join(repoDir, "keys")
	if err := os.RemoveAll(keysDir); err != nil {
		t.Logf("Warning: Failed to remove old keys dir: %v", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}
	srcKeys := filepath.Join(homeDir, ".ndn", "keys")
	if _, err := os.Stat(srcKeys); os.IsNotExist(err) {
		t.Skipf("NDN keys not found at %s, skipping integration test", srcKeys)
	}
	copyDir := exec.Command("cp", "-r", srcKeys, keysDir)
	if output, err := copyDir.CombinedOutput(); err != nil {
		t.Fatalf("Failed to copy keys: %v\n%s", err, output)
	}
	t.Log("Keys copied successfully")

	t.Log("Building Docker image...")
	buildCmd := exec.Command("docker", "build", "-t", dockerImage,
		"-f", "experiments/Dockerfile.integration", ".")
	buildCmd.Dir = repoDir
	output, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build Docker image: %v\n%s", err, output)
	}
	t.Log("Docker image built successfully")

	resultsDir := t.TempDir()
	t.Logf("Results directory: %s", resultsDir)

	t.Log("Running integration test in Docker...")
	runCmd := exec.Command("docker", "run",
		"--rm",
		"--privileged",
		"-m", "4g",
		"--cpus", "4",
		"-v", "/lib/modules:/lib/modules",
		"-v", resultsDir+":/results",
		dockerImage,
		"-c", "python3 /usr/local/bin/runner.py --node-count "+strconv.Itoa(config.NodeCount)+
			" --timeout "+strconv.Itoa(config.Timeout)+
			" --replication-factor "+strconv.Itoa(config.ReplicationFactor)+
			" --routing-wait "+strconv.Itoa(config.RoutingWait)+
			" --results-dir /results",
	)
	runCmd.Dir = repoDir
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr

	startTime := time.Now()
	err = runCmd.Run()
	elapsed := time.Since(startTime)
	t.Logf("Docker container completed in %v", elapsed)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 2 {
				t.Log("Container reported replication failure (exit code 2)")
			} else {
				t.Logf("Container exited with code %d", exitErr.ExitCode())
			}
		} else {
			t.Fatalf("Docker container failed: %v", err)
		}
	}

	metadataPath := filepath.Join(resultsDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("Failed to read metadata.json: %v", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("Failed to parse metadata.json: %v", err)
	}

	t.Log("")
	t.Log("=== EVALUATION RESULTS ===")
	t.Logf("0) Command correctly replicated: %v (expected %d, got %d nodes)",
		metadata.Replicated, config.ReplicationFactor, metadata.JobClaims)

	if metadata.ReplicationTimeSecs != nil {
		t.Logf("1) Replication time: %.2fs", *metadata.ReplicationTimeSecs)
	} else {
		t.Log("1) Replication time: N/A (not replicated)")
	}

	t.Logf("2) Sync interests sent: %d", metadata.SyncInterests)
	t.Logf("3) Data packets sent: %d", metadata.DataPackets)
	t.Logf("4) Max replication reached: %d", metadata.MaxReplication)
	t.Logf("5) Over-replicated: %v", metadata.OverReplicated)
	t.Logf("6) Under-replicated: %v", metadata.UnderReplicated)

	t.Log("")
	t.Log("=== DETAILED EVENT ANALYSIS ===")

	allEvents := make([]testutil.Event, 0)
	for _, nodeName := range metadata.Nodes {
		logPath := filepath.Join(resultsDir, "events-"+nodeName+".jsonl")
		events, err := testutil.ParseEventLog(logPath)
		if err != nil {
			t.Logf("Warning: Could not parse log for node %s: %v", nodeName, err)
			continue
		}
		allEvents = append(allEvents, events...)

		cmdReceived := testutil.FilterEvents(events, testutil.EventCommandReceived)
		jobClaimed := testutil.FilterEvents(events, testutil.EventJobClaimed)
		syncSent := testutil.FilterEvents(events, testutil.EventSyncInterestSent)
		dataSent := testutil.FilterEvents(events, testutil.EventDataSent)
		repChecks := testutil.FilterEvents(events, testutil.EventReplicationCheck)
		nodeUpdates := testutil.FilterEvents(events, testutil.EventNodeUpdate)

		t.Logf("  %s: commands=%d, claims=%d, sync=%d, data=%d, repChecks=%d, nodeUpdates=%d",
			nodeName, len(cmdReceived), len(jobClaimed), len(syncSent), len(dataSent), len(repChecks), len(nodeUpdates))
		for _, rc := range repChecks {
			t.Logf("    repCheck: target=%s shouldClaim=%v reason=%s", rc.Target, rc.ShouldClaim, rc.Reason)
		}
	}

	results := TestResults{
		Replicated:       metadata.Replicated,
		ExpectedReplicas: config.ReplicationFactor,
		NodesWithJob:     metadata.JobClaims,
		SyncInterests:    metadata.SyncInterests,
		DataPackets:      metadata.DataPackets,
		MaxReplication:   metadata.MaxReplication,
		OverReplicated:   metadata.OverReplicated,
		UnderReplicated:  metadata.UnderReplicated,
	}

	if metadata.ReplicationTimeSecs != nil {
		results.ReplicationTime = *metadata.ReplicationTimeSecs
	}

	cmdEvents := testutil.FilterEvents(allEvents, testutil.EventCommandReceived)
	if len(cmdEvents) > 0 {
		results.CommandTarget = cmdEvents[0].Target
	}

	resultsPath := filepath.Join(resultsDir, "test_results.json")
	resultsJSON, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(resultsPath, resultsJSON, 0644); err != nil {
		t.Logf("Warning: Failed to write test_results.json: %v", err)
	} else {
		t.Logf("Results written to: %s", resultsPath)
	}

	t.Log("")
	if !metadata.Replicated {
		t.Errorf("FAIL: Command was not replicated to %d nodes (only %d)",
			config.ReplicationFactor, metadata.JobClaims)
	} else {
		t.Log("PASS: Command successfully replicated")
	}
}

func TestMiniNDNIntegration_CustomNodeCount(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker not available, skipping integration test")
	}

	nodeCountStr := os.Getenv("NODE_COUNT")
	if nodeCountStr == "" {
		t.Skip("Skipping custom node count test (set NODE_COUNT to enable)")
	}
	nodeCount, err := strconv.Atoi(nodeCountStr)
	if err != nil {
		t.Fatalf("Invalid NODE_COUNT: %v", err)
	}

	replicationFactorStr := os.Getenv("REPLICATION_FACTOR")
	replicationFactor := 3
	if replicationFactorStr != "" {
		if rf, err := strconv.Atoi(replicationFactorStr); err == nil {
			replicationFactor = rf
		}
	}

	timeout := 60 + (nodeCount-5)*5
	routingWait := 30 + (nodeCount-5)*3

	config := IntegrationConfig{
		NodeCount:         nodeCount,
		ReplicationFactor: replicationFactor,
		Timeout:           timeout,
		RoutingWait:       routingWait,
	}

	repoDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("Failed to get repo directory: %v", err)
	}

	t.Log("Building Go binaries...")
	binDir := filepath.Join(repoDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("Failed to create bin directory: %v", err)
	}

	repoBuild := exec.Command("go", "build", "-o", filepath.Join(binDir, "repo"), ".")
	repoBuild.Dir = filepath.Join(repoDir, "repo")
	if output, err := repoBuild.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build repo: %v\n%s", err, output)
	}

	producerBuild := exec.Command("go", "build", "-o", filepath.Join(binDir, "producer"), ".")
	producerBuild.Dir = filepath.Join(repoDir, "producer")
	if output, err := producerBuild.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build producer: %v\n%s", err, output)
	}
	t.Log("Binaries built successfully")

	t.Log("Copying NDN keys...")
	keysDir := filepath.Join(repoDir, "keys")
	if err := os.RemoveAll(keysDir); err != nil {
		t.Logf("Warning: Failed to remove old keys dir: %v", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}
	srcKeys := filepath.Join(homeDir, ".ndn", "keys")
	if _, err := os.Stat(srcKeys); os.IsNotExist(err) {
		t.Skipf("NDN keys not found at %s, skipping integration test", srcKeys)
	}
	copyDir := exec.Command("cp", "-r", srcKeys, keysDir)
	if output, err := copyDir.CombinedOutput(); err != nil {
		t.Fatalf("Failed to copy keys: %v\n%s", err, output)
	}
	t.Log("Keys copied successfully")

	t.Log("Building Docker image...")
	buildCmd := exec.Command("docker", "build", "-t", dockerImage,
		"-f", "experiments/Dockerfile.integration", ".")
	buildCmd.Dir = repoDir
	output, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build Docker image: %v\n%s", err, output)
	}
	t.Log("Docker image built successfully")

	resultsDir := t.TempDir()
	t.Logf("Results directory: %s", resultsDir)

	t.Logf("Running integration test in Docker (%d nodes)...", nodeCount)
	runCmd := exec.Command("docker", "run",
		"--rm",
		"--privileged",
		"-m", "4g",
		"--cpus", "4",
		"-v", "/lib/modules:/lib/modules",
		"-v", resultsDir+":/results",
		dockerImage,
		"-c", "python3 /usr/local/bin/runner.py --node-count "+strconv.Itoa(config.NodeCount)+
			" --timeout "+strconv.Itoa(config.Timeout)+
			" --replication-factor "+strconv.Itoa(config.ReplicationFactor)+
			" --routing-wait "+strconv.Itoa(config.RoutingWait)+
			" --results-dir /results",
	)
	runCmd.Dir = repoDir
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr

	startTime := time.Now()
	err = runCmd.Run()
	elapsed := time.Since(startTime)
	t.Logf("Docker container completed in %v", elapsed)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 2 {
				t.Log("Container reported replication failure (exit code 2)")
			} else {
				t.Logf("Container exited with code %d", exitErr.ExitCode())
			}
		} else {
			t.Fatalf("Docker container failed: %v", err)
		}
	}

	metadataPath := filepath.Join(resultsDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("Failed to read metadata.json: %v", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("Failed to parse metadata.json: %v", err)
	}

	t.Log("")
	t.Log("=== EVALUATION RESULTS ===")
	t.Logf("Nodes: %d, Replication factor: %d", config.NodeCount, config.ReplicationFactor)
	t.Logf("0) Command correctly replicated: %v (expected %d, got %d nodes)",
		metadata.Replicated, config.ReplicationFactor, metadata.JobClaims)

	if metadata.ReplicationTimeSecs != nil {
		t.Logf("1) Replication time: %.2fs", *metadata.ReplicationTimeSecs)
	} else {
		t.Log("1) Replication time: N/A (not replicated)")
	}

	t.Logf("2) Sync interests sent: %d", metadata.SyncInterests)
	t.Logf("3) Data packets sent: %d", metadata.DataPackets)
	t.Logf("4) Max replication reached: %d", metadata.MaxReplication)
	t.Logf("5) Over-replicated: %v", metadata.OverReplicated)
	t.Logf("6) Under-replicated: %v", metadata.UnderReplicated)

	results := TestResults{
		Replicated:       metadata.Replicated,
		ExpectedReplicas: config.ReplicationFactor,
		NodesWithJob:     metadata.JobClaims,
		SyncInterests:    metadata.SyncInterests,
		DataPackets:      metadata.DataPackets,
		MaxReplication:   metadata.MaxReplication,
		OverReplicated:   metadata.OverReplicated,
		UnderReplicated:  metadata.UnderReplicated,
	}

	if metadata.ReplicationTimeSecs != nil {
		results.ReplicationTime = *metadata.ReplicationTimeSecs
	}

	resultsPath := filepath.Join(resultsDir, "test_results.json")
	resultsJSON, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(resultsPath, resultsJSON, 0644); err != nil {
		t.Logf("Warning: Failed to write test_results.json: %v", err)
	} else {
		t.Logf("Results written to: %s", resultsPath)
	}

	t.Log("")
	if !metadata.Replicated {
		t.Errorf("FAIL: Command was not replicated to %d nodes (only %d)",
			config.ReplicationFactor, metadata.JobClaims)
	} else if metadata.OverReplicated {
		t.Logf("WARN: Command was over-replicated (max %d nodes)", metadata.MaxReplication)
	} else {
		t.Log("PASS: Command successfully replicated")
	}
}

func TestMiniNDNIntegration_FullTopology(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping full topology test in short mode")
	}

	if os.Getenv("RUN_FULL_TOPOLOGY") == "" {
		t.Skip("Skipping full topology test (set RUN_FULL_TOPOLOGY=1 to enable)")
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker not available, skipping integration test")
	}

	config := IntegrationConfig{
		NodeCount:         24,
		ReplicationFactor: 3,
		Timeout:           180,
		RoutingWait:       90,
	}

	repoDir, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("Failed to get repo directory: %v", err)
	}

	t.Log("Building Go binaries...")
	binDir := filepath.Join(repoDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("Failed to create bin directory: %v", err)
	}

	repoBuild := exec.Command("go", "build", "-o", filepath.Join(binDir, "repo"), ".")
	repoBuild.Dir = filepath.Join(repoDir, "repo")
	if output, err := repoBuild.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build repo: %v\n%s", err, output)
	}

	producerBuild := exec.Command("go", "build", "-o", filepath.Join(binDir, "producer"), ".")
	producerBuild.Dir = filepath.Join(repoDir, "producer")
	if output, err := producerBuild.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build producer: %v\n%s", err, output)
	}
	t.Log("Binaries built successfully")

	t.Log("Copying NDN keys...")
	keysDir := filepath.Join(repoDir, "keys")
	if err := os.RemoveAll(keysDir); err != nil {
		t.Logf("Warning: Failed to remove old keys dir: %v", err)
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("Failed to get home directory: %v", err)
	}
	srcKeys := filepath.Join(homeDir, ".ndn", "keys")
	if _, err := os.Stat(srcKeys); os.IsNotExist(err) {
		t.Skipf("NDN keys not found at %s, skipping integration test", srcKeys)
	}
	copyDir := exec.Command("cp", "-r", srcKeys, keysDir)
	if output, err := copyDir.CombinedOutput(); err != nil {
		t.Fatalf("Failed to copy keys: %v\n%s", err, output)
	}
	t.Log("Keys copied successfully")

	t.Log("Building Docker image...")
	buildCmd := exec.Command("docker", "build", "-t", dockerImage,
		"-f", "experiments/Dockerfile.integration", ".")
	buildCmd.Dir = repoDir
	output, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build Docker image: %v\n%s", err, output)
	}

	resultsDir := t.TempDir()

	t.Log("Running FULL topology integration test (24 nodes)...")
	runCmd := exec.Command("docker", "run",
		"--rm",
		"--privileged",
		"-m", "8g",
		"--cpus", "8",
		"-v", "/lib/modules:/lib/modules",
		"-v", resultsDir+":/results",
		dockerImage,
		"-c", "python3 /usr/local/bin/runner.py --node-count "+strconv.Itoa(config.NodeCount)+
			" --timeout "+strconv.Itoa(config.Timeout)+
			" --replication-factor "+strconv.Itoa(config.ReplicationFactor)+
			" --routing-wait "+strconv.Itoa(config.RoutingWait)+
			" --results-dir /results",
	)
	runCmd.Dir = repoDir
	runCmd.Stdout = os.Stdout
	runCmd.Stderr = os.Stderr

	startTime := time.Now()
	err = runCmd.Run()
	elapsed := time.Since(startTime)
	t.Logf("Docker container completed in %v", elapsed)

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			t.Logf("Container exited with code %d", exitErr.ExitCode())
		} else {
			t.Fatalf("Docker container failed: %v", err)
		}
	}

	metadataPath := filepath.Join(resultsDir, "metadata.json")
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("Failed to read metadata.json: %v", err)
	}

	var metadata Metadata
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("Failed to parse metadata.json: %v", err)
	}

	t.Log("")
	t.Log("=== FULL TOPOLOGY RESULTS ===")
	t.Logf("Nodes: %d", metadata.NodeCount)
	t.Logf("Replicated: %v (%d/%d nodes)",
		metadata.Replicated, metadata.JobClaims, config.ReplicationFactor)
	if metadata.ReplicationTimeSecs != nil {
		t.Logf("Replication time: %.2fs", *metadata.ReplicationTimeSecs)
	}
	t.Logf("Sync interests: %d", metadata.SyncInterests)
	t.Logf("Data packets: %d", metadata.DataPackets)
	t.Logf("Max replication: %d", metadata.MaxReplication)
	t.Logf("Over-replicated: %v", metadata.OverReplicated)
	t.Logf("Under-replicated: %v", metadata.UnderReplicated)

	if !metadata.Replicated {
		t.Errorf("FAIL: Full topology replication failed")
	} else {
		t.Log("PASS: Full topology test passed")
	}
}
