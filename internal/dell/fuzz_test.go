package dell

import (
	"bytes"
	"net"
	"testing"
	"time"
)

// streamConn is a net.Conn that reads a fixed stream and ignores deadlines and writes.
type streamConn struct {
	net.Conn // nil: unused methods panic
	r        *bytes.Reader
}

func (c *streamConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *streamConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

// FuzzReadPacket reads framed packets from arbitrary BMC bytes until the stream ends, and parses
// any video frame among them.
func FuzzReadPacket(f *testing.F) {
	f.Add(beefPacket(pktHeartbeat, [8]byte{7}))
	f.Add(zeroMagicPacket(pktVideoAuthResp, []byte{0, 0}))
	f.Add(longPacket(pktVideoFrame, frame(5, 10, 20, 400, 720, 1, 0x32, 1, 2, 3, 4)))
	f.Add(append(credentialReply("user", "pass"), []byte("BEEF\x00\x01\x00\x04")...))
	f.Add([]byte("BEEF\x00\x01\xff\xff"))
	f.Fuzz(func(t *testing.T, data []byte) {
		conn := &streamConn{r: bytes.NewReader(data)}
		for {
			pkt, err := readPacket(conn, time.Second)
			if err != nil {
				return
			}
			if len(pkt.Payload) > 0xFFFF-beefHeaderLen {
				t.Fatalf("payload of %d bytes", len(pkt.Payload))
			}
			if pkt.ID == pktVideoFrame {
				checkVideoFrame(t, pkt.Payload)
			}
		}
	})
}

func checkVideoFrame(t *testing.T, p []byte) {
	v := parseVideoFrame(p)
	if v == nil {
		return
	}
	if v.Flags&1 != 0 && (v.Width > maxVideoSide || v.Height > maxVideoSide) {
		t.Fatalf("first chunk of %dx%d accepted", v.Width, v.Height)
	}
	if len(v.Bitstream) != len(p)-videoFrameHeaderLen || v.Mode420 > 1 || v.LumaSelector > 15 || v.ChromaSelector > 15 ||
		v.CursorX > 0xFFF || v.CursorY > 0xFFF {
		t.Fatalf("parsed %+v from %d bytes", *v, len(p))
	}
}

// FuzzParseVideoFrame parses arbitrary ib payloads.
func FuzzParseVideoFrame(f *testing.F) {
	f.Add(frame(5, 0, 0, 400, 720, 1, 0x74, 0xAA))
	f.Add(frame(4, 300, 200, 768, 1024, 0, 0x55, 0xBB))
	f.Add(frame(2, 1, 1, 32, 32, 0, 0, 0xCC))
	f.Add(frame(5, 0, 0, 0xFFFF, 0xFFFF, 1, 0, 0xAA))
	f.Add([]byte{5, 0, 0})
	f.Fuzz(func(t *testing.T, p []byte) { checkVideoFrame(t, p) })
}

// FuzzParseVMCredentials parses arbitrary 0x8434 payloads.
func FuzzParseVMCredentials(f *testing.F) {
	f.Add(credentialReply("user", "pass")[beefHeaderLen:])
	f.Add(append([]byte("u \x00junk"), make([]byte, 60)...))
	f.Add(make([]byte, 64))
	f.Add(make([]byte, 40))
	f.Fuzz(func(t *testing.T, p []byte) {
		user, password, err := parseVMCredentials(p)
		if err != nil {
			return
		}
		for _, s := range []string{user, password} {
			if s == "" || len(s) > vmTokenField || bytes.IndexByte([]byte(s), 0) >= 0 {
				t.Fatalf("token %q from % x", s, p)
			}
		}
	})
}
