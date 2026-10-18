package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

func keyLog(fc *fakeConsole) string {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	var parts []string
	for _, k := range fc.keys {
		dir := "up"
		if k.down {
			dir = "down"
		}
		parts = append(parts, fmt.Sprintf("%02x %s", k.key, dir))
	}
	return strings.Join(parts, ", ")
}

func TestTypeText(t *testing.T) {
	fc := &fakeConsole{}
	srv := NewServer(0, bmc.SessionState{})
	srv.Console = fc
	srv.typeDelay = 0

	typed, skipped, err := srv.typeText(context.Background(), "aB\né")
	if err != nil || typed != 3 || skipped != "é" {
		t.Fatalf("typed %d, skipped %q, err %v", typed, skipped, err)
	}
	want := "04 down, 04 up, e1 down, 05 down, 05 up, e1 up, 28 down, 28 up"
	if got := keyLog(fc); got != want {
		t.Errorf("keys\n got %s\nwant %s", got, want)
	}
}

func TestTypeTextLimits(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{})
	srv.Console = &fakeConsole{}
	srv.typeDelay = 0
	if _, _, err := srv.typeText(context.Background(), strings.Repeat("a", maxTypeRunes+1)); err == nil {
		t.Error("accepted text over the limit")
	}

	// One paste at a time.
	srv.typeDelay = 5 * time.Millisecond
	done := make(chan struct{})
	go func() {
		_, _, _ = srv.typeText(context.Background(), strings.Repeat("a", 50))
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	if _, _, err := srv.typeText(context.Background(), "b"); err == nil {
		t.Error("a second paste ran while the first was typing")
	}
	<-done
}

func TestTypeTextCancel(t *testing.T) {
	fc := &fakeConsole{}
	srv := NewServer(0, bmc.SessionState{})
	srv.Console = fc
	srv.typeDelay = 5 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(30 * time.Millisecond); cancel() }()
	typed, _, err := srv.typeText(ctx, strings.Repeat("A", 200))
	if err == nil || typed >= 200 {
		t.Errorf("typed %d, err %v: want a cancelled, partial paste", typed, err)
	}
	if log := keyLog(fc); strings.Count(log, "down") != strings.Count(log, "up") {
		t.Errorf("a key was left held after cancelling: %s", log)
	}
}

func TestReplySurvivesVideoDrain(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{})
	c := newWSClient()
	for i := 0; i < clientQueueLen; i++ {
		c.out <- outMessage{data: []byte{0x04}}
	}
	srv.reply(c, map[string]any{"type": "typed", "count": 3})
	c.queueFullFrame([]byte{0x02}) // a slow browser is resynchronised
	select {
	case m := <-c.ctrl:
		if !strings.Contains(string(m.data), `"typed"`) {
			t.Errorf("control message %s", m.data)
		}
	default:
		t.Fatal("the paste result was dropped with the video backlog")
	}
}

// failingUpConsole fails every key release once.
type failingUpConsole struct {
	fakeConsole
	failed map[uint16]bool
}

func (f *failingUpConsole) SendKeyEvent(down bool, usage uint16) error {
	if !down && !f.failed[usage] {
		f.failed[usage] = true
		_ = f.fakeConsole.SendKeyEvent(down, usage)
		return errors.New("send failed")
	}
	return f.fakeConsole.SendKeyEvent(down, usage)
}

func TestTypingReleasesKeysEvenWhenAReleaseFails(t *testing.T) {
	fc := &failingUpConsole{failed: map[uint16]bool{}}
	srv := NewServer(0, bmc.SessionState{})
	srv.Console = fc
	srv.typeDelay = 0
	if _, _, err := srv.typeText(context.Background(), "A"); err == nil {
		t.Fatal("want the send error")
	}
	log := keyLog(&fc.fakeConsole)
	if !strings.Contains(log, "04 up") || !strings.Contains(log, "e1 up") {
		t.Errorf("A or Shift not released: %s", log)
	}
}

func TestOtherTabsInputIsIgnoredWhileTyping(t *testing.T) {
	fc := &fakeConsole{}
	srv := NewServer(0, bmc.SessionState{})
	other := newInputState(fc)
	other.typing = &srv.typing // as handleWS wires it
	srv.typing.Store(true)
	other.handle([]byte(`{"type":"macro","name":"cad"}`))
	other.handle([]byte(`{"type":"key","down":true,"hidCode":4}`))
	if len(fc.keys) != 0 {
		t.Errorf("another tab's input reached the host during a paste: %v", fc.keys)
	}
}
