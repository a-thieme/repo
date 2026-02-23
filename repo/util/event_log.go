package util

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type EventType string

const (
	EventSyncInterestSent EventType = "sync_interest_sent"
	EventDataSent         EventType = "data_sent"
	EventInterestReceived EventType = "interest_received"
	EventDataReceived     EventType = "data_received"
	EventCommandReceived  EventType = "command_received"
	EventCommandSynced    EventType = "command_synced"
	EventJobClaimed       EventType = "job_claimed"
	EventJobReleased      EventType = "job_released"
	EventNodeUpdate       EventType = "node_update"
	EventReplicationCheck EventType = "replication_check"
	EventStorageChanged   EventType = "storage_changed"
	EventJobAssignment    EventType = "job_assignment"
)

type Event struct {
	Timestamp          time.Time         `json:"ts"`
	EventType          EventType         `json:"event"`
	Node               string            `json:"node,omitempty"`
	Name               string            `json:"name,omitempty"`
	Target             string            `json:"target,omitempty"`
	Type               string            `json:"type,omitempty"`
	From               string            `json:"from,omitempty"`
	To                 string            `json:"to,omitempty"`
	Jobs               []string          `json:"jobs,omitempty"`
	Capacity           uint64            `json:"capacity,omitempty"`
	Used               uint64            `json:"used,omitempty"`
	Delta              uint64            `json:"delta,omitempty"`
	Replication        int               `json:"replication,omitempty"`
	ShouldClaim        bool              `json:"shouldClaim,omitempty"`
	Reason             string            `json:"reason,omitempty"`
	Total              uint64            `json:"total,omitempty"`
	Count              int               `json:"count,omitempty"`
	CurrentReplication int               `json:"currentReplication,omitempty"`
	NeededReplication  int               `json:"neededReplication,omitempty"`
	Candidates         []string          `json:"candidates,omitempty"`
	SelectedCandidates []string          `json:"selectedCandidates,omitempty"`
	FreeSpace          map[string]uint64 `json:"freeSpace,omitempty"`
	Assignees          []string          `json:"assignees,omitempty"`
}

type EventLogger struct {
	file   *os.File
	mu     sync.Mutex
	nodeID string
}

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
	})
}

func (l *EventLogger) LogCommandSynced(cmdType string, target string, fromNode string) {
	l.Log(Event{
		EventType: EventCommandSynced,
		Type:      cmdType,
		Target:    target,
		From:      fromNode,
	})
}

func (l *EventLogger) LogJobClaimed(target string, replication int) {
	l.Log(Event{
		EventType:   EventJobClaimed,
		Target:      target,
		Replication: replication,
	})
}

func (l *EventLogger) LogJobReleased(target string) {
	l.Log(Event{
		EventType: EventJobReleased,
		Target:    target,
	})
}

func (l *EventLogger) LogNodeUpdate(from string, jobs []string, capacity, used uint64) {
	l.Log(Event{
		EventType: EventNodeUpdate,
		From:      from,
		Jobs:      jobs,
		Capacity:  capacity,
		Used:      used,
	})
}

func (l *EventLogger) LogReplicationDecision(
	target string,
	shouldClaim bool,
	reason string,
	currentReplication int,
	needed int,
	candidates []string,
	selectedCandidates []string,
	freeSpace map[string]uint64,
) {
	l.Log(Event{
		EventType:          EventReplicationCheck,
		Target:             target,
		ShouldClaim:        shouldClaim,
		Reason:             reason,
		CurrentReplication: currentReplication,
		NeededReplication:  needed,
		Candidates:         candidates,
		SelectedCandidates: selectedCandidates,
		FreeSpace:          freeSpace,
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
		Assignees: assignees,
	})
}

type PacketStats struct {
	SyncInterestsSent   uint64
	DataPacketsSent     uint64
	InterestsReceived   uint64
	DataPacketsReceived uint64
}
