package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

func startTestServer(t *testing.T, console bmc.Console) (*Server, string) {
	t.Helper()
	srv := NewServer(0, bmc.SessionState{ScreenWidth: 720, ScreenHeight: 400})
	srv.Console = console
	h, err := srv.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return srv, "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 2s")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWebSocketSessionReleasesKeysOnClose(t *testing.T) {
	fc := &fakeConsole{}
	srv, url := startTestServer(t, fc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv, url), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || !strings.Contains(string(data), `"resize"`) {
		t.Fatalf("first message = %v %q %v, want resize", typ, data, err)
	}
	// Every new browser asks the BMC for a full frame, so reloading recovers a damaged picture.
	eventually(t, func() bool { _, r := fc.snapshot(); return r == 1 })

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"key","down":true,"hidCode":224}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	eventually(t, func() bool { k, _ := fc.snapshot(); return len(k) == 1 })

	_ = conn.Close(websocket.StatusNormalClosure, "")
	eventually(t, func() bool { k, _ := fc.snapshot(); return len(k) == 2 })
	if keys, _ := fc.snapshot(); !reflect.DeepEqual(keys, []keyEvent{{true, 224}, {false, 224}}) {
		t.Errorf("keys = %v, want press then release of 224", keys)
	}
}

func TestWebSocketRejectsCrossSiteOrigin(t *testing.T) {
	srv, url := startTestServer(t, &fakeConsole{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	hdr := http.Header{"Origin": []string{"https://evil.example"}}
	conn, resp, err := websocket.Dial(ctx, wsURL(srv, url), &websocket.DialOptions{HTTPHeader: hdr})
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("cross-site WebSocket was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %v, want 403", resp)
	}
}

func TestWebSocketRequiresSession(t *testing.T) {
	_, url := startTestServer(t, &fakeConsole{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, url, nil)
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("WebSocket without the session cookie was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %v, want 401", resp)
	}
}

// Full frames (raw RGBA) supersede each other, so a slow browser keeps only the newest.
func TestBroadcastFullFrameKeepsOnlyLatest(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{})
	c := newWSClient()
	srv.addClient(c)

	for i := 0; i < clientQueueLen+5; i++ {
		srv.broadcastFull([]byte{byte(i)})
	}
	if len(c.out) != 1 {
		t.Fatalf("queued %d full frames, want only the latest", len(c.out))
	}
	if m := <-c.out; m.data[0] != clientQueueLen+4 {
		t.Errorf("kept frame %d, want the newest", m.data[0])
	}
}

// On shutdown, open sockets are closed and their held input released before Run returns, so
// main can log out of the BMC afterwards.
func TestRunReleasesHeldInputBeforeReturning(t *testing.T) {
	fc := &fakeConsole{}
	srv := NewServer(0, bmc.SessionState{})
	srv.Console = fc
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, false) }()
	eventually(t, func() bool { return srv.boundPort() != 0 })

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()
	url := fmt.Sprintf("ws://127.0.0.1:%d/ws?t=%s", srv.boundPort(), srv.token)
	conn, _, err := websocket.Dial(dialCtx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	if err := conn.Write(dialCtx, websocket.MessageText, []byte(`{"type":"key","down":true,"hidCode":225}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	eventually(t, func() bool { k, _ := fc.snapshot(); return len(k) == 1 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}
	if keys, _ := fc.snapshot(); !reflect.DeepEqual(keys, []keyEvent{{true, 225}, {false, 225}}) {
		t.Errorf("keys at return = %v; the held key must be released before logout", keys)
	}
}

func TestBroadcastGivesClientsThatFallBehindTheFullPicture(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{})
	slow, fast := newWSClient(), newWSClient()
	srv.addClient(slow)
	srv.addClient(fast)
	const w, h = 32, 16
	srv.UpdateScreen(w, h, solid(w, h, 0))
	<-fast.out

	// The initial full frame plus clientQueueLen-1 changes fill the slow queue; change number
	// clientQueueLen overflows it.
	for i := 1; i <= clientQueueLen; i++ {
		pix := solid(w, h, 0)
		setPixel(pix, w, i%w, 0, byte(i))
		srv.UpdateScreen(w, h, pix)
		for len(fast.out) > 0 {
			<-fast.out
		}
	}

	if len(slow.out) != 1 {
		t.Fatalf("slow client has %d messages queued, want just the full picture", len(slow.out))
	}
	if m := <-slow.out; m.data[0] != msgFullFrame {
		t.Errorf("slow client got type %#x, want a full frame", m.data[0])
	}
}

func TestNewClientRequestsRefreshEvenWithAPicture(t *testing.T) {
	fc := &fakeConsole{}
	srv, url := startTestServer(t, fc)
	srv.UpdateScreen(32, 16, solid(32, 16, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL(srv, url), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	eventually(t, func() bool { _, r := fc.snapshot(); return r == 1 })
}
