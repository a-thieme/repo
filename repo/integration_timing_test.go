package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
)

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
	SVSConvergenceMs  []int `json:"svs_convergence_ms"`
	CommandRttMs      []int `json:"command_rtt_ms"`
	ReplicationTimeMs []int `json:"replication_time_ms"`
}

type TimingStatistics struct {
	SVSConvergenceMaxMs  int `json:"svs_convergence_max_ms"`
	CommandRttMaxMs      int `json:"command_rtt_max_ms"`
	ReplicationTimeMaxMs int `json:"replication_time_max_ms"`
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

	cfg := TimingConfig{
		Iterations:     *timingIterations,
		NodeCount:      *timingNodes,
		ReplicationFac: *timingReplicationFac,
	}

	results := TimingResults{
		Config: cfg,
		Measurements: TimingMeasurements{
			SVSConvergenceMs:  make([]int, 0, cfg.Iterations),
			CommandRttMs:      make([]int, 0, cfg.Iterations),
			ReplicationTimeMs: make([]int, 0, cfg.Iterations),
		},
	}

	t.Logf("=== TIMING CONFIGURATION TEST ===")
	t.Logf("Iterations: %d, Nodes: %d, RF: %d", cfg.Iterations, cfg.NodeCount, cfg.ReplicationFac)

	restore := setupCSCache(false)
	defer restore()

	out, _ := exec.Command("nfdc", "strategy", "set", "/ndn/drepo/group-messages/32=svs", "/localhost/nfd/strategy/multicast").CombinedOutput()
	t.Logf("nfdc strategy set: %s", string(out))
	exec.Command("nfdc", "strategy", "set", "/ndn/drepo/notify", "/localhost/nfd/strategy/best-route").Run()

	time.Sleep(*routingConvergeWait)

	repoBinary := buildRepoBinary(t)
	producerBinary := buildProducerBinary(t)

	for i := 0; i < cfg.Iterations; i++ {
		t.Logf("--- Iteration %d/%d ---", i+1, cfg.Iterations)

		tmpDir := t.TempDir()
		repos := startTimingRepos(t, cfg.NodeCount, repoBinary, tmpDir)

		svsTime := measureSVSConvergence(t, repos, 30*time.Second)
		results.Measurements.SVSConvergenceMs = append(results.Measurements.SVSConvergenceMs, svsTime)
		t.Logf("  SVS convergence: %dms", svsTime)

		cmdRtt := measureCommandRtt(t, producerBinary, 10*time.Second)
		results.Measurements.CommandRttMs = append(results.Measurements.CommandRttMs, cmdRtt)
		t.Logf("  Command RTT: %dms", cmdRtt)

		repTime := measureReplicationTime(t, repos, cfg.ReplicationFac, 30*time.Second)
		results.Measurements.ReplicationTimeMs = append(results.Measurements.ReplicationTimeMs, repTime)
		t.Logf("  Replication time: %dms", repTime)

		stopRepos(repos)
	}

	results.Statistics = calculateStatistics(&results.Measurements)
	results.Recommended = calculateRecommended(&results.Statistics)

	t.Logf("\n=== TIMING RESULTS ===")
	t.Logf("SVS Convergence: samples=%v, max=%dms, recommended=%dms",
		results.Measurements.SVSConvergenceMs, results.Statistics.SVSConvergenceMaxMs, results.Recommended.SVSHealthMs)
	t.Logf("Command RTT: samples=%v, max=%dms, recommended=%dms",
		results.Measurements.CommandRttMs, results.Statistics.CommandRttMaxMs, results.Recommended.ProducerCmdMs)
	t.Logf("Replication: samples=%v, max=%dms, recommended=%dms",
		results.Measurements.ReplicationTimeMs, results.Statistics.ReplicationTimeMaxMs, results.Recommended.ReplicationMs)

	t.Logf("\nRecommended timeout values (max * 1.5):")
	t.Logf("  --svs-timeout=%dms", results.Recommended.SVSHealthMs)
	t.Logf("  --producer-timeout=%dms", results.Recommended.ProducerCmdMs)
	t.Logf("  --replication-timeout=%dms", results.Recommended.ReplicationMs)

	if *timingOutputJson {
		jsonOut, _ := json.MarshalIndent(results, "", "  ")
		fmt.Println(string(jsonOut))
	}
}

func startTimingRepos(t *testing.T, nodeCount int, repoBinary string, tmpDir string) []*repoProcess {
	repos := make([]*repoProcess, nodeCount)

	for i := 0; i < nodeCount; i++ {
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
	}

	return repos
}

func measureSVSConvergence(t *testing.T, repos []*repoProcess, timeout time.Duration) int {
	start := time.Now()
	deadline := start.Add(timeout)
	expectedPeers := len(repos) - 1

	for time.Now().Before(deadline) {
		allHealthy := true
		for _, r := range repos {
			events, _ := testutil.ParseEventLog(r.logPath)
			peers := countUniquePeerUpdates(events, r.prefix)
			if len(peers) < expectedPeers {
				allHealthy = false
				break
			}
		}
		if allHealthy {
			return int(time.Since(start).Milliseconds())
		}
		time.Sleep(100 * time.Millisecond)
	}

	return int(timeout.Milliseconds())
}

func measureCommandRtt(t *testing.T, producerBinary string, timeout time.Duration) int {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, producerBinary)
	output, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	if err != nil {
		t.Logf("  Producer error: %v, output: %s", err, string(output))
		return int(timeout.Milliseconds())
	}

	return int(elapsed.Milliseconds())
}

func measureReplicationTime(t *testing.T, repos []*repoProcess, rf int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	start := time.Time{}

	for _, r := range repos {
		events, _ := testutil.ParseEventLog(r.logPath)
		for _, e := range events {
			if e.EventType == testutil.EventCommandReceived && start.IsZero() {
				start = e.Timestamp
				break
			}
		}
		if !start.IsZero() {
			break
		}
	}

	if start.IsZero() {
		return int(timeout.Milliseconds())
	}

	claimCounts := make(map[string]map[string]bool)

	for time.Now().Before(deadline) {
		for _, r := range repos {
			events, _ := testutil.ParseEventLog(r.logPath)
			for _, e := range events {
				if e.EventType == testutil.EventJobClaimed && e.Target != "" {
					if claimCounts[e.Target] == nil {
						claimCounts[e.Target] = make(map[string]bool)
					}
					claimCounts[e.Target][r.nodeID] = true
				}
			}
		}

		for target, nodes := range claimCounts {
			if len(nodes) >= rf {
				return int(time.Since(start).Milliseconds())
			}
			_ = target
		}

		time.Sleep(100 * time.Millisecond)
	}

	return int(timeout.Milliseconds())
}

func calculateStatistics(m *TimingMeasurements) TimingStatistics {
	return TimingStatistics{
		SVSConvergenceMaxMs:  max(m.SVSConvergenceMs),
		CommandRttMaxMs:      max(m.CommandRttMs),
		ReplicationTimeMaxMs: max(m.ReplicationTimeMs),
	}
}

func calculateRecommended(s *TimingStatistics) TimingRecommended {
	return TimingRecommended{
		SVSHealthMs:   int(float64(s.SVSConvergenceMaxMs) * 1.5),
		ProducerCmdMs: int(float64(s.CommandRttMaxMs) * 1.5),
		ReplicationMs: int(float64(s.ReplicationTimeMaxMs) * 1.5),
	}
}

func max(values []int) int {
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

func avg(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sum := 0
	for _, v := range values {
		sum += v
	}
	return sum / len(values)
}

func median(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]int, len(values))
	copy(sorted, values)
	sort.Ints(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 0 {
		return (sorted[mid-1] + sorted[mid]) / 2
	}
	return sorted[mid]
}
