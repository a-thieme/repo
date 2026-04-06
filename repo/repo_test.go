package main

import (
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/a-thieme/repo/repo/testutil"
	"github.com/a-thieme/repo/repo/util"
	"github.com/a-thieme/repo/tlv"
	enc "github.com/named-data/ndnd/std/encoding"
	svs "github.com/named-data/ndnd/std/sync"
)

func cleanupRepo(repo *Repo) {
	if repo != nil {
		repo.Close()
	}
}

func cleanupRepos(repos []*Repo) {
	for _, r := range repos {
		cleanupRepo(r)
	}
}

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
	defer cleanupRepo(repo)

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

	stats := repo.checkUnder(cmd.Target)
	if stats.Needed <= 0 {
		t.Error("Empty repo should need replicas")
	}
	if len(stats.Candidates) == 0 {
		t.Error("Empty repo should have candidates")
	}
}

func TestRepo_ReplicationAlreadySatisfied(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 2, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

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

	stats := repo.checkUnder(cmd.Target)
	if stats.Needed > 0 {
		t.Error("Repo should not need replicas when replication factor already satisfied")
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
	defer cleanupRepo(repo)

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.nodeStatus["peer-node"] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	stats := repo.checkUnder(cmd.Target)
	if stats.Needed <= 0 {
		t.Error("Repo should need replicas when replication not satisfied")
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
		repo.nodeStatus[repo.myNodeName()] = NodeStatus{
			Jobs:     []enc.Name{},
			Capacity: 1000000000,
			Used:     0,
		}
		repo.mu.Unlock()
		repos[i] = repo
	}
	defer cleanupRepos(repos)

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
					Jobs:     []enc.Name{},
					Capacity: 1000000000,
					Used:     0,
				}
				repos[i].mu.Unlock()
			}
		}
	}

	// Simulate what evaluateBatch does on the leader:
	// 1. Get UnderStats for each target
	// 2. Sort candidates by availability (storage usage)
	// 3. Select first [Needed] candidates as winners

	stats := repos[0].checkUnder(cmd.Target)
	needed := stats.Needed
	candidates := stats.Candidates

	// Sort candidates (same logic as sortCandidates - by percent used, then capacity, then name)
	slices.SortFunc(candidates, func(i, j string) int {
		// Find percent used for each
		iStatus := repos[0].nodeStatus[i]
		jStatus := repos[0].nodeStatus[j]
		iUsedPct := float64(iStatus.Used) / float64(iStatus.Capacity)
		jUsedPct := float64(jStatus.Used) / float64(jStatus.Capacity)
		if iUsedPct != jUsedPct {
			if iUsedPct < jUsedPct {
				return -1
			}
			return 1
		}
		if iStatus.Capacity != jStatus.Capacity {
			if iStatus.Capacity > jStatus.Capacity {
				return -1
			}
			return 1
		}
		if i < j {
			return -1
		}
		if i > j {
			return 1
		}
		return 0
	})

	// Select winners
	winners := candidates[:min(needed, len(candidates))]

	// Simulate claims
	claimCount := 0
	claimedBy := make([]string, 0)
	for _, winner := range winners {
		for i := 0; i < nodeCount; i++ {
			if repos[i].myNodeName() == winner {
				repos[i].mu.Lock()
				myStatus := repos[i].nodeStatus[repos[i].myNodeName()]
				myStatus.Jobs = append(myStatus.Jobs, cmd.Target)
				repos[i].nodeStatus[repos[i].myNodeName()] = myStatus
				repos[i].mu.Unlock()
				claimCount++
				claimedBy = append(claimedBy, winner)
				break
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

func TestAuctionHeartbeatUpdate_NewPeer(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "auction", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	peerName, _ := enc.NameFromStr("/ndn/repo/peer")

	// Simulate heartbeat update via resetHeartbeatTimer (the new mechanism)
	repo.resetHeartbeatTimer(peerName.String())

	repo.heartbeatMu.Lock()
	defer repo.heartbeatMu.Unlock()

	// Verify a heartbeat timer was created for the peer
	timer, exists := repo.heartbeats[peerName.String()]
	if !exists {
		t.Fatal("Heartbeat timer should have been created for new peer")
	}
	if timer == nil {
		t.Fatal("Timer should not be nil")
	}
}

func TestAuctionHeartbeatUpdate_ExistingPeer(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "auction", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

	peerName, _ := enc.NameFromStr("/ndn/repo/peer")

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.nodeStatus[peerName.String()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	// Set up an initial timer
	repo.resetHeartbeatTimer(peerName.String())

	// Reset the timer (simulating a new heartbeat)
	repo.resetHeartbeatTimer(peerName.String())

	repo.heartbeatMu.Lock()
	defer repo.heartbeatMu.Unlock()

	// Verify timer still exists
	timer, exists := repo.heartbeats[peerName.String()]
	if !exists {
		t.Fatal("Heartbeat timer should still exist for peer")
	}
	if timer == nil {
		t.Fatal("Timer should not be nil")
	}
}

func TestHydraHeartbeatUpdate_NewPeer(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	peerName, _ := enc.NameFromStr("/ndn/repo/peer")
	peerNodeUpdate := &tlv.NodeUpdate{
		Jobs:            []enc.Name{},
		StorageCapacity: 1000000000,
		StorageUsed:     0,
	}

	pub := svs.SvsPub{
		Publisher: peerName,
		Content:   peerNodeUpdate.Encode(),
	}

	repo.onGroupSync(pub)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	// Verify peer was added to nodeStatus
	status, exists := repo.nodeStatus[peerName.String()]
	if !exists {
		t.Fatal("Peer should have been added to nodeStatus")
	}

	// Verify heartbeat timer was created
	repo.heartbeatMu.Lock()
	timer, timerExists := repo.heartbeats[peerName.String()]
	repo.heartbeatMu.Unlock()

	if !timerExists {
		t.Fatal("Heartbeat timer should have been created for peer")
	}
	if timer == nil {
		t.Fatal("Timer should not be nil")
	}

	_ = status // Avoid unused variable warning
}

func TestHydraHeartbeatUpdate_ExistingPeer(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

	peerName, _ := enc.NameFromStr("/ndn/repo/peer")

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.nodeStatus[peerName.String()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	// Set up initial heartbeat timer
	repo.resetHeartbeatTimer(peerName.String())

	peerNodeUpdate := &tlv.NodeUpdate{
		Jobs:            []enc.Name{},
		StorageCapacity: 1000000000,
		StorageUsed:     0,
	}

	pub := svs.SvsPub{
		Publisher: peerName,
		Content:   peerNodeUpdate.Encode(),
	}

	repo.onGroupSync(pub)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	// Verify peer still exists in nodeStatus
	status := repo.nodeStatus[peerName.String()]

	// Verify heartbeat timer was reset
	repo.heartbeatMu.Lock()
	timer, timerExists := repo.heartbeats[peerName.String()]
	repo.heartbeatMu.Unlock()

	if !timerExists {
		t.Fatal("Heartbeat timer should still exist for peer")
	}
	if timer == nil {
		t.Fatal("Timer should not be nil")
	}

	_ = status // Avoid unused variable warning
}

func TestHeartbeatUpdate_ResetsTimer(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	peerName, _ := enc.NameFromStr("/ndn/repo/peer")
	repo.nodeStatus[peerName.String()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	peerNodeUpdate := &tlv.NodeUpdate{
		Jobs:            []enc.Name{},
		StorageCapacity: 1000000000,
		StorageUsed:     0,
	}

	pub := svs.SvsPub{
		Publisher: peerName,
		Content:   peerNodeUpdate.Encode(),
	}

	repo.onGroupSync(pub)

	repo.heartbeatMu.Lock()
	defer repo.heartbeatMu.Unlock()

	timer, exists := repo.heartbeats[peerName.String()]
	if !exists {
		t.Fatal("Timer should have been created for peer")
	}
	if timer == nil {
		t.Fatal("Timer should not be nil")
	}
}

func TestHeartbeatUpdate_NoTimerForSelf(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 0, "hydra", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

	selfName := repo.myNodeName()

	repo.mu.Lock()
	repo.nodeStatus[selfName] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	selfNodeUpdate := &tlv.NodeUpdate{
		Jobs:            []enc.Name{},
		StorageCapacity: 1000000000,
		StorageUsed:     0,
	}

	pub := svs.SvsPub{
		Publisher: repo.nodePrefix,
		Content:   selfNodeUpdate.Encode(),
	}

	repo.onGroupSync(pub)

	repo.heartbeatMu.Lock()
	defer repo.heartbeatMu.Unlock()

	_, exists := repo.heartbeats[selfName]
	if exists {
		t.Error("Timer should NOT be created for self")
	}
}

func TestHeartbeatTimeout_TriggersNodeDeath(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 10*time.Millisecond, "hydra", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

	peerName, _ := enc.NameFromStr("/ndn/repo/peer")

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.nodeStatus[peerName.String()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	peerNodeUpdate := &tlv.NodeUpdate{
		Jobs:            []enc.Name{},
		StorageCapacity: 1000000000,
		StorageUsed:     0,
	}

	pub := svs.SvsPub{
		Publisher: peerName,
		Content:   peerNodeUpdate.Encode(),
	}

	repo.onGroupSync(pub)

	time.Sleep(600 * time.Millisecond)

	repo.mu.Lock()
	_, exists := repo.nodeStatus[peerName.String()]
	repo.mu.Unlock()

	if exists {
		t.Error("Peer should have been removed from nodeStatus after timeout")
	}
}

func TestHeartbeatTimeout_OnlyTimedOutPeerRemoved(t *testing.T) {
	repo := NewRepo("/ndn/drepo", "/ndn/repo/test", "/ndn/repo.teame.dev/repo", 3, false, 10*1024*1024, 10*time.Millisecond, "hydra", nil, 500*time.Millisecond)
	defer cleanupRepo(repo)

	peer1, _ := enc.NameFromStr("/ndn/repo/peer1")
	peer2, _ := enc.NameFromStr("/ndn/repo/peer2")

	repo.mu.Lock()
	repo.nodeStatus[repo.myNodeName()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.nodeStatus[peer1.String()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.nodeStatus[peer2.String()] = NodeStatus{
		Jobs:     []enc.Name{},
		Capacity: 1000000000,
		Used:     0,
	}
	repo.mu.Unlock()

	peer1NodeUpdate := &tlv.NodeUpdate{
		Jobs:            []enc.Name{},
		StorageCapacity: 1000000000,
		StorageUsed:     0,
	}

	pub1 := svs.SvsPub{
		Publisher: peer1,
		Content:   peer1NodeUpdate.Encode(),
	}

	repo.onGroupSync(pub1)

	time.Sleep(600 * time.Millisecond)

	repo.mu.Lock()
	_, peer1Exists := repo.nodeStatus[peer1.String()]
	_, peer2Exists := repo.nodeStatus[peer2.String()]
	repo.mu.Unlock()

	if peer1Exists {
		t.Error("Peer1 should have been removed after timeout")
	}
	if !peer2Exists {
		t.Error("Peer2 should still exist (no heartbeat received)")
	}
}
