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

type StatusResponse struct {
	//+field:name
	Target enc.Name `tlv:"0x280"`
	//+field:string
	Status string `tlv:"0x281"`
}
