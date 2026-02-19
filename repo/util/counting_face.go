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
	f.inner.OnPacket(onPkt)
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
	name := parsePacketName(pkt)

	switch pktType {
	case TlvInterest:
		atomic.AddUint64(&f.stats.SyncInterestsSent, 1)
		if f.eventLogger != nil {
			f.eventLogger.LogSyncInterestSent(f.stats.SyncInterestsSent)
		}
	case TlvData:
		atomic.AddUint64(&f.stats.DataPacketsSent, 1)
		if f.eventLogger != nil && name != "" {
			f.eventLogger.LogDataSent(name, f.stats.DataPacketsSent)
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
		SyncInterestsSent: atomic.LoadUint64(&f.stats.SyncInterestsSent),
		DataPacketsSent:   atomic.LoadUint64(&f.stats.DataPacketsSent),
	}
}

const (
	TlvInterest = 0x05
	TlvData     = 0x06
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
			innerType, innerLen, newPos := parseTLV(buf, pos)
			if innerType == 0x07 {
				if name, err := enc.NameFromBytes(buf[newPos : newPos+int(innerLen)]); err == nil {
					return name.String()
				}
			}
			pos = newPos + int(innerLen)
		}
	} else {
		if pos < len(buf) {
			nameType, nameLen, namePos := parseTLV(buf, pos)
			if nameType == 0x07 {
				if name, err := enc.NameFromBytes(buf[namePos : namePos+int(nameLen)]); err == nil {
					return name.String()
				}
			}
		}
	}
	return ""
}

func parseTLV(buf []byte, pos int) (tlvType uint64, tlvLen uint64, newPos int) {
	if pos >= len(buf) {
		return 0, 0, pos
	}
	typ := uint64(buf[pos] & 0x1F)
	pos++
	for pos < len(buf) && buf[pos]&0x80 != 0 {
		typ = (typ << 7) | uint64(buf[pos]&0x7F)
		pos++
	}
	if pos >= len(buf) {
		return 0, 0, pos
	}
	length := uint64(buf[pos] & 0x7F)
	pos++
	if buf[pos-1]&0x80 != 0 {
		for pos < len(buf) && buf[pos-1]&0x80 != 0 {
			length = (length << 7) | uint64(buf[pos]&0x7F)
			pos++
		}
	}
	return typ, length, pos
}
