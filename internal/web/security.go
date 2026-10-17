package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// maxJSONBody bounds API request bodies; every API payload is a few bytes of JSON.
const maxJSONBody = 4096

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

// guard protects the local console.
//
//   - Host must be a loopback name, defeating DNS rebinding.
//   - The API and WebSocket need the per-run token (Authorization: Bearer, or ?t= on the socket).
//     The page gets it by exchanging the one-time code from its launch link; it is never a cookie,
//     because browsers share 127.0.0.1 cookies with every other local port and service.
//   - State-changing requests must not come from another site.
//
// Static assets carry no secrets and are served to anyone on loopback: the page has to load
// before it can exchange its code.
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
		if r.Method != http.MethodGet && r.Method != http.MethodHead && isCrossSite(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/session" {
			s.handleSession(w, r)
			return
		}
		protected := r.URL.Path == "/ws" || strings.HasPrefix(r.URL.Path, "/api/")
		if protected && !s.authorized(r) {
			http.Error(w, "not signed in: open the console link printed in the terminal", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleSession exchanges the one-time launch code for the session token. The code works once:
// the launch URL is visible in the process list of the browser it was handed to.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !requireJSON(w, r) {
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	s.sessionMu.Lock()
	ok := !s.launchCodeUsed && subtle.ConstantTimeCompare([]byte(req.Code), []byte(s.launchCode)) == 1
	if ok {
		s.launchCodeUsed = true
	}
	s.sessionMu.Unlock()
	if !ok {
		http.Error(w, "launch link already used or invalid: restart ipmi-connect for a new one", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": s.token})
}

// LaunchURL is the link that signs a browser in. The code sits in the fragment, which browsers
// never send to a server.
func (s *Server) LaunchURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/#c=%s", s.boundPort(), s.launchCode)
}

func (s *Server) authorized(r *http.Request) bool {
	if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && s.tokenMatches(bearer) {
		return true
	}
	// Browsers cannot set headers on a WebSocket handshake, so the page passes the token here.
	return r.URL.Path == "/ws" && s.tokenMatches(r.URL.Query().Get("t"))
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
