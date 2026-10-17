package supermicro

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// recordingSink captures what the session hands to the web layer.
type recordingSink struct {
	mu       sync.Mutex
	frames   int
	w, h     int
	pix      []byte
	statuses []string
}

func (s *recordingSink) UpdateScreen(w, h int, pix []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.frames++
	s.w, s.h = w, h
	s.pix = append([]byte(nil), pix...)
}

func (s *recordingSink) SetStatus(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statuses = append(s.statuses, text)
}

func (s *recordingSink) snapshot() (int, int, int, []byte, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.frames, s.w, s.h, s.pix, append([]string(nil), s.statuses...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// bmcSide reads what the client sends, in order, on the fake BMC's end of the pipe.
type bmcSide struct {
	conn net.Conn
	mu   sync.Mutex
	got  bytes.Buffer
}

func (b *bmcSide) readLoop() {
	buf := make([]byte, 256)
	for {
		n, err := b.conn.Read(buf)
		b.mu.Lock()
		b.got.Write(buf[:n])
		b.mu.Unlock()
		if err != nil {
			return
		}
	}
}

func (b *bmcSide) sent() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.got.Bytes()...)
}

func startSession(t *testing.T, info ServerInfo) (*Session, *bmcSide, *recordingSink, context.CancelFunc) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	s := newSession(client, bufio.NewReader(client), info)
	s.keepAliveEvery = 50 * time.Millisecond
	bmc := &bmcSide{conn: server}
	go bmc.readLoop()
	sink := &recordingSink{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = s.Run(ctx, sink) }()
	return s, bmc, sink, cancel
}

func hermonUpdate(w, h uint16, payload []byte) []byte {
	return fbu(rect(0, 0, w, h, 0x59, payload))
}

func TestSessionStartsLikeTheVendorClient(t *testing.T) {
	_, bmc, _, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	want := append(mouseInfoRequest(), framebufferUpdateRequest(false, 0x27f, 0x1df)...)
	waitFor(t, "init messages", func() bool { return len(bmc.sent()) >= len(want) })
	if got := bmc.sent()[:len(want)]; !bytes.Equal(got, want) {
		t.Errorf("init sent % x, want % x", got, want)
	}
	waitFor(t, "keep-alive", func() bool { return bytes.Contains(bmc.sent(), keepAlive()) })
}

func TestSessionDecodesUpdatesAndRequestsTheNext(t *testing.T) {
	_, bmc, sink, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	go func() {
		_, _ = bmc.conn.Write(hermonUpdate(32, 16, hermon(0, 0, 1, tile(0, 1, []byte{0x00, 0x7C}, 256))))
	}()

	waitFor(t, "a frame", func() bool { n, _, _, _, _ := sink.snapshot(); return n > 0 })
	_, w, h, pix, _ := sink.snapshot()
	if w != 32 || h != 16 || rgbaAt(pix, 32, 16, 0) != red {
		t.Errorf("frame %dx%d, pixel (16,0) = %v", w, h, rgbaAt(pix, w, 16, 0))
	}
	go func() { _, _ = bmc.conn.Write(hermonUpdate(32, 16, hermon(0, 0, 0, nil))) }()
	waitFor(t, "incremental update request", func() bool {
		return bytes.Contains(bmc.sent(), framebufferUpdateRequest(true, 32, 16))
	})
}

func TestSessionReportsNoSignalWithoutSpinning(t *testing.T) {
	_, bmc, sink, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	go func() { _, _ = bmc.conn.Write(fbu(rect(0, 0, 0xFD80, 0xFE20, 0x59, nil))) }()

	waitFor(t, "no-signal status", func() bool {
		_, _, _, _, st := sink.snapshot()
		return len(st) > 0 && st[len(st)-1] == statusNoSignal
	})
	time.Sleep(100 * time.Millisecond)
	if bytes.Contains(bmc.sent(), framebufferUpdateRequest(true, 0xFD80, 0xFE20)) {
		t.Error("answered a screen-off update with an immediate request (spins at ~1000/s on real BMCs)")
	}
}

func TestSessionInput(t *testing.T) {
	s, bmc, _, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	if err := s.SendKeyEvent(true, 0x04); err != nil {
		t.Fatal(err)
	}
	if err := s.SendPointerEvent(1, 10, 20); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "key and pointer", func() bool {
		got := bmc.sent()
		return bytes.Contains(got, keyEvent(true, 4)) && bytes.Contains(got, pointerEvent(1, 10, 20))
	})
}

func TestSessionEncryptsMouseWhenAsked(t *testing.T) {
	s, bmc, _, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	go func() { _, _ = bmc.conn.Write([]byte{0x37, 1, 0, 1}) }()
	waitFor(t, "mouse info", func() bool { return s.encryptMouse.Load() })

	if err := s.SendPointerEvent(1, 10, 20); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "encrypted pointer", func() bool { return bytes.Contains(bmc.sent(), []byte{0x05, 0x01}) })
	if bytes.Contains(bmc.sent(), pointerEvent(1, 10, 20)) {
		t.Error("sent a plaintext pointer event after the BMC asked for encryption")
	}
}

// The trailer's keyboard/mouse flag is unverified on this firmware, so a view-only session only
// says so; input is still sent (as smkvm does) and the BMC decides.
func TestSessionViewOnlyStillSendsInput(t *testing.T) {
	s, bmc, sink, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: false})
	if err := s.SendKeyEvent(true, 4); err != nil {
		t.Fatalf("SendKeyEvent: %v", err)
	}
	waitFor(t, "key event", func() bool { return bytes.Contains(bmc.sent(), keyEvent(true, 4)) })
	_, _, _, _, st := sink.snapshot()
	if len(st) == 0 || st[0] != statusViewOnly {
		t.Errorf("statuses %q, want view-only notice", st)
	}
}

func TestSessionIgnoresImplausibleResolution(t *testing.T) {
	_, bmc, sink, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	go func() {
		_, _ = bmc.conn.Write(hermonUpdate(0xFFFF, 0xFFFF, hermon(0, 0, 0, nil)))
		_, _ = bmc.conn.Write(hermonUpdate(32, 16, hermon(0, 0, 1, tile(0, 0, []byte{0x00, 0x7C}, 256))))
	}()
	waitFor(t, "the plausible frame", func() bool { n, _, _, _, _ := sink.snapshot(); return n > 0 })
	if _, w, h, _, _ := sink.snapshot(); w != 32 || h != 16 {
		t.Errorf("frame %dx%d, want 32x16", w, h)
	}
}

func TestSessionRequestsFullFrameAfterResize(t *testing.T) {
	_, bmc, _, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	go func() { _, _ = bmc.conn.Write(hermonUpdate(32, 16, hermon(0, 0, 0, nil))) }()
	waitFor(t, "full request at the new size", func() bool {
		return bytes.Contains(bmc.sent(), framebufferUpdateRequest(false, 32, 16))
	})
	go func() { _, _ = bmc.conn.Write(hermonUpdate(32, 16, hermon(0, 0, 0, nil))) }()
	waitFor(t, "incremental request once the size is stable", func() bool {
		return bytes.Contains(bmc.sent(), framebufferUpdateRequest(true, 32, 16))
	})
}

func TestSessionClearsTransientStatus(t *testing.T) {
	_, bmc, sink, _ := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	go func() {
		_, _ = bmc.conn.Write(fbu(rect(0, 0, 1, 1, 0x59, nil)))
		_, _ = bmc.conn.Write(hermonUpdate(32, 16, hermon(0, 0, 1, tile(0, 0, []byte{0x00, 0x7C}, 256))))
	}()
	waitFor(t, "status cleared after video resumed", func() bool {
		_, _, _, _, st := sink.snapshot()
		return len(st) >= 2 && st[0] == statusOtherVideo && st[len(st)-1] == ""
	})
}

func TestSessionSendFailsAfterEnd(t *testing.T) {
	s, _, _, cancel := startSession(t, ServerInfo{Video: true, KeyboardMouse: true})
	cancel()
	<-s.done
	for i := 0; i < 20; i++ {
		if err := s.SendKeyEvent(true, 4); err == nil {
			t.Fatal("send after the session ended reported success")
		}
	}
}

func TestSessionEndsOnProtocolError(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	s := newSession(client, bufio.NewReader(client), ServerInfo{Video: true, KeyboardMouse: true})
	go func() { _, _ = io.Copy(io.Discard, server) }()
	go func() { _, _ = server.Write([]byte{0x7f, 0, 0}) }()
	errc := make(chan error, 1)
	go func() { errc <- s.Run(context.Background(), &recordingSink{}) }()
	select {
	case err := <-errc:
		if err == nil {
			t.Error("want error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not end on an unknown message")
	}
}
