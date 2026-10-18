package atenvm_test

import (
	"bytes"
	"encoding/binary"
	"net"
	"testing"

	"ipmi-connect/internal/atenvm"
)

func TestBuildDeviceSetupPacketCDROM(t *testing.T) {
	opts := atenvm.SetupOptions{
		Username:    "testtoken123456",
		Password:    "testtoken123456",
		IsSessionID: true,
		PortIndex:   0,
		Descriptors: atenvm.CDROMDescriptors(),
		Encrypt:     true,
		Nonce:       0x11223344,
	}

	h, wirePayload, err := atenvm.BuildDeviceSetupPacket(opts)
	if err != nil {
		t.Fatalf("BuildDeviceSetupPacket failed: %v", err)
	}

	if h.Tag != atenvm.TagDeviceSetupReq {
		t.Errorf("Tag = 0x%02x, want 0x01", h.Tag)
	}
	if h.Flags != atenvm.FlagEncrypted {
		t.Errorf("Flags = 0x%02x, want 0x80", h.Flags)
	}
	if h.PayloadLen != 218 {
		t.Errorf("PayloadLen = %d, want 218", h.PayloadLen)
	}
	if len(wirePayload) != 218 {
		t.Fatalf("wirePayload length = %d, want 218", len(wirePayload))
	}

	// Decrypt payload to check contents
	dec, err := atenvm.RC4CryptDefault(wirePayload)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}

	user := string(bytes.TrimRight(dec[0:16], "\x00"))
	if user != "testtoken123456" {
		t.Errorf("user = %q, want testtoken123456", user)
	}

	nonce := binary.BigEndian.Uint32(dec[36:40])
	if nonce != 0x11223344 {
		t.Errorf("nonce = 0x%08x, want 0x11223344", nonce)
	}

	// Flags should be 0x83: bit 7 (session ID) + bit 1 (port index 0 -> port 1) + bit 0 (allocate)
	if dec[40] != 0x83 {
		t.Errorf("flags = 0x%02x, want 0x83", dec[40])
	}

	// Offset 79 (packet byte 87): CD-ROM subclass 0x05
	if dec[79] != atenvm.SubClassSFF8070i {
		t.Errorf("CD-ROM subclass = 0x%02x, want 0x05", dec[79])
	}
}

func TestBuildDeviceSetupPacketDiskUserAccount(t *testing.T) {
	opts := atenvm.SetupOptions{
		Username:    "ADMIN",
		Password:    "ADMIN",
		IsSessionID: false,
		PortIndex:   0,
		Descriptors: atenvm.DiskDescriptors(),
		Encrypt:     false,
		Nonce:       0x55667788,
	}

	h, wirePayload, err := atenvm.BuildDeviceSetupPacket(opts)
	if err != nil {
		t.Fatalf("BuildDeviceSetupPacket failed: %v", err)
	}

	if h.Flags != 0x00 {
		t.Errorf("Flags = 0x%02x, want 0x00 for unencrypted", h.Flags)
	}

	// For direct user account auth, bit 7 of flags is 0, so flags = 0x03
	if wirePayload[40] != 0x03 {
		t.Errorf("flags = 0x%02x, want 0x03", wirePayload[40])
	}

	// Offset 79 (packet byte 87): Disk subclass 0x06
	if wirePayload[79] != atenvm.SubClassSCSI {
		t.Errorf("Disk subclass = 0x%02x, want 0x06", wirePayload[79])
	}
}

func TestParseDeviceSetupResponse(t *testing.T) {
	// Success response: 9 bytes
	respOK := []byte{0x00, 0x79, 0xca, 0xd8, 0x1d, 0x00, 0x00, 0x00, 0x00}
	status, sid, err := atenvm.ParseDeviceSetupResponse(respOK)
	if err != nil {
		t.Fatalf("unexpected error for OK response: %v", err)
	}
	if status != atenvm.StatusSetupOK {
		t.Errorf("status = 0x%02x, want 0x00", status)
	}
	if sid != 0x1DD8CA79 {
		t.Errorf("session ID = 0x%08x, want 0x1DD8CA79", sid)
	}

	// Error response: Auth Failed (status 1)
	respFail := []byte{0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, _, err = atenvm.ParseDeviceSetupResponse(respFail)
	if err == nil {
		t.Error("expected error for status 0x01, got nil")
	}

	// Short response
	_, _, err = atenvm.ParseDeviceSetupResponse([]byte{0x00})
	if err == nil {
		t.Error("expected error for short response, got nil")
	}
}

func TestMockBMCHandshake(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	done := make(chan struct{})

	// Mock BMC server loop
	go func() {
		defer close(done)

		// 1. Read Device Setup Request (Tag 0x01)
		h, payload, err := atenvm.ReadFrame(server)
		if err != nil {
			t.Errorf("BMC: ReadFrame(SetupReq) failed: %v", err)
			return
		}
		if h.Tag != atenvm.TagDeviceSetupReq || h.Flags != atenvm.FlagEncrypted {
			t.Errorf("BMC: unexpected setup header: %+v", h)
			return
		}
		dec, err := atenvm.RC4CryptDefault(payload)
		if err != nil {
			t.Errorf("BMC: decrypt failed: %v", err)
			return
		}
		if len(dec) != 218 {
			t.Errorf("BMC: payload length = %d, want 218", len(dec))
			return
		}

		// 2. Reply with Device Setup Response (Tag 0x02, status 0, sid 0x12345678)
		respPayload := []byte{0x00, 0x78, 0x56, 0x34, 0x12, 0x00, 0x00, 0x00, 0x00}
		hResp := atenvm.Header{
			Endpoint:   atenvm.EndpointControl,
			Flags:      0x00,
			DevPort:    0x00,
			Tag:        atenvm.TagDeviceSetupResp,
			PayloadLen: uint32(len(respPayload)),
		}
		if err := atenvm.WriteFrame(server, hResp, respPayload); err != nil {
			t.Errorf("BMC: WriteFrame(SetupResp) failed: %v", err)
			return
		}

		// 3. Read EP Setup (Tag 0x07)
		hEP, epPayload, err := atenvm.ReadFrame(server)
		if err != nil {
			t.Errorf("BMC: ReadFrame(EPSetup) failed: %v", err)
			return
		}
		if hEP.Tag != atenvm.TagEPSetup || len(epPayload) != 8 {
			t.Errorf("BMC: unexpected EP setup: %+v %v", hEP, epPayload)
			return
		}

		// 4. Send Keepalive Ping Request (Tag 0x04)
		hPing := atenvm.Header{
			Endpoint:   atenvm.EndpointControl,
			Flags:      0x00,
			DevPort:    0x00,
			Tag:        atenvm.TagPingReq,
			PayloadLen: 4,
		}
		if err := atenvm.WriteFrame(server, hPing, []byte{0xde, 0xad, 0xbe, 0xef}); err != nil {
			t.Errorf("BMC: WriteFrame(PingReq) failed: %v", err)
			return
		}

		// 5. Read Keepalive Ping Response (Tag 0x03)
		hPong, pongPayload, err := atenvm.ReadFrame(server)
		if err != nil {
			t.Errorf("BMC: ReadFrame(PingResp) failed: %v", err)
			return
		}
		if hPong.Tag != atenvm.TagPingResp || !bytes.Equal(pongPayload, []byte{0xde, 0xad, 0xbe, 0xef}) {
			t.Errorf("BMC: unexpected Ping response: %+v %v", hPong, pongPayload)
			return
		}

		// 6. Read Detach Request (Tag 0x05)
		hDetach, _, err := atenvm.ReadFrame(server)
		if err != nil {
			t.Errorf("BMC: ReadFrame(DetachReq) failed: %v", err)
			return
		}
		if hDetach.Tag != atenvm.TagDetachReq {
			t.Errorf("BMC: unexpected detach header: %+v", hDetach)
			return
		}

		// 7. Send Detach Response (Tag 0x06)
		hDetachResp := atenvm.Header{
			Endpoint:   atenvm.EndpointControl,
			Flags:      0x00,
			DevPort:    0x00,
			Tag:        atenvm.TagDetachResp,
			PayloadLen: 1,
		}
		_ = atenvm.WriteFrame(server, hDetachResp, []byte{0x00})
	}()

	// Client execution:
	// 1. Send Setup Request
	hSetup, setupPayload, err := atenvm.BuildDeviceSetupPacket(atenvm.SetupOptions{
		Username:    "token123",
		Password:    "token123",
		IsSessionID: true,
		Encrypt:     true,
	})
	if err != nil {
		t.Fatalf("build setup failed: %v", err)
	}
	if err := atenvm.WriteFrame(client, hSetup, setupPayload); err != nil {
		t.Fatalf("send setup failed: %v", err)
	}

	// 2. Read Setup Response
	hResp, respPayload, err := atenvm.ReadFrame(client)
	if err != nil {
		t.Fatalf("read setup response failed: %v", err)
	}
	if hResp.Tag != atenvm.TagDeviceSetupResp {
		t.Fatalf("unexpected setup response tag: 0x%02x", hResp.Tag)
	}
	status, sid, err := atenvm.ParseDeviceSetupResponse(respPayload)
	if err != nil || status != 0 || sid != 0x12345678 {
		t.Fatalf("parse setup response failed: status=%d, sid=0x%08x, err=%v", status, sid, err)
	}

	// 3. Send EP Setup
	hEP, epPayload := atenvm.BuildEPSetupPacket()
	if err := atenvm.WriteFrame(client, hEP, epPayload); err != nil {
		t.Fatalf("send EP setup failed: %v", err)
	}

	// 4. Read Ping Request from BMC and reply
	hPing, pingPayload, err := atenvm.ReadFrame(client)
	if err != nil {
		t.Fatalf("read ping failed: %v", err)
	}
	if hPing.Tag != atenvm.TagPingReq {
		t.Fatalf("unexpected ping tag: 0x%02x", hPing.Tag)
	}
	token := binary.LittleEndian.Uint32(pingPayload)
	hPong, pongPayload := atenvm.BuildKeepalivePacket(token)
	if err := atenvm.WriteFrame(client, hPong, pongPayload); err != nil {
		t.Fatalf("send keepalive response failed: %v", err)
	}

	// 5. Send Detach Request
	hDetach, detachPayload := atenvm.BuildDetachPacket(1)
	if err := atenvm.WriteFrame(client, hDetach, detachPayload); err != nil {
		t.Fatalf("send detach failed: %v", err)
	}

	// 6. Read Detach Response
	hDetachResp, _, err := atenvm.ReadFrame(client)
	if err != nil {
		t.Fatalf("read detach response failed: %v", err)
	}
	if hDetachResp.Tag != atenvm.TagDetachResp {
		t.Fatalf("unexpected detach response tag: 0x%02x", hDetachResp.Tag)
	}

	<-done
}
