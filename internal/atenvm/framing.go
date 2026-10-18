package atenvm

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// HeaderLen is the fixed size of every ATEN Virtual Media message header.
const HeaderLen = 8

// Endpoints. High nibble is the endpoint type (1=Bulk Out, 2=Bulk In, 3=Int In).
// Low nibble is the endpoint number.
const (
	EndpointControl byte = 0x00
	EndpointBulkOut byte = 0x11 // EP1 Bulk-OUT (host -> device): CBWs
	EndpointBulkIn  byte = 0x22 // EP2 Bulk-IN  (device -> host): Data + CSW
	EndpointIntIn   byte = 0x33 // EP3 Interrupt-IN
)

// Flags.
const (
	FlagEncrypted byte = 0x80 // Bit 7: Payload is RC4-encrypted
	FlagBMCQueue  byte = 0x02 // Bit 1: Set by BMC on CBW frames
)

// Message tag constants (header byte 3).
const (
	TagEnqueue         byte = 0x00 // Endpoint transfer enqueue (bulk data)
	TagEnqueueAndGo    byte = 0xFF // Endpoint transfer enqueue and flush (CSW)
	TagDeviceSetupReq  byte = 0x01 // Client -> BMC: authenticate & plug in device
	TagDeviceSetupResp byte = 0x02 // BMC -> Client: setup result (0x00 = OK)
	TagPingResp        byte = 0x03 // Keepalive reply (the BMC answers the client's 0x04 with this)
	TagPingReq         byte = 0x04 // Keepalive request (the client sends these every 10 s)
	TagDetachReq       byte = 0x05 // Client -> BMC: detach / unmount virtual device
	TagDetachResp      byte = 0x06 // BMC -> Client: detach acknowledged
	TagEPSetup         byte = 0x07 // Client -> BMC: configure USB endpoints
	TagStatusReq       byte = 0x08 // Client -> BMC: query drive status
	TagStatusResp      byte = 0x09 // BMC -> Client: drive status bytes
	TagHTTPPortReq     byte = 0x0A // Client -> BMC: query web HTTP port
	TagHTTPPortResp    byte = 0x0B // BMC -> Client: HTTP port (uint16 LE)
)

// Header represents the 8-byte framing header of an ATEN Virtual Media PDU.
type Header struct {
	Endpoint   byte   // Byte 0: endpoint number and type
	Flags      byte   // Byte 1: flags (bit 7 = RC4 encrypted)
	DevPort    byte   // Byte 2: virtual device port index
	Tag        byte   // Byte 3: message type
	PayloadLen uint32 // Bytes 4..7: payload length (Little Endian)
}

// Encode serializes the Header into an 8-byte slice.
func (h Header) Encode() []byte {
	b := make([]byte, HeaderLen)
	b[0] = h.Endpoint
	b[1] = h.Flags
	b[2] = h.DevPort
	b[3] = h.Tag
	binary.LittleEndian.PutUint32(b[4:8], h.PayloadLen)
	return b
}

// DecodeHeader deserializes an 8-byte slice into a Header.
func DecodeHeader(b []byte) (Header, error) {
	if len(b) < HeaderLen {
		return Header{}, errors.New("header too short")
	}
	return Header{
		Endpoint:   b[0],
		Flags:      b[1],
		DevPort:    b[2],
		Tag:        b[3],
		PayloadLen: binary.LittleEndian.Uint32(b[4:8]),
	}, nil
}

// ReadFrame reads a complete message (8-byte header plus payload) from r.
// If the message is a DeviceSetupReq with a 44-byte auth prefix length,
// it also reads the appended 8-table USB descriptor set.
func ReadFrame(r io.Reader) (Header, []byte, error) {
	hdrBuf := make([]byte, HeaderLen)
	if _, err := io.ReadFull(r, hdrBuf); err != nil {
		return Header{}, nil, err
	}
	h, err := DecodeHeader(hdrBuf)
	if err != nil {
		return Header{}, nil, err
	}

	// Sanity limit on payload length to prevent unbounded allocations (e.g. 16 MiB max).
	const maxPayload = 16 * 1024 * 1024
	if h.PayloadLen > maxPayload {
		return Header{}, nil, fmt.Errorf("payload length %d exceeds maximum %d", h.PayloadLen, maxPayload)
	}

	payload := make([]byte, h.PayloadLen)
	if h.PayloadLen > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return Header{}, nil, fmt.Errorf("reading payload: %w", err)
		}
	}

	return h, payload, nil
}

// WriteFrame writes an 8-byte header and payload to w.
func WriteFrame(w io.Writer, h Header, payload []byte) error {
	hdr := h.Encode()
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}
