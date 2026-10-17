package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"ipmi-connect/internal/bmc"
)

// fakeMediaConsole is a console with virtual media; it records calls.
type fakeMediaConsole struct {
	fakeConsole
	mu     sync.Mutex
	calls  []string
	failOn string
}

func (f *fakeMediaConsole) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
	if f.failOn != "" && strings.HasPrefix(call, f.failOn) {
		return errors.New("BMC refused")
	}
	return nil
}

func (f *fakeMediaConsole) AddImage(path string, ro bool) (bmc.MediaImage, error) {
	if err := f.record("add " + path); err != nil {
		return bmc.MediaImage{}, err
	}
	return bmc.MediaImage{ID: "abc", Path: path, ReadOnly: ro}, nil
}
func (f *fakeMediaConsole) RemoveImage(id string) error { return f.record("remove " + id) }
func (f *fakeMediaConsole) SetImageReadOnly(id string, ro bool) error {
	if ro {
		return f.record("readonly " + id)
	}
	return f.record("writable " + id)
}
func (f *fakeMediaConsole) MapImage(id string, t bmc.MediaTarget) error {
	return f.record("map " + id + " " + string(t))
}
func (f *fakeMediaConsole) UnmapDrive(t bmc.MediaTarget) error { return f.record("unmap " + string(t)) }
func (f *fakeMediaConsole) USBReset() error                    { return f.record("usb-reset") }
func (f *fakeMediaConsole) MediaStatus() bmc.MediaStatus {
	return bmc.MediaStatus{Images: []bmc.MediaImage{}, Drives: []bmc.MediaDrive{}}
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

func mediaRequest(srv *Server, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(srv, r))
	return rec
}

func TestMediaAPI(t *testing.T) {
	fc := &fakeMediaConsole{}
	srv, h := mediaServer(t, fc)
	steps := []struct {
		method, path, body string
		wantCode           int
		wantCall           string
	}{
		{http.MethodGet, "/api/media", "", 200, ""},
		{http.MethodPost, "/api/media/images", `{"path":"/isos/x.iso","read_only":true}`, 200, "add /isos/x.iso"},
		{http.MethodPatch, "/api/media/images/abc", `{"read_only":false}`, 200, "writable abc"},
		{http.MethodPost, "/api/media/drives/cd", `{"image_id":"abc"}`, 200, "map abc cd"},
		{http.MethodDelete, "/api/media/drives/cd", "", 200, "unmap cd"},
		{http.MethodPost, "/api/media/usb-reset", "", 200, "usb-reset"},
		{http.MethodDelete, "/api/media/images/abc", "", 200, "remove abc"},
		{http.MethodPost, "/api/media/drives/tape", `{"image_id":"abc"}`, 400, ""},
		{http.MethodPost, "/api/media/images", `{}`, 400, ""},
		{http.MethodPost, "/api/media/drives/cd", `{}`, 400, ""},
	}
	for _, st := range steps {
		before := len(fc.calls)
		rec := mediaRequest(srv, h, st.method, st.path, st.body)
		if rec.Code != st.wantCode {
			t.Errorf("%s %s: HTTP %d (%s), want %d", st.method, st.path, rec.Code, strings.TrimSpace(rec.Body.String()), st.wantCode)
			continue
		}
		if st.wantCall != "" && (len(fc.calls) != before+1 || fc.calls[before] != st.wantCall) {
			t.Errorf("%s %s: calls %v, want %q", st.method, st.path, fc.calls[before:], st.wantCall)
		}
		if rec.Code == 200 {
			var status bmc.MediaStatus
			if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || status.Images == nil {
				t.Errorf("%s %s: body %s is not a media status", st.method, st.path, rec.Body.String())
			}
		}
	}
}

func TestMediaAPIReportsConsoleErrors(t *testing.T) {
	srv, h := mediaServer(t, &fakeMediaConsole{failOn: "map"})
	rec := mediaRequest(srv, h, http.MethodPost, "/api/media/drives/cd", `{"image_id":"abc"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "BMC refused") {
		t.Errorf("HTTP %d %s", rec.Code, rec.Body.String())
	}
}

func TestMediaAPIUnsupportedConsole(t *testing.T) {
	srv, h := mediaServer(t, &fakeConsole{})
	if rec := mediaRequest(srv, h, http.MethodGet, "/api/media", ""); rec.Code != http.StatusNotImplemented {
		t.Errorf("HTTP %d, want 501 for a console without virtual media", rec.Code)
	}
}

func TestMediaAPIRequiresTokenAndJSON(t *testing.T) {
	srv, h := mediaServer(t, &fakeMediaConsole{})
	r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/media/images", strings.NewReader(`{"path":"/x.iso"}`))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("HTTP %d, want 401 without the session token", rec.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/media/images", strings.NewReader(`{"path":"/x.iso"}`))
	r.Header.Set("Content-Type", "text/plain")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, authed(srv, r))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("HTTP %d, want 415 for a non-JSON body", rec.Code)
	}
}

func TestCreateImageAPI(t *testing.T) {
	fc := &fakeMediaConsole{}
	srv, h := mediaServer(t, fc)
	dir := t.TempDir()
	blank := dir + "/rescue.img"
	rec := mediaRequest(srv, h, http.MethodPost, "/api/media/create", `{"path":"`+blank+`","size":1474560,"label":"rescue"}`)
	if rec.Code != 200 {
		t.Fatalf("blank: HTTP %d %s", rec.Code, rec.Body.String())
	}
	if st, err := os.Stat(blank); err != nil || st.Size() != 1474560 {
		t.Errorf("blank image: %v", err)
	}
	if fc.calls[len(fc.calls)-1] != "add "+blank {
		t.Errorf("calls %v: the new image was not added to the list", fc.calls)
	}

	src := t.TempDir()
	_ = os.WriteFile(src+"/a.txt", []byte("x"), 0o600)
	rec = mediaRequest(srv, h, http.MethodPost, "/api/media/create", `{"path":"`+dir+`/files.img","folder":"`+src+`"}`)
	if rec.Code != 200 {
		t.Fatalf("folder: HTTP %d %s", rec.Code, rec.Body.String())
	}

	for _, body := range []string{
		`{"path":"` + dir + `/x.img"}`,                             // neither size nor folder
		`{"path":"` + dir + `/y.img","size":1474560,"folder":"/"}`, // both
	} {
		if rec := mediaRequest(srv, h, http.MethodPost, "/api/media/create", body); rec.Code != 400 {
			t.Errorf("%s: HTTP %d, want 400", body, rec.Code)
		}
	}
	if rec := mediaRequest(srv, h, http.MethodPost, "/api/media/create", `{"path":"`+blank+`","size":1474560}`); rec.Code != 502 ||
		!strings.Contains(rec.Body.String(), "exists") {
		t.Errorf("overwrite: HTTP %d %s", rec.Code, rec.Body.String())
	}
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }
