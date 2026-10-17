package dell

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeAppWeb mimics the AppWeb endpoints used to obtain KVM tokens. When sessionValid is false
// the JNLP request gets the HTML login page, as the real BMC does after its web session expires.
type fakeAppWeb struct {
	sessionValid atomic.Bool
	loginOK      bool
	jnlpStatus   int // non-zero: answer viewer.jnlp with this HTTP status
	logins       atomic.Int32
}

func (f *fakeAppWeb) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/login.html":
		reply(w, "<html>login</html>")
	case r.URL.Path == "/data/login":
		f.logins.Add(1)
		if !f.loginOK {
			reply(w, "<root><status>error</status></root>")
			return
		}
		f.sessionValid.Store(true)
		reply(w, "<root><status>ok</status></root>")
	case r.URL.Path == "/index.html":
		reply(w, `var CSRF_TOKEN_1 = "ST1" + "=" + "abc123"; var CSRF_TOKEN_2_VALUE = "def456";`)
	case strings.HasPrefix(r.URL.Path, "/viewer.jnlp("):
		if f.jnlpStatus != 0 {
			w.WriteHeader(f.jnlpStatus)
			return
		}
		if !f.sessionValid.Load() {
			reply(w, "<html><body>Please log in</body></html>")
			return
		}
		reply(w, `<jnlp><application-desc>
			<argument>user=111</argument><argument>passwd=222</argument>
			<argument>kmport=7578</argument><argument>vport=7578</argument>
		</application-desc></jnlp>`)
	default:
		http.NotFound(w, r)
	}
}

func reply(w http.ResponseWriter, body string) {
	_, _ = io.WriteString(w, body)
}

func newFakeAppWebClient(t *testing.T, f *fakeAppWeb) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(f)
	t.Cleanup(srv.Close)
	c := NewClient(srv.Listener.Addr().String(), "root", "pw")
	c.IsLoggedIn = true
	c.ST1Token = "abc123"
	return c
}

func TestRefreshKVMTokens(t *testing.T) {
	tests := []struct {
		name         string
		sessionValid bool
		loginOK      bool
		wantErr      bool
		wantLogins   int32
	}{
		{name: "valid web session reuses it", sessionValid: true, loginOK: true, wantLogins: 0},
		{name: "expired web session logs in again", sessionValid: false, loginOK: true, wantLogins: 1},
		{name: "rejected re-login is an error", sessionValid: false, loginOK: false, wantErr: true, wantLogins: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeAppWeb{loginOK: tt.loginOK}
			f.sessionValid.Store(tt.sessionValid)
			c := newFakeAppWebClient(t, f)

			err := c.RefreshKVMTokens()

			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got := f.logins.Load(); got != tt.wantLogins {
				t.Errorf("logins = %d, want %d", got, tt.wantLogins)
			}
			if !tt.wantErr && (c.UserTokenStr != "111" || c.PassTokenStr != "222") {
				t.Errorf("tokens = %q/%q, want 111/222", c.UserTokenStr, c.PassTokenStr)
			}
		})
	}
}

// A failure that is not "the BMC served its login page" (e.g. a 503 or a network blip) must not
// trigger a re-login: the old session is probably alive, and the BMC allows only one.
func TestRefreshKVMTokensDoesNotReloginOnOtherFailures(t *testing.T) {
	f := &fakeAppWeb{loginOK: true, jnlpStatus: http.StatusServiceUnavailable}
	f.sessionValid.Store(true)
	c := newFakeAppWebClient(t, f)

	if err := c.RefreshKVMTokens(); err == nil {
		t.Fatal("want error")
	}
	if got := f.logins.Load(); got != 0 {
		t.Errorf("logins = %d, want 0", got)
	}
	if !c.IsLoggedIn {
		t.Error("IsLoggedIn cleared; KeepAlive would stop refreshing a live session")
	}
}

func TestFetchKVMParametersRejectsPageWithoutTokens(t *testing.T) {
	f := &fakeAppWeb{}
	c := newFakeAppWebClient(t, f) // session invalid: JNLP URL serves the login page

	if err := c.FetchKVMParameters(); err == nil {
		t.Fatal("want error when the BMC returns no KVM tokens")
	}
}
