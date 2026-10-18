package atenvm_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"ipmi-connect/internal/atenvm"
)

func TestBuildDeviceSetupPacketCDROM(t *testing.T) {
	h, wire, err := atenvm.BuildDeviceSetupPacket(atenvm.SetupOptions{
		Username: "testtoken123456", Password: "testtoken123456", IsSessionID: true,
		PortIndex: 0, Class: atenvm.ClassCDROM, Descriptors: atenvm.CDROMDescriptors(),
		Encrypt: true, Nonce: 0x11223344,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Tag != atenvm.TagDeviceSetupReq || h.Flags != atenvm.FlagEncrypted {
		t.Errorf("header %+v", h)
	}
	// The header declares only the 44-byte auth prefix; the descriptors follow on the wire.
	if h.PayloadLen != 44 {
		t.Errorf("PayloadLen = %d, want 44", h.PayloadLen)
	}
	if len(wire) != 44+174 {
		t.Fatalf("wire payload = %d, want 218", len(wire))
	}
	dec, err := atenvm.RC4CryptDefault(wire)
	if err != nil {
		t.Fatal(err)
	}
	if u := string(bytes.TrimRight(dec[0:16], "\x00")); u != "testtoken123456" {
		t.Errorf("user = %q", u)
	}
	if n := binary.BigEndian.Uint32(dec[36:40]); n != 0x11223344 {
		t.Errorf("nonce = 0x%08x", n)
	}
	if dec[40] != 0x83 { // session-id | desired-port(0->2) | allocate
		t.Errorf("flags = 0x%02x, want 0x83", dec[40])
	}
	if dec[41] != atenvm.ClassCDROM {
		t.Errorf("class = 0x%02x, want CD-ROM", dec[41])
	}
	// Byte 0x57 of the packet (offset 79 of the payload) must be 0x05 for the X7SB3 to emulate a
	// 2048-byte CD-ROM.
	if dec[79] != 0x05 {
		t.Errorf("packet byte 0x57 = 0x%02x, want 0x05 (CD-ROM)", dec[79])
	}
}

func TestBuildDeviceSetupPacketDiskCleartext(t *testing.T) {
	h, wire, err := atenvm.BuildDeviceSetupPacket(atenvm.SetupOptions{
		Username: "ADMIN", Password: "secret", IsSessionID: false,
		PortIndex: 1, Class: atenvm.ClassRemovable, Descriptors: atenvm.DiskDescriptors(),
		Encrypt: false, Nonce: 0x55667788,
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Flags != 0x00 {
		t.Errorf("flags = 0x%02x, want cleartext", h.Flags)
	}
	if wire[40] != 0x05 { // no session bit | desired-port(1->4) | allocate
		t.Errorf("flags byte = 0x%02x, want 0x05", wire[40])
	}
	if wire[79] != 0x06 {
		t.Errorf("packet byte 0x57 = 0x%02x, want 0x06 (disk)", wire[79])
	}
}

func TestParseDeviceSetupResponse(t *testing.T) {
	status, sid, err := atenvm.ParseDeviceSetupResponse([]byte{0x00, 0x79, 0xca, 0xd8, 0x1d, 0, 0, 0, 0})
	if err != nil || status != atenvm.StatusSetupOK || sid != 0x1DD8CA79 {
		t.Errorf("OK: status=%#x sid=%#x err=%v", status, sid, err)
	}
	if _, _, err := atenvm.ParseDeviceSetupResponse([]byte{0x01, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("want error for auth-fail status")
	}
	if _, _, err := atenvm.ParseDeviceSetupResponse([]byte{0x00}); err == nil {
		t.Error("want error for short response")
	}
}

func TestKeepaliveAndDetachBuilders(t *testing.T) {
	h, p := atenvm.BuildKeepaliveRequest()
	if h.Tag != atenvm.TagPingReq {
		t.Errorf("keepalive request tag = 0x%02x, want 0x04", h.Tag)
	}
	if len(p) != 4 || h.PayloadLen != 4 {
		t.Errorf("keepalive payload len %d", len(p))
	}
	hd, pd := atenvm.BuildDetachPacket()
	if hd.Tag != atenvm.TagDetachReq || len(pd) != 0 {
		t.Errorf("detach %+v %v", hd, pd)
	}
}
