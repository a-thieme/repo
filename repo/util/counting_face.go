package util

import (
	enc "github.com/named-data/ndnd/std/encoding"
	"github.com/named-data/ndnd/std/ndn"
	"sync/atomic"
)

type CountingFace struct {
	inner        ndn.Face
	eventLogger  Logger
	syncPrefixes []string
	stats        PacketStats
}

func NewCountingFace(inner ndn.Face, eventLogger Logger, syncPrefixes []string) *CountingFace {
	return &CountingFace{
		inner:        inner,
		eventLogger:  eventLogger,
		syncPrefixes: syncPrefixes,
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

func (f *CountingFace) OnError(onError func(err error)) {
	f.inner.OnError(onError)
}

func (f *CountingFace) Open() error {
	return f.inner.Open()
}

func (f *CountingFace) Close() error {
	return f.inner.Close()
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

func (f *CountingFace) OnPacket(onPkt func(frame []byte)) {
	f.inner.OnPacket(func(frame []byte) {
		pktType, name := ExtractPacketInfo(enc.Wire{frame})
		switch pktType {
		case TlvInterest:
			count := atomic.AddUint64(&f.stats.InterestsReceived, 1)
			if f.eventLogger != nil && name != "" {
				f.eventLogger.LogInterestReceived(name, count)
			}
		case TlvData:
			count := atomic.AddUint64(&f.stats.DataPacketsReceived, 1)
			if f.eventLogger != nil && name != "" {
				f.eventLogger.LogDataReceived(name, count)
			}
		}
		onPkt(frame)
	})
}

func (f *CountingFace) Send(pkt enc.Wire) error {
	pktType, name := ExtractPacketInfo(pkt)

	switch pktType {
	case TlvInterest:
		if name != "" && len(f.syncPrefixes) > 0 && hasAnyPrefix(name, f.syncPrefixes) {
			count := atomic.AddUint64(&f.stats.SyncInterestsSent, 1)
			if f.eventLogger != nil {
				f.eventLogger.LogSyncInterestSent(count)
			}
		}
	case TlvData:
		if name != "" {
			count := atomic.AddUint64(&f.stats.DataPacketsSent, 1)
			if f.eventLogger != nil {
				f.eventLogger.LogDataSent(name, count)
			}
		}
	}

	return f.inner.Send(pkt)
}

// ExtractPacketInfo unwraps LpPackets if necessary and returns the underlying
// packet type (TlvInterest or TlvData) and its URI-encoded name string.
func ExtractPacketInfo(wire enc.Wire) (pktType uint8, name string) {
	buf := wire.Join()
	if len(buf) == 0 {
		return 0, ""
	}
	pktType = buf[0]
	if pktType == TlvLpPacket {
		pos := 0
		tlvType, tlvLen, startPos := parseTLV(buf, pos)
		if tlvType != TlvLpPacket {
			return 0, ""
		}
		pos = startPos
		endPos := startPos + int(tlvLen)
		for pos < endPos && pos < len(buf) {
			fieldType, fieldLen, fieldStart := parseTLV(buf, pos)
			if fieldType == 0 || fieldStart == len(buf) {
				break
			}
			if fieldType == TlvFragment && fieldStart+int(fieldLen) <= len(buf) {
				fragment := buf[fieldStart : fieldStart+int(fieldLen)]
				if len(fragment) > 0 {
					innerType := fragment[0]
					if innerType == TlvInterest || innerType == TlvData {
						return innerType, parsePacketName(enc.Wire{fragment})
					}
				}
			}
			pos = fieldStart + int(fieldLen)
		}
		return 0, ""
	} else if pktType == TlvInterest || pktType == TlvData {
		return pktType, parsePacketName(wire)
	}
	return pktType, ""
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
	if tlvType == 0 || newPos == len(buf) {
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

// readVarNum parses NDN TLV VAR-NUMBER format (1, 3, 5, or 9 bytes)
func readVarNum(buf []byte, pos int) (uint64, int) {
	if pos >= len(buf) {
		return 0, -1
	}
	first := buf[pos]
	if first < 253 {
		return uint64(first), pos + 1
	} else if first == 253 {
		if pos+3 > len(buf) {
			return 0, -1
		}
		val := uint64(buf[pos+1])<<8 | uint64(buf[pos+2])
		return val, pos + 3
	} else if first == 254 {
		if pos+5 > len(buf) {
			return 0, -1
		}
		val := uint64(buf[pos+1])<<24 | uint64(buf[pos+2])<<16 | uint64(buf[pos+3])<<8 | uint64(buf[pos+4])
		return val, pos + 5
	} else {
		if pos+9 > len(buf) {
			return 0, -1
		}
		val := uint64(buf[pos+1])<<56 | uint64(buf[pos+2])<<48 | uint64(buf[pos+3])<<40 | uint64(buf[pos+4])<<32 |
			uint64(buf[pos+5])<<24 | uint64(buf[pos+6])<<16 | uint64(buf[pos+7])<<8 | uint64(buf[pos+8])
		return val, pos + 9
	}
}

func parseTLV(buf []byte, pos int) (tlvType uint64, tlvLen uint64, newPos int) {
	tlvType, pos = readVarNum(buf, pos)
	if pos == -1 {
		return 0, 0, len(buf)
	}
	tlvLen, pos = readVarNum(buf, pos)
	if pos == -1 {
		return 0, 0, len(buf)
	}
	return tlvType, tlvLen, pos
}

func hasPrefix(name, prefix string) bool {
	return len(name) >= len(prefix) && name[:len(prefix)] == prefix
}

func hasAnyPrefix(name string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if hasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
