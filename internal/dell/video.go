package dell

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync/atomic"
	"time"
)

// VideoPacket contains an Aspeed video bitstream chunk and its geometry.
type VideoPacket struct {
	X               uint16
	Y               uint16
	Width           uint16
	Height          uint16
	Flags           byte
	Mode420         byte
	Selector        byte
	AdvanceSelector byte
	Bitstream       []byte
}

// ReadVideoPacket reads and decodes the next video packet from the Video Channel with a 3-second timeout.
func (c *Client) ReadVideoPacket() (*VideoPacket, error) {
	return c.ReadVideoPacketWithTimeout(packetBodyTimeout)
}

// ReadVideoPacketWithTimeout reads and decodes the next video packet from the Video Channel
// with a caller-specified read timeout.
func (c *Client) ReadVideoPacketWithTimeout(timeout time.Duration) (*VideoPacket, error) {
	c.ConnMu.Lock()
	conn := c.VideoConn
	c.ConnMu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("video channel is not connected")
	}

	frameHeader := make([]byte, beefHeaderLen)
	// Read the first byte with the caller's timeout. If no packet is arriving, this times
	// out without consuming any bytes from the TCP stream, preventing desynchronization.
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	if _, err := io.ReadFull(conn, frameHeader[:1]); err != nil {
		return nil, err
	}

	// Once the packet has started arriving, allow generous time to read the rest of it.
	_ = conn.SetReadDeadline(time.Now().Add(packetBodyTimeout))
	if _, err := io.ReadFull(conn, frameHeader[1:]); err != nil {
		return nil, err
	}

	magic := binary.BigEndian.Uint32(frameHeader[:4])
	if magic != videoFrameMagic && string(frameHeader[:4]) != "BEEF" {
		return nil, fmt.Errorf("unexpected video frame magic: 0x%08x (%s)", magic, string(frameHeader[:4]))
	}

	fID := binary.BigEndian.Uint16(frameHeader[4:6])
	fLen := binary.BigEndian.Uint16(frameHeader[6:8])
	if fLen < beefHeaderLen {
		return nil, fmt.Errorf("invalid packet length: %d", fLen)
	}

	payloadLen := int(fLen) - beefHeaderLen
	fPayload := make([]byte, payloadLen)
	if _, err := io.ReadFull(conn, fPayload); err != nil {
		return nil, err
	}

	if fID == pktVideoFrame && len(fPayload) >= videoFrameHeaderLen {
		atomic.AddUint32(&c.AckCount, 1)
		// Payload byte 0 is a batch count, always 1. A rolling counter stalls the AST2300
		// when it wraps to 0 on the 256th packet.
		ackPkt := beefPacket(pktVideoAck, [8]byte{1})
		c.ConnMu.Lock()
		if c.VideoConn != nil {
			_ = c.VideoConn.SetWriteDeadline(time.Now().Add(socketWriteTimeout))
			_, _ = c.VideoConn.Write(ackPkt)
		}
		c.ConnMu.Unlock()

		mode := fPayload[0]
		mode420 := mode & 1

		n3 := (uint32(fPayload[1]) << 16) | (uint32(fPayload[2]) << 8) | uint32(fPayload[3])
		x := uint16((n3 >> 12) & 0xFFF)
		y := uint16(n3 & 0xFFF)

		// Note: in ib packet, byte 4..5 is height (rows) and byte 6..7 is width (columns)
		h := binary.BigEndian.Uint16(fPayload[4:6])
		w := binary.BigEndian.Uint16(fPayload[6:8])

		flags := fPayload[8]
		selector := fPayload[11] & 0x0F
		advanceSelector := (fPayload[11] >> 4) & 0x0F

		bitstream := fPayload[videoFrameHeaderLen:]

		return &VideoPacket{
			X:               x,
			Y:               y,
			Width:           w,
			Height:          h,
			Flags:           flags,
			Mode420:         mode420,
			Selector:        selector,
			AdvanceSelector: advanceSelector,
			Bitstream:       bitstream,
		}, nil
	}

	return nil, nil
}
