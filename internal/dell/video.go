package dell

import (
	"encoding/binary"
	"fmt"
	"sync/atomic"
	"time"
)

// VideoPacket is one video frame packet: an ASPEED bitstream that updates the whole frame.
//
// Field meanings follow the C6220's own viewer: the bitstream always describes the full Width x Height frame, drawn at
// the origin; bytes 1-3 carry the hardware cursor position, not a tile offset.
type VideoPacket struct {
	CursorX        uint16
	CursorY        uint16
	Width          uint16
	Height         uint16
	Flags          byte // bit 0: first chunk of a frame
	Mode420        byte // 1 = YUV420, 0 = YUV444
	LumaSelector   byte // quantisation table for Y (high nibble of byte 11)
	ChromaSelector byte // quantisation table for Cb/Cr (low nibble of byte 11)
	Bitstream      []byte
}

// Video modes 2 and 3 carry hardware-cursor bitmaps rather than frame data.
const (
	videoModeCursorA = 2
	videoModeCursorB = 3
)

// ReadVideoPacket reads and decodes the next video packet from the Video Channel with a 3-second timeout.
func (c *Client) ReadVideoPacket() (*VideoPacket, error) {
	return c.ReadVideoPacketWithTimeout(packetBodyTimeout)
}

// ReadVideoPacketWithTimeout reads the next packet from the Video Channel, waiting up to timeout
// for it to start. It returns (nil, nil) for packets that are not video frames.
func (c *Client) ReadVideoPacketWithTimeout(timeout time.Duration) (*VideoPacket, error) {
	c.ConnMu.Lock()
	conn := c.VideoConn
	c.ConnMu.Unlock()

	if conn == nil {
		return nil, fmt.Errorf("video channel is not connected")
	}

	pkt, err := readPacket(conn, timeout)
	if err != nil {
		return nil, err
	}
	if pkt.ID != pktVideoFrame {
		return nil, nil
	}

	if err := c.ackVideoPacket(); err != nil {
		// Without ACKs the BMC stops streaming; surface it so the caller reconnects. Not wrapped,
		// so a write timeout is not mistaken for an idle read timeout.
		return nil, fmt.Errorf("video ACK failed: %v", err)
	}
	return parseVideoFrame(pkt.Payload), nil
}

// ackVideoPacket acknowledges one ib packet. Payload byte 0 is a batch count, always 1; a rolling
// counter stalls the AST2300 when it wraps to 0 on the 256th packet.
func (c *Client) ackVideoPacket() error {
	atomic.AddUint32(&c.AckCount, 1)
	return c.writeVideo(beefPacket(pktVideoAck, [8]byte{1}))
}

// parseVideoFrame decodes an ib payload, returning nil for cursor bitmaps and short payloads.
func parseVideoFrame(p []byte) *VideoPacket {
	if len(p) < videoFrameHeaderLen {
		return nil
	}
	mode := p[0]
	if mode == videoModeCursorA || mode == videoModeCursorB {
		return nil
	}

	cursor := uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
	return &VideoPacket{
		CursorX:        uint16(cursor >> 12 & 0xFFF),
		CursorY:        uint16(cursor & 0xFFF),
		Height:         binary.BigEndian.Uint16(p[4:6]),
		Width:          binary.BigEndian.Uint16(p[6:8]),
		Flags:          p[8],
		Mode420:        mode & 1,
		LumaSelector:   p[11] >> 4,
		ChromaSelector: p[11] & 0x0F,
		Bitstream:      p[videoFrameHeaderLen:],
	}
}
