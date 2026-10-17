package dell

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"ipmi-connect/internal/avmp"
	"ipmi-connect/internal/bmc"
)

const (
	vmCredentialTimeout = 30 * time.Second // the vendor viewer's wait for 0x8434
	// vmMountTimeout covers login, waiting for the BMC to free a drive (avmp waits up to 90 s)
	// and the map request.
	vmMountTimeout = 150 * time.Second
	vmEjectTimeout = 5 * time.Second
	vmStatusPoll   = 50 * time.Millisecond
	vmTokenField   = 32
)

// mediaMount is one virtual media session, from the mount request until it ends. runMount owns
// it: it always reports on ready exactly once and closes finished when everything is released.
type mediaMount struct {
	image    string
	session  *avmp.Session // set under mediaMu once connected
	cancel   context.CancelFunc
	finished chan struct{}
}

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

// openImage validates and opens a disc image.
func openImage(path string) (*os.File, int64, error) {
	if !filepath.IsAbs(path) {
		return nil, 0, errors.New("give the image's full path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open image: %w", err)
	}
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		_ = f.Close()
		return nil, 0, errors.New("the image must be a non-empty regular file")
	}
	return f, st.Size(), nil
}

// MountISO attaches the image at path to the host as a read-only USB CD-ROM. It needs the KVM
// session (for credentials) and returns once the BMC has accepted the drive mapping, the mount
// failed, or UnmountISO cancelled it.
func (c *Client) MountISO(path string) error {
	if c.loggingOut.Load() {
		return errors.New("the session is closing")
	}
	f, size, err := openImage(path)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &mediaMount{image: filepath.Base(path), cancel: cancel, finished: make(chan struct{})}
	c.mediaMu.Lock()
	if c.media != nil {
		c.mediaMu.Unlock()
		cancel()
		_ = f.Close()
		return fmt.Errorf("%s is already mounted; eject it first", c.media.image)
	}
	c.media, c.mediaErr = m, ""
	c.mediaMu.Unlock()

	ready := make(chan error, 1)
	go c.runMount(ctx, m, f, size, ready)
	return <-ready
}

// runMount drives one mount to its end. Until the mapping is accepted, the outcome goes to
// ready; afterwards, why it ended is kept for MediaStatus.
func (c *Client) runMount(ctx context.Context, m *mediaMount, f *os.File, size int64, ready chan<- error) {
	mounted := false
	var err error
	defer func() {
		_ = f.Close()
		m.cancel()
		c.mediaMu.Lock()
		if c.media == m {
			c.media = nil
		}
		if mounted && err != nil {
			c.mediaErr = err.Error()
			log.Printf("[Dell] Virtual media ended: %v", err)
		}
		c.mediaMu.Unlock()
		if !mounted {
			if err == nil || errors.Is(err, context.Canceled) {
				err = errors.New("the mount was cancelled")
			}
			ready <- err
		}
		close(m.finished)
	}()

	user, password, err := c.requestVMCredentials(ctx)
	if err != nil {
		return
	}
	conn, err := c.connectAPCPVersion(c.VPort, channelModeVMedia, 0, 0)
	if err != nil {
		err = fmt.Errorf("virtual media channel: %w", err)
		return
	}
	if err = ctx.Err(); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{}) // the AVMP session sets its own timeouts

	session := avmp.NewSession(conn, user, password)
	c.mediaMu.Lock()
	m.session = session
	c.mediaMu.Unlock()
	done := make(chan error, 1)
	go func() { done <- session.Mount(ctx, f, size) }()

	if err = waitMapped(ctx, session, done); err != nil {
		return
	}
	mounted = true
	log.Printf("[Dell] Virtual media: %s mounted", m.image)
	ready <- nil
	err = <-done
}

// waitMapped waits until the BMC accepts the mapping. On failure the session has ended.
func waitMapped(ctx context.Context, session *avmp.Session, done <-chan error) error {
	poll := time.NewTicker(vmStatusPoll)
	defer poll.Stop()
	deadline := time.After(vmMountTimeout)
	for !session.Status().Mounted {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("BMC released the drive straight away")
			}
			return err
		case <-ctx.Done():
			<-done // Mount returns once it sees the cancellation
			return ctx.Err()
		case <-deadline:
			return errors.New("virtual media mount timed out")
		case <-poll.C:
		}
	}
	return nil
}

// UnmountISO ejects the image, or abandons a mount still in progress, and waits for the
// session to close.
func (c *Client) UnmountISO() error {
	c.mediaMu.Lock()
	m := c.media
	c.mediaMu.Unlock()
	if m == nil {
		return errors.New("no image is mounted")
	}
	m.cancel()
	select {
	case <-m.finished:
		return nil
	case <-time.After(vmEjectTimeout):
		return errors.New("virtual media session did not close in time")
	}
}

// MediaStatus implements bmc.VirtualMedia.
func (c *Client) MediaStatus() bmc.MediaStatus {
	c.mediaMu.Lock()
	defer c.mediaMu.Unlock()
	if c.media == nil {
		return bmc.MediaStatus{State: bmc.MediaNone, Error: c.mediaErr}
	}
	st := bmc.MediaStatus{State: bmc.MediaMounting, Image: c.media.image}
	if s := c.media.session; s != nil {
		if ss := s.Status(); ss.Mounted {
			st.State, st.BytesServed = bmc.MediaMounted, ss.BytesServed
		}
	}
	return st
}

// ejectOnLogout releases the drive, or stops a mount in progress, before the session goes away.
// Logout sets loggingOut afterwards, so MountISO refuses new mounts from then on.
func (c *Client) ejectOnLogout() {
	if c.MediaStatus().State == bmc.MediaNone {
		return
	}
	if err := c.UnmountISO(); err != nil {
		log.Printf("[Dell] Virtual media eject on logout: %v", err)
	}
}
