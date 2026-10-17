package web

import (
	"encoding/json"
	"log"
	"net/http"

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

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "action": string(req.Action)})
}
