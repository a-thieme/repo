package util

import (
	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/ndn"
	"sync/atomic"
)

type CountingFace struct {
	inner       ndn.Face
	eventLogger *EventLogger
	stats       PacketStats
}

func NewCountingFace(inner ndn.Face, eventLogger *EventLogger) *CountingFace {
	return &CountingFace{
		inner:       inner,
		eventLogger: eventLogger,
	}
}

func (f *CountingFace) String() string {
	return f.inner.String()
}

func (f *CountingFace) IsRunning() bool {
	return f.inner.IsRunning()
}

func (f *CountingFace) IsLocal() bool {
	return f.inner.IsLocal()
}

func (f *CountingFace) OnPacket(onPkt func(frame []byte)) {
	f.inner.OnPacket(func(frame []byte) {
		pktType := ParseTLVType(enc.Wire{frame})
		switch pktType {
		case TlvInterest:
			atomic.AddUint64(&f.stats.InterestsReceived, 1)
			if f.eventLogger != nil {
				name := parsePacketName(enc.Wire{frame})
				if name != "" {
					f.eventLogger.LogInterestReceived(name, f.stats.InterestsReceived)
				}
			}
		case TlvData:
			atomic.AddUint64(&f.stats.DataPacketsReceived, 1)
			if f.eventLogger != nil {
				name := parsePacketName(enc.Wire{frame})
				if name != "" {
					f.eventLogger.LogDataReceived(name, f.stats.DataPacketsReceived)
				}
			}
		}
		onPkt(frame)
	})
}

func (f *CountingFace) OnError(onError func(err error)) {
	f.inner.OnError(onError)
}

func (f *CountingFace) Open() error {
	return f.inner.Open()
}

func (f *CountingFace) Close() error {
	return f.inner.Close()
}

func (f *CountingFace) Send(pkt enc.Wire) error {
	pktType := ParseTLVType(pkt)
	var name string

	switch pktType {
	case TlvInterest:
		atomic.AddUint64(&f.stats.SyncInterestsSent, 1)
		if f.eventLogger != nil {
			f.eventLogger.LogSyncInterestSent(f.stats.SyncInterestsSent)
		}
	case TlvData:
		atomic.AddUint64(&f.stats.DataPacketsSent, 1)
		name = parsePacketName(pkt)
		if f.eventLogger != nil && name != "" {
			f.eventLogger.LogDataSent(name, f.stats.DataPacketsSent)
		}
	case TlvLpPacket:
		name = ParseLpPacketDataName(pkt)
		if name != "" {
			atomic.AddUint64(&f.stats.DataPacketsSent, 1)
			if f.eventLogger != nil {
				f.eventLogger.LogDataSent(name, f.stats.DataPacketsSent)
			}
		}
	}

	return f.inner.Send(pkt)
}

func (f *CountingFace) OnUp(onUp func()) (cancel func()) {
	return f.inner.OnUp(onUp)
}

func (f *CountingFace) OnDown(onDown func()) (cancel func()) {
	return f.inner.OnDown(onDown)
}

func (f *CountingFace) GetStats() PacketStats {
	return PacketStats{
		SyncInterestsSent:   atomic.LoadUint64(&f.stats.SyncInterestsSent),
		DataPacketsSent:     atomic.LoadUint64(&f.stats.DataPacketsSent),
		InterestsReceived:   atomic.LoadUint64(&f.stats.InterestsReceived),
		DataPacketsReceived: atomic.LoadUint64(&f.stats.DataPacketsReceived),
	}
}

const (
	TlvInterest = 0x05
	TlvData     = 0x06
	TlvLpPacket = 0x64
	TlvFragment = 0x50
)

func ParseTLVType(wire enc.Wire) uint8 {
	if len(wire) == 0 {
		return 0
	}
	buf := wire.Join()
	if len(buf) == 0 {
		return 0
	}
	return buf[0]
}

func parsePacketName(wire enc.Wire) string {
	buf := wire.Join()
	if len(buf) == 0 {
		return ""
	}
	pktType := buf[0]
	if pktType != TlvInterest && pktType != TlvData {
		return ""
	}
	pos := 0
	tlvType, tlvLen, newPos := parseTLV(buf, pos)
	if tlvType == 0 {
		return ""
	}
	pos = newPos
	if pktType == TlvData {
		for pos < len(buf) && pos < int(tlvLen)+newPos {
			innerType, innerLen, innerStart := parseTLV(buf, pos)
			if innerType == 0x07 && innerStart+int(innerLen) <= len(buf) {
				if name, err := enc.NameFromBytes(buf[pos : innerStart+int(innerLen)]); err == nil {
					return name.String()
				}
			}
			pos = innerStart + int(innerLen)
		}
	} else {
		if pos < len(buf) {
			nameType, nameLen, nameStart := parseTLV(buf, pos)
			if nameType == 0x07 && nameStart+int(nameLen) <= len(buf) {
				if name, err := enc.NameFromBytes(buf[pos : nameStart+int(nameLen)]); err == nil {
					return name.String()
				}
			}
		}
	}
	return ""
}

func ParseLpPacketDataName(wire enc.Wire) string {
	buf := wire.Join()
	if len(buf) == 0 || buf[0] != TlvLpPacket {
		return ""
	}
	pos := 0
	tlvType, tlvLen, startPos := parseTLV(buf, pos)
	if tlvType != TlvLpPacket {
		return ""
	}
	pos = startPos
	endPos := startPos + int(tlvLen)
	for pos < endPos && pos < len(buf) {
		fieldType, fieldLen, fieldStart := parseTLV(buf, pos)
		if fieldType == 0 {
			break
		}
		if fieldType == TlvFragment && fieldStart+int(fieldLen) <= len(buf) {
			fragment := buf[fieldStart : fieldStart+int(fieldLen)]
			if len(fragment) > 0 && fragment[0] == TlvData {
				return parseDataName(fragment)
			}
		}
		pos = fieldStart + int(fieldLen)
	}
	return ""
}

func parseDataName(buf []byte) string {
	if len(buf) == 0 || buf[0] != TlvData {
		return ""
	}
	tlvType, tlvLen, pos := parseTLV(buf, 0)
	if tlvType != TlvData {
		return ""
	}
	endPos := pos + int(tlvLen)
	for pos < endPos && pos < len(buf) {
		innerType, innerLen, innerStart := parseTLV(buf, pos)
		if innerType == 0x07 && innerStart+int(innerLen) <= len(buf) {
			if name, err := enc.NameFromBytes(buf[pos : innerStart+int(innerLen)]); err == nil {
				return name.String()
			}
		}
		pos = innerStart + int(innerLen)
	}
	return ""
}

func parseTLV(buf []byte, pos int) (tlvType uint64, tlvLen uint64, newPos int) {
	if pos >= len(buf) {
		return 0, 0, pos
	}

	if buf[pos] < 0xFD {
		tlvType = uint64(buf[pos])
		pos++
	} else {
		tlvType = uint64(buf[pos] & 0x03)
		pos++
		for pos < len(buf) && buf[pos]&0x80 != 0 {
			tlvType = (tlvType << 7) | uint64(buf[pos]&0x7F)
			pos++
		}
	}

	if pos >= len(buf) {
		return 0, 0, pos
	}

	if buf[pos] < 0xFD {
		tlvLen = uint64(buf[pos])
		pos++
	} else {
		tlvLen = uint64(buf[pos] & 0x03)
		pos++
		for pos < len(buf) && buf[pos-1]&0x80 != 0 {
			tlvLen = (tlvLen << 7) | uint64(buf[pos]&0x7F)
			pos++
		}
	}

	return tlvType, tlvLen, pos
}
