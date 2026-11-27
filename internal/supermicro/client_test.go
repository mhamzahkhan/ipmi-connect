package supermicro

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/atenvm"
	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

var (
	_ bmc.Console      = (*Client)(nil)
	_ bmc.VirtualMedia = (*Client)(nil)
)

// fakeRFB accepts KVM connections, runs the X7SB3 handshake and sends one Hermon update.
func fakeRFB(t *testing.T) (addr string, conns chan net.Conn) {
	return fakeRFBWith(t, func(int) rfbConn { return rfbConn{keyboardMouse: true} })
}

// rfbConn is how fakeRFBWith treats one connection.
type rfbConn struct {
	keyboardMouse bool // false: the BMC says another session has keyboard and mouse control
	drop          bool // close the connection after the first update, as a network outage would
}

// fakeRFBWith is fakeRFB with per-connection behaviour; n counts connections from 0.
func fakeRFBWith(t *testing.T, behave func(n int) rfbConn) (addr string, conns chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	conns = make(chan net.Conn, 64)
	go func() {
		for n := 0; ; n++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- c
			go func(c net.Conn, b rfbConn) {
				_, _ = c.Write([]byte("RFB 003.008\n"))
				_, _ = io.ReadFull(c, make([]byte, 12))
				_, _ = c.Write([]byte{1, 0x10})
				_, _ = io.ReadFull(c, make([]byte, 1))
				_, _ = c.Write(make([]byte, 24))
				cred := make([]byte, 48)
				_, _ = io.ReadFull(c, cred)
				if !bytes.HasPrefix(cred, []byte("tok\x00")) {
					_, _ = c.Write([]byte{0, 0, 0, 1, 0, 0, 0, 3, 'b', 'a', 'd'})
					return
				}
				_, _ = c.Write([]byte{0, 0, 0, 0})
				_, _ = io.ReadFull(c, make([]byte, 1))
				km := byte(0)
				if b.keyboardMouse {
					km = 1
				}
				_, _ = c.Write(serverInit("ATEN iKVM Server", uint32(7+n), 1, km, 1, 1))
				_, _ = c.Write(hermonUpdate(32, 16, hermon(0, 0, 1, tile(0, 0, []byte{0x00, 0x7C}, 256))))
				if b.drop {
					_ = c.Close()
					return
				}
				_, _ = io.Copy(io.Discard, c)
			}(c, behave(n))
		}
	}()
	return ln.Addr().String(), conns
}

// newReclaimClient is a logged-in client on fakeRFBWith with fast retries.
func newReclaimClient(t *testing.T, behave func(n int) rfbConn) (*Client, chan net.Conn) {
	t.Helper()
	rfbAddr, conns := fakeRFBWith(t, behave)
	c := NewClient("192.0.2.20", "ADMIN", "pw")
	c.web = newFakeWeb(t, &fakeBMCWeb{password: "pw", sid: "0123456789abcdef"})
	c.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", rfbAddr)
	}
	c.retryMin, c.reclaimDelay = 10*time.Millisecond, 10*time.Millisecond
	if err := c.Login(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	return c, conns
}

func (c *Client) hasControl() bool {
	s, err := c.current()
	return err == nil && !s.viewOnly.Load()
}

// The X7SB3 keeps a session that vanished in a network outage (a host reboot on a shared NIC)
// as the keyboard and mouse owner for a while, so the reconnect is view only. The client must
// keep reconnecting until the BMC lets go, instead of settling for view only.
func TestClientReclaimsControlAfterLosingASession(t *testing.T) {
	c, conns := newReclaimClient(t, func(n int) rfbConn {
		switch n {
		case 0:
			return rfbConn{keyboardMouse: true, drop: true}
		case 1, 2:
			return rfbConn{} // the stale session still holds control
		default:
			return rfbConn{keyboardMouse: true}
		}
	})
	sink := &recordingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Logout() }()
	go c.Run(ctx, sink)

	waitFor(t, "keyboard and mouse control on a later connection", func() bool {
		return len(conns) >= 4 && c.hasControl()
	})
	_, _, _, _, statuses := sink.snapshot()
	if !slices.Contains(statuses, statusReclaiming) {
		t.Errorf("statuses %q, want %q while waiting", statuses, statusReclaiming)
	}
}

// View only from the very first connection is another user's session: no reconnect loop.
func TestClientKeepsViewOnlyWhenAnotherUserHadControl(t *testing.T) {
	c, conns := newReclaimClient(t, func(int) rfbConn { return rfbConn{} })
	sink := &recordingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Logout() }()
	go c.Run(ctx, sink)

	waitFor(t, "a view-only session", func() bool { s, err := c.current(); return err == nil && s.viewOnly.Load() })
	time.Sleep(100 * time.Millisecond) // ten reclaim delays
	if n := len(conns); n != 1 {
		t.Errorf("%d connections, want 1: view only from the start must not loop", n)
	}
}

// Reclaiming gives up after the window: it might be a real user who took over meanwhile.
func TestClientStopsReclaimingAfterTheWindow(t *testing.T) {
	c, conns := newReclaimClient(t, func(n int) rfbConn {
		return rfbConn{keyboardMouse: n == 0, drop: n == 0}
	})
	c.reclaimWindow = 100 * time.Millisecond
	sink := &recordingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Logout() }()
	go c.Run(ctx, sink)

	waitFor(t, "settling for a view-only session", func() bool { s, err := c.current(); return err == nil && s.viewOnly.Load() })
	n := len(conns)
	time.Sleep(100 * time.Millisecond)
	if len(conns) != n {
		t.Errorf("still reconnecting after the window (%d -> %d connections)", n, len(conns))
	}
}

func TestClientConnectsDecodesAndLogsOutAfterTheSocket(t *testing.T) {
	web := &fakeBMCWeb{password: "pw", sid: "0123456789abcdef"}
	rfbAddr, _ := fakeRFB(t)
	c := NewClient("192.0.2.20", "ADMIN", "pw")
	c.web = newFakeWeb(t, web)
	c.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", rfbAddr)
	}

	if err := c.Login(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	sink := &recordingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx, sink)

	waitFor(t, "a decoded frame", func() bool { n, _, _, _, _ := sink.snapshot(); return n > 0 })
	if _, _, _, pix, _ := sink.snapshot(); rgbaAt(pix, 32, 0, 0) != red {
		t.Errorf("pixel (0,0) = %v, want red", rgbaAt(pix, 32, 0, 0))
	}
	if err := c.SendKeyEvent(true, 4); err != nil {
		t.Errorf("SendKeyEvent: %v", err)
	}

	cancel()
	c.Logout()
	web.mu.Lock()
	defer web.mu.Unlock()
	if web.logouts != 1 {
		t.Errorf("web logouts = %d, want 1 (after the KVM socket closed)", web.logouts)
	}
}

func TestClientPowerUsesCGIWithoutASession(t *testing.T) {
	web := &fakeBMCWeb{password: "pw", sid: "s"}
	c := NewClient("192.0.2.20", "ADMIN", "pw")
	c.web = newFakeWeb(t, web)
	if err := c.Login(); err != nil {
		t.Fatal(err)
	}
	if err := c.PowerControl(bmc.PowerOn); err != nil {
		t.Fatalf("PowerControl: %v", err)
	}
	if len(web.power) != 1 || web.power[0] != "(1,1)" {
		t.Errorf("CGI power %v, want (1,1)", web.power)
	}
}

func TestInBandPowerCodes(t *testing.T) {
	want := map[bmc.PowerAction]byte{bmc.PowerOff: 0, bmc.PowerOn: 1, bmc.PowerReset: 2, bmc.PowerSoftOff: 3}
	for action, code := range want {
		if got, ok := inBandPower[action]; !ok || got != code {
			t.Errorf("%s: %d, %v", action, got, ok)
		}
	}
	if _, ok := inBandPower[bmc.PowerCycle]; ok {
		t.Error("power cycle has no in-band code; it must use the CGI")
	}
}

func TestBackoff(t *testing.T) {
	d := reconnectMin
	for i := 0; i < 10; i++ {
		d = nextDelay(d)
	}
	if d != reconnectMax {
		t.Errorf("delay %v, want capped at %v", d, reconnectMax)
	}
}

func TestClientVMediaIntegration(t *testing.T) {
	c := NewClient("192.0.2.20", "ADMIN", "pw")
	c.web = newFakeWeb(t, &fakeBMCWeb{password: "pw", sid: "0123456789abcdef"})
	if err := c.web.Login(); err != nil { // the web session must be up for the JNLP token
		t.Fatalf("web login: %v", err)
	}

	serverConnCh := make(chan net.Conn, 1)
	c.dial = func(ctx context.Context, addr string) (net.Conn, error) {
		cli, srv := net.Pipe()
		serverConnCh <- srv
		return cli, nil
	}

	// Fake BMC virtual media service: one connection per mapped drive.
	go func() {
		srv := <-serverConnCh
		defer srv.Close()
		// The setup frame declares a 44-byte prefix but carries the descriptors too.
		hdr := make([]byte, atenvm.HeaderLen)
		if _, err := io.ReadFull(srv, hdr); err != nil {
			return
		}
		h, _ := atenvm.DecodeHeader(hdr)
		if h.Tag != atenvm.TagDeviceSetupReq {
			t.Errorf("first frame tag 0x%02x, want device setup", h.Tag)
			return
		}
		if _, err := io.ReadFull(srv, make([]byte, int(h.PayloadLen)+174)); err != nil {
			return
		}
		_ = atenvm.WriteFrame(srv, atenvm.Header{Tag: atenvm.TagDeviceSetupResp, PayloadLen: 9},
			[]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x00, 0x00, 0x00, 0x00})
		for {
			fh, _, err := atenvm.ReadFrame(srv)
			if err != nil {
				return
			}
			if fh.Tag == atenvm.TagDetachReq {
				_ = atenvm.WriteFrame(srv, atenvm.Header{Tag: atenvm.TagDetachResp, PayloadLen: 1}, []byte{0x00})
				return
			}
		}
	}()

	isoPath := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(isoPath, make([]byte, 4096), 0600); err != nil {
		t.Fatal(err)
	}
	img, err := c.AddImage(isoPath, false)
	if err != nil {
		t.Fatalf("AddImage: %v", err)
	}
	if err := c.MapImage(img.ID, bmc.TargetCD); err != nil {
		t.Fatalf("MapImage: %v", err)
	}
	st := c.MediaStatus()
	if len(st.Drives) != 1 || st.Images[0].MappedTo != bmc.TargetCD {
		t.Fatalf("unexpected media status: %+v", st)
	}
	if err := c.UnmapDrive(bmc.TargetCD); err != nil {
		t.Fatalf("UnmapDrive: %v", err)
	}
	c.Logout()
}
