package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ipmi-connect/internal/bmc"
)

// authed makes r look like it came from the console page: local Host and the session cookie.
func authed(s *Server, r *http.Request) *http.Request {
	r.Host = "127.0.0.1"
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: s.token})
	return r
}

func authHeader(s *Server) http.Header {
	return http.Header{"Cookie": []string{sessionCookie + "=" + s.token}}
}

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

func TestAuthLinkSetsSessionCookie(t *testing.T) {
	srv, h, _ := newGuardedServer(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/auth?t="+srv.token, nil))

	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("status %d location %q, want 303 to /", rec.Code, rec.Header().Get("Location"))
	}
	cookie := rec.Header().Get("Set-Cookie")
	for _, want := range []string{sessionCookie + "=" + srv.token, "HttpOnly", "SameSite=Strict", "Path=/"} {
		if !strings.Contains(cookie, want) {
			t.Errorf("Set-Cookie %q lacks %q", cookie, want)
		}
	}
}

func TestGuardRejects(t *testing.T) {
	srv, h, fc := newGuardedServer(t)

	tests := []struct {
		name   string
		req    func() *http.Request
		status int
	}{
		{"page without session", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil)
		}, http.StatusUnauthorized},
		{"auth link with wrong token", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "http://127.0.0.1/auth?t=nope", nil)
		}, http.StatusUnauthorized},
		{"wrong session cookie", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/api/status", nil)
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: "nope"})
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
