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

type NodeUpdate struct {
	//+field:sequence:enc.Name:name
	Jobs []enc.Name `tlv:"0x290"`
	//+field:struct:Command
	NewCommand *Command `tlv:"0x291"`
	//+field:natural
	StorageCapacity uint64 `tlv:"0x292"`
	//+field:natural
	StorageUsed uint64 `tlv:"0x293"`
	//+field:struct:InternalCommand
	JobRelease *InternalCommand `tlv:"0x294"`
}
