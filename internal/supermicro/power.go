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

// cgiPowerCodes maps power actions to power_ctrl.cgi's web_power_control values.
var cgiPowerCodes = map[bmc.PowerAction]string{
	bmc.PowerOn:      "1",
	bmc.PowerOff:     "2",
	bmc.PowerReset:   "3",
	bmc.PowerCycle:   "4",
	bmc.PowerSoftOff: "5",
}

func cgiPowerCode(action bmc.PowerAction) (string, error) {
	code, ok := cgiPowerCodes[action]
	if !ok {
		return "", fmt.Errorf("unknown power action: %s", action)
	}
	return code, nil
}

// PowerControl issues a chassis power command to the server.
func (c *Client) PowerControl(action bmc.PowerAction) error {
	cgiAction, err := cgiPowerCode(action)
	if err != nil {
		return err
	}

	powerURL := fmt.Sprintf("https://%s/cgi/power_ctrl.cgi", c.Host)
	form := url.Values{}
	form.Set("web_power_control", cgiAction)

	req, err := http.NewRequest("POST", powerURL, strings.NewReader(form.Encode()))
	if err != nil {
		return bmc.FormatError("Supermicro PowerControl request", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.applyHeaders(req)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return bmc.FormatError("Supermicro PowerControl POST", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	log.Printf("[Supermicro] Power control '%s' sent, response: %s", action, strings.TrimSpace(string(bodyBytes)))
	return nil
}

// QueryPowerState checks whether host chassis is on or off.
func (c *Client) QueryPowerState() (string, error) {
	urlStr := fmt.Sprintf("https://%s/cgi/url_redirect.cgi?url_name=man_chassis", c.Host)
	req, err := http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return "unknown", err
	}

	c.applyHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return "unknown", err
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := strings.ToLower(string(bodyBytes))

	if strings.Contains(body, "system is on") || strings.Contains(body, "power is on") || strings.Contains(body, "host is on") {
		return "ON", nil
	}
	if strings.Contains(body, "system is off") || strings.Contains(body, "power is off") || strings.Contains(body, "host is off") {
		return "OFF", nil
	}

	return "ONLINE", nil
}
