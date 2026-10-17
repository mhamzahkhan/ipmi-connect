package supermicro

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"ipmi-connect/internal/bmc"
)

// Login authenticates with the Supermicro BMC CGI interface.
func (c *Client) Login() error {
	c.SessionMu.Lock()
	defer c.SessionMu.Unlock()

	loginURL := fmt.Sprintf("https://%s/cgi/login.cgi", c.Host)
	form := url.Values{}
	form.Set("name", c.User)
	form.Set("pwd", c.Password)

	req, err := http.NewRequest("POST", loginURL, strings.NewReader(form.Encode()))
	if err != nil {
		return bmc.FormatError("Supermicro Login NewRequest", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return bmc.FormatError("Supermicro Login POST", err)
	}
	defer resp.Body.Close()

	// Extract SID cookie from raw headers and parsed cookies
	for _, raw := range resp.Header["Set-Cookie"] {
		for _, part := range strings.Split(raw, ";") {
			part = strings.TrimSpace(part)
			if strings.HasPrefix(part, "SID=") {
				val := strings.TrimPrefix(part, "SID=")
				if len(val) > 0 {
					c.SID = val
				}
			}
		}
	}

	if c.SID == "" {
		for _, cookie := range resp.Cookies() {
			if cookie.Name == "SID" && cookie.Value != "" {
				c.SID = cookie.Value
				break
			}
		}
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)

	if c.SID == "" {
		// Fallback check if SID is in body or redirect
		if strings.Contains(body, "SID=") {
			parts := strings.Split(body, "SID=")
			if len(parts) > 1 {
				c.SID = strings.Split(parts[1], ";")[0]
			}
		}
	}

	if c.SID == "" {
		return fmt.Errorf("login failed: SID cookie not received (body: %s)", strings.TrimSpace(body))
	}

	c.IsLoggedIn = true
	log.Printf("[Supermicro] Authenticated successfully with host %s (SID=%s)", c.Host, c.SID)
	return nil
}

// Logout closes the session.
func (c *Client) Logout() {
	c.SessionMu.Lock()
	defer c.SessionMu.Unlock()

	if !c.IsLoggedIn || c.SID == "" {
		return
	}

	logoutURL := fmt.Sprintf("https://%s/cgi/logout.cgi", c.Host)
	req, err := http.NewRequest("GET", logoutURL, nil)
	if err == nil {
		c.applyHeaders(req)
		resp, err := c.HTTPClient.Do(req)
		if err == nil {
			resp.Body.Close()
			log.Printf("[Supermicro] Successfully logged out session from %s", c.Host)
		}
	}

	c.IsLoggedIn = false
}

// applyHeaders injects the mandatory cookies and referer to bypass the 500 error bug.
func (c *Client) applyHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", fmt.Sprintf("https://%s/cgi/url_redirect.cgi?url_name=man_ikvm", c.Host))
	cookieStr := fmt.Sprintf("SID=%s; langSetFlag=0; language=English; mainpage=remote; subpage=man_ikvm", c.SID)
	req.Header.Set("Cookie", cookieStr)
}
