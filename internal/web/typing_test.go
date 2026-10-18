package web

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"ipmi-connect/internal/bmc"
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
