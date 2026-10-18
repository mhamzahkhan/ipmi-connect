package supermicro

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

const (
	webTimeout = 10 * time.Second
	// maxWebBody bounds what we read from any CGI response.
	maxWebBody = 1 << 20
)

// WebSession is the BMC's web (CGI) session: login, the KVM ticket, power, logout.
type WebSession struct {
	host, user, password string
	http                 *http.Client
	ticketRetryDelay     time.Duration

	mu  sync.Mutex
	sid string
}

// NewWebSession prepares a session; nothing is sent until Login.
func NewWebSession(host, user, password string) *WebSession {
	c := bmc.NewLegacyHTTPClient(webTimeout)
	c.Jar = nil // cookies are set by hand; a jar would send SID twice
	return &WebSession{host: host, user: user, password: password, http: c, ticketRetryDelay: time.Second}
}

func (w *WebSession) url(path string) string { return bmc.BaseURL(w.host) + path }

// do sends a request with the session cookies and returns the (bounded) body.
func (w *WebSession) do(method, path string, form url.Values) (string, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, w.url(path), body)
	if err != nil {
		return "", err
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	w.mu.Lock()
	sid := w.sid
	w.mu.Unlock()
	if sid != "" {
		req.Header.Set("Cookie", "SID="+sid+"; langSetFlag=0; language=English")
		req.Header.Set("Referer", w.url("/cgi/url_redirect.cgi?url_name=man_ikvm"))
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return "", bmc.FormatError("Supermicro "+path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxWebBody))
	if err != nil {
		return "", bmc.FormatError("Supermicro "+path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("supermicro %s: HTTP %d", path, resp.StatusCode)
	}
	return string(data), nil
}

// Login authenticates and keeps the SID cookie.
func (w *WebSession) Login() error {
	req, err := http.NewRequest(http.MethodPost, w.url("/cgi/login.cgi"),
		strings.NewReader(url.Values{"name": {w.user}, "pwd": {w.password}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := w.http.Do(req)
	if err != nil {
		return bmc.FormatError("Supermicro login", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxWebBody))

	// Some firmware first clears SID; the last non-empty value is the session.
	sid := ""
	for _, c := range resp.Cookies() {
		if c.Name == "SID" && c.Value != "" {
			sid = c.Value
		}
	}
	if resp.StatusCode != http.StatusOK || sid == "" || strings.Contains(string(data), "login_alert") {
		return errors.New("supermicro login failed: check the username and password")
	}
	w.mu.Lock()
	w.sid = sid
	w.mu.Unlock()
	log.Printf("[Supermicro] Authenticated successfully with host %s", w.host)
	return nil
}

// FetchTicket gets one-time KVM credentials from the iKVM JNLP. The session may not be ready
// right after login, so it retries once.
func (w *WebSession) FetchTicket(host string) (Ticket, error) {
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		if attempt > 0 {
			time.Sleep(w.ticketRetryDelay)
		}
		for _, name := range []string{"ikvm", "man_ikvm"} {
			body, err := w.do(http.MethodGet, "/cgi/url_redirect.cgi?url_name="+name+"&url_type=jwsk", nil)
			if err != nil {
				lastErr = err
				continue
			}
			t, err := parseTicket(body, host)
			if err == nil {
				return t, nil
			}
			lastErr = err
		}
	}
	return Ticket{}, fmt.Errorf("supermicro KVM ticket: %w", lastErr)
}

// cgiPowerArg maps a power action to ipmi.cgi's POWER_INFO.XML argument. The codes follow the
// IPMI Chassis Control command, like the Dell's.
func cgiPowerArg(action bmc.PowerAction) (string, error) {
	codes := map[bmc.PowerAction]int{
		bmc.PowerOff: 0, bmc.PowerOn: 1, bmc.PowerCycle: 2, bmc.PowerReset: 3, bmc.PowerSoftOff: 5,
	}
	code, ok := codes[action]
	if !ok {
		return "", fmt.Errorf("unknown power action: %s", action)
	}
	return fmt.Sprintf("(1,%d)", code), nil
}

// PowerCGI issues a power action through the web interface (used when no KVM session is up).
func (w *WebSession) PowerCGI(action bmc.PowerAction) error {
	arg, err := cgiPowerArg(action)
	if err != nil {
		return err
	}
	body, err := w.do(http.MethodPost, "/cgi/ipmi.cgi", url.Values{"POWER_INFO.XML": {arg}})
	if err != nil {
		return err
	}
	if !strings.Contains(body, "POWER_INFO") {
		return errors.New("supermicro power: unexpected reply (web session expired?)")
	}
	log.Printf("[Supermicro] Power %s sent", action)
	return nil
}

var powerStatusRE = regexp.MustCompile(`STATUS="([A-Z]+)"`)

// PowerStatus returns "ON" or "OFF".
func (w *WebSession) PowerStatus() (string, error) {
	body, err := w.do(http.MethodPost, "/cgi/ipmi.cgi", url.Values{"POWER_INFO.XML": {"(0,0)"}})
	if err != nil {
		return "", err
	}
	m := powerStatusRE.FindStringSubmatch(body)
	if m == nil {
		return "", errors.New("supermicro power status: unexpected reply")
	}
	return m[1], nil
}

// Logout ends the web session. It also ends any KVM session, so call it after the socket closes.
func (w *WebSession) Logout() {
	w.mu.Lock()
	sid := w.sid
	w.mu.Unlock()
	if sid == "" {
		return
	}
	if _, err := w.do(http.MethodGet, "/cgi/logout.cgi", nil); err != nil {
		log.Printf("[Supermicro] Logout: %v", err)
		return
	}
	w.mu.Lock()
	w.sid = ""
	w.mu.Unlock()
	log.Printf("[Supermicro] Successfully logged out session from %s", w.host)
}
