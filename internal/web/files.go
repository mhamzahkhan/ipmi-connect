package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxListing bounds one folder listing.
const maxListing = 5000

type fileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}

// handleFiles lists a folder on this machine for the image picker: subfolders and, in "file"
// mode, .iso and .img files. Browsers never reveal a picked file's real path, and copying
// images through the browser would break writes to .img files, so the server, which runs on
// the user's own machine, browses for them. Hidden entries are left out; file contents are
// never read.
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			http.Error(w, "no home folder", http.StatusBadRequest)
			return
		}
		path = home
	}
	if !filepath.IsAbs(path) {
		http.Error(w, "give a full path", http.StatusBadRequest)
		return
	}
	path = filepath.Clean(path)
	dirents, err := os.ReadDir(path)
	if err != nil {
		http.Error(w, "cannot open that folder", http.StatusBadRequest)
		return
	}
	files := r.URL.Query().Get("mode") == "file"

	entries := []fileEntry{}
	for _, d := range dirents {
		if strings.HasPrefix(d.Name(), ".") || len(entries) >= maxListing {
			continue
		}
		info, err := os.Stat(filepath.Join(path, d.Name())) // follows links
		if err != nil {
			continue
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		switch {
		case info.IsDir():
			entries = append(entries, fileEntry{Name: d.Name(), Dir: true})
		case files && info.Mode().IsRegular() && (ext == ".iso" || ext == ".img"):
			entries = append(entries, fileEntry{Name: d.Name(), Size: info.Size()})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"path": path, "parent": filepath.Dir(path), "entries": entries})
}
