package supermicro

import (
	"errors"
	"net"
	"sync"

	"ipmi-connect/internal/bmc"
)

// errKeyboardUnsupported: the ATEN key event carries a USB HID usage in an 18-byte message, which
// the current RFB code does not implement yet.
var errKeyboardUnsupported = errors.New("supermicro keyboard input is not implemented yet")

// Console adapts the Supermicro client to bmc.Console. The RFB connection is attached once it
// is established; until then input is rejected.
type Console struct {
	client *Client
	mu     sync.Mutex // guards conn and serialises writes on it
	conn   net.Conn
}

// NewConsole wraps a logged-in client.
func NewConsole(client *Client) *Console {
	return &Console{client: client}
}

// AttachRFB sets the connection input is written to.
func (c *Console) AttachRFB(conn net.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.conn = conn
}

// SendPointerEvent writes an RFB PointerEvent.
func (c *Console) SendPointerEvent(buttons byte, x, y uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("supermicro RFB connection not established")
	}
	return SendPointerEvent(c.conn, buttons, x, y)
}

// SendKeyEvent is not supported until the ATEN input encoding is implemented.
func (c *Console) SendKeyEvent(bool, uint16) error { return errKeyboardUnsupported }

// RequestFrameRefresh is a no-op: video decoding is not implemented yet.
func (c *Console) RequestFrameRefresh() error { return nil }

// PowerControl issues a chassis power command through the web CGI.
func (c *Console) PowerControl(action bmc.PowerAction) error { return c.client.PowerControl(action) }
