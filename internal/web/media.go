package web

import (
	"encoding/json"
	"log"
	"net/http"

	"ipmi-connect/internal/bmc"
	"ipmi-connect/internal/diskimage"
)

// mediaRoutes registers the virtual media API. Paths name files on this machine; the server
// and the browser belong to the same user, behind the session token.
func (s *Server) mediaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/media", s.media(func(vm bmc.VirtualMedia, r *http.Request) error { return nil }))
	mux.HandleFunc("POST /api/media/images", s.mediaJSON(func(vm bmc.VirtualMedia, r *http.Request) error {
		var req struct {
			Path     string `json:"path"`
			ReadOnly bool   `json:"read_only"`
		}
		if err := decode(r, &req); err != nil || req.Path == "" {
			return badRequest(`send {"path": "/full/path/to/image.iso", "read_only": false}`)
		}
		_, err := vm.AddImage(req.Path, req.ReadOnly)
		return err
	}))
	mux.HandleFunc("PATCH /api/media/images/{id}", s.mediaJSON(func(vm bmc.VirtualMedia, r *http.Request) error {
		var req struct {
			ReadOnly *bool `json:"read_only"`
		}
		if err := decode(r, &req); err != nil || req.ReadOnly == nil {
			return badRequest(`send {"read_only": true}`)
		}
		return vm.SetImageReadOnly(r.PathValue("id"), *req.ReadOnly)
	}))
	mux.HandleFunc("DELETE /api/media/images/{id}", s.media(func(vm bmc.VirtualMedia, r *http.Request) error {
		return vm.RemoveImage(r.PathValue("id"))
	}))
	mux.HandleFunc("POST /api/media/drives/{target}", s.mediaJSON(func(vm bmc.VirtualMedia, r *http.Request) error {
		target, err := mediaTarget(r)
		if err != nil {
			return err
		}
		var req struct {
			ImageID string `json:"image_id"`
		}
		if err := decode(r, &req); err != nil || req.ImageID == "" {
			return badRequest(`send {"image_id": "..."}`)
		}
		log.Printf("[Server] Mapping image %s to the %s drive", req.ImageID, target)
		return vm.MapImage(req.ImageID, target)
	}))
	mux.HandleFunc("DELETE /api/media/drives/{target}", s.media(func(vm bmc.VirtualMedia, r *http.Request) error {
		target, err := mediaTarget(r)
		if err != nil {
			return err
		}
		return vm.UnmapDrive(target)
	}))
	mux.HandleFunc("POST /api/media/create", s.mediaJSON(func(vm bmc.VirtualMedia, r *http.Request) error {
		var req struct {
			Path   string `json:"path"`
			Size   int64  `json:"size"`   // a blank image of this many bytes, or
			Folder string `json:"folder"` // an image holding a copy of this folder
			Label  string `json:"label"`
		}
		if err := decode(r, &req); err != nil || req.Path == "" || (req.Size > 0) == (req.Folder != "") {
			return badRequest(`send {"path": "/full/path/new.img", "size": bytes} or {"path": ..., "folder": "/full/path"}`)
		}
		var err error
		if req.Folder != "" {
			log.Printf("[Server] Creating image %q from %q", req.Path, req.Folder)
			err = diskimage.CreateFromFolder(req.Path, req.Folder, req.Label)
		} else {
			log.Printf("[Server] Creating a blank %d-byte image %q", req.Size, req.Path)
			err = diskimage.CreateBlank(req.Path, req.Size, req.Label)
		}
		if err != nil {
			return err
		}
		_, err = vm.AddImage(req.Path, false)
		return err
	}))
	mux.HandleFunc("POST /api/media/usb-reset", s.media(func(vm bmc.VirtualMedia, r *http.Request) error {
		return vm.USBReset()
	}))
}

type badRequest string

func (e badRequest) Error() string { return string(e) }

func mediaTarget(r *http.Request) (bmc.MediaTarget, error) {
	switch t := bmc.MediaTarget(r.PathValue("target")); t {
	case bmc.TargetCD, bmc.TargetRemovable, bmc.TargetFloppy:
		return t, nil
	}
	return "", badRequest("the drive must be cd, removable or floppy")
}

func decode(r *http.Request, v any) error { return json.NewDecoder(r.Body).Decode(v) }

// media wraps a virtual media action: it answers with the full media status, or the action's
// error (400 for bad input, 502 for anything the console or BMC refused).
func (s *Server) media(action func(bmc.VirtualMedia, *http.Request) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vm, ok := s.Console.(bmc.VirtualMedia)
		if !ok {
			http.Error(w, "virtual media is not supported for this BMC", http.StatusNotImplemented)
			return
		}
		if err := action(vm, r); err != nil {
			code := http.StatusBadGateway
			if _, bad := err.(badRequest); bad {
				code = http.StatusBadRequest
			}
			writeJSONError(w, code, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(vm.MediaStatus())
	}
}

// mediaJSON is media for actions with a JSON body.
func (s *Server) mediaJSON(action func(bmc.VirtualMedia, *http.Request) error) http.HandlerFunc {
	h := s.media(action)
	return func(w http.ResponseWriter, r *http.Request) {
		if requireJSON(w, r) {
			h(w, r)
		}
	}
}

func writeJSONError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
