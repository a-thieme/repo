package util

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/log"
)

type EventType string

const (
	EventSyncInterestSent  EventType = "sync_interest_sent"
	EventDataSent          EventType = "data_sent"
	EventInterestReceived  EventType = "interest_received"
	EventDataReceived      EventType = "data_received"
	EventCommandReceived   EventType = "command_received"
	EventCommandSynced     EventType = "command_synced"
	EventCommandPublished  EventType = "command_published"
	EventDecisionStarted   EventType = "decision_started"
	EventDecisionMade      EventType = "decision_made"
	EventJobClaimed        EventType = "job_claimed"
	EventJobReleased       EventType = "job_released"
	EventNodeUpdate        EventType = "node_update"
	EventHeartbeatReceived EventType = "heartbeat_received"
	EventReplicationCheck  EventType = "replication_check"
	EventStorageChanged    EventType = "storage_changed"
	EventJobAssignment     EventType = "job_assignment"
	EventAssignmentHandled EventType = "assignment_handled"
	EventSeqStats          EventType = "seq_stats"
	EventAssignStats       EventType = "assign_stats"
	EventNodeDetectedDead  EventType = "node_detected_dead"

	// Auction-specific events
	EventAuctionStarted EventType = "auction_started"
	EventAuctionWinners EventType = "auction_winners"
	EventAuctionResults EventType = "auction_results"
	EventAuctionBid     EventType = "auction_bid"
	EventAuctionDelayed EventType = "auction_delayed"
)

type Event struct {
	Timestamp          time.Time          `json:"ts"`
	EventType          EventType          `json:"event"`
	Node               string             `json:"node,omitempty"`
	Name               string             `json:"name,omitempty"`
	Target             string             `json:"target,omitempty"`
	CommandID          string             `json:"cmdId,omitempty"`
	SequenceNum        uint64             `json:"seq,omitempty"`
	Type               string             `json:"type,omitempty"`
	From               string             `json:"from,omitempty"`
	To                 string             `json:"to,omitempty"`
	Jobs               []string           `json:"jobs,omitempty"`
	Capacity           uint64             `json:"capacity,omitempty"`
	Used               uint64             `json:"used,omitempty"`
	Delta              uint64             `json:"delta,omitempty"`
	Replication        int                `json:"replication,omitempty"`
	ShouldClaim        bool               `json:"shouldClaim,omitempty"`
	Action             string             `json:"action,omitempty"`
	Reason             string             `json:"reason,omitempty"`
	DecisionDetails    string             `json:"decisionDetails,omitempty"`
	Total              uint64             `json:"total,omitempty"`
	Count              int                `json:"count,omitempty"`
	CurrentReplication int                `json:"currentReplication,omitempty"`
	NeededReplication  int                `json:"neededReplication,omitempty"`
	Candidates         []string           `json:"candidates,omitempty"`
	CandidateScores    map[string]int     `json:"candidateScores,omitempty"`
	SelectedCandidates []string           `json:"selectedCandidates,omitempty"`
	FreeSpace          map[string]float64 `json:"freeSpace,omitempty"`
	Assignees          []string           `json:"assignees,omitempty"`
	Nonce              uint64             `json:"nonce,omitempty"`
	NewSeqCount        uint64             `json:"newSeqCount,omitempty"`
	DuplicateSeqCount  uint64             `json:"duplicateSeqCount,omitempty"`
	PublishCount       uint64             `json:"publishCount,omitempty"`
	RepublishCount     uint64             `json:"republishCount,omitempty"`
}

type EventLogger struct {
	file        *os.File
	nodeID      string
	mu          sync.Mutex
	flushMu     sync.Mutex
	flushTicker *time.Ticker
}

type Logger interface {
	Close() error
	Flush() error
	Log(event Event)
	LogSyncInterestSent(total uint64)
	LogDataSent(name string, total uint64)
	LogInterestReceived(name string, total uint64)
	LogDataReceived(name string, total uint64)
	LogCommandReceived(cmdType string, target string)
	LogCommandSynced(cmdType string, target string, fromNode string)
	LogCommandPublished(target string)
	LogDecisionStarted(target string, currentReplication int, needed int)
	LogDecisionMade(target string, shouldClaim bool, reason string, decisionDetails string, currentReplication int, needed int, candidates []string, candidateScores map[string]int, selectedCandidates []string)
	LogJobClaimed(target string)
	LogJobReleased(target string)
	LogNodeUpdate(from string, jobs []enc.Name, capacity, used uint64)
	LogStorageChanged(used, delta uint64)
	LogJobAssignment(target string, assignees []string)
	LogAssignmentHandled(target string, action string, reason string)
	LogSeqStats(newSeq uint64, duplicateSeq uint64)
	LogAssignStats(publishCount uint64, republishCount uint64)
	LogAuctionStarted(target string, currentReplication int, needed int, nonce uint64)
	LogAuctionWinners(target string, candidates []string, winnerScores map[string]float64, winners []string)
	LogAuctionResults(target string, resultsName string, winners []string)
	LogAuctionBid(target string, peer string, capacity uint64, used uint64)
	LogAuctionDelayed(target string, reason string)
	LogNodeDetectedDead(deadNode string, jobsCount int)
	LogHeartbeatReceived(node string)
}

type NullEventLogger struct{}

func (l *NullEventLogger) Close() error                                                         { return nil }
func (l *NullEventLogger) Flush() error                                                         { return nil }
func (l *NullEventLogger) Log(event Event)                                                      {}
func (l *NullEventLogger) LogSyncInterestSent(total uint64)                                     {}
func (l *NullEventLogger) LogDataSent(name string, total uint64)                                {}
func (l *NullEventLogger) LogInterestReceived(name string, total uint64)                        {}
func (l *NullEventLogger) LogDataReceived(name string, total uint64)                            {}
func (l *NullEventLogger) LogCommandReceived(cmdType string, target string)                     {}
func (l *NullEventLogger) LogCommandSynced(cmdType string, target string, fromNode string)      {}
func (l *NullEventLogger) LogCommandPublished(target string)                                    {}
func (l *NullEventLogger) LogDecisionStarted(target string, currentReplication int, needed int) {}
func (l *NullEventLogger) LogDecisionMade(target string, shouldClaim bool, reason string, decisionDetails string, currentReplication int, needed int, candidates []string, candidateScores map[string]int, selectedCandidates []string) {
}
func (l *NullEventLogger) LogJobClaimed(target string)                                       {}
func (l *NullEventLogger) LogJobReleased(target string)                                      {}
func (l *NullEventLogger) LogNodeUpdate(from string, jobs []enc.Name, capacity, used uint64) {}
func (l *NullEventLogger) LogStorageChanged(used, delta uint64)                              {}
func (l *NullEventLogger) LogJobAssignment(target string, assignees []string)                {}
func (l *NullEventLogger) LogAssignmentHandled(target string, action string, reason string) {
}
func (l *NullEventLogger) LogSeqStats(newSeq uint64, duplicateSeq uint64)            {}
func (l *NullEventLogger) LogAssignStats(publishCount uint64, republishCount uint64) {}
func (l *NullEventLogger) LogAuctionStarted(target string, currentReplication int, needed int, nonce uint64) {
}

func (l *NullEventLogger) LogAuctionWinners(target string, candidates []string, winnerScores map[string]float64, winners []string) {
}
func (l *NullEventLogger) LogAuctionResults(target string, resultsName string, winners []string) {}
func (l *NullEventLogger) LogAuctionBid(target string, peer string, capacity uint64, used uint64) {
}
func (l *NullEventLogger) LogAuctionDelayed(target string, reason string)     {}
func (l *NullEventLogger) LogNodeDetectedDead(deadNode string, jobsCount int) {}
func (l *NullEventLogger) LogHeartbeatReceived(node string)                   {}

func NewEventLogger(path string, nodeID string) (*EventLogger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	el := &EventLogger{
		file:        f,
		nodeID:      nodeID,
		flushMu:     sync.Mutex{},
		flushTicker: nil,
	}
	el.startFlushTimer()
	return el, nil
}

func (l *EventLogger) Close() error {
	l.flushMu.Lock()
	defer l.flushMu.Unlock()
	if l.flushTicker != nil {
		l.flushTicker.Stop()
	}
	return l.file.Close()
}

func (l *EventLogger) startFlushTimer() {
	if l.flushTicker != nil {
		l.flushTicker.Stop()
	}
	l.flushTicker = time.NewTicker(500 * time.Millisecond)
	go func() {
		for range l.flushTicker.C {
			l.flushMu.Lock()
			if l.file != nil {
				l.file.Sync()
			}
			l.flushMu.Unlock()
		}
	}()
}

func (l *EventLogger) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Sync()
}

func (l *EventLogger) String() string {
	return l.nodeID
}

func (l *EventLogger) Log(event Event) {
	l.mu.Lock()
	defer l.mu.Unlock()

	event.Timestamp = time.Now().UTC()
	if event.Node == "" {
		event.Node = l.nodeID
	}

	encoder := json.NewEncoder(l.file)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(event); err != nil {
		log.Warn(l, "event_log_encode_failed", "err", err)
	}
}

func (l *EventLogger) LogSyncInterestSent(total uint64) {
	l.Log(Event{
		EventType: EventSyncInterestSent,
		Total:     total,
	})
}

func (l *EventLogger) LogDataSent(name string, total uint64) {
	l.Log(Event{
		EventType: EventDataSent,
		Name:      name,
		Total:     total,
	})
}

func (l *EventLogger) LogInterestReceived(name string, total uint64) {
	l.Log(Event{
		EventType: EventInterestReceived,
		Name:      name,
		Total:     total,
	})
}

func (l *EventLogger) LogDataReceived(name string, total uint64) {
	l.Log(Event{
		EventType: EventDataReceived,
		Name:      name,
		Total:     total,
	})
}

func (l *EventLogger) LogCommandReceived(cmdType string, target string) {
	l.Log(Event{
		EventType: EventCommandReceived,
		Type:      cmdType,
		Target:    target,
		CommandID: target,
	})
	l.Flush()
}

func (l *EventLogger) LogCommandSynced(cmdType string, target string, fromNode string) {
	l.Log(Event{
		EventType: EventCommandSynced,
		Type:      cmdType,
		Target:    target,
		CommandID: target,
		From:      fromNode,
	})
	l.Flush()
}

func (l *EventLogger) LogCommandPublished(target string) {
	l.Log(Event{
		EventType: EventCommandPublished,
		Target:    target,
		CommandID: target,
	})
}

func (l *EventLogger) LogDecisionStarted(target string, currentReplication int, needed int) {
	l.Log(Event{
		EventType:          EventDecisionStarted,
		Target:             target,
		CommandID:          target,
		CurrentReplication: currentReplication,
		NeededReplication:  needed,
	})
}

func (l *EventLogger) LogDecisionMade(
	target string,
	shouldClaim bool,
	reason string,
	decisionDetails string,
	currentReplication int,
	needed int,
	candidates []string,
	candidateScores map[string]int,
	selectedCandidates []string,
) {
	l.Log(Event{
		EventType:          EventDecisionMade,
		Target:             target,
		CommandID:          target,
		ShouldClaim:        shouldClaim,
		Reason:             reason,
		DecisionDetails:    decisionDetails,
		CurrentReplication: currentReplication,
		NeededReplication:  needed,
		Candidates:         candidates,
		CandidateScores:    candidateScores,
		SelectedCandidates: selectedCandidates,
	})
}

func (l *EventLogger) LogJobClaimed(target string) {
	l.Log(Event{
		EventType: EventJobClaimed,
		Target:    target,
		CommandID: target,
	})
	l.Flush()
}

func (l *EventLogger) LogJobReleased(target string) {
	l.Log(Event{
		EventType: EventJobReleased,
		Target:    target,
		CommandID: target,
	})
}

func (l *EventLogger) LogNodeUpdate(from string, jobs []enc.Name, capacity, used uint64) {
	jobsString := make([]string, len(jobs))
	for i, j := range jobs {
		jobsString[i] = j.String()
	}

	l.Log(Event{
		EventType: EventNodeUpdate,
		From:      from,
		Jobs:      jobsString,
		Capacity:  capacity,
		Used:      used,
	})
}

func (l *EventLogger) LogHeartbeatReceived(node string) {
	l.Log(Event{
		EventType: EventHeartbeatReceived,
		From:      node,
	})
}

func (l *EventLogger) LogStorageChanged(used, delta uint64) {
	l.Log(Event{
		EventType: EventStorageChanged,
		Used:      used,
		Delta:     delta,
	})
}

func (l *EventLogger) LogJobAssignment(target string, assignees []string) {
	l.Log(Event{
		EventType: EventJobAssignment,
		Target:    target,
		CommandID: target,
		Assignees: assignees,
	})
}

func (l *EventLogger) LogAssignmentHandled(target string, action string, reason string) {
	l.Log(Event{
		EventType: EventAssignmentHandled,
		Target:    target,
		CommandID: target,
		Action:    action,
		Reason:    reason,
	})
}

func (l *EventLogger) LogSeqStats(newSeq uint64, duplicateSeq uint64) {
	l.Log(Event{
		EventType:         EventSeqStats,
		NewSeqCount:       newSeq,
		DuplicateSeqCount: duplicateSeq,
	})
}

func (l *EventLogger) LogAssignStats(publishCount uint64, republishCount uint64) {
	l.Log(Event{
		EventType:      EventAssignStats,
		PublishCount:   publishCount,
		RepublishCount: republishCount,
	})
}

func (l *EventLogger) LogAuctionStarted(target string, currentReplication int, needed int, nonce uint64) {
	l.Log(Event{
		EventType:          EventAuctionStarted,
		Target:             target,
		CommandID:          target,
		CurrentReplication: currentReplication,
		NeededReplication:  needed,
		Nonce:              nonce,
	})
	l.Flush()
}

func (l *EventLogger) LogAuctionWinners(target string, candidates []string, winnerScores map[string]float64, winners []string) {
	l.Log(Event{
		EventType:          EventAuctionWinners,
		Target:             target,
		CommandID:          target,
		Candidates:         candidates,
		FreeSpace:          winnerScores,
		SelectedCandidates: winners,
	})
}

func (l *EventLogger) LogAuctionResults(target string, resultsName string, winners []string) {
	l.Log(Event{
		EventType: EventAuctionResults,
		Target:    target,
		CommandID: target,
		Name:      resultsName,
		Assignees: winners,
	})
	l.Flush()
}

func (l *EventLogger) LogAuctionBid(target string, peer string, capacity uint64, used uint64) {
	l.Log(Event{
		EventType: EventAuctionBid,
		Target:    target,
		CommandID: target,
		From:      peer,
		Capacity:  capacity,
		Used:      used,
	})
}

func (l *EventLogger) LogAuctionDelayed(target string, reason string) {
	l.Log(Event{
		EventType: EventAuctionDelayed,
		Target:    target,
		CommandID: target,
		Reason:    reason,
	})
	l.Flush()
}

func (l *EventLogger) LogNodeDetectedDead(deadNode string, jobsCount int) {
	l.Log(Event{
		EventType:   EventNodeDetectedDead,
		Name:        deadNode,
		Replication: jobsCount,
	})
	l.Flush()
}

type PacketStats struct {
	SyncInterestsSent   uint64
	DataPacketsSent     uint64
	InterestsReceived   uint64
	DataPacketsReceived uint64
}
