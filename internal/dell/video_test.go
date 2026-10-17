package dell

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestReadVideoPacket_ValidIB(t *testing.T) {
	// Construct simulated packet 134 (ib)
	var buf bytes.Buffer
	buf.WriteString("BEEF")
	_ = binary.Write(&buf, binary.BigEndian, uint16(134))

	// Payload: 12 bytes header + 16 bytes bitstream = 28 bytes. Total pkt len = 36
	totalLen := uint16(36)
	_ = binary.Write(&buf, binary.BigEndian, totalLen)

	payload := make([]byte, 28)
	payload[0] = 1 // mode420 = 1

	// x = 10, y = 20 -> n3 = (10 << 12) | 20 = 0x00A014
	payload[1] = 0x00
	payload[2] = 0xA0
	payload[3] = 0x14

	// Height = 400, Width = 720
	binary.BigEndian.PutUint16(payload[4:6], 400)
	binary.BigEndian.PutUint16(payload[6:8], 720)

	payload[8] = 0x85  // flags (isFirst = true)
	payload[11] = 0x32 // advanceSelector = 3, selector = 2

	// 16 bytes dummy bitstream
	for i := 12; i < 28; i++ {
		payload[i] = byte(i)
	}
	buf.Write(payload)

	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	client := &Client{
		VideoConn: clientConn,
	}

	go func() {
		_, _ = serverConn.Write(buf.Bytes())
		// Read ACK packet back
		ackBuf := make([]byte, 16)
		_, _ = serverConn.Read(ackBuf)
	}()

	pkt, err := client.ReadVideoPacketWithTimeout(1 * time.Second)
	if err != nil {
		t.Fatalf("ReadVideoPacket error: %v", err)
	}
	if pkt == nil {
		t.Fatal("expected non-nil packet")
	}

	if pkt.X != 10 || pkt.Y != 20 {
		t.Errorf("expected X=10, Y=20, got X=%d, Y=%d", pkt.X, pkt.Y)
	}
	if pkt.Width != 720 || pkt.Height != 400 {
		t.Errorf("expected 720x400, got %dx%d", pkt.Width, pkt.Height)
	}
	if pkt.Flags != 0x85 {
		t.Errorf("expected flags 0x85, got 0x%02x", pkt.Flags)
	}
	if pkt.Mode420 != 1 {
		t.Errorf("expected Mode420=1, got %d", pkt.Mode420)
	}
	if pkt.Selector != 2 || pkt.AdvanceSelector != 3 {
		t.Errorf("expected sel=2, advSel=3, got sel=%d, advSel=%d", pkt.Selector, pkt.AdvanceSelector)
	}
	if len(pkt.Bitstream) != 16 {
		t.Errorf("expected 16 bytes bitstream, got %d", len(pkt.Bitstream))
	}
}

func TestReadVideoPacket_InvalidMagic(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	client := &Client{
		VideoConn: clientConn,
	}

	go func() {
		_, _ = serverConn.Write([]byte("NOPE1234"))
	}()

	_, err := client.ReadVideoPacketWithTimeout(500 * time.Millisecond)
	if err == nil {
		t.Fatal("expected error for invalid magic, got nil")
	}
}
