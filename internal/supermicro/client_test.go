package supermicro

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"

	"ipmi-connect/internal/atenvm"
	"ipmi-connect/internal/bmc"
)

var (
	_ bmc.Console      = (*Client)(nil)
	_ bmc.VirtualMedia = (*Client)(nil)
)

// fakeRFB accepts KVM connections, runs the X7SB3 handshake and sends one Hermon update.
func fakeRFB(t *testing.T) (addr string, conns chan net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	conns = make(chan net.Conn, 4)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- c
			go func(c net.Conn) {
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
				_, _ = c.Write(serverInit("ATEN iKVM Server", 7, 1, 1, 1, 1))
				_, _ = c.Write(hermonUpdate(32, 16, hermon(0, 0, 1, tile(0, 0, []byte{0x00, 0x7C}, 256))))
				_, _ = io.Copy(io.Discard, c)
			}(c)
		}
	}()
	return ln.Addr().String(), conns
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
