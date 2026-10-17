package web

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

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

	var req struct {
		Action bmc.PowerAction `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("[Server] Received chassis power action: %s", req.Action)

	var err error
	if s.SMClient != nil {
		err = s.SMClient.PowerControl(req.Action)
	} else if s.DellClient != nil {
		err = s.DellClient.PowerControl(req.Action)
	}

	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "action": string(req.Action)})
}

func (s *Server) handleMount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseMultipartForm(500 * 1024 * 1024)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "missing file in form", http.StatusBadRequest)
		return
	}

	cleanName := filepath.Base(filepath.Clean(header.Filename))
	if cleanName == "." || cleanName == "/" || cleanName == "" {
		file.Close()
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}

	tempPath := filepath.Join(os.TempDir(), cleanName)
	out, err := os.Create(tempPath)
	if err != nil {
		file.Close()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_, err = io.Copy(out, file)
	file.Close()
	// Close flushes the written file, so its error matters as much as the copy's.
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.StateMu.Lock()
	s.MountedISO = header.Filename
	s.State.VirtualMedia = header.Filename
	s.StateMu.Unlock()

	log.Printf("[Server] Mounted local ISO image: %s (%s)", header.Filename, tempPath)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":   "mounted",
		"filename": header.Filename,
	})
}

func (s *Server) handleUnmount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.StateMu.Lock()
	s.MountedISO = ""
	s.State.VirtualMedia = ""
	s.StateMu.Unlock()

	log.Printf("[Server] Ejected / Unmounted virtual media")

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ejected"})
}
