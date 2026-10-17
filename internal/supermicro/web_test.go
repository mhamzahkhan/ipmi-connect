package supermicro

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ipmi-connect/internal/bmc"
)

// fakeBMCWeb mimics the X7SB3's lighttpd CGI endpoints.
type fakeBMCWeb struct {
	mu        sync.Mutex
	password  string
	sid       string
	jnlpFails int // answer the first N JNLP requests with an HTML page
	power     []string
	logouts   int
	cookies   []string
}

func (f *fakeBMCWeb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cookies = append(f.cookies, r.Header.Get("Cookie"))
	authed := strings.Contains(r.Header.Get("Cookie"), "SID="+f.sid)
	switch r.URL.Path {
	case "/cgi/login.cgi":
		_ = r.ParseForm()
		if r.PostForm.Get("pwd") != f.password {
			_, _ = io.WriteString(w, `<html><script>login_alert();</script></html>`)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: ""})
		http.SetCookie(w, &http.Cookie{Name: "SID", Value: f.sid})
		_, _ = io.WriteString(w, `<html>url_redirect.cgi?url_name=mainmenu</html>`)
	case "/cgi/url_redirect.cgi":
		if !authed || f.jnlpFails > 0 {
			f.jnlpFails--
			_, _ = io.WriteString(w, `<html>please log in</html>`)
			return
		}
		_, _ = io.WriteString(w, `<jnlp><application-desc><argument>192.0.2.20</argument>`+
			`<argument>tok</argument><argument>tok</argument><argument>null</argument>`+
			`<argument>5900</argument></application-desc></jnlp>`)
	case "/cgi/ipmi.cgi":
		_ = r.ParseForm()
		q := r.PostForm.Get("POWER_INFO.XML")
		if q == "(0,0)" {
			_, _ = io.WriteString(w, `<?xml version="1.0"?>  <IPMI>  <POWER_INFO>  <POWER STATUS="ON"/>  </POWER_INFO>  </IPMI>`)
			return
		}
		f.power = append(f.power, q)
		_, _ = io.WriteString(w, `<?xml version="1.0"?><IPMI><POWER_INFO><POWER STATUS="ON"/></POWER_INFO></IPMI>`)
	case "/cgi/logout.cgi":
		f.logouts++
	default:
		http.NotFound(w, r)
	}
}

func newFakeWeb(t *testing.T, f *fakeBMCWeb) *WebSession {
	t.Helper()
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	ws := NewWebSession(srv.Listener.Addr().String(), "ADMIN", "pw")
	ws.ticketRetryDelay = 0
	return ws
}

func TestLoginTicketPowerLogout(t *testing.T) {
	f := &fakeBMCWeb{password: "pw", sid: "0123456789abcdef", jnlpFails: 1}
	ws := newFakeWeb(t, f)

	if err := ws.Login(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	tk, err := ws.FetchTicket("192.0.2.20")
	if err != nil {
		t.Fatalf("FetchTicket (should retry once): %v", err)
	}
	if tk.User != "tok" || tk.Port != 5900 {
		t.Errorf("ticket %+v", tk)
	}
	if st, err := ws.PowerStatus(); err != nil || st != "ON" {
		t.Errorf("PowerStatus = %q, %v", st, err)
	}
	if err := ws.PowerCGI(bmc.PowerSoftOff); err != nil {
		t.Fatalf("PowerCGI: %v", err)
	}
	ws.Logout()

	if len(f.power) != 1 || f.power[0] != "(1,5)" {
		t.Errorf("power requests %v, want soft shutdown (1,5)", f.power)
	}
	if f.logouts != 1 {
		t.Errorf("logouts = %d", f.logouts)
	}
	for _, c := range f.cookies {
		if strings.Count(c, "SID=") > 1 {
			t.Errorf("SID sent twice in %q", c)
		}
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	ws := newFakeWeb(t, &fakeBMCWeb{password: "other", sid: "x"})
	if err := ws.Login(); err == nil {
		t.Fatal("want error for a login_alert page")
	}
}

func TestPowerCGICodes(t *testing.T) {
	want := map[bmc.PowerAction]string{
		bmc.PowerOff: "(1,0)", bmc.PowerOn: "(1,1)", bmc.PowerCycle: "(1,2)",
		bmc.PowerReset: "(1,3)", bmc.PowerSoftOff: "(1,5)",
	}
	for action, code := range want {
		got, err := cgiPowerArg(action)
		if err != nil || got != code {
			t.Errorf("%s: got %q, %v; want %s", action, got, err, code)
		}
	}
	if _, err := cgiPowerArg("power_explode"); err == nil {
		t.Error("want error for unknown action")
	}
}
