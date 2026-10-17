package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
)

const (
	sessionCookie = "ipmi_connect_session"
	// maxJSONBody bounds API request bodies; every API payload is a few bytes of JSON.
	maxJSONBody = 4096
)

// contentSecurityPolicy allows only this origin's own assets and forbids framing (clickjacking
// the power buttons). Inline style attributes are used by the page.
const contentSecurityPolicy = "default-src 'self'; connect-src 'self'; img-src 'self' data: blob:; " +
	"style-src 'self' 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// newToken returns a random per-run secret.
func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b) // crypto/rand.Read never fails (Go 1.24+)
	return hex.EncodeToString(b)
}

// guard protects every route of the local console:
//   - Host must be a loopback name, defeating DNS rebinding;
//   - a per-run session (cookie set via /auth?t=<token>, or a Bearer token) is required, so other
//     sites and other local users cannot drive the console;
//   - state-changing requests must not come from another origin.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")

		if !isLoopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/auth" {
			s.handleAuth(w, r)
			return
		}
		if !s.authorized(r) {
			http.Error(w, "Not signed in: open the console link printed in the terminal.", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && isCrossSite(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleAuth exchanges the token from the terminal link for a session cookie, then redirects so
// the token leaves the address bar and browser history.
func (s *Server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if !s.tokenMatches(r.URL.Query().Get("t")) {
		http.Error(w, "invalid console link", http.StatusUnauthorized)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    s.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) authorized(r *http.Request) bool {
	if c, err := r.Cookie(sessionCookie); err == nil && s.tokenMatches(c.Value) {
		return true
	}
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && s.tokenMatches(bearer)
}

func (s *Server) tokenMatches(candidate string) bool {
	return subtle.ConstantTimeCompare([]byte(candidate), []byte(s.token)) == 1
}

// isLoopbackHost reports whether a Host header names this machine's loopback interface.
func isLoopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport // no port
	}
	host = strings.Trim(host, "[]")
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

// isCrossSite reports whether the browser says the request came from another site.
func isCrossSite(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false // not a browser cross-origin request
	}
	u, err := url.Parse(origin)
	return err != nil || !isLoopbackHost(u.Host)
}

// requireJSON rejects requests that are not application/json, which a cross-site page cannot send
// without a CORS preflight, and caps the body size.
func requireJSON(w http.ResponseWriter, r *http.Request) bool {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	return true
}
