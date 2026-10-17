package supermicro

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
)

// serverInit builds ServerInit with ATEN's 12-byte trailer.
func serverInit(name string, sessionID uint32, video, km, kick, vmedia byte) []byte {
	b := make([]byte, 24)
	binary.BigEndian.PutUint16(b[0:2], 1024) // placeholder size
	binary.BigEndian.PutUint16(b[2:4], 768)
	binary.BigEndian.PutUint32(b[20:24], uint32(len(name)))
	b = append(b, name...)
	trailer := make([]byte, 12)
	binary.BigEndian.PutUint32(trailer[4:8], sessionID)
	trailer[8], trailer[9], trailer[10], trailer[11] = video, km, kick, vmedia
	return append(b, trailer...)
}

// fakeHandshake plays the X7SB3's side of the handshake and records what the client sends.
func fakeHandshake(t *testing.T, secTypes []byte, authResult uint32, after []byte) (*bufio.Reader, io.Writer, *bytes.Buffer) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	sent := &bytes.Buffer{}
	go func() {
		_, _ = server.Write([]byte("RFB 003.008\n"))
		buf := make([]byte, 12)
		_, _ = io.ReadFull(server, buf)
		sent.Write(buf)
		_, _ = server.Write(append([]byte{byte(len(secTypes))}, secTypes...))
		if len(secTypes) == 0 {
			_, _ = server.Write([]byte{0, 0, 0, 4, 'n', 'o', 'p', 'e'})
			return
		}
		choice := make([]byte, 1)
		_, _ = io.ReadFull(server, choice)
		sent.Write(choice)
		_, _ = server.Write(bytes.Repeat([]byte{0xa7}, 24)) // challenge, ignored
		cred := make([]byte, 48)
		_, _ = io.ReadFull(server, cred)
		sent.Write(cred)
		res := make([]byte, 4)
		binary.BigEndian.PutUint32(res, authResult)
		_, _ = server.Write(res)
		if authResult != 0 {
			_, _ = server.Write([]byte{0, 0, 0, 7, 'r', 'e', 'f', 'u', 's', 'e', 'd'})
			return
		}
		ci := make([]byte, 1)
		_, _ = io.ReadFull(server, ci)
		sent.Write(ci)
		_, _ = server.Write(after)
	}()
	return bufio.NewReader(client), client, sent
}

func TestHandshake(t *testing.T) {
	r, w, sent := fakeHandshake(t, []byte{0x10}, 0, serverInit("ATEN iKVM Server", 0x1234, 1, 1, 1, 1))

	info, err := handshake(r, w, Ticket{User: "tok", Password: "tok"})
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if info.Name != "ATEN iKVM Server" || info.SessionID != 0x1234 || !info.Video || !info.KeyboardMouse {
		t.Errorf("info %+v", info)
	}
	got := sent.Bytes()
	if string(got[:12]) != "RFB 003.008\n" || got[12] != 0x10 || string(got[13:16]) != "tok" || got[13+24] != 't' || got[len(got)-1] != 0x00 {
		t.Errorf("client sent % x", got)
	}
}

func TestHandshakeFallsBackToLastSecurityType(t *testing.T) {
	r, w, sent := fakeHandshake(t, []byte{0x01, 0x02}, 0, serverInit("x", 1, 1, 1, 1, 1))
	if _, err := handshake(r, w, Ticket{User: "t", Password: "t"}); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if sent.Bytes()[12] != 0x02 {
		t.Errorf("chose type %#x, want the last offered (0x02) as the vendor client does", sent.Bytes()[12])
	}
}

func TestHandshakeErrors(t *testing.T) {
	r, w, _ := fakeHandshake(t, []byte{0x10}, 1, nil)
	if _, err := handshake(r, w, Ticket{}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Errorf("auth failure: err = %v, want the BMC's reason", err)
	}

	r, w, _ = fakeHandshake(t, nil, 0, nil)
	if _, err := handshake(r, w, Ticket{}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("no security types: err = %v, want the BMC's reason", err)
	}
}

func TestHandshakeCapsServerNameLength(t *testing.T) {
	init := serverInit("x", 1, 1, 1, 1, 1)
	binary.BigEndian.PutUint32(init[20:24], 1<<31) // hostile length
	r, w, _ := fakeHandshake(t, []byte{0x10}, 0, init)
	if _, err := handshake(r, w, Ticket{}); err == nil {
		t.Fatal("want error for an absurd name length")
	}
}
