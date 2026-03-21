package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
	"github.com/a-thieme/repo/repo/util"
	"github.com/a-thieme/repo/tlv"
	enc "github.com/named-data/ndnd/std/encoding"
	svs "github.com/named-data/ndnd/std/sync"
)

func TestEventLogger_WriteAndParse(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "test.jsonl")

	logger, err := util.NewEventLogger(logPath, "test-node")
	if err != nil {
		t.Fatalf("Failed to create event logger: %v", err)
	}

	logger.LogCommandReceived("INSERT", "/ndn/test/target")
	logger.LogJobClaimed("/ndn/test/target")
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
	if claimEvents[0].Replication != 0 {
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

	jobOne, _ := enc.NameFromStr("/ndn/job/1")
	jobTwo, _ := enc.NameFromStr("/ndn/job/2")
	logger.LogNodeUpdate("node-b", []enc.Name{jobOne, jobTwo}, 1e9, 5e8)

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

func TestCountingFace_ExtractPacketInfo(t *testing.T) {
	dataPkt := []byte{
		0x06, 0x12,
		0x07, 0x0b, 0x08, 0x03, 'n', 'd', 'n', 0x08, 0x04, 'd', 'a', 't', 'a',
		0x14, 0x00,
		0x15, 0x00,
		0x16, 0x03, 0x1b, 0x01, 0x00,
	}

	lpPkt := []byte{
		0x64, 0x00,
		0x62, 0x01, 0x00,
		0x50, byte(len(dataPkt)),
	}
	lpPkt = append(lpPkt, dataPkt...)
	lpPkt[1] = byte(len(lpPkt) - 2)

	pktType, name := util.ExtractPacketInfo(enc.Wire{lpPkt})
	if name == "" {
		t.Error("Expected to find data name in LpPacket, got empty string")
	}
	if name != "/ndn/data" {
		t.Errorf("Expected /ndn/data, got %s", name)
	}
	if pktType != util.TlvData {
		t.Errorf("Expected TlvData (%d), got %d", util.TlvData, pktType)
	}
}

func TestRepo_ReplicationLogic(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	target, _ := enc.NameFromStr("/ndn/target/1")
	cmd := &tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	shouldClaim := repo.determineWinnersHydra(cmd)
	if shouldClaim == nil {
		t.Error("Empty repo should claim first job")
	}
}

func TestRepo_ReplicationAlreadySatisfied(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 2, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)

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

	shouldClaim := repo.determineWinnersHydra(cmd)
	if shouldClaim != nil {
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

	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:        []enc.Name{},
		Capacity:    1000000000,
		Used:        0,
		LastUpdated: time.Now(),
	}
	repo.nodeStatus["peer-node"] = NodeStatus{
		Jobs:        []enc.Name{},
		Capacity:    1000000000,
		Used:        0,
		LastUpdated: time.Now(),
	}
	repo.mu.Unlock()

	winners := repo.determineWinnersHydra(cmd)
	if winners == nil {
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
		repo := NewRepo("/ndn/drepo", nodeNames[i], "/ndn/repo.teame.dev/repo", replicationFactor, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)
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
		winners := repos[i].determineWinnersHydra(cmd)
		if winners != nil {
			for _, w := range winners {
				if w == nodeNames[i] {
					repos[i].mu.Lock()
					myStatus := repos[i].nodeStatus[repos[i].myNodeName()]
					myStatus.Jobs = append(myStatus.Jobs, cmd.Target)
					repos[i].nodeStatus[repos[i].myNodeName()] = myStatus
					repos[i].mu.Unlock()
					claimCount++
					claimedBy = append(claimedBy, nodeNames[i])
					break
				}
			}
		}
	}

	t.Logf("Claims: %d, by: %v", claimCount, claimedBy)

	if claimCount != replicationFactor {
		t.Errorf("Expected %d claims, got %d", replicationFactor, claimCount)
	}
}

func TestHydraLeaderSelection(t *testing.T) {
	nodeStatus := map[string]NodeStatus{
		"/ndn/repo/n2": {Jobs: []enc.Name{}},
		"/ndn/repo/n0": {Jobs: []enc.Name{}},
		"/ndn/repo/n1": {Jobs: []enc.Name{}},
	}

	leader := selectLeader(nodeStatus)
	if leader != "/ndn/repo/n0" {
		t.Errorf("Expected leader /ndn/repo/n0, got %s", leader)
	}
}

func TestHydraLeaderSelection_Empty(t *testing.T) {
	leader := selectLeader(map[string]NodeStatus{})
	if leader != "" {
		t.Errorf("Expected empty leader, got %s", leader)
	}
}

func TestHydraLeaderSelection_SingleNode(t *testing.T) {
	nodeStatus := map[string]NodeStatus{
		"/ndn/repo/solo": {Jobs: []enc.Name{}},
	}

	leader := selectLeader(nodeStatus)
	if leader != "/ndn/repo/solo" {
		t.Errorf("Expected leader /ndn/repo/solo, got %s", leader)
	}
}

func TestHydraLeaderRedistribution(t *testing.T) {
	nodeCount := 5
	replicationFactor := 3
	nodeNames := make([]string, nodeCount)
	repos := make([]*Repo, nodeCount)

	for i := 0; i < nodeCount; i++ {
		nodeNames[i] = fmt.Sprintf("/ndn/repo/n%d", i)
		repos[i] = NewRepo("/ndn/drepo", nodeNames[i], "/ndn/repo.teame.dev/repo", replicationFactor, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)
	}

	target, _ := enc.NameFromStr("/ndn/target/test")
	cmd := &tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	for i := 0; i < nodeCount; i++ {
		repos[i].mu.Lock()
		repos[i].nodeStatus[repos[i].myNodeName()] = NodeStatus{
			Jobs:        []enc.Name{},
			Capacity:    1000000000,
			Used:        0,
			LastUpdated: time.Now(),
		}
		repos[i].mu.Unlock()
		repos[i].addCommand(cmd)
	}

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

	repos[0].mu.Lock()
	repos[0].nodeStatus["/ndn/repo/n3"] = NodeStatus{
		Jobs:     []enc.Name{target},
		Capacity: 1000000000,
		Used:     0,
	}
	repos[0].mu.Unlock()

	leader := selectLeader(repos[0].nodeStatus)
	t.Logf("Leader: %s", leader)

	if leader != "/ndn/repo/n0" {
		t.Errorf("Expected leader /ndn/repo/n0, got %s", leader)
	}

	nonLeaderIdx := 1
	nonLeader := repos[nonLeaderIdx]
	nonLeader.mu.Lock()
	nonLeader.nodeStatus["/ndn/repo/n3"] = NodeStatus{
		Jobs:     []enc.Name{target},
		Capacity: 1000000000,
		Used:     0,
	}
	nonLeader.mu.Unlock()

	nonLeader.onHeartbeatTimeoutHydra("/ndn/repo/n3")

	nonLeader.redistMu.Lock()
	_, hasScheduled := nonLeader.scheduledRedistributions[target.String()]
	nonLeader.redistMu.Unlock()

	if !hasScheduled {
		t.Error("Non-leader should have scheduled re-evaluation after detecting node failure")
	}
}

func TestHydraCancelWhenReplicated(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)

	target, _ := enc.NameFromStr("/ndn/target/cancel-test")
	cmd := &tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.nodeStatus["peer-1"] = NodeStatus{
		Jobs:     []enc.Name{target},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.nodeStatus["peer-2"] = NodeStatus{
		Jobs:     []enc.Name{target},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	repo.addCommand(cmd)
	repo.scheduleHydraReevaluation(target)

	repo.redistMu.Lock()
	_, hasScheduled := repo.scheduledRedistributions[target.String()]
	repo.redistMu.Unlock()

	if !hasScheduled {
		t.Error("Should have scheduled re-evaluation initially")
	}

	repo.cancelHydraReevaluation(target)

	repo.redistMu.Lock()
	_, stillScheduled := repo.scheduledRedistributions[target.String()]
	repo.redistMu.Unlock()

	if stillScheduled {
		t.Error("Re-evaluation should have been cancelled")
	}
}

func TestHydraRescheduleOnAssignment(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)

	target, _ := enc.NameFromStr("/ndn/target/reschedule-test")
	cmd := &tlv.Command{
		Type:   "INSERT",
		Target: target,
	}

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	repo.addCommand(cmd)
	repo.scheduleHydraReevaluation(target)

	repo.redistMu.Lock()
	originalTimer := repo.scheduledRedistributions[target.String()]
	repo.redistMu.Unlock()

	if originalTimer == nil {
		t.Fatal("Timer should be scheduled")
	}

	repo.scheduleHydraReevaluation(target)

	repo.redistMu.Lock()
	newTimer := repo.scheduledRedistributions[target.String()]
	repo.redistMu.Unlock()

	if newTimer == nil {
		t.Fatal("Timer should still be scheduled after reschedule")
	}

	if originalTimer == newTimer {
		t.Error("Timer should have been replaced on reschedule")
	}
}

func TestAuctionHeartbeatUpdateCallback(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "auction", nil, 500*time.Millisecond)

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:        []enc.Name{},
		Capacity:    1000000000,
		Used:        0,
		LastUpdated: time.Now().Add(-10 * time.Second),
	}
	repo.mu.Unlock()

	peerName, _ := enc.NameFromStr("/ndn/repo/peer")
	update := svs.SvSyncUpdate{
		Name: peerName,
		Boot: 1,
		High: 1,
		Low:  1,
	}

	repo.handleAuctionHeartbeatSvSyncUpdate(update)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	status, exists := repo.nodeStatus[peerName.String()]
	if !exists {
		t.Fatal("Peer should have been added to nodeStatus")
	}

	if time.Since(status.LastUpdated) > time.Second {
		t.Error("LastUpdated should have been updated to now")
	}
}

func TestAuctionHeartbeatUpdateCallback_ExistingNode(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "auction", nil, 500*time.Millisecond)

	oldTime := time.Now().Add(-10 * time.Second)
	peerName, _ := enc.NameFromStr("/ndn/repo/peer")

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:        []enc.Name{},
		Capacity:    1000000000,
		Used:        0,
		LastUpdated: time.Now(),
	}
	repo.nodeStatus[peerName.String()] = NodeStatus{
		Jobs:        []enc.Name{},
		Capacity:    1000000000,
		Used:        0,
		LastUpdated: oldTime,
	}
	repo.mu.Unlock()

	update := svs.SvSyncUpdate{
		Name: peerName,
		Boot: 1,
		High: 1,
		Low:  1,
	}

	repo.handleAuctionHeartbeatSvSyncUpdate(update)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	status := repo.nodeStatus[peerName.String()]
	if time.Since(status.LastUpdated) > time.Second {
		t.Error("LastUpdated should have been updated to now for existing node")
	}
	if status.LastUpdated == oldTime {
		t.Error("LastUpdated should have changed from old time")
	}
}
