package util

import (
	"encoding/json"
	enc "github.com/named-data/ndnd/std/encoding"
	"os"
	"sync"
	"time"
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
	EventReplicationCheck  EventType = "replication_check"
	EventStorageChanged    EventType = "storage_changed"
	EventJobAssignment     EventType = "job_assignment"
	EventAssignmentHandled EventType = "assignment_handled"
	EventSeqStats          EventType = "seq_stats"
	EventAssignStats       EventType = "assign_stats"
)

type Event struct {
	Timestamp          time.Time         `json:"ts"`
	EventType          EventType         `json:"event"`
	Node               string            `json:"node,omitempty"`
	Name               string            `json:"name,omitempty"`
	Target             string            `json:"target,omitempty"`
	CommandID          string            `json:"cmdId,omitempty"`
	SequenceNum        uint64            `json:"seq,omitempty"`
	Type               string            `json:"type,omitempty"`
	From               string            `json:"from,omitempty"`
	To                 string            `json:"to,omitempty"`
	Jobs               []string          `json:"jobs,omitempty"`
	Capacity           uint64            `json:"capacity,omitempty"`
	Used               uint64            `json:"used,omitempty"`
	Delta              uint64            `json:"delta,omitempty"`
	Replication        int               `json:"replication,omitempty"`
	ShouldClaim        bool              `json:"shouldClaim,omitempty"`
	Action             string            `json:"action,omitempty"`
	Reason             string            `json:"reason,omitempty"`
	DecisionDetails    string            `json:"decisionDetails,omitempty"`
	Total              uint64            `json:"total,omitempty"`
	Count              int               `json:"count,omitempty"`
	CurrentReplication int               `json:"currentReplication,omitempty"`
	NeededReplication  int               `json:"neededReplication,omitempty"`
	Candidates         []string          `json:"candidates,omitempty"`
	CandidateScores    map[string]int    `json:"candidateScores,omitempty"`
	SelectedCandidates []string          `json:"selectedCandidates,omitempty"`
	FreeSpace          map[string]uint64 `json:"freeSpace,omitempty"`
	Assignees          []string          `json:"assignees,omitempty"`
	NewSeqCount        uint64            `json:"newSeqCount,omitempty"`
	DuplicateSeqCount  uint64            `json:"duplicateSeqCount,omitempty"`
	PublishCount       uint64            `json:"publishCount,omitempty"`
	RepublishCount     uint64            `json:"republishCount,omitempty"`
}

type EventLogger struct {
	file   *os.File
	mu     sync.Mutex
	nodeID string
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
	LogAssignmentHandled(target string, fromPublisher string, action string, reason string, assignees []string)
	LogSeqStats(newSeq uint64, duplicateSeq uint64)
	LogAssignStats(publishCount uint64, republishCount uint64)
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
func (l *NullEventLogger) LogAssignmentHandled(target string, fromPublisher string, action string, reason string, assignees []string) {
}
func (l *NullEventLogger) LogSeqStats(newSeq uint64, duplicateSeq uint64)            {}
func (l *NullEventLogger) LogAssignStats(publishCount uint64, republishCount uint64) {}

func NewEventLogger(path string, nodeID string) (*EventLogger, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &EventLogger{
		file:   f,
		nodeID: nodeID,
	}, nil
}

func (l *EventLogger) Close() error {
	return l.file.Close()
}

func (l *EventLogger) Flush() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Sync()
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
	encoder.Encode(event)
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
}

func (l *EventLogger) LogCommandSynced(cmdType string, target string, fromNode string) {
	l.Log(Event{
		EventType: EventCommandSynced,
		Type:      cmdType,
		Target:    target,
		CommandID: target,
		From:      fromNode,
	})
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

func (l *EventLogger) LogAssignmentHandled(target string, fromPublisher string, action string, reason string, assignees []string) {
	l.Log(Event{
		EventType: EventAssignmentHandled,
		Target:    target,
		CommandID: target,
		From:      fromPublisher,
		Action:    action,
		Reason:    reason,
		Assignees: assignees,
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

type PacketStats struct {
	SyncInterestsSent   uint64
	DataPacketsSent     uint64
	InterestsReceived   uint64
	DataPacketsReceived uint64
}
