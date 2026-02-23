package testutil

import (
	"bufio"
	"encoding/json"
	"os"
	"time"

	enc "github.com/named-data/ndnd/std/encoding"
)

type EventType string

const (
	EventSyncInterestSent EventType = "sync_interest_sent"
	EventDataSent         EventType = "data_sent"
	EventCommandReceived  EventType = "command_received"
	EventCommandSynced    EventType = "command_synced"
	EventJobClaimed       EventType = "job_claimed"
	EventJobReleased      EventType = "job_released"
	EventNodeUpdate       EventType = "node_update"
	EventReplicationCheck EventType = "replication_check"
	EventStorageChanged   EventType = "storage_changed"
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
}

func ParseEventLog(path string) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var event Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			continue
		}
		events = append(events, event)
	}
	return events, scanner.Err()
}

func FilterEvents(events []Event, eventType EventType) []Event {
	var filtered []Event
	for _, e := range events {
		if e.EventType == eventType {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func FilterEventsByNode(events []Event, node string) []Event {
	var filtered []Event
	for _, e := range events {
		if e.Node == node {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func FilterEventsByTarget(events []Event, target string) []Event {
	var filtered []Event
	for _, e := range events {
		if e.Target == target {
			filtered = append(filtered, e)
		}
	}
	return filtered
}

func GetLatestPacketStats(events []Event) (syncInterests uint64, dataPackets uint64) {
	for _, e := range events {
		if e.EventType == EventSyncInterestSent {
			if e.Total > syncInterests {
				syncInterests = e.Total
			}
		}
		if e.EventType == EventDataSent {
			if e.Total > dataPackets {
				dataPackets = e.Total
			}
		}
	}
	return
}

func CountJobClaims(events []Event, target string) int {
	claims := FilterEventsByTarget(events, target)
	claims = FilterEvents(claims, EventJobClaimed)
	return len(claims)
}

func GetNodesWithJob(events []Event, target string) []string {
	claims := FilterEventsByTarget(events, target)
	claims = FilterEvents(claims, EventJobClaimed)
	nodes := make(map[string]bool)
	for _, c := range claims {
		nodes[c.Node] = true
	}
	result := make([]string, 0, len(nodes))
	for n := range nodes {
		result = append(result, n)
	}
	return result
}

func WaitForEvents(path string, count int, timeout time.Duration) ([]Event, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		events, err := ParseEventLog(path)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if len(events) >= count {
			return events, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return ParseEventLog(path)
}

func TargetFromName(name string) enc.Name {
	n, _ := enc.NameFromStr(name)
	return n
}

type ReplicationState struct {
	Timestamp time.Time
	Count     int
	Nodes     []string
}

func ComputeGlobalReplicationAtTime(events []Event, target string, beforeTime time.Time) int {
	claimed := make(map[string]bool)

	for _, e := range events {
		if e.Timestamp.After(beforeTime) {
			continue
		}
		if e.Target != target {
			continue
		}
		if e.EventType == EventJobClaimed {
			claimed[e.Node] = true
		}
		if e.EventType == EventJobReleased {
			delete(claimed, e.Node)
		}
	}
	return len(claimed)
}

func ComputeGlobalReplicationTimeline(events []Event, target string) []ReplicationState {
	var states []ReplicationState
	claimed := make(map[string]bool)

	for _, e := range events {
		if e.Target != target {
			continue
		}
		if e.EventType == EventJobClaimed {
			claimed[e.Node] = true
		}
		if e.EventType == EventJobReleased {
			delete(claimed, e.Node)
		}

		nodes := make([]string, 0, len(claimed))
		for n := range claimed {
			nodes = append(nodes, n)
		}
		states = append(states, ReplicationState{
			Timestamp: e.Timestamp,
			Count:     len(claimed),
			Nodes:     nodes,
		})
	}
	return states
}
