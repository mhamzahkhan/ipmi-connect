package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

// authed makes r look like it came from the console page: local Host and the session token.
func authed(s *Server, r *http.Request) *http.Request {
	r.Host = "127.0.0.1"
	r.Header.Set("Authorization", "Bearer "+s.token)
	return r
}

// wsURL appends the session token, which the page passes as a query parameter on the socket.
func wsURL(s *Server, base string) string { return base + "?t=" + s.token }

func newGuardedServer(t *testing.T) (*Server, http.Handler, *fakeConsole) {
	t.Helper()
	fc := &fakeConsole{}
	srv := NewServer(0, bmc.SessionState{})
	srv.Console = fc
	h, err := srv.routes()
	if err != nil {
		t.Fatalf("routes: %v", err)
	}
	return srv, h, fc
}

func TestLaunchCodeIsExchangedOnceForTheToken(t *testing.T) {
	srv, h, _ := newGuardedServer(t)

	exchange := func(code string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/session", strings.NewReader(`{"code":"`+code+`"}`))
		r.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}

	if rec := exchange("wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong code: status %d, want 401", rec.Code)
	}
	rec := exchange(srv.launchCode)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), srv.token) {
		t.Fatalf("first exchange: status %d body %q", rec.Code, rec.Body.String())
	}
	if rec := exchange(srv.launchCode); rec.Code != http.StatusUnauthorized {
		t.Errorf("second exchange: status %d, want 401 (the launch link is visible in the process list)", rec.Code)
	}
	if strings.Contains(srv.LaunchURL(), srv.token) {
		t.Error("launch URL contains the long-lived token")
	}
	if !strings.Contains(srv.LaunchURL(), "#c="+srv.launchCode) {
		t.Errorf("launch URL %q should carry the code in the fragment, which is never sent to a server", srv.LaunchURL())
	}
}

func TestStaticAssetsNeedNoSession(t *testing.T) {
	_, h, _ := newGuardedServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status %d: the page must load before it can exchange its launch code", rec.Code)
	}
}

func TestGuardRejects(t *testing.T) {
	srv, h, fc := newGuardedServer(t)

	tests := []struct {
		name   string
		req    func() *http.Request
		status int
	}{
		{"API without session", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/status", nil)
		}, http.StatusUnauthorized},
		{"API with wrong token", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/status", nil)
			r.Header.Set("Authorization", "Bearer nope")
			return r
		}, http.StatusUnauthorized},
		{"API with token in a cookie only", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/status", nil)
			r.AddCookie(&http.Cookie{Name: "ipmi_connect_session", Value: srv.token})
			return r
		}, http.StatusUnauthorized},
		{"DNS rebinding Host", func() *http.Request {
			r := authed(srv, httptest.NewRequest(http.MethodGet, "/api/status", nil))
			r.Host = "evil.example:8080"
			return r
		}, http.StatusForbidden},
		{"power as text/plain (no-CORS form post)", func() *http.Request {
			return authed(srv, httptest.NewRequest(http.MethodPost, "/api/power", strings.NewReader(`{"action":"power_off"}`)))
		}, http.StatusUnsupportedMediaType},
		{"power from another origin", func() *http.Request {
			r := authed(srv, httptest.NewRequest(http.MethodPost, "/api/power", strings.NewReader(`{"action":"power_off"}`)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Origin", "https://evil.example")
			return r
		}, http.StatusForbidden},
		{"power from a cross-site fetch", func() *http.Request {
			r := authed(srv, httptest.NewRequest(http.MethodPost, "/api/power", strings.NewReader(`{"action":"power_off"}`)))
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Sec-Fetch-Site", "cross-site")
			return r
		}, http.StatusForbidden},
		{"oversized power body", func() *http.Request {
			r := authed(srv, httptest.NewRequest(http.MethodPost, "/api/power", strings.NewReader(`{"action":"`+strings.Repeat("x", 10000)+`"}`)))
			r.Header.Set("Content-Type", "application/json")
			return r
		}, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, tt.req())
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d", rec.Code, tt.status)
			}
		})
	}
	if len(fc.power) != 0 {
		t.Errorf("a rejected request reached the BMC: %v", fc.power)
	}
}

func TestGuardAllowsSameOriginPower(t *testing.T) {
	srv, h, fc := newGuardedServer(t)

	r := authed(srv, httptest.NewRequest(http.MethodPost, "/api/power", strings.NewReader(`{"action":"power_on"}`)))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)

	if rec.Code != http.StatusOK || len(fc.power) != 1 || fc.power[0] != bmc.PowerOn {
		t.Errorf("status %d, power %v", rec.Code, fc.power)
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv, h, _ := newGuardedServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(srv, httptest.NewRequest(http.MethodGet, "/", nil)))

	for header, want := range map[string]string{
		"Content-Security-Policy": "frame-ancestors 'none'",
		"X-Frame-Options":         "DENY",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
	} {
		if got := rec.Header().Get(header); !strings.Contains(got, want) {
			t.Errorf("%s = %q, want it to contain %q", header, got, want)
		}
	}
}

func TestVirtualMediaUploadIsGone(t *testing.T) {
	srv, h, _ := newGuardedServer(t)
	r := authed(srv, httptest.NewRequest(http.MethodPost, "/api/vmedia/mount", strings.NewReader("x")))
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404: the fake upload endpoint wrote files to disk", rec.Code)
	}
}
