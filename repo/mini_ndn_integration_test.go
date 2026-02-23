package main

import (
	"encoding/json"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
)

const dockerImage = "mini-ndn-integration"

var (
	flagNodeCount         = flag.Int("node-count", 0, "node count for custom test (0 skips custom test)")
	flagReplicationFactor = flag.Int("replication-factor", 3, "replication factor")
	flagProducerCount     = flag.Int("producer-count", 1, "number of producers")
	flagCommandCount      = flag.Int("command-count", 1, "commands per producer")
	flagCommandRate       = flag.Int("command-rate", 1, "commands per second")
	flagRunFullTopology   = flag.Bool("run-full-topology", false, "run full 24-node topology test")
)

type IntegrationConfig struct {
	NodeCount         int
	ReplicationFactor int
	Timeout           int
	RoutingWait       int
	ProducerCount     int
	CommandCount      int
	CommandRate       int
}

type Metadata struct {
	Nodes                     []string               `json:"nodes"`
	NodeCount                 int                    `json:"node_count"`
	ReplicationFactor         int                    `json:"replication_factor"`
	Timeout                   int                    `json:"timeout"`
	Replicated                bool                   `json:"replicated"`
	ReplicationTimeSecs       *float64               `json:"replication_time_seconds"`
	ReplicationTimeMinMs      *float64               `json:"replication_time_min_ms"`
	ReplicationTimeMaxMs      *float64               `json:"replication_time_max_ms"`
	ReplicationTimeAvgMs      *float64               `json:"replication_time_avg_ms"`
	ReplicationTimeMedianMs   *float64               `json:"replication_time_median_ms"`
	UpdatePropagationMinMs    *float64               `json:"update_propagation_min_ms"`
	UpdatePropagationMaxMs    *float64               `json:"update_propagation_max_ms"`
	UpdatePropagationAvgMs    *float64               `json:"update_propagation_avg_ms"`
	UpdatePropagationMedianMs *float64               `json:"update_propagation_median_ms"`
	JobClaims                 int                    `json:"job_claims"`
	SyncInterests             uint64                 `json:"sync_interests"`
	DataPackets               uint64                 `json:"data_packets"`
	MaxReplication            int                    `json:"max_replication"`
	FinalReplication          int                    `json:"final_replication"`
	OverReplicated            bool                   `json:"over_replicated"`
	UnderReplicated           bool                   `json:"under_replicated"`
	Timeline                  []TimelineEntry        `json:"timeline"`
	TotalCommands             int                    `json:"total_commands"`
	CommandsAtRF              int                    `json:"commands_at_rf"`
	CommandsOver              int                    `json:"commands_over"`
	CommandsUnder             int                    `json:"commands_under"`
	AnyEverOverReplicated     bool                   `json:"any_ever_over_replicated"`
	Commands                  map[string]CommandInfo `json:"commands"`
}

type CommandInfo struct {
	FinalReplication      int  `json:"final_replication"`
	MaxReplication        int  `json:"max_replication"`
	WasEverOverReplicated bool `json:"was_ever_over_replicated"`
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

	nodeCount := *flagNodeCount
	if nodeCount == 0 {
		t.Skip("Skipping custom node count test (set -node-count to enable)")
	}

	replicationFactor := *flagReplicationFactor
	producerCount := *flagProducerCount
	commandCount := *flagCommandCount
	commandRate := *flagCommandRate

	timeout := 60 + (nodeCount-5)*5
	routingWait := 30 + (nodeCount-5)*3

	config := IntegrationConfig{
		NodeCount:         nodeCount,
		ReplicationFactor: replicationFactor,
		Timeout:           timeout,
		RoutingWait:       routingWait,
		ProducerCount:     producerCount,
		CommandCount:      commandCount,
		CommandRate:       commandRate,
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
		"-m", "8g",
		"--cpus", "8",
		"-v", "/lib/modules:/lib/modules",
		"-v", resultsDir+":/results",
		dockerImage,
		"-c", "python3 /usr/local/bin/runner.py --node-count "+strconv.Itoa(config.NodeCount)+
			" --timeout "+strconv.Itoa(config.Timeout)+
			" --replication-factor "+strconv.Itoa(config.ReplicationFactor)+
			" --routing-wait "+strconv.Itoa(config.RoutingWait)+
			" --producer-count "+strconv.Itoa(config.ProducerCount)+
			" --command-count "+strconv.Itoa(config.CommandCount)+
			" --command-rate "+strconv.Itoa(config.CommandRate)+
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
	t.Logf("Nodes: %d, Replication factor: %d, Producers: %d, Commands: %d", config.NodeCount, config.ReplicationFactor, config.ProducerCount, config.CommandCount)
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

	if !*flagRunFullTopology {
		t.Skip("Skipping full topology test (set -run-full-topology to enable)")
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

func getCommandRateFromCalibration(t *testing.T, producers int) int {
	repoDir, err := filepath.Abs("..")
	if err != nil {
		return 1
	}

	calPath := filepath.Join(repoDir, "experiments", "results", "calibration", "latest_calibration.json")
	data, err := os.ReadFile(calPath)
	if err != nil {
		t.Logf("No calibration file found at %s, using default rate=1", calPath)
		return 1
	}

	var cal struct {
		MaxValues struct {
			SafeIntervalMs float64 `json:"safe_interval_ms"`
		} `json:"max_values"`
	}
	if err := json.Unmarshal(data, &cal); err != nil {
		t.Logf("Failed to parse calibration file: %v, using default rate=1", err)
		return 1
	}

	if cal.MaxValues.SafeIntervalMs <= 0 {
		return 1
	}

	totalRate := int(1000.0 / (cal.MaxValues.SafeIntervalMs * 1.25))
	perProducer := totalRate / producers
	if perProducer < 1 {
		perProducer = 1
	}
	t.Logf("Calibrated rate: %d cmd/sec total, %d per producer (safe_interval=%.2fms)",
		totalRate, perProducer, cal.MaxValues.SafeIntervalMs)
	return perProducer
}

type CalibrationData struct {
	SvsConvergenceMs    int
	ReplicationTimeMs   float64
	UpdatePropagationMs int
	SafeIntervalMs      float64
}

func getCalibrationData(t *testing.T) CalibrationData {
	repoDir, err := filepath.Abs("..")
	if err != nil {
		t.Logf("Failed to get repo directory, using defaults")
		return CalibrationData{SvsConvergenceMs: 5000, ReplicationTimeMs: 100, UpdatePropagationMs: 100, SafeIntervalMs: 200}
	}

	calPath := filepath.Join(repoDir, "experiments", "results", "calibration", "latest_calibration.json")
	data, err := os.ReadFile(calPath)
	if err != nil {
		t.Logf("No calibration file found at %s, using defaults", calPath)
		return CalibrationData{SvsConvergenceMs: 5000, ReplicationTimeMs: 100, UpdatePropagationMs: 100, SafeIntervalMs: 200}
	}

	var cal struct {
		MaxValues struct {
			SvsConvergenceMs    int     `json:"svs_convergence_ms"`
			ReplicationTimeMs   float64 `json:"replication_time_ms"`
			UpdatePropagationMs int     `json:"update_propagation_ms"`
			SafeIntervalMs      float64 `json:"safe_interval_ms"`
		} `json:"max_values"`
	}
	if err := json.Unmarshal(data, &cal); err != nil {
		t.Logf("Failed to parse calibration file: %v, using defaults", err)
		return CalibrationData{SvsConvergenceMs: 5000, ReplicationTimeMs: 100, UpdatePropagationMs: 100, SafeIntervalMs: 200}
	}

	t.Logf("Calibration data: svs_convergence=%dms, replication=%.2fms, propagation=%dms, safe_interval=%.2fms",
		cal.MaxValues.SvsConvergenceMs, cal.MaxValues.ReplicationTimeMs,
		cal.MaxValues.UpdatePropagationMs, cal.MaxValues.SafeIntervalMs)

	return CalibrationData{
		SvsConvergenceMs:    cal.MaxValues.SvsConvergenceMs,
		ReplicationTimeMs:   cal.MaxValues.ReplicationTimeMs,
		UpdatePropagationMs: cal.MaxValues.UpdatePropagationMs,
		SafeIntervalMs:      cal.MaxValues.SafeIntervalMs,
	}
}

func calculateTimeouts(cal CalibrationData, producers, commandsPerProducer, replicationFactor int) (timeout, routingWait int) {
	totalRate := int(1000.0 / (cal.SafeIntervalMs * 1.25))
	if totalRate < 1 {
		totalRate = 1
	}
	perProducerRate := totalRate / producers
	if perProducerRate < 1 {
		perProducerRate = 1
	}

	producerTime := float64(commandsPerProducer) / float64(perProducerRate)
	svsTime := float64(cal.SvsConvergenceMs) / 1000.0
	repBuffer := 60.0

	totalCommands := producers * commandsPerProducer
	replicationTime := float64(totalCommands*replicationFactor) * (cal.ReplicationTimeMs / 1000.0) / float64(producers)
	if replicationTime < 30 {
		replicationTime = 30
	}

	timeout = int(svsTime + producerTime + replicationTime + repBuffer)
	if timeout < 120 {
		timeout = 120
	}

	routingWait = 30 + (24-5)*2
	if routingWait < 60 {
		routingWait = 60
	}

	return timeout, routingWait
}

func logPerCommandReplication(t *testing.T, metadata Metadata, expectedCommands int) {
	t.Log("=== PER-COMMAND REPLICATION STATUS ===")
	i := 1
	for target, cmd := range metadata.Commands {
		t.Logf("Command %d: %s - final=%d, max=%d, over=%v",
			i, target, cmd.FinalReplication, cmd.MaxReplication, cmd.WasEverOverReplicated)
		i++
	}
	t.Log("=== SUMMARY ===")
	t.Logf("Commands at rf=%d: %d/%d", metadata.ReplicationFactor, metadata.CommandsAtRF, metadata.TotalCommands)
	t.Logf("Commands under-replicated: %d", metadata.CommandsUnder)
	t.Logf("Commands over-replicated: %d", metadata.CommandsOver)
}

func validateMultiCommandReplication(t *testing.T, config IntegrationConfig, metadata Metadata) {
	expectedCommands := config.ProducerCount * config.CommandCount

	logPerCommandReplication(t, metadata, expectedCommands)

	var failed bool
	if metadata.TotalCommands != expectedCommands {
		t.Errorf("Expected %d commands, got %d", expectedCommands, metadata.TotalCommands)
		failed = true
	}
	if metadata.CommandsAtRF != expectedCommands {
		t.Errorf("FAIL: Only %d/%d commands replicated to rf=%d",
			metadata.CommandsAtRF, expectedCommands, config.ReplicationFactor)
		failed = true
	}
	if metadata.CommandsUnder > 0 {
		t.Errorf("FAIL: %d commands under-replicated", metadata.CommandsUnder)
		failed = true
	}
	if metadata.CommandsOver > 0 {
		t.Errorf("FAIL: %d commands over-replicated", metadata.CommandsOver)
		failed = true
	}

	if !failed {
		t.Logf("PASS: All %d commands replicated correctly to rf=%d", expectedCommands, config.ReplicationFactor)
	}
}

func TestMiniNDNIntegration_TwoProducers_SingleCommand(t *testing.T) {
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
		ProducerCount:     2,
		CommandCount:      1,
		CommandRate:       getCommandRateFromCalibration(t, 2),
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

	t.Logf("Running integration test: 2 producers, 1 command each...")
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
			" --producer-count "+strconv.Itoa(config.ProducerCount)+
			" --command-count "+strconv.Itoa(config.CommandCount)+
			" --command-rate "+strconv.Itoa(config.CommandRate)+
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
	t.Log("=== TEST: Two Producers, Single Command ===")
	t.Logf("Producers: %d, Commands per producer: %d", config.ProducerCount, config.CommandCount)
	t.Logf("Replication factor: %d", config.ReplicationFactor)

	validateMultiCommandReplication(t, config, metadata)
}

func TestMiniNDNIntegration_TwoProducers_MultiCommand(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker not available, skipping integration test")
	}

	config := IntegrationConfig{
		NodeCount:         5,
		ReplicationFactor: 3,
		Timeout:           90,
		RoutingWait:       30,
		ProducerCount:     2,
		CommandCount:      5,
		CommandRate:       getCommandRateFromCalibration(t, 2),
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

	t.Logf("Running integration test: 2 producers, 5 commands each...")
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
			" --producer-count "+strconv.Itoa(config.ProducerCount)+
			" --command-count "+strconv.Itoa(config.CommandCount)+
			" --command-rate "+strconv.Itoa(config.CommandRate)+
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
	t.Log("=== TEST: Two Producers, Multi-Command ===")
	t.Logf("Producers: %d, Commands per producer: %d", config.ProducerCount, config.CommandCount)
	t.Logf("Replication factor: %d", config.ReplicationFactor)

	validateMultiCommandReplication(t, config, metadata)
}

func run100CommandTest(t *testing.T, producerCount, commandsPerProducer int, testName string) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker not available, skipping integration test")
	}

	cal := getCalibrationData(t)
	commandRate := getCommandRateFromCalibration(t, producerCount)
	timeout, routingWait := calculateTimeouts(cal, producerCount, commandsPerProducer, 3)

	config := IntegrationConfig{
		NodeCount:         24,
		ReplicationFactor: 3,
		Timeout:           timeout,
		RoutingWait:       routingWait,
		ProducerCount:     producerCount,
		CommandCount:      commandsPerProducer,
		CommandRate:       commandRate,
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

	t.Logf("Running 100-command test: %d producers × %d commands = 100 total", producerCount, commandsPerProducer)
	t.Logf("Timeout: %ds, Routing wait: %ds, Command rate: %d/sec per producer", timeout, routingWait, commandRate)
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
			" --producer-count "+strconv.Itoa(config.ProducerCount)+
			" --command-count "+strconv.Itoa(config.CommandCount)+
			" --command-rate "+strconv.Itoa(config.CommandRate)+
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
	t.Logf("=== TEST: %s ===", testName)
	t.Logf("Producers: %d, Commands per producer: %d (Total: 100)", config.ProducerCount, config.CommandCount)
	t.Logf("Replication factor: %d", config.ReplicationFactor)

	validateMultiCommandReplication(t, config, metadata)
}

func TestMiniNDNIntegration_100Commands_2Producers(t *testing.T) {
	run100CommandTest(t, 2, 50, "100 Commands - 2 Producers × 50")
}

func TestMiniNDNIntegration_100Commands_5Producers(t *testing.T) {
	run100CommandTest(t, 5, 20, "100 Commands - 5 Producers × 20")
}

func TestMiniNDNIntegration_100Commands_10Producers(t *testing.T) {
	run100CommandTest(t, 10, 10, "100 Commands - 10 Producers × 10")
}

func TestMiniNDNIntegration_100Commands_20Producers(t *testing.T) {
	run100CommandTest(t, 20, 5, "100 Commands - 20 Producers × 5")
}
