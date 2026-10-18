package web

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"ipmi-connect/internal/bmc"
)

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.StateMu.RLock()
	defer s.StateMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.State)
}

func (s *Server) handlePower(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !requireJSON(w, r) {
		return
	}
	var req struct {
		Action bmc.PowerAction `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[Server] Received chassis power action: %s", req.Action)

	if s.Console == nil {
		http.Error(w, "BMC console not connected", http.StatusServiceUnavailable)
		return
	}
	err := s.Console.PowerControl(req.Action)

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	s.startFollowPower()
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "action": string(req.Action)})
}

const (
	powerFollowInterval = time.Second
	powerFollowDuration = 30 * time.Second
)

// startFollowPower re-reads the power state every second for a while after a power action, so
// the UI reflects it within a second or two instead of at the next slow poll. Repeated actions
// extend the one running follower rather than starting more; it reports whether it started one.
func (s *Server) startFollowPower() bool {
	reader, ok := s.Console.(bmc.PowerReader)
	if !ok {
		return false
	}
	s.followMu.Lock()
	defer s.followMu.Unlock()
	s.followUntil = time.Now().Add(s.powerFollowFor)
	if s.following {
		return false
	}
	s.following = true
	go s.followPower(reader)
	return true
}

func (s *Server) followPower(reader bmc.PowerReader) {
	t := time.NewTicker(s.powerFollowEvery)
	defer t.Stop()
	for {
		s.followMu.Lock()
		if time.Now().After(s.followUntil) {
			s.following = false
			s.followMu.Unlock()
			return
		}
		s.followMu.Unlock()
		select {
		case <-s.connCtx.Done():
			s.followMu.Lock()
			s.following = false
			s.followMu.Unlock()
			return
		case <-t.C:
			state, err := reader.PowerState()
			if err != nil {
				state = bmc.PowerStateUnknown
			}
			s.StateMu.Lock()
			s.State.PowerState = state
			s.StateMu.Unlock()
		}
	}
}
