package dell

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

var (
	st1Regex = regexp.MustCompile(`CSRF_TOKEN_1\s*=\s*"ST1"\s*\+\s*"="\s*\+\s*"([0-9a-fA-F]+)"|(?:ST1=)([0-9a-fA-F]+)`)
	st2Regex = regexp.MustCompile(`CSRF_TOKEN_2_VALUE\s*=\s*"([0-9a-fA-F]+)"|(?:ST2\s*:\s*)([0-9a-fA-F]+)`)
)

// encodeSetValue implements Avocent password character escaping before URL encoding.
func encodeSetValue(val string) string {
	var sb strings.Builder
	for _, ch := range val {
		switch ch {
		case '\\', ':', ',', '(', ')', '=', '&', '?':
			sb.WriteRune('\\')
			sb.WriteRune(ch)
		default:
			sb.WriteRune(ch)
		}
	}
	return sb.String()
}

// FetchCSRFTokens requests index.html with active cookies to acquire current ST1 and ST2 tokens.
func (c *Client) FetchCSRFTokens() error {
	urlStr := fmt.Sprintf("%s/index.html", bmc.BaseURL(c.Host))
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return bmc.FormatError("Dell FetchCSRFTokens", err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return bmc.FormatError("Dell FetchCSRFTokens request", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return bmc.FormatError("Dell FetchCSRFTokens read", err)
	}
	body := string(bodyBytes)

	m1 := st1Regex.FindStringSubmatch(body)
	if len(m1) > 1 {
		if m1[1] != "" {
			c.ST1Token = m1[1]
		} else if len(m1) > 2 && m1[2] != "" {
			c.ST1Token = m1[2]
		}
	}
	m2 := st2Regex.FindStringSubmatch(body)
	if len(m2) > 1 {
		if m2[1] != "" {
			c.ST2Token = m2[1]
		} else if len(m2) > 2 && m2[2] != "" {
			c.ST2Token = m2[2]
		}
	}

	return nil
}

// Login authenticates with the Dell BMC web application.
func (c *Client) Login() error {
	c.SessionMu.Lock()
	defer c.SessionMu.Unlock()

	initURL := fmt.Sprintf("%s/login.html", bmc.BaseURL(c.Host))
	initReq, _ := http.NewRequest("GET", initURL, nil)
	if initResp, err := c.HTTPClient.Do(initReq); err == nil {
		initResp.Body.Close()
	}

	loginURL := fmt.Sprintf("%s/data/login", bmc.BaseURL(c.Host))
	escapedPass := url.QueryEscape(encodeSetValue(c.Password))
	data := fmt.Sprintf("user=%s&password=%s", url.QueryEscape(c.User), escapedPass)

	req, err := http.NewRequest("POST", loginURL, strings.NewReader(data))
	if err != nil {
		return bmc.FormatError("Dell Login NewRequest", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return bmc.FormatError("Dell Login POST", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return bmc.FormatError("Dell Login ReadBody", err)
	}
	body := string(bodyBytes)

	if !strings.Contains(body, "<status>ok</status>") {
		if strings.Contains(body, "code>6<") {
			return fmt.Errorf("login rejected: an active GUI session already exists on the BMC. Wait 60s for timeout")
		}
		return fmt.Errorf("login failed: %s", strings.TrimSpace(body))
	}

	if err := c.FetchCSRFTokens(); err != nil {
		log.Printf("[Dell] Notice: post-login CSRF token fetch: %v", err)
	}

	c.IsLoggedIn = true
	log.Printf("[Dell] Authenticated successfully with host %s", c.Host)
	return nil
}

// Logout releases the GUI session on the BMC.
func (c *Client) Logout() {
	c.loggingOut.Store(true) // first, so no new virtual media session can start
	c.Manager.Close()        // eject everything while the KVM session still stands
	c.SessionMu.Lock()
	defer c.SessionMu.Unlock()

	c.Close()

	if !c.IsLoggedIn || c.ST1Token == "" {
		return
	}

	logoutURL := fmt.Sprintf("%s/data/logout?ST1=%s", bmc.BaseURL(c.Host), c.ST1Token)
	req, err := http.NewRequest("GET", logoutURL, nil)
	if err == nil {
		if c.ST2Token != "" {
			req.Header.Set("ST2", c.ST2Token)
		}
		resp, err := c.HTTPClient.Do(req)
		if err == nil {
			resp.Body.Close()
			log.Printf("[Dell] Successfully logged out session from %s", c.Host)
		}
	}

	c.IsLoggedIn = false
}

// JNLPResponse parses arguments from viewer.jnlp.
type JNLPResponse struct {
	XMLName     xml.Name `xml:"jnlp"`
	Application struct {
		Arguments []string `xml:"argument"`
	} `xml:"application-desc"`
}

// FetchKVMParameters downloads viewer.jnlp to acquire one-time session credentials.
func (c *Client) FetchKVMParameters() error {
	ts := time.Now().UnixMilli()
	st1, _ := c.csrfTokens()
	urlStr := fmt.Sprintf("%s/viewer.jnlp(%s@0@%d)?ST1=%s", bmc.BaseURL(c.Host), c.Host, ts, st1)

	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return bmc.FormatError("Dell FetchKVMParameters request", err)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return bmc.FormatError("Dell FetchKVMParameters get", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return bmc.FormatError("Dell FetchKVMParameters read", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("viewer.jnlp: HTTP %d", resp.StatusCode)
	}

	// An expired web session makes the BMC serve its HTML login page here instead of a JNLP.
	var jnlp JNLPResponse
	if err := xml.Unmarshal(bodyBytes, &jnlp); err != nil {
		return fmt.Errorf("%w: viewer.jnlp is not a JNLP document", errSessionExpired)
	}

	user, passwd := "", ""
	kmPort, vPort := c.KMPort, c.VPort
	for _, arg := range jnlp.Application.Arguments {
		k, v, ok := strings.Cut(arg, "=")
		if !ok {
			continue
		}
		switch k {
		case "user":
			user = v
		case "passwd":
			passwd = v
		case "kmport":
			if p, err := strconv.Atoi(v); err == nil {
				kmPort = p
			}
		case "vport":
			if p, err := strconv.Atoi(v); err == nil {
				vPort = p
			}
		}
	}
	if user == "" || passwd == "" {
		return fmt.Errorf("%w: viewer.jnlp carried no KVM tokens", errSessionExpired)
	}

	c.UserTokenStr, c.PassTokenStr = user, passwd
	c.KMPort, c.VPort = kmPort, vPort
	log.Printf("[Dell] KVM tokens acquired (kmport=%d, vport=%d)", c.KMPort, c.VPort)
	return nil
}

// csrfTokens returns the ST1/ST2 tokens under SessionMu, which Login holds while replacing them.
// Callers must not already hold SessionMu.
func (c *Client) csrfTokens() (st1, st2 string) {
	c.SessionMu.Lock()
	defer c.SessionMu.Unlock()
	return c.ST1Token, c.ST2Token
}

// errSessionExpired means the BMC answered, but not with KVM tokens: its web session is gone.
var errSessionExpired = errors.New("BMC web session expired")

// RefreshKVMTokens fetches fresh one-time KVM tokens. Only when the BMC shows its web session
// has expired does it log in again: after a network error the old session is probably still
// alive, and the BMC rejects a second GUI session.
func (c *Client) RefreshKVMTokens() error {
	err := c.FetchKVMParameters()
	if err == nil || !errors.Is(err, errSessionExpired) {
		return err
	}
	log.Printf("[Dell] %v; logging in to the web UI again", err)
	if err := c.Login(); err != nil {
		return fmt.Errorf("re-login: %w", err)
	}
	return c.FetchKVMParameters()
}

// KeepAlive sends a heartbeat to prevent the BMC WebGUI session from expiring.
func (c *Client) KeepAlive() error {
	c.SessionMu.Lock()
	defer c.SessionMu.Unlock()

	if !c.IsLoggedIn || c.ST1Token == "" {
		return nil
	}

	urlStr := fmt.Sprintf("%s/data/keepGUISessionAlive?ST1=%s", bmc.BaseURL(c.Host), c.ST1Token)
	req, err := http.NewRequest("POST", urlStr, strings.NewReader(""))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.ST2Token != "" {
		req.Header.Set("ST2", c.ST2Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return bmc.FormatError("Dell KeepAlive", err)
	}
	defer resp.Body.Close()
	return nil
}
