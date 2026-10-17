package dell

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ipmi-connect/internal/bmc"
)

func TestAPCPGreeting(t *testing.T) {
	nonce := bytes.Repeat([]byte{0xab}, 32)
	tests := []struct {
		name               string
		mode, major, minor byte
	}{
		{"KVM control", channelModeControl, 2, 34},
		{"virtual media (vendor sends version 0.0)", channelModeVMedia, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := apcpGreeting(tt.mode, tt.major, tt.minor, nonce)
			want := append([]byte("APCP\x00\x00\x00\x35\x01\x00\x00\x00"), tt.mode, tt.major, tt.minor, 0, 0, 0, 0, 5, 32)
			if !bytes.Equal(g[:21], want) || !bytes.Equal(g[21:], nonce) || len(g) != 53 {
				t.Errorf("greeting % x", g)
			}
		})
	}
}

// credentialReply is the 0x8434 body: user and password, each NUL-padded to 32 bytes.
func credentialReply(user, pass string) []byte {
	b := make([]byte, 64)
	copy(b, user)
	copy(b[32:], pass)
	return longPacket(pktVMCredentials, b)
}

// fakeControl attaches a pipe as the control channel with its reader running, and answers the
// first credential request with the given tokens.
func fakeControl(t *testing.T, c *Client, user, pass string) <-chan []byte {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	c.CtrlConn = client
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go c.drainControl(stop)

	requests := make(chan []byte, 1)
	go func() {
		req := make([]byte, shortPacketLen)
		if _, err := io.ReadFull(server, req); err != nil {
			return
		}
		requests <- req
		_, _ = server.Write(credentialReply(user, pass))
		_, _ = io.Copy(io.Discard, server)
	}()
	return requests
}

func TestRequestVMCredentials(t *testing.T) {
	c := NewClient("192.0.2.10", "root", "pw")
	requests := fakeControl(t, c, "719885386", "1649760492")

	user, pass, err := c.requestVMCredentials(context.Background())
	if err != nil {
		t.Fatalf("requestVMCredentials: %v", err)
	}
	if user != "719885386" || pass != "1649760492" {
		t.Errorf("got %q / %q", user, pass)
	}
	req := <-requests
	if id := binary.BigEndian.Uint16(req[4:6]); id != pktRequestVMCredentials {
		t.Errorf("request ID %#x, want 0x0427", id)
	}
	if v := binary.BigEndian.Uint32(req[8:12]); v != 30 {
		t.Errorf("request payload %d, want 30", v)
	}
}

func TestParseVMCredentialsRejectsShortReply(t *testing.T) {
	if _, _, err := parseVMCredentials(make([]byte, 40)); err == nil {
		t.Error("want error for a short reply")
	}
	if _, _, err := parseVMCredentials(make([]byte, 64)); err == nil {
		t.Error("want error for empty tokens")
	}
}

// avmpBMC plays the BMC's virtual media side after the APCP greeting.
func avmpBMC(t *testing.T, released chan<- struct{}) func(conn net.Conn) {
	return func(conn net.Conn) {
		read := func() (uint16, []byte) {
			hdr := make([]byte, 10)
			if _, err := io.ReadFull(conn, hdr); err != nil {
				return 0, nil
			}
			body := make([]byte, binary.BigEndian.Uint32(hdr[4:8])-10)
			_, _ = io.ReadFull(conn, body)
			return binary.BigEndian.Uint16(hdr[8:10]), body
		}
		send := func(typ uint16, body []byte) {
			b := []byte("AVMP")
			b = binary.BigEndian.AppendUint32(b, uint32(10+len(body)))
			b = binary.BigEndian.AppendUint16(b, typ)
			_, _ = conn.Write(append(b, body...))
		}
		if typ, body := read(); typ != 0x0100 || !strings.Contains(string(body), "719885386") {
			t.Errorf("first AVMP packet %#x, want login with the minted token", typ)
			return
		}
		send(0x8100, make([]byte, 101))
		read() // GET_VDISK_INFO
		send(0x8200, []byte{0xff, 0xff, 0, 1, 2, 0, 0, 0})
		read() // map
		for {
			typ, _ := read()
			if typ == 0x0220 {
				released <- struct{}{}
				return
			}
			if typ == 0 {
				return
			}
		}
	}
}

func TestMountAndEjectISO(t *testing.T) {
	released := make(chan struct{}, 1)
	host, port := fakeAPCP(t, avmpBMC(t, released))
	c := NewClient(host, "root", "pw")
	c.VPort = port
	fakeControl(t, c, "719885386", "1649760492")

	iso := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(iso, make([]byte, 4*2048), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.MountISO(iso); err != nil {
		t.Fatalf("MountISO: %v", err)
	}
	if st := c.MediaStatus(); st.State != bmc.MediaMounted || st.Image != "test.iso" {
		t.Errorf("status %+v", st)
	}
	if err := c.MountISO(iso); err == nil {
		t.Error("second mount should be refused while one is active")
	}
	if err := c.UnmountISO(); err != nil {
		t.Fatalf("UnmountISO: %v", err)
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("BMC never saw the release")
	}
	if st := c.MediaStatus(); st.State != bmc.MediaNone {
		t.Errorf("status after eject %+v", st)
	}
}

func TestMountISORejectsBadPaths(t *testing.T) {
	c := NewClient("192.0.2.10", "root", "pw")
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.iso")
	_ = os.WriteFile(empty, nil, 0o600)
	for _, p := range []string{"relative.iso", filepath.Join(dir, "missing.iso"), dir, empty} {
		if err := c.MountISO(p); err == nil {
			t.Errorf("MountISO(%q) succeeded", p)
		}
	}
}

// stallingBMC logs the client in and then never answers the drive-info request.
func stallingBMC(conn net.Conn) {
	hdr := make([]byte, 10)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return
	}
	_, _ = io.ReadFull(conn, make([]byte, binary.BigEndian.Uint32(hdr[4:8])-10))
	b := []byte("AVMP\x00\x00\x00\x6f\x81\x00")
	_, _ = conn.Write(append(b, make([]byte, 101)...))
	_, _ = io.Copy(io.Discard, conn)
}

func TestEjectStopsAMountInProgress(t *testing.T) {
	host, port := fakeAPCP(t, stallingBMC)
	c := NewClient(host, "root", "pw")
	c.VPort = port
	fakeControl(t, c, "719885386", "1649760492")
	iso := filepath.Join(t.TempDir(), "test.iso")
	_ = os.WriteFile(iso, make([]byte, 2048), 0o600)

	mountErr := make(chan error, 1)
	go func() { mountErr <- c.MountISO(iso) }()
	waitUntil(t, func() bool { return c.MediaStatus().State == bmc.MediaMounting })

	start := time.Now()
	if err := c.UnmountISO(); err != nil {
		t.Errorf("UnmountISO during a mount: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("eject took %v", time.Since(start))
	}
	if err := <-mountErr; err == nil {
		t.Error("MountISO should report that it was cancelled")
	}
	if st := c.MediaStatus(); st.State != bmc.MediaNone {
		t.Errorf("status %+v after eject", st)
	}
}

func TestNoMountsAfterLogout(t *testing.T) {
	c := NewClient("192.0.2.10", "root", "pw")
	c.loggingOut.Store(true)
	iso := filepath.Join(t.TempDir(), "test.iso")
	_ = os.WriteFile(iso, make([]byte, 2048), 0o600)
	if err := c.MountISO(iso); err == nil {
		t.Error("MountISO accepted a mount during logout")
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
