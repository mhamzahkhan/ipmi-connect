package atenvm

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
)

// Device setup flags (in the auth prefix byte 40).
const (
	FlagAuthSessionID byte = 0x80 // Bit 7: 1 = JNLP session token auth, 0 = BMC user account auth
	FlagAuthCheckOnly byte = 0x40 // Bit 6: 1 = Pre-verification check only
	FlagAllocatePort  byte = 0x01 // Bit 0: 1 = Allocate port
)

// Status codes returned in TagDeviceSetupResp (payload byte 0).
const (
	StatusSetupOK            byte = 0x00
	StatusSetupAuthFailed    byte = 0x01
	StatusSetupBusyOrFull    byte = 0x02
	StatusSetupInvalidPrivs  byte = 0x03
	StatusSetupSessionExists byte = 0x04
	StatusSetupFWUpdating    byte = 0x05
	StatusSetupTokenExpired  byte = 0x06
)

// SetupOptions specifies configuration for building the Device Setup packet.
type SetupOptions struct {
	Username    string // JNLP token or BMC username (up to 16 ASCII chars)
	Password    string // JNLP token or BMC password (up to 20 ASCII chars)
	IsSessionID bool   // true if authenticating with JNLP token; false for root/admin user account
	PortIndex   byte   // Target port index: 0 for drive 0, 1 for drive 1
	Class       byte   // Target device class selector (e.g. 4 for CD-ROM, 1 for Removable/Flash, 2 for Floppy)
	Descriptors []byte // USB descriptor tables (174 bytes)
	Encrypt     bool   // true to RC4-encrypt the payload (standard vendor behavior)
	Nonce       uint32 // Optional session nonce (if 0, generated randomly)
}

// BuildDeviceSetupPacket creates the Tag 0x01 Device Setup PDU.
// It formats the 44-byte auth prefix, appends the USB descriptor tables,
// optionally encrypts the payload with RC4, and returns the Header and wire payload.
func BuildDeviceSetupPacket(opts SetupOptions) (Header, []byte, error) {
	if len(opts.Descriptors) == 0 {
		opts.Descriptors = CDROMDescriptors()
	}

	authPrefix := make([]byte, 44)

	// Offset 0..15: Username (16 bytes, NUL-padded)
	copy(authPrefix[0:16], opts.Username)

	// Offset 16..35: Password (20 bytes, NUL-padded)
	copy(authPrefix[16:36], opts.Password)

	// Offset 36..39: Nonce (4 bytes)
	nonce := opts.Nonce
	if nonce == 0 {
		var nBuf [4]byte
		if _, err := rand.Read(nBuf[:]); err != nil {
			return Header{}, nil, fmt.Errorf("generating setup nonce: %w", err)
		}
		nonce = binary.BigEndian.Uint32(nBuf[:])
	}
	binary.BigEndian.PutUint32(authPrefix[36:40], nonce)

	// Offset 40: Setup flags
	// desired_port is ((PortIndex + 1) * 2). For port 0, (0+1)*2 = 2 (0x02).
	desiredPort := (opts.PortIndex + 1) * 2
	flags := (desiredPort & 0x0E) | FlagAllocatePort
	if opts.IsSessionID {
		flags |= FlagAuthSessionID
	}
	authPrefix[40] = flags

	// Offset 41: Device class selector
	if opts.Class == 0 {
		opts.Class = 0x03 // Default to standard class index observed in capture
	}
	authPrefix[41] = opts.Class

	// Offset 42..43: Padding 0x00, 0x00
	authPrefix[42] = 0x00
	authPrefix[43] = 0x00

	// Full plaintext payload: 44-byte auth prefix + descriptor tables
	plainPayload := make([]byte, 0, len(authPrefix)+len(opts.Descriptors))
	plainPayload = append(plainPayload, authPrefix...)
	plainPayload = append(plainPayload, opts.Descriptors...)

	hdrFlags := byte(0x00)
	wirePayload := plainPayload

	if opts.Encrypt {
		hdrFlags = FlagEncrypted
		enc, err := RC4CryptDefault(plainPayload)
		if err != nil {
			return Header{}, nil, fmt.Errorf("encrypting device setup payload: %w", err)
		}
		wirePayload = enc
	}

	// In the vendor protocol, the 8-byte header payload_len is set to 44 (the auth prefix length),
	h := Header{
		Endpoint:   EndpointControl,
		Flags:      hdrFlags,
		DevPort:    0x00,
		Tag:        TagDeviceSetupReq,
		PayloadLen: uint32(len(wirePayload)),
	}

	return h, wirePayload, nil
}

// ParseDeviceSetupResponse parses a Tag 0x02 Device Setup Response payload.
func ParseDeviceSetupResponse(payload []byte) (byte, uint32, error) {
	if len(payload) < 5 {
		return 0, 0, fmt.Errorf("setup response payload too short: %d bytes (want at least 5)", len(payload))
	}
	status := payload[0]
	sessionID := binary.LittleEndian.Uint32(payload[1:5])

	switch status {
	case StatusSetupOK:
		return status, sessionID, nil
	case StatusSetupAuthFailed:
		return status, sessionID, errors.New("authentication failed (invalid token or credentials)")
	case StatusSetupBusyOrFull:
		return status, sessionID, errors.New("BMC virtual media slots are full or busy")
	case StatusSetupInvalidPrivs:
		return status, sessionID, errors.New("account lacks administrative privileges")
	case StatusSetupSessionExists:
		return status, sessionID, errors.New("conflicting remote session already active")
	case StatusSetupFWUpdating:
		return status, sessionID, errors.New("BMC firmware update in progress")
	case StatusSetupTokenExpired:
		return status, sessionID, errors.New("session token expired")
	default:
		return status, sessionID, fmt.Errorf("device setup failed with status 0x%02x", status)
	}
}

// BuildEPSetupPacket returns the Tag 0x07 USB EP Setup PDU to configure endpoint queues.
func BuildEPSetupPacket() (Header, []byte) {
	payload := []byte{0x06, 0x03, 0x01, 0x10, 0x02, 0x20, 0x03, 0x30}
	h := Header{
		Endpoint:   EndpointControl,
		Flags:      0x00,
		DevPort:    0x00,
		Tag:        TagEPSetup,
		PayloadLen: uint32(len(payload)),
	}
	return h, payload
}

// BuildKeepalivePacket returns a Tag 0x03 Keepalive response PDU.
func BuildKeepalivePacket(token uint32) (Header, []byte) {
	payload := make([]byte, 4)
	binary.LittleEndian.PutUint32(payload, token)
	h := Header{
		Endpoint:   EndpointControl,
		Flags:      0x00,
		DevPort:    0x00,
		Tag:        TagPingResp,
		PayloadLen: 4,
	}
	return h, payload
}

// BuildDetachPacket returns a Tag 0x05 Detach request PDU.
func BuildDetachPacket(devPort byte) (Header, []byte) {
	h := Header{
		Endpoint:   EndpointControl,
		Flags:      0x00,
		DevPort:    devPort,
		Tag:        TagDetachReq,
		PayloadLen: 0,
	}
	return h, nil
}
