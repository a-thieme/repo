//go:generate gondn_tlv_gen
package tlv

import enc "github.com/named-data/ndnd/std/encoding"

type Command struct {
	//+field:string
	Type string `tlv:"0x252"`
	//+field:name
	Target enc.Name `tlv:"0x253"`
	//+field:natural
	SnapshotThreshold uint64 `tlv:"0x255"`
}

type InternalCommand struct {
	//+field:string
	Type string `tlv:"0x252"`
	//+field:name
	Target enc.Name `tlv:"0x253"`
	//+field:natural
	SnapshotThreshold uint64 `tlv:"0x255"`
	//+field:natural
	StorageSpace uint64 `tlv:"0x294"`
}

type StatusResponse struct {
	//+field:name
	Target enc.Name `tlv:"0x280"`
	//+field:string
	Status string `tlv:"0x281"`
}

type JobAssignment struct {
	//+field:name
	Target enc.Name `tlv:"0x295"`
	//+field:sequence:enc.Name:name
	Assignees []enc.Name `tlv:"0x296"`
}

type NodeUpdate struct {
	//+field:sequence:enc.Name:name
	Jobs []enc.Name `tlv:"0x290"`
	//+field:struct:Command
	NewCommand *Command `tlv:"0x291"`
	//+field:natural
	StorageCapacity uint64 `tlv:"0x292"`
	//+field:natural
	StorageUsed uint64 `tlv:"0x293"`
	//+field:sequence:*InternalCommand:struct:InternalCommand
	JobRelease []*InternalCommand `tlv:"0x294"`
	//+field:sequence:*JobAssignment:struct:JobAssignment
	JobAssignments []*JobAssignment `tlv:"0x297"`
}

type MetricRequest struct {
	//+field:name
	Target enc.Name `tlv:"0x298"`
	//+field:name
	ResultsName enc.Name `tlv:"0x299"`
	//+field:natural
	Timestamp uint64 `tlv:"0x29A"`
	//+field:name
	Auctioneer enc.Name `tlv:"0x29C"`
}

type MetricResponse struct {
	//+field:natural
	Capacity uint64 `tlv:"0x292"`
	//+field:natural
	Used uint64 `tlv:"0x293"`
	//+field:natural
	Timestamp uint64 `tlv:"0x29A"`
	//+field:bool
	Delay bool `tlv:"0x29B"`
}
