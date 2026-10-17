package supermicro

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
)

const (
	securityATEN = 0x10
	// maxWireString bounds lengths the BMC sends for names and error reasons.
	maxWireString = 4096
)

// ServerInfo is what ServerInit and ATEN's trailer say about the session.
type ServerInfo struct {
	Name          string
	SessionID     uint32
	Video         bool // false: video is not available to this session
	KeyboardMouse bool // false: view only
	Kick          bool
	VirtualMedia  bool
}

// handshake runs ATEN's RFB 3.8 variant: version echo, security type (16 if offered, else the last
// one, as the vendor client does), 24-byte challenge (ignored), the ticket's credentials, the
// result, ClientInit 0 and ServerInit with its 12-byte ATEN trailer. The caller sets deadlines.
func handshake(r *bufio.Reader, w io.Writer, t Ticket) (ServerInfo, error) {
	version, err := readBytes(r, 12)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read version: %w", err)
	}
	if !bytes.HasPrefix(version, []byte("RFB ")) {
		return ServerInfo{}, fmt.Errorf("not an RFB server (banner %q)", version)
	}
	if _, err := w.Write(version); err != nil {
		return ServerInfo{}, err
	}

	n, err := r.ReadByte()
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read security types: %w", err)
	}
	if n == 0 {
		return ServerInfo{}, fmt.Errorf("BMC refused the connection: %s", readReason(r))
	}
	types, err := readBytes(r, int(n))
	if err != nil {
		return ServerInfo{}, err
	}
	choice := types[len(types)-1]
	if bytes.IndexByte(types, securityATEN) >= 0 {
		choice = securityATEN
	} else {
		log.Printf("[Supermicro] BMC does not offer ATEN security (types % x); trying %#x", types, choice)
	}
	if _, err := w.Write([]byte{choice}); err != nil {
		return ServerInfo{}, err
	}

	if _, err := readBytes(r, 24); err != nil {
		return ServerInfo{}, fmt.Errorf("read challenge: %w", err)
	}
	if _, err := w.Write(credentials(t.User, t.Password)); err != nil {
		return ServerInfo{}, err
	}
	var result uint32
	if err := binary.Read(r, binary.BigEndian, &result); err != nil {
		return ServerInfo{}, fmt.Errorf("read auth result: %w", err)
	}
	if result != 0 {
		return ServerInfo{}, fmt.Errorf("BMC refused the KVM login: %s", readReason(r))
	}
	if _, err := w.Write([]byte{0}); err != nil { // ClientInit, written straight after the result
		return ServerInfo{}, err
	}

	init, err := readBytes(r, 24) // u16 w, u16 h (placeholders), pixel format, u32 name length
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read ServerInit: %w", err)
	}
	nameLen := binary.BigEndian.Uint32(init[20:24])
	if nameLen > maxWireString {
		return ServerInfo{}, fmt.Errorf("ServerInit name length %d is not plausible", nameLen)
	}
	name, err := readBytes(r, int(nameLen))
	if err != nil {
		return ServerInfo{}, err
	}
	trailer, err := readBytes(r, 12)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("read ATEN ServerInit trailer: %w", err)
	}
	return ServerInfo{
		Name:          string(name),
		SessionID:     binary.BigEndian.Uint32(trailer[4:8]),
		Video:         trailer[8] != 0,
		KeyboardMouse: trailer[9] != 0,
		Kick:          trailer[10] != 0,
		VirtualMedia:  trailer[11] != 0,
	}, nil
}

func readBytes(r io.Reader, n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return b, err
}

// readReason reads an RFB u32-length reason string, bounded.
func readReason(r io.Reader) string {
	var n uint32
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return "no reason given"
	}
	if n > maxWireString {
		return "reason too long"
	}
	b, err := readBytes(r, int(n))
	if err != nil {
		return "reason truncated"
	}
	return string(b)
}

var errUnknownMessage = errors.New("unknown message type")
