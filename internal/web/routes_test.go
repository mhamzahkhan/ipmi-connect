package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ipmi-connect/internal/bmc"
)

func TestRoutesServeEmbeddedUI(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{})
	h, err := srv.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	tests := []struct {
		path            string
		wantContentType string
		wantBodySnippet string
	}{
		{"/", "text/html", "screenCanvas"},
		{"/app.js", "application/javascript", "aspeed/decoder.js"},
		{"/style.css", "text/css", ""},
		{"/contrast.js", "application/javascript", "export function buildContrastLUT"},
		{"/keymap.js", "application/javascript", "export const KEY_TO_HID"},
		{"/aspeed/decoder.js", "application/javascript", "export function decode"},
		{"/aspeed/decoder_wasm.wasm", "application/wasm", "\x00asm"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, tt.wantContentType) {
				t.Errorf("Content-Type = %q, want prefix %q", ct, tt.wantContentType)
			}
			if !strings.Contains(rec.Body.String(), tt.wantBodySnippet) {
				t.Errorf("body does not contain %q", tt.wantBodySnippet)
			}
		})
	}
}

func TestStatusReportsSessionState(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{Host: "10.0.0.1", TargetType: bmc.TargetDell, ScreenWidth: 720})
	h, err := srv.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	var got bmc.SessionState
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Host != "10.0.0.1" || got.TargetType != bmc.TargetDell || got.ScreenWidth != 720 {
		t.Errorf("status = %+v", got)
	}
}

func TestPowerRejectsNonPost(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{})
	h, err := srv.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/power", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rec.Code)
	}
}

func TestSetScreenSizeUpdatesStatus(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{ScreenWidth: 1024, ScreenHeight: 768})
	h, err := srv.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}

	srv.SetScreenSize(720, 400)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	var got bmc.SessionState
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ScreenWidth != 720 || got.ScreenHeight != 400 {
		t.Errorf("status size = %dx%d, want 720x400", got.ScreenWidth, got.ScreenHeight)
	}
}
