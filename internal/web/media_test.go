package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ipmi-connect/internal/bmc"
)

// fakeMediaConsole is a console with virtual media.
type fakeMediaConsole struct {
	fakeConsole
	mu      sync.Mutex
	mounted string
	failOn  string
}

func (f *fakeMediaConsole) MountISO(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if path == f.failOn {
		return errors.New("BMC refused")
	}
	f.mounted = path
	return nil
}

func (f *fakeMediaConsole) UnmountISO() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mounted == "" {
		return errors.New("no image is mounted")
	}
	f.mounted = ""
	return nil
}

func (f *fakeMediaConsole) MediaStatus() bmc.MediaStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.mounted == "" {
		return bmc.MediaStatus{State: bmc.MediaNone}
	}
	return bmc.MediaStatus{State: bmc.MediaMounted, Image: "x.iso"}
}

func mediaServer(t *testing.T, console bmc.Console) (*Server, http.Handler) {
	t.Helper()
	srv := NewServer(0, bmc.SessionState{})
	srv.Console = console
	h, err := srv.routes()
	if err != nil {
		t.Fatal(err)
	}
	return srv, h
}

func mediaRequest(srv *Server, h http.Handler, method, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1/api/media", strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(srv, r))
	return rec
}

func TestMediaAPI(t *testing.T) {
	fc := &fakeMediaConsole{failOn: "/bad.iso"}
	srv, h := mediaServer(t, fc)

	steps := []struct {
		name, method, body string
		wantCode           int
		wantState          bmc.MediaState
	}{
		{"status before mounting", http.MethodGet, "", http.StatusOK, bmc.MediaNone},
		{"mount", http.MethodPost, `{"path":"/isos/x.iso"}`, http.StatusOK, bmc.MediaMounted},
		{"status while mounted", http.MethodGet, "", http.StatusOK, bmc.MediaMounted},
		{"eject", http.MethodDelete, "", http.StatusOK, bmc.MediaNone},
		{"eject with nothing mounted", http.MethodDelete, "", http.StatusConflict, ""},
		{"mount failure is reported", http.MethodPost, `{"path":"/bad.iso"}`, http.StatusBadGateway, ""},
		{"missing path", http.MethodPost, `{}`, http.StatusBadRequest, ""},
		{"wrong method", http.MethodPut, "", http.StatusMethodNotAllowed, ""},
	}
	for _, st := range steps {
		rec := mediaRequest(srv, h, st.method, st.body)
		if rec.Code != st.wantCode {
			t.Errorf("%s: HTTP %d (%s), want %d", st.name, rec.Code, strings.TrimSpace(rec.Body.String()), st.wantCode)
			continue
		}
		if st.wantState != "" {
			var got bmc.MediaStatus
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.State != st.wantState {
				t.Errorf("%s: body %s, want state %s", st.name, rec.Body.String(), st.wantState)
			}
		}
	}
}

func TestMediaAPIUnsupportedConsole(t *testing.T) {
	srv, h := mediaServer(t, &fakeConsole{})
	if rec := mediaRequest(srv, h, http.MethodGet, ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("HTTP %d, want 501 for a console without virtual media", rec.Code)
	}
}

func TestMediaAPIRequiresToken(t *testing.T) {
	_, h := mediaServer(t, &fakeMediaConsole{})
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/media", strings.NewReader(`{"path":"/x.iso"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("HTTP %d, want 401 without the session token", rec.Code)
	}
}
