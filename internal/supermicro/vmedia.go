package supermicro

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"ipmi-connect/internal/atenvm"
	"ipmi-connect/internal/vmedia"
)

const defaultVMediaPort = 623

// openVMedia opens an ATEN Virtual Storage session over TCP port 623.
func (c *Client) openVMedia(ctx context.Context) (vmedia.Session, error) {
	user := c.web.user
	pass := c.web.password
	isSessionID := false

	// Fetch a one-time JNLP token if possible
	ticket, err := c.web.FetchTicket(c.host)
	if err == nil && ticket.User != "" {
		user = ticket.User
		pass = ticket.Password
		isSessionID = true
	}

	addr := net.JoinHostPort(c.host, strconv.Itoa(defaultVMediaPort))
	conn, err := c.dial(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("connect virtual media (%s): %w", addr, err)
	}

	sess := atenvm.NewSession(conn, atenvm.SessionOptions{
		Username:    user,
		Password:    pass,
		IsSessionID: isSessionID,
		PortIndex:   0,
		Encrypt:     true,
	})

	if err := sess.Open(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("virtual media handshake: %w", err)
	}

	return sess, nil
}
