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
