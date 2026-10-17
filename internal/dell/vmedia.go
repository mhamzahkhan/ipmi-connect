package dell

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"ipmi-connect/internal/avmp"
)

const (
	vmCredentialTimeout = 30 * time.Second // the vendor viewer's wait for 0x8434
	vmTokenField        = 32
)

// parseVMCredentials splits a 0x8434 payload into its two NUL-padded 32-byte tokens.
func parseVMCredentials(p []byte) (user, password string, err error) {
	if len(p) < 2*vmTokenField {
		return "", "", fmt.Errorf("short virtual media credentials (%d bytes)", len(p))
	}
	field := func(b []byte) string { return string(bytes.TrimRight(bytes.SplitN(b, []byte{0}, 2)[0], " ")) }
	user, password = field(p[:vmTokenField]), field(p[vmTokenField:2*vmTokenField])
	if user == "" || password == "" {
		return "", "", errors.New("BMC returned empty virtual media credentials (is virtual media enabled for this user?)")
	}
	return user, password, nil
}

// requestVMCredentials mints one-time AVMP tokens over the KVM control channel, as the vendor
// viewer's "Launch Virtual Media" does: no web request and no second login.
func (c *Client) requestVMCredentials(ctx context.Context) (user, password string, err error) {
	select { // discard a stale answer
	case <-c.vmCreds:
	default:
	}
	var payload [8]byte
	binary.BigEndian.PutUint32(payload[0:4], 30)
	if err := c.writeControl(beefPacket(pktRequestVMCredentials, payload)); err != nil {
		return "", "", fmt.Errorf("request virtual media credentials: %w", err)
	}
	select {
	case p := <-c.vmCreds:
		return parseVMCredentials(p)
	case <-ctx.Done():
		return "", "", ctx.Err()
	case <-time.After(vmCredentialTimeout):
		return "", "", errors.New("BMC did not issue virtual media credentials")
	}
}

// openVMedia opens and logs in an AVMP session on its own APCP channel (mode 2, version 0.0
// as the vendor client sends). It needs the KVM session for the credentials.
func (c *Client) openVMedia(ctx context.Context) (*avmp.Session, error) {
	if c.loggingOut.Load() {
		return nil, errors.New("the session is closing")
	}
	user, password, err := c.requestVMCredentials(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := c.connectAPCPVersion(c.VPort, channelModeVMedia, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("virtual media channel: %w", err)
	}
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{}) // the AVMP session sets its own timeouts
	s := avmp.NewSession(conn, user, password)
	if err := s.Open(ctx); err != nil {
		return nil, err
	}
	return s, nil
}
