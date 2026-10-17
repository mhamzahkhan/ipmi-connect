package supermicro

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"
)

func TestConnectRFB_Handshake(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()

	// Simulate server side of RFB
	go func() {
		// 1. Send banner
		_, _ = serverConn.Write([]byte("RFB 003.008\n"))

		// Read echo banner back
		clientBanner := make([]byte, 12)
		_, _ = serverConn.Read(clientBanner)

		// 2. Send Security Types (1 type: type 16 = ATEN)
		_, _ = serverConn.Write([]byte{1, 16})

		// Read chosen security type (1 byte: 16)
		chosen := make([]byte, 1)
		_, _ = serverConn.Read(chosen)

		// 3. Send 24-byte ATEN challenge nonce
		challenge := make([]byte, 24)
		_, _ = serverConn.Write(challenge)

		// Read ATEN credentials (24 bytes user, 24 bytes pass)
		userBuf := make([]byte, 24)
		_, _ = serverConn.Read(userBuf)
		passBuf := make([]byte, 24)
		_, _ = serverConn.Read(passBuf)

		// 4. Send SecurityResult (4 bytes 0 = OK)
		_ = binary.Write(serverConn, binary.BigEndian, uint32(0))

		// 5. Read ClientInit (1 byte)
		shared := make([]byte, 1)
		_, _ = serverConn.Read(shared)

		// 6. Send ServerInit (width=1024, height=768, pixel format 16 bytes, name len=4, name="TEST")
		var sInit bytes.Buffer
		_ = binary.Write(&sInit, binary.BigEndian, uint16(1024))
		_ = binary.Write(&sInit, binary.BigEndian, uint16(768))
		// Pixel format: 16 bytes
		sInit.Write(make([]byte, 16))
		// Name len: 4
		_ = binary.Write(&sInit, binary.BigEndian, uint32(4))
		sInit.WriteString("TEST")
		_, _ = serverConn.Write(sInit.Bytes())
	}()

	client := &Client{
		Host:     "127.0.0.1",
		User:     "ADMIN",
		Password: "ADMIN",
	}

	if err := client.HandshakeRFB(clientConn); err != nil {
		t.Fatalf("HandshakeRFB failed: %v", err)
	}

	if client.ScreenWidth != 1024 || client.ScreenHeight != 768 {
		t.Errorf("expected 1024x768, got %dx%d", client.ScreenWidth, client.ScreenHeight)
	}
	if client.ServerName != "TEST" {
		t.Errorf("expected ServerName='TEST', got %q", client.ServerName)
	}
}
