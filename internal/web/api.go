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

	go s.followPower()
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "action": string(req.Action)})
}

const (
	powerFollowInterval = time.Second
	powerFollowDuration = 30 * time.Second
)

// followPower re-reads the power state every second for a while after a power action, so the
// UI reflects it within a second or two instead of at the next slow poll.
func (s *Server) followPower() {
	reader, ok := s.Console.(bmc.PowerReader)
	if !ok {
		return
	}
	t := time.NewTicker(s.powerFollowEvery)
	defer t.Stop()
	stop := time.After(s.powerFollowFor)
	for {
		select {
		case <-stop:
			return
		case <-s.connCtx.Done():
			return
		case <-t.C:
			if state, err := reader.PowerState(); err == nil {
				s.StateMu.Lock()
				s.State.PowerState = state
				s.StateMu.Unlock()
			}
		}
	}
}
