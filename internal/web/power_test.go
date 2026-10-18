package web

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

// powerConsole reports OFF until a power action, then ON.
type powerConsole struct {
	fakeConsole
	mu    sync.Mutex
	state string
	reads int
}

func (p *powerConsole) PowerControl(a bmc.PowerAction) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state = "ON"
	return nil
}

func (p *powerConsole) PowerState() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	return p.state, nil
}

func TestPowerActionIsFollowedByQuickStatusPolls(t *testing.T) {
	pc := &powerConsole{state: "OFF"}
	srv, h := mediaServer(t, pc)
	srv.powerFollowEvery, srv.powerFollowFor = 10*time.Millisecond, 200*time.Millisecond

	r := httptestRequest(srv, http.MethodPost, "/api/power", `{"action":"power_on"}`)
	rec := newRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != 200 {
		t.Fatalf("HTTP %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		srv.StateMu.RLock()
		state := srv.State.PowerState
		srv.StateMu.RUnlock()
		if state == "ON" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("power state %q, want ON shortly after the action", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	pc.mu.Lock()
	reads := pc.reads
	pc.mu.Unlock()
	if reads < 3 || reads > 40 {
		t.Errorf("%d status reads, want a short burst that then stops", reads)
	}
}

func httptestRequest(srv *Server, method, path, body string) *http.Request {
	r, _ := http.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return authed(srv, r)
}

func TestRepeatedPowerActionsShareOneFollower(t *testing.T) {
	pc := &powerConsole{state: "OFF"}
	srv, _ := mediaServer(t, pc)
	srv.powerFollowEvery, srv.powerFollowFor = 10*time.Millisecond, 100*time.Millisecond
	if !srv.startFollowPower() {
		t.Fatal("first follower did not start")
	}
	if srv.startFollowPower() {
		t.Error("a second follower started instead of extending the first")
	}
}
