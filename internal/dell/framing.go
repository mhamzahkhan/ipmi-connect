package dell

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// rawPacket is one framed packet: 8-byte header (magic, u16 ID, u16 total length) plus payload.
type rawPacket struct {
	ID      uint16
	Payload []byte
}

// readPacket reads one framed packet. Only the first byte waits up to idle: a timeout there
// consumes nothing, so the stream stays aligned. Once a packet has started, the rest of it gets
// packetBodyTimeout, because abandoning a half-read packet would desynchronise the stream.
func readPacket(conn net.Conn, idle time.Duration) (rawPacket, error) {
	header := make([]byte, beefHeaderLen)

	_ = conn.SetReadDeadline(time.Now().Add(idle))
	if _, err := io.ReadFull(conn, header[:1]); err != nil {
		return rawPacket{}, err
	}

	_ = conn.SetReadDeadline(time.Now().Add(packetBodyTimeout))
	if _, err := io.ReadFull(conn, header[1:]); err != nil {
		return rawPacket{}, err
	}

	magic := binary.BigEndian.Uint32(header[:4])
	if magic != videoFrameMagic && string(header[:4]) != "BEEF" {
		return rawPacket{}, fmt.Errorf("unexpected packet magic: 0x%08x", magic)
	}

	length := binary.BigEndian.Uint16(header[6:8])
	if length < beefHeaderLen {
		return rawPacket{}, fmt.Errorf("invalid packet length: %d", length)
	}

	payload := make([]byte, int(length)-beefHeaderLen)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return rawPacket{}, err
	}
	return rawPacket{ID: binary.BigEndian.Uint16(header[4:6]), Payload: payload}, nil
}

// maxPreludePackets bounds how many unrelated packets we skip while waiting for an auth reply.
const maxPreludePackets = 16

// awaitPacket reads packets until want matches one, skipping notifications the BMC may send
// first. Each read waits up to apcpHandshakeTimeout.
func awaitPacket(conn net.Conn, want func(id uint16) bool) (rawPacket, error) {
	for i := 0; i < maxPreludePackets; i++ {
		pkt, err := readPacket(conn, apcpHandshakeTimeout)
		if err != nil {
			return rawPacket{}, err
		}
		if want(pkt.ID) {
			return pkt, nil
		}
	}
	return rawPacket{}, fmt.Errorf("no expected reply within %d packets", maxPreludePackets)
}

// isTimeout reports whether err is a network timeout (as opposed to EOF or a protocol error).
func isTimeout(err error) bool {
	netErr, ok := err.(net.Error)
	return ok && netErr.Timeout()
}
