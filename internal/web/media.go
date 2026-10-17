package web

import (
	"encoding/json"
	"log"
	"net/http"

	"ipmi-connect/internal/bmc"
)

// handleMedia serves the virtual CD: GET reports it, POST {"path": ...} mounts a local image,
// DELETE ejects. The path is on this machine; the server and browser are the same user's.
func (s *Server) handleMedia(w http.ResponseWriter, r *http.Request) {
	vm, ok := s.Console.(bmc.VirtualMedia)
	if !ok {
		http.Error(w, "virtual media is not supported for this BMC", http.StatusNotImplemented)
		return
	}
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if !requireJSON(w, r) {
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Path == "" {
			http.Error(w, "send {\"path\": \"/full/path/to/image.iso\"}", http.StatusBadRequest)
			return
		}
		log.Printf("[Server] Mounting virtual media image %q", req.Path)
		if err := vm.MountISO(req.Path); err != nil {
			writeJSONError(w, http.StatusBadGateway, err)
			return
		}
	case http.MethodDelete:
		if err := vm.UnmountISO(); err != nil {
			writeJSONError(w, http.StatusConflict, err)
			return
		}
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(vm.MediaStatus())
}

func writeJSONError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
