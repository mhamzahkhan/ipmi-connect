package supermicro

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"

	"github.com/mhamzahkhan/ipmi-connect/internal/atenvm"
	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

const defaultVMediaPort = 623

// openVMedia returns an ATEN Virtual Storage group. Each mapped drive gets a fresh one-time iKVM
// JNLP token and its own connection; it never falls back to the real BMC password (that would
// cross the network protected only by a public fixed RC4 key, i.e. effectively cleartext).
func (c *Client) openVMedia(_ context.Context) (vmedia.Session, error) {
	connect := func(ctx context.Context) (net.Conn, atenvm.Credentials, error) {
		ticket, err := c.web.FetchTicket(c.host)
		if err != nil {
			return nil, atenvm.Credentials{}, fmt.Errorf("virtual media login token: %w", err)
		}
		if ticket.User == "" || ticket.Password == "" {
			return nil, atenvm.Credentials{}, errors.New("BMC did not issue a virtual media login token")
		}
		addr := net.JoinHostPort(c.host, strconv.Itoa(defaultVMediaPort))
		conn, err := c.dial(ctx, addr)
		if err != nil {
			return nil, atenvm.Credentials{}, err
		}
		return conn, atenvm.Credentials{
			Username: ticket.User, Password: ticket.Password, IsSessionID: true, Encrypt: true,
		}, nil
	}
	return atenvm.NewGroup(connect), nil
}
