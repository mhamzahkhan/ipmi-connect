package dell

import (
	"bytes"
	"encoding/binary"
	"net"
	"reflect"
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
	payload[11] = 0x32 // high nibble = luma table 3, low nibble = chroma table 2

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

	if pkt.CursorX != 10 || pkt.CursorY != 20 {
		t.Errorf("expected cursor 10,20, got %d,%d", pkt.CursorX, pkt.CursorY)
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
	if pkt.LumaSelector != 3 || pkt.ChromaSelector != 2 {
		t.Errorf("expected luma=3, chroma=2, got luma=%d, chroma=%d", pkt.LumaSelector, pkt.ChromaSelector)
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

// frame builds an ib payload: mode, packed cursor x/y, height, width, flags, selectors, bitstream.
func frame(mode byte, cx, cy, h, w uint16, flags, selectors byte, bitstream ...byte) []byte {
	p := make([]byte, videoFrameHeaderLen, videoFrameHeaderLen+len(bitstream))
	p[0] = mode
	packed := uint32(cx)<<12 | uint32(cy)
	p[1], p[2], p[3] = byte(packed>>16), byte(packed>>8), byte(packed)
	binary.BigEndian.PutUint16(p[4:6], h)
	binary.BigEndian.PutUint16(p[6:8], w)
	p[8] = flags
	p[11] = selectors
	return append(p, bitstream...)
}

func TestParseVideoFrame(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		want    *VideoPacket // nil means "not a video frame"
	}{
		{
			name:    "YUV420 frame: high nibble is luma, low nibble chroma",
			payload: frame(5, 0, 0, 400, 720, 1, 0x74, 0xAA),
			want:    &VideoPacket{Width: 720, Height: 400, Flags: 1, Mode420: 1, LumaSelector: 7, ChromaSelector: 4, Bitstream: []byte{0xAA}},
		},
		{
			name:    "YUV444 frame keeps the hardware cursor position separate from geometry",
			payload: frame(4, 300, 200, 768, 1024, 0, 0x55, 0xBB),
			want:    &VideoPacket{CursorX: 300, CursorY: 200, Width: 1024, Height: 768, Mode420: 0, LumaSelector: 5, ChromaSelector: 5, Bitstream: []byte{0xBB}},
		},
		{name: "mode 2 is a cursor bitmap, not video", payload: frame(2, 1, 1, 32, 32, 0, 0, 0xCC), want: nil},
		{name: "mode 3 is a cursor bitmap, not video", payload: frame(3, 1, 1, 32, 32, 0, 0, 0xCC), want: nil},
		{name: "truncated header is ignored", payload: []byte{5, 0, 0}, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVideoFrame(tt.payload)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
			if got == nil {
				return
			}
			if !bytes.Equal(got.Bitstream, tt.want.Bitstream) {
				t.Errorf("bitstream % x, want % x", got.Bitstream, tt.want.Bitstream)
			}
			got.Bitstream, tt.want.Bitstream = nil, nil
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %+v\nwant %+v", *got, *tt.want)
			}
		})
	}
}
