package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
)

const timingDockerImage = "mini-ndn-integration"

var (
	timingIterations     = flag.Int("timing-iterations", 5, "number of iterations for timing tests")
	timingNodes          = flag.Int("timing-nodes", 24, "node count for timing tests")
	timingReplicationFac = flag.Int("timing-rf", 3, "replication factor for timing tests")
	timingOutputJson     = flag.Bool("timing-json", true, "output JSON to stdout")
	timingEnable         = flag.Bool("timing-enable", false, "enable timing tests (requires Docker/mini-ndn)")
)

type TimingConfig struct {
	Iterations     int `json:"iterations"`
	NodeCount      int `json:"node_count"`
	ReplicationFac int `json:"replication_factor"`
}

type TimingMeasurements struct {
	SVSConvergenceMs    []int `json:"svs_convergence_ms"`
	ReplicationTimeMs   []int `json:"replication_time_ms"`
	UpdatePropagationMs []int `json:"update_propagation_ms"`
	SafeIntervalMs      []int `json:"safe_interval_ms"`
}

type TimingStatistics struct {
	SVSConvergenceMaxMs    int `json:"svs_convergence_max_ms"`
	ReplicationTimeMaxMs   int `json:"replication_time_max_ms"`
	UpdatePropagationMaxMs int `json:"update_propagation_max_ms"`
	SafeIntervalMaxMs      int `json:"safe_interval_max_ms"`
}

type TimingRecommended struct {
	SVSHealthMs   int `json:"svs_health_ms"`
	ProducerCmdMs int `json:"producer_cmd_ms"`
	ReplicationMs int `json:"replication_ms"`
}

type TimingResults struct {
	Config       TimingConfig       `json:"config"`
	Measurements TimingMeasurements `json:"measurements"`
	Statistics   TimingStatistics   `json:"statistics"`
	Recommended  TimingRecommended  `json:"recommended_timeouts"`
}

func TestConfiguration_Timing(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping timing test in short mode")
	}

	flag.Parse()

	if !*timingEnable {
		t.Skip("Skipping timing test (use -args -timing-enable=true to run; requires Docker/mini-ndn)")
	}

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker not available, skipping timing test")
	}

	cfg := TimingConfig{
		Iterations:     *timingIterations,
		NodeCount:      *timingNodes,
		ReplicationFac: *timingReplicationFac,
	}

	t.Logf("=== TIMING CONFIGURATION TEST ===")
	t.Logf("Iterations: %d, Nodes: %d, RF: %d", cfg.Iterations, cfg.NodeCount, cfg.ReplicationFac)

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
		t.Skipf("NDN keys not found at %s, skipping timing test", srcKeys)
	}
	copyDir := exec.Command("cp", "-r", srcKeys, keysDir)
	if output, err := copyDir.CombinedOutput(); err != nil {
		t.Fatalf("Failed to copy keys: %v\n%s", err, output)
	}
	t.Log("Keys copied successfully")

	t.Log("Building Docker image...")
	buildCmd := exec.Command("docker", "build", "-t", timingDockerImage,
		"-f", "experiments/Dockerfile.integration", ".")
	buildCmd.Dir = repoDir
	output, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Failed to build Docker image: %v\n%s", err, output)
	}
	t.Log("Docker image built successfully")

	results := TimingResults{
		Config: cfg,
		Measurements: TimingMeasurements{
			SVSConvergenceMs:    make([]int, 0, cfg.Iterations),
			ReplicationTimeMs:   make([]int, 0, cfg.Iterations),
			UpdatePropagationMs: make([]int, 0, cfg.Iterations),
			SafeIntervalMs:      make([]int, 0, cfg.Iterations),
		},
	}

	for i := 0; i < cfg.Iterations; i++ {
		t.Logf("--- Iteration %d/%d ---", i+1, cfg.Iterations)

		resultsDir := t.TempDir()

		t.Log("Running mini-ndn in Docker...")
		runCmd := exec.Command("docker", "run",
			"--rm",
			"--privileged",
			"-m", "8g",
			"--cpus", "8",
			"-v", "/lib/modules:/lib/modules",
			"-v", resultsDir+":/results",
			timingDockerImage,
			"-c", "python3 /usr/local/bin/runner.py --node-count "+strconv.Itoa(cfg.NodeCount)+
				" --timeout 60"+
				" --replication-factor "+strconv.Itoa(cfg.ReplicationFac)+
				" --routing-wait 30"+
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
			t.Logf("Warning: Container error: %v", err)
		}

		metadataPath := filepath.Join(resultsDir, "metadata.json")
		metadataBytes, err := os.ReadFile(metadataPath)
		if err != nil {
			t.Logf("Warning: Could not read metadata.json: %v", err)
			continue
		}

		var metadata timingMetadata
		if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
			t.Logf("Warning: Could not parse metadata.json: %v", err)
			continue
		}

		svsTime := measureSVSConvergenceFromLogs(resultsDir, cfg.NodeCount)
		results.Measurements.SVSConvergenceMs = append(results.Measurements.SVSConvergenceMs, svsTime)
		t.Logf("  SVS convergence: %dms", svsTime)

		if metadata.ReplicationTimeMaxMs != nil {
			repTimeMs := int(*metadata.ReplicationTimeMaxMs)
			results.Measurements.ReplicationTimeMs = append(results.Measurements.ReplicationTimeMs, repTimeMs)
			t.Logf("  Replication time: %dms", repTimeMs)
		}

		propTime := 0
		if metadata.UpdatePropagationMaxMs != nil {
			propTime = int(*metadata.UpdatePropagationMaxMs)
		}
		if propTime == 0 {
			propTime = measureUpdatePropagationFromLogs(resultsDir, cfg.NodeCount)
		}
		results.Measurements.UpdatePropagationMs = append(results.Measurements.UpdatePropagationMs, propTime)
		t.Logf("  Update propagation: %dms", propTime)

		safeInterval := 0
		if metadata.ReplicationTimeMaxMs != nil && propTime > 0 {
			safeInterval = int(*metadata.ReplicationTimeMaxMs) + propTime
		} else {
			safeInterval = 1000
		}
		results.Measurements.SafeIntervalMs = append(results.Measurements.SafeIntervalMs, safeInterval)
		t.Logf("  Safe interval: %dms", safeInterval)

		_ = elapsed
	}

	results.Statistics = calculateTimingStatistics(&results.Measurements)
	results.Recommended = calculateTimingRecommended(&results.Statistics)

	t.Logf("\n=== TIMING RESULTS ===")
	t.Logf("SVS Convergence: samples=%v, max=%dms, recommended=%dms",
		results.Measurements.SVSConvergenceMs, results.Statistics.SVSConvergenceMaxMs, results.Recommended.SVSHealthMs)
	t.Logf("Replication: samples=%v, max=%dms, recommended=%dms",
		results.Measurements.ReplicationTimeMs, results.Statistics.ReplicationTimeMaxMs, results.Recommended.ReplicationMs)
	t.Logf("Update Propagation: samples=%v, max=%dms",
		results.Measurements.UpdatePropagationMs, results.Statistics.UpdatePropagationMaxMs)
	t.Logf("Safe Interval: samples=%v, max=%dms",
		results.Measurements.SafeIntervalMs, results.Statistics.SafeIntervalMaxMs)

	t.Logf("\nRecommended timeout values (max * 1.5):")
	t.Logf("  --svs-timeout=%dms", results.Recommended.SVSHealthMs)
	t.Logf("  --producer-timeout=%dms", results.Recommended.ProducerCmdMs)
	t.Logf("  --replication-timeout=%dms", results.Recommended.ReplicationMs)

	if *timingOutputJson {
		jsonOut, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(jsonOut))
	}
}

type timingMetadata struct {
	ReplicationTimeMaxMs   *float64 `json:"replication_time_max_ms"`
	ReplicationTimeMinMs   *float64 `json:"replication_time_min_ms"`
	ReplicationTimeAvgMs   *float64 `json:"replication_time_avg_ms"`
	ReplicationTimeMedMs   *float64 `json:"replication_time_median_ms"`
	ReplicationTimeP95Ms   *float64 `json:"replication_time_p95_ms"`
	ReplicationTimeP99Ms   *float64 `json:"replication_time_p99_ms"`
	UpdatePropagationMaxMs *float64 `json:"update_propagation_max_ms"`
}

func measureSVSConvergenceFromLogs(resultsDir string, nodeCount int) int {
	maxSVS := 0

	for _, nodeName := range []string{"UCLA", "NEU", "SAVI", "OSAKA", "AFA", "ANYANG", "TNO", "MEMPHIS",
		"QUB", "URJC", "WASEDA", "UFBA", "AVEIRO", "MML2", "MML1", "ARIZONA",
		"IIITH", "SINGAPORE", "FRANKFURT", "SRRU", "DELFT", "WU", "BERN", "MINHO"} {
		if nodeCount == 0 {
			break
		}
		logPath := filepath.Join(resultsDir, "events-"+nodeName+".jsonl")
		events, err := testutil.ParseEventLog(logPath)
		if err != nil {
			continue
		}

		var firstSync, firstUpdate time.Time
		for _, e := range events {
			if e.EventType == testutil.EventSyncInterestSent && firstSync.IsZero() {
				firstSync = e.Timestamp
			}
			if e.EventType == testutil.EventNodeUpdate && firstUpdate.IsZero() {
				firstUpdate = e.Timestamp
			}
		}

		if !firstSync.IsZero() && !firstUpdate.IsZero() {
			svsMs := int(firstUpdate.Sub(firstSync).Milliseconds())
			if svsMs > maxSVS {
				maxSVS = svsMs
			}
		}
	}

	if maxSVS == 0 {
		return 5000
	}
	return maxSVS
}

func measureUpdatePropagationFromLogs(resultsDir string, nodeCount int) int {
	allEvents := make([]testutil.Event, 0)

	nodeNames := []string{}
	for _, n := range []string{"UCLA", "NEU", "SAVI", "OSAKA", "AFA", "ANYANG", "TNO", "MEMPHIS",
		"QUB", "URJC", "WASEDA", "UFBA", "AVEIRO", "MML2", "MML1", "ARIZONA",
		"IIITH", "SINGAPORE", "FRANKFURT", "SRRU", "DELFT", "WU", "BERN", "MINHO"} {
		if len(nodeNames) >= nodeCount {
			break
		}
		nodeNames = append(nodeNames, n)
	}

	for _, nodeName := range nodeNames {
		logPath := filepath.Join(resultsDir, "events-"+nodeName+".jsonl")
		events, err := testutil.ParseEventLog(logPath)
		if err != nil {
			continue
		}
		allEvents = append(allEvents, events...)
	}

	if len(allEvents) == 0 {
		return 100
	}

	type claimInfo struct {
		target    string
		node      string
		timestamp time.Time
	}
	claims := make([]claimInfo, 0)
	updates := make(map[string]map[string]time.Time)

	for _, e := range allEvents {
		if e.EventType == testutil.EventJobClaimed && e.Target != "" {
			claims = append(claims, claimInfo{
				target:    e.Target,
				node:      e.Node,
				timestamp: e.Timestamp,
			})
		}
		if e.EventType == testutil.EventNodeUpdate && len(e.Jobs) > 0 {
			if updates[e.From] == nil {
				updates[e.From] = make(map[string]time.Time)
			}
			for _, job := range e.Jobs {
				updates[e.From][job] = e.Timestamp
			}
		}
	}

	maxProp := 0
	for _, c := range claims {
		if updateTimes, ok := updates[c.node]; ok {
			if u, ok := updateTimes[c.target]; ok {
				propMs := int(u.Sub(c.timestamp).Milliseconds())
				if propMs > maxProp {
					maxProp = propMs
				}
			}
		}
	}

	if maxProp == 0 {
		return 100
	}
	return maxProp
}

func calculateTimingStatistics(m *TimingMeasurements) TimingStatistics {
	return TimingStatistics{
		SVSConvergenceMaxMs:    maxInt(m.SVSConvergenceMs),
		ReplicationTimeMaxMs:   maxInt(m.ReplicationTimeMs),
		UpdatePropagationMaxMs: maxInt(m.UpdatePropagationMs),
		SafeIntervalMaxMs:      maxInt(m.SafeIntervalMs),
	}
}

func calculateTimingRecommended(s *TimingStatistics) TimingRecommended {
	return TimingRecommended{
		SVSHealthMs:   int(float64(s.SVSConvergenceMaxMs) * 1.5),
		ProducerCmdMs: int(float64(s.ReplicationTimeMaxMs) * 1.5),
		ReplicationMs: int(float64(s.SafeIntervalMaxMs) * 1.5),
	}
}

func maxInt(values []int) int {
	if len(values) == 0 {
		return 0
	}
	m := values[0]
	for _, v := range values {
		if v > m {
			m = v
		}
	}
	return m
}
