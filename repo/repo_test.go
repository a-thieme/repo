package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
	"github.com/a-thieme/repo/repo/util"
	"github.com/a-thieme/repo/tlv"
	enc "github.com/named-data/ndnd/std/encoding"
)

func TestEventLogger_WriteAndParse(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.jsonl")

	logger, err := util.NewEventLogger(logPath, "test-node")
	if err != nil {
		t.Fatalf("Failed to create event logger: %v", err)
	}

	logger.LogCommandReceived("INSERT", "/ndn/test/target")
	logger.LogJobClaimed("/ndn/test/target", 1)
	logger.LogStorageChanged(1000, 100)

	if err := logger.Close(); err != nil {
		t.Fatalf("Failed to close logger: %v", err)
	}

	events, err := testutil.ParseEventLog(logPath)
	if err != nil {
		t.Fatalf("Failed to parse event log: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("Expected 3 events, got %d", len(events))
	}

	cmdEvents := testutil.FilterEvents(events, testutil.EventCommandReceived)
	if len(cmdEvents) != 1 {
		t.Fatalf("Expected 1 command event, got %d", len(cmdEvents))
	}
	if cmdEvents[0].Type != "INSERT" {
		t.Errorf("Expected INSERT type, got %s", cmdEvents[0].Type)
	}

	claimEvents := testutil.FilterEvents(events, testutil.EventJobClaimed)
	if len(claimEvents) != 1 {
		t.Fatalf("Expected 1 claim event, got %d", len(claimEvents))
	}
	if claimEvents[0].Replication != 1 {
		t.Errorf("Expected replication 1, got %d", claimEvents[0].Replication)
	}
}

func TestEventLogger_SyncInterestAndData(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "packets.jsonl")

	logger, err := util.NewEventLogger(logPath, "test-node")
	if err != nil {
		t.Fatalf("Failed to create event logger: %v", err)
	}

	logger.LogSyncInterestSent(1)
	logger.LogSyncInterestSent(2)
	logger.LogDataSent("/ndn/test/data/1", 1)
	logger.LogDataSent("/ndn/test/data/2", 2)

	if err := logger.Close(); err != nil {
		t.Fatalf("Failed to close logger: %v", err)
	}

	events, err := testutil.ParseEventLog(logPath)
	if err != nil {
		t.Fatalf("Failed to parse event log: %v", err)
	}

	syncInterests, dataPackets := testutil.GetLatestPacketStats(events)
	if syncInterests != 2 {
		t.Errorf("Expected 2 sync interests, got %d", syncInterests)
	}
	if dataPackets != 2 {
		t.Errorf("Expected 2 data packets, got %d", dataPackets)
	}
}

func TestEventLogger_NodeUpdate(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "node_update.jsonl")

	logger, err := util.NewEventLogger(logPath, "node-a")
	if err != nil {
		t.Fatalf("Failed to create event logger: %v", err)
	}

	logger.LogNodeUpdate("node-b", []string{"/ndn/job/1", "/ndn/job/2"}, 1e9, 5e8)

	if err := logger.Close(); err != nil {
		t.Fatalf("Failed to close logger: %v", err)
	}

	events, err := testutil.ParseEventLog(logPath)
	if err != nil {
		t.Fatalf("Failed to parse event log: %v", err)
	}

	updateEvents := testutil.FilterEvents(events, testutil.EventNodeUpdate)
	if len(updateEvents) != 1 {
		t.Fatalf("Expected 1 node update event, got %d", len(updateEvents))
	}

	if updateEvents[0].From != "node-b" {
		t.Errorf("Expected from node-b, got %s", updateEvents[0].From)
	}
	if len(updateEvents[0].Jobs) != 2 {
		t.Errorf("Expected 2 jobs, got %d", len(updateEvents[0].Jobs))
	}
}

func TestCountingFace_ParseTLVType(t *testing.T) {
	tests := []struct {
		name     string
		wire     []byte
		expected uint8
	}{
		{"empty", []byte{}, 0},
		{"interest", []byte{0x05, 0x10}, 0x05},
		{"data", []byte{0x06, 0x20}, 0x06},
		{"other", []byte{0x07, 0x00}, 0x07},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := util.ParseTLVType(enc.Wire{tt.wire})
			if result != tt.expected {
				t.Errorf("Expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestRepo_ReplicationLogic(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3)

	target, _ := enc.NameFromStr("/ndn/target/1")
	cmd := &tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	shouldClaim := repo.shouldClaimJobHydra(cmd)
	if !shouldClaim {
		t.Error("Empty repo should claim first job")
	}
}

func TestRepo_ReplicationAlreadySatisfied(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 2)

	target, _ := enc.NameFromStr("/ndn/target/1")
	cmd := &tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	repo.mu.Lock()
	repo.nodeStatus["other-node-1"] = NodeStatus{
		Jobs: []enc.Name{target},
	}
	repo.nodeStatus["other-node-2"] = NodeStatus{
		Jobs: []enc.Name{target},
	}
	repo.mu.Unlock()

	shouldClaim := repo.shouldClaimJobHydra(cmd)
	if shouldClaim {
		t.Error("Repo should not claim when replication factor already satisfied")
	}
}

func TestRepo_SyncNewCommandProcessing(t *testing.T) {
	target, _ := enc.NameFromStr("/ndn/target/sync-test")
	cmd := &tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	update := &tlv.NodeUpdate{
		Jobs:            []enc.Name{},
		StorageCapacity: 1000000000,
		StorageUsed:     0,
		NewCommand:      cmd,
	}

	encodedUpdate := update.Encode()
	parsed, err := tlv.ParseNodeUpdate(enc.NewWireView(encodedUpdate), false)
	if err != nil {
		t.Fatalf("Failed to parse encoded update: %v", err)
	}
	if parsed.NewCommand == nil {
		t.Fatal("NewCommand was not preserved after encode/decode")
	}
	if !parsed.NewCommand.Target.Equal(target) {
		t.Errorf("Target mismatch: expected %s, got %s", target, parsed.NewCommand.Target)
	}

	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3)

	repo.mu.Lock()
	repo.nodeStatus["peer-node"] = NodeStatus{
		Jobs:        []enc.Name{},
		Capacity:    1000000000,
		Used:        0,
		LastUpdated: time.Now(),
	}
	repo.mu.Unlock()

	shouldClaim := repo.shouldClaimJobHydra(cmd)
	if !shouldClaim {
		t.Error("Repo should claim job when replication not satisfied")
	}

	repo.addCommand(cmd)
	repo.mu.Lock()
	myStatus := repo.nodeStatus[repo.myNodeName()]
	myStatus.Jobs = append(myStatus.Jobs, cmd.Target)
	repo.nodeStatus[repo.myNodeName()] = myStatus
	repo.mu.Unlock()

	claims := repo.countReplication(target)
	if claims != 1 {
		t.Errorf("Expected 1 claim after manual claim, got %d", claims)
	}
}

func TestRepo_MultiNodeSyncSimulation(t *testing.T) {
	replicationFactor := 3
	nodeCount := 5

	nodeNames := []string{"/ndn/repo/node-a", "/ndn/repo/node-b", "/ndn/repo/node-c", "/ndn/repo/node-d", "/ndn/repo/node-e"}
	repos := make([]*Repo, nodeCount)

	for i := 0; i < nodeCount; i++ {
		repo := NewRepo("/ndn/drepo", nodeNames[i], "/ndn/repo.teame.dev/repo", replicationFactor)
		repo.mu.Lock()
		repo.storageCapacity = 1000000000
		repo.nodeStatus[repo.myNodeName()] = NodeStatus{
			Jobs:     []enc.Name{},
			Capacity: 1000000000,
			Used:     0,
		}
		repo.mu.Unlock()
		repos[i] = repo
	}

	target, _ := enc.NameFromStr("/ndn/target/multi-sync-test")
	cmd := &tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	repos[0].addCommand(cmd)

	for i := 0; i < nodeCount; i++ {
		for j := 0; j < nodeCount; j++ {
			if i != j {
				repos[i].mu.Lock()
				repos[i].nodeStatus[nodeNames[j]] = NodeStatus{
					Jobs:        []enc.Name{},
					Capacity:    1000000000,
					Used:        0,
					LastUpdated: time.Now(),
				}
				repos[i].mu.Unlock()
			}
		}
	}

	claimCount := 0
	claimedBy := make([]string, 0)
	for i := 0; i < nodeCount; i++ {
		if repos[i].shouldClaimJobHydra(cmd) {
			repos[i].mu.Lock()
			myStatus := repos[i].nodeStatus[repos[i].myNodeName()]
			myStatus.Jobs = append(myStatus.Jobs, cmd.Target)
			repos[i].nodeStatus[repos[i].myNodeName()] = myStatus
			repos[i].mu.Unlock()
			claimCount++
			claimedBy = append(claimedBy, nodeNames[i])
		}
	}

	t.Logf("Claims: %d, by: %v", claimCount, claimedBy)

	if claimCount != replicationFactor {
		t.Errorf("Expected %d claims, got %d", replicationFactor, claimCount)
	}
}
