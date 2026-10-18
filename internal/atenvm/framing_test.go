package atenvm_test

import (
	"bytes"
	"encoding/hex"
	"net"
	"testing"

	"github.com/mhamzahkhan/ipmi-connect/internal/atenvm"
)

func TestHeaderEncodeDecode(t *testing.T) {
	orig := atenvm.Header{
		Endpoint:   atenvm.EndpointBulkIn,
		Flags:      atenvm.FlagEncrypted,
		DevPort:    1,
		Tag:        atenvm.TagEnqueueAndGo,
		PayloadLen: 13,
	}

	encoded := orig.Encode()
	if len(encoded) != atenvm.HeaderLen {
		t.Fatalf("expected header length %d, got %d", atenvm.HeaderLen, len(encoded))
	}

	decoded, err := atenvm.DecodeHeader(encoded)
	if err != nil {
		t.Fatalf("decode header failed: %v", err)
	}

	if decoded != orig {
		t.Errorf("decoded header mismatch: got %+v, want %+v", decoded, orig)
	}
}

func TestRC4Crypt(t *testing.T) {
	plain := []byte("Hello, ATEN Virtual Media!")
	enc, err := atenvm.RC4CryptDefault(plain)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}
	if bytes.Equal(enc, plain) {
		t.Fatal("encrypted data matches plaintext")
	}

	dec, err := atenvm.RC4CryptDefault(enc)
	if err != nil {
		t.Fatalf("decrypt failed: %v", err)
	}
	if !bytes.Equal(dec, plain) {
		t.Fatalf("decrypted data %q != plain %q", dec, plain)
	}
}

// Golden vector from Wireshark capture frame 31 (X10SLM-F / X7SB3 ATEN iKVM session).
// Client sent 226 bytes: 8 bytes header + 218 bytes RC4 encrypted payload.
func TestFrame31GoldenVector(t *testing.T) {
	wireHeaderHex := "008000012c000000"
	wireHeaderBytes, _ := hex.DecodeString(wireHeaderHex)
	h, err := atenvm.DecodeHeader(wireHeaderBytes)
	if err != nil {
		t.Fatalf("failed to decode frame 31 header: %v", err)
	}

	if h.Endpoint != atenvm.EndpointControl || h.Flags != atenvm.FlagEncrypted || h.Tag != atenvm.TagDeviceSetupReq || h.PayloadLen != 44 {
		t.Errorf("frame 31 header mismatch: %+v", h)
	}

	encryptedPayloadHex := "c9d708c31570ba4d498c208fc7c38c8ca1f8" +
		"91d34417d8629d88e0a5e82e8541e9ca" +
		"4d2dd95dd4fc6abe552f246b1093195c" +
		"a74a9dadf7676d2afcb4bea8fda23673" +
		"acfcd4f214b9c9c1da3f761adccf522d" +
		"e2853675576a189503cb67850e15246a" +
		"f54cdbf6cf8b7a3ce093cc101c966239" +
		"8e2fa3cebfc1a32e175953de0ee70bf8" +
		"ae58cc82c31fb45b4717298c80ca1aba" +
		"b6fd4c722caecad5efd9fe7ace771141" +
		"df0d86adf2f3f81e0f23587624e8a6fd" +
		"f136506e5f143025e303f7add81014c6" +
		"988a9a5f363f68190ac06fda757c4419" +
		"2bb2b0a79cfe97b6"
	encryptedPayload, _ := hex.DecodeString(encryptedPayloadHex)
	if len(encryptedPayload) != 218 {
		t.Fatalf("expected 218 bytes encrypted payload, got %d", len(encryptedPayload))
	}

	decrypted, err := atenvm.RC4CryptDefault(encryptedPayload)
	if err != nil {
		t.Fatalf("RC4 decryption failed: %v", err)
	}

	// The captured session's token is public and expired; assert structure, not its value, so no
	// credential string is embedded in the product repo. User and password are the same 16-char
	// token, NUL-padded.
	user := bytes.TrimRight(decrypted[0:16], "\x00")
	pass := bytes.TrimRight(decrypted[16:36], "\x00")
	if len(user) != 16 || !bytes.Equal(user, pass) {
		t.Errorf("expected a 16-char token repeated as user and password, got user=%q pass=%q", user, pass)
	}

	// Verify flags: 0x83 (JNLP SID auth + port 1)
	if decrypted[40] != 0x83 {
		t.Errorf("decrypted flags = 0x%02x, want 0x83", decrypted[40])
	}

	// Verify descriptors (offset 44 onward, 174 bytes)
	descriptors := decrypted[44:]
	if len(descriptors) != 174 {
		t.Fatalf("expected 174 bytes descriptors, got %d", len(descriptors))
	}

	// Verify descriptor chunk 0 (Device descriptor)
	if descriptors[0] != 0x12 || descriptors[1] != 0x12 || descriptors[2] != 0x01 {
		t.Errorf("invalid chunk 0 in golden vector")
	}

	// Verify byte 87 of the packet (offset 79 of decrypted payload)
	// In the capture it is 0x06 (SCSI subclass)
	if decrypted[79] != 0x06 {
		t.Errorf("byte 87 (offset 79) = 0x%02x, want 0x06", decrypted[79])
	}
}

func TestReadWriteFrameRoundtrip(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	hSent := atenvm.Header{
		Endpoint:   atenvm.EndpointBulkOut,
		Flags:      atenvm.FlagBMCQueue,
		DevPort:    1,
		Tag:        atenvm.TagEnqueue,
		PayloadLen: 31,
	}
	payloadSent := []byte("USBC123456789012345678901234567")

	errCh := make(chan error, 1)
	go func() {
		errCh <- atenvm.WriteFrame(client, hSent, payloadSent)
	}()

	hRecv, payloadRecv, err := atenvm.ReadFrame(server)
	if err != nil {
		t.Fatalf("ReadFrame failed: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}

	if hRecv != hSent {
		t.Errorf("received header mismatch: %+v != %+v", hRecv, hSent)
	}
	if !bytes.Equal(payloadRecv, payloadSent) {
		t.Errorf("received payload mismatch: %q != %q", payloadRecv, payloadSent)
	}
}
