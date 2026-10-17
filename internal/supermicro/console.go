package supermicro

import (
	"errors"
	"net"
	"sync"
	"time"

	"ipmi-connect/internal/bmc"
)

// errKeyboardUnsupported: the ATEN key event carries a USB HID usage in an 18-byte message, which
// the current RFB code does not implement yet.
var errKeyboardUnsupported = errors.New("supermicro keyboard input is not implemented yet")

// Console adapts the Supermicro client to bmc.Console. The RFB connection is attached once it
// is established; until then input is rejected.
type Console struct {
	client *Client
	mu     sync.Mutex // guards conn and serialises every write on it
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

// rfbWriteTimeout bounds each write so a stalled BMC cannot hold the lock indefinitely.
const rfbWriteTimeout = 2 * time.Second

// WriteRFB sends raw RFB bytes. All writers (input and the video loop) go through here, so writes
// never interleave and every one has a deadline.
func (c *Console) WriteRFB(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return errors.New("supermicro RFB connection not established")
	}
	_ = c.conn.SetWriteDeadline(time.Now().Add(rfbWriteTimeout))
	_, err := c.conn.Write(b)
	return err
}

// SendPointerEvent writes an RFB PointerEvent.
func (c *Console) SendPointerEvent(buttons byte, x, y uint16) error {
	return c.WriteRFB(pointerEvent(buttons, x, y))
}

// SendKeyEvent is not supported until the ATEN input encoding is implemented.
func (c *Console) SendKeyEvent(bool, uint16) error { return errKeyboardUnsupported }

// RequestFrameRefresh is a no-op: video decoding is not implemented yet.
func (c *Console) RequestFrameRefresh() error { return nil }

// PowerControl issues a chassis power command through the web CGI.
func (c *Console) PowerControl(action bmc.PowerAction) error { return c.client.PowerControl(action) }
