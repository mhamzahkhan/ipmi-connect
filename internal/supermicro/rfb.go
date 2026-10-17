package supermicro

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"ipmi-connect/internal/bmc"
)

// ConnectRFB establishes the extended RFB connection on port 5900, performs version banner
// exchange, ATEN extended authentication with 24-byte credentials, and initializes the display.
func (c *Client) ConnectRFB() (net.Conn, error) {
	addr := fmt.Sprintf("%s:5900", c.Host)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, bmc.FormatError("Supermicro ConnectRFB dial", err)
	}

	if err := c.HandshakeRFB(conn); err != nil {
		conn.Close()
		return nil, err
	}

	return conn, nil
}

// HandshakeRFB performs RFB version exchange, security negotiation, credentials, and ServerInit on an established connection.
func (c *Client) HandshakeRFB(conn net.Conn) error {
	// 1. Version banner exchange (12 bytes: "RFB 003.008\n")
	banner := make([]byte, 12)
	if _, err := io.ReadFull(conn, banner); err != nil {
		return bmc.FormatError("Supermicro RFB read banner", err)
	}

	if !strings.HasPrefix(string(banner), "RFB") {
		return fmt.Errorf("invalid RFB banner: %s", string(banner))
	}

	// Echo banner back
	if _, err := conn.Write(banner); err != nil {
		return bmc.FormatError("Supermicro RFB write banner", err)
	}

	// 2. Read Security Types count and types
	secCountBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, secCountBuf); err != nil {
		return bmc.FormatError("Supermicro RFB read sec count", err)
	}

	secCount := int(secCountBuf[0])
	secTypes := make([]byte, secCount)
	if _, err := io.ReadFull(conn, secTypes); err != nil {
		return bmc.FormatError("Supermicro RFB read sec types", err)
	}

	// Verify type 16 (0x10 = ATEN auth) is supported
	hasAtenAuth := false
	for _, t := range secTypes {
		if t == 16 {
			hasAtenAuth = true
			break
		}
	}

	if !hasAtenAuth {
		return fmt.Errorf("server does not support ATEN auth (types: %v)", secTypes)
	}

	// Send selected security type 16
	if _, err := conn.Write([]byte{16}); err != nil {
		return bmc.FormatError("Supermicro RFB select sec type", err)
	}

	// 3. ATEN Extended Authentication: read 24-byte challenge nonce
	challenge := make([]byte, 24)
	if _, err := io.ReadFull(conn, challenge); err != nil {
		return bmc.FormatError("Supermicro RFB read challenge", err)
	}

	// Send username and password padded to 24 bytes
	userPadded := make([]byte, 24)
	copy(userPadded, []byte(c.User))

	passPadded := make([]byte, 24)
	copy(passPadded, []byte(c.Password))

	if _, err := conn.Write(userPadded); err != nil {
		return bmc.FormatError("Supermicro RFB send user", err)
	}
	if _, err := conn.Write(passPadded); err != nil {
		return bmc.FormatError("Supermicro RFB send pass", err)
	}

	// Read 4-byte auth result code (0 = OK)
	var authCode uint32
	if err := binary.Read(conn, binary.BigEndian, &authCode); err != nil {
		return bmc.FormatError("Supermicro RFB read auth result", err)
	}

	if authCode != 0 {
		var errLen uint32
		_ = binary.Read(conn, binary.BigEndian, &errLen)
		errMsgBytes := make([]byte, errLen)
		_, _ = io.ReadFull(conn, errMsgBytes)
		return fmt.Errorf("rfb authentication failed: %s (code: %d)", string(errMsgBytes), authCode)
	}

	log.Printf("[Supermicro] RFB authenticated successfully on port 5900")

	// 4. Send ClientInit shared flag (0x00 = exclusive)
	if _, err := conn.Write([]byte{0}); err != nil {
		return bmc.FormatError("Supermicro RFB write client init", err)
	}

	// 5. Read ServerInit structure
	// Width (2B), Height (2B), PixelFormat (16B), NameLen (4B), Name (NameLen B)
	var initHdr struct {
		Width   uint16
		Height  uint16
		PixFmt  [16]byte
		NameLen uint32
	}

	if err := binary.Read(conn, binary.BigEndian, &initHdr); err != nil {
		return bmc.FormatError("Supermicro RFB read server init", err)
	}

	nameBytes := make([]byte, initHdr.NameLen)
	if _, err := io.ReadFull(conn, nameBytes); err != nil {
		return bmc.FormatError("Supermicro RFB read server name", err)
	}

	c.ScreenWidth = int(initHdr.Width)
	c.ScreenHeight = int(initHdr.Height)
	c.ServerName = string(nameBytes)

	log.Printf("[Supermicro] RFB ServerInit: Display=%dx%d, ServerName='%s'",
		c.ScreenWidth, c.ScreenHeight, c.ServerName)

	return nil
}

// pointerEvent encodes an RFB PointerEvent: type 5, button mask, x, y.
func pointerEvent(buttonMask byte, x, y uint16) []byte {
	buf := make([]byte, 6)
	buf[0] = 5
	buf[1] = buttonMask
	binary.BigEndian.PutUint16(buf[2:4], x)
	binary.BigEndian.PutUint16(buf[4:6], y)
	return buf
}
