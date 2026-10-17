package dell

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func TestHeartbeatLifecycle(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	client := &Client{
		CtrlConn: clientConn,
	}

	client.StartHeartbeat()
	// Calling StartHeartbeat again should be idempotent
	client.StartHeartbeat()

	receivedHeartbeat := make(chan struct{})
	go func() {
		buf := make([]byte, 16)
		n, err := serverConn.Read(buf)
		if err == nil && n == 16 && string(buf[:4]) == "BEEF" && binary.BigEndian.Uint16(buf[4:6]) == 1024 {
			close(receivedHeartbeat)
		}
	}()

	select {
	case <-receivedHeartbeat:
		// Heartbeat packet 1024 received
	case <-time.After(7 * time.Second):
		t.Fatal("timed out waiting for heartbeat packet")
	}

	client.Close()
	if client.heartbeatStop != nil {
		t.Error("expected heartbeatStop to be nil after Close()")
	}
}

func TestDrainControlClosesSessionOnProtocolError(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	video, videoPeer := net.Pipe()
	defer videoPeer.Close()
	c := &Client{CtrlConn: client, VideoConn: video}

	go func() { _, _ = server.Write([]byte("JUNKJUNK")) }()
	done := make(chan struct{})
	go func() { c.drainControl(make(chan struct{})); close(done) }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("drainControl did not return after a protocol error")
	}
	c.ConnMu.Lock()
	defer c.ConnMu.Unlock()
	if c.CtrlConn != nil || c.VideoConn != nil {
		t.Error("channels left open; the video loop would never notice and reconnect")
	}
}
