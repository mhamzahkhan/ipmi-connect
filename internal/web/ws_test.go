package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"ipmi-connect/internal/bmc"
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

	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: authHeader(srv)})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || !strings.Contains(string(data), `"resize"`) {
		t.Fatalf("first message = %v %q %v, want resize", typ, data, err)
	}
	eventually(t, func() bool { _, r := fc.snapshot(); return r == 1 }) // new client asks for a full frame

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

	hdr := authHeader(srv)
	hdr.Set("Origin", "https://evil.example")
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
	if err == nil {
		_ = conn.CloseNow()
		t.Fatal("cross-site WebSocket was accepted")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %v, want 403", resp)
	}
}

func TestBroadcastResyncsClientsThatFallBehind(t *testing.T) {
	fc := &fakeConsole{}
	srv := NewServer(0, bmc.SessionState{})
	srv.Console = fc
	slow := newWSClient()
	fast := newWSClient()
	srv.addClient(slow)
	srv.addClient(fast)

	for i := 0; i < clientQueueLen; i++ {
		srv.broadcast([]byte{byte(i)})
	}
	for len(fast.out) > 0 { // the fast client keeps up
		<-fast.out
	}
	srv.broadcast([]byte{0xFF}) // slow client's queue is full

	if got := len(slow.out); got != 0 {
		t.Errorf("slow client still holds %d stale frames; deltas on top of a gap corrupt the picture", got)
	}
	if got := len(fast.out); got != 1 {
		t.Errorf("fast client has %d frames queued, want 1", got)
	}
	eventually(t, func() bool { _, r := fc.snapshot(); return r == 1 })
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
