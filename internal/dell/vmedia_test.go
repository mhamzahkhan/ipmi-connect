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

// avmpBMC plays the BMC's virtual media side after the APCP greeting: login OK, the C6220's
// three free drives, and it reports when the client releases a drive.
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
		for {
			typ, _ := read()
			switch typ {
			case 0:
				return
			case 0x0200:
				send(0x8200, []byte{0xff, 0xff, 0, 3, 2, 0, 0, 1, 1, 0, 0, 1, 4, 0, 0, 1})
			case 0x0220:
				released <- struct{}{}
			}
		}
	}
}

func TestVirtualMediaThroughTheControlChannel(t *testing.T) {
	released := make(chan struct{}, 1)
	host, port := fakeAPCP(t, avmpBMC(t, released))
	c := NewClient(host, "root", "pw")
	c.VPort = port
	fakeControl(t, c, "719885386", "1649760492")

	iso := filepath.Join(t.TempDir(), "test.iso")
	if err := os.WriteFile(iso, make([]byte, 4*2048), 0o600); err != nil {
		t.Fatal(err)
	}
	img, err := c.AddImage(iso, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.MapImage(img.ID, bmc.TargetCD); err != nil {
		t.Fatalf("MapImage: %v", err)
	}
	if st := c.MediaStatus(); len(st.Drives) != 1 || st.Drives[0].Image != "test.iso" || st.Drives[0].Mounting {
		t.Errorf("status %+v", st)
	}
	if err := c.UnmapDrive(bmc.TargetCD); err != nil {
		t.Fatalf("UnmapDrive: %v", err)
	}
	select {
	case <-released:
	case <-time.After(3 * time.Second):
		t.Fatal("BMC never saw the release")
	}
}

func TestNoVirtualMediaAfterLogout(t *testing.T) {
	c := NewClient("192.0.2.10", "root", "pw")
	c.loggingOut.Store(true)
	if _, err := c.openVMedia(context.Background()); err == nil {
		t.Error("opened a virtual media session during logout")
	}
}
