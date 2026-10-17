package dell

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// fakeAPCP accepts one plain-text (no TLS) APCP connection and runs script on it after the
// 53-byte greeting exchange.
func fakeAPCP(t *testing.T, script func(conn net.Conn)) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		greeting := make([]byte, 53)
		if _, err := io.ReadFull(conn, greeting); err != nil {
			return
		}
		reply := make([]byte, 53)
		copy(reply, "APCP")
		binary.BigEndian.PutUint32(reply[14:18], 1) // plain only, no TLS
		if _, err := conn.Write(reply); err != nil {
			return
		}
		script(conn)
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port
}

func longPacket(id uint16, payload []byte) []byte {
	pkt := make([]byte, beefHeaderLen, beefHeaderLen+len(payload))
	copy(pkt, "BEEF")
	binary.BigEndian.PutUint16(pkt[4:6], id)
	binary.BigEndian.PutUint16(pkt[6:8], uint16(beefHeaderLen+len(payload)))
	return append(pkt, payload...)
}

// zeroMagicPacket is how the C6220 frames its video-auth reply.
func zeroMagicPacket(id uint16, payload []byte) []byte {
	pkt := longPacket(id, payload)
	copy(pkt[:4], []byte{0, 0, 0, 0})
	return pkt
}

func loginResponse(status byte, token uint32) []byte {
	payload := make([]byte, 107)
	payload[0] = status
	binary.BigEndian.PutUint32(payload[4:8], token)
	return longPacket(0x8300, payload)
}

func TestConnectControlSkipsPacketsBeforeLoginResponse(t *testing.T) {
	host, port := fakeAPCP(t, func(conn net.Conn) {
		_, _ = io.ReadFull(conn, make([]byte, controlAuthLen))
		_, _ = conn.Write(longPacket(0x8430, []byte{1, 2, 3, 4}))
		_, _ = conn.Write(loginResponse(0, 0xCAFEF00D))
		time.Sleep(200 * time.Millisecond)
	})
	c := &Client{Host: host, VPort: port, UserTokenStr: "1", PassTokenStr: "2"}
	defer c.Close()

	if err := c.ConnectControl(); err != nil {
		t.Fatalf("ConnectControl: %v", err)
	}
	if c.ApplianceSessionToken != 0xCAFEF00D {
		t.Errorf("token = %#x, want 0xcafef00d", c.ApplianceSessionToken)
	}
}

func TestConnectControlReportsRejectedLogin(t *testing.T) {
	host, port := fakeAPCP(t, func(conn net.Conn) {
		_, _ = io.ReadFull(conn, make([]byte, controlAuthLen))
		_, _ = conn.Write(loginResponse(2, 0))
	})
	c := &Client{Host: host, VPort: port}

	if err := c.ConnectControl(); err == nil {
		t.Fatal("want error for login status 2")
	}
	if c.CtrlConn != nil {
		t.Error("CtrlConn set after a rejected login")
	}
}

func TestConnectControlTimesOutOnSilentBMC(t *testing.T) {
	old := apcpHandshakeTimeout
	apcpHandshakeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { apcpHandshakeTimeout = old })

	host, port := fakeAPCP(t, func(conn net.Conn) {
		_, _ = io.ReadFull(conn, make([]byte, controlAuthLen))
		time.Sleep(2 * time.Second) // never answer
	})
	c := &Client{Host: host, VPort: port}

	start := time.Now()
	err := c.ConnectControl()
	if err == nil {
		t.Fatal("want timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v; the handshake deadline was not applied", elapsed)
	}
}

func TestConnectVideoTimesOutOnSilentBMC(t *testing.T) {
	old := apcpHandshakeTimeout
	apcpHandshakeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { apcpHandshakeTimeout = old })

	host, port := fakeAPCP(t, func(conn net.Conn) { time.Sleep(2 * time.Second) })
	c := &Client{Host: host, VPort: port}

	start := time.Now()
	if err := c.ConnectVideo(); err == nil {
		t.Fatal("want timeout error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("took %v; the handshake deadline was not applied", elapsed)
	}
}

func TestConnectVideoAcceptsZeroMagicAuthReply(t *testing.T) {
	host, port := fakeAPCP(t, func(conn net.Conn) {
		_, _ = io.ReadFull(conn, make([]byte, shortPacketLen))
		_, _ = conn.Write(zeroMagicPacket(pktVideoAuthResp, make([]byte, 8)))
		time.Sleep(200 * time.Millisecond)
	})
	c := &Client{Host: host, VPort: port}
	defer c.Close()

	if err := c.ConnectVideo(); err != nil {
		t.Fatalf("ConnectVideo: %v", err)
	}
}
