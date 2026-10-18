package dell

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"ipmi-connect/internal/bmc"
)

// GetPowerState queries the current chassis power status (bmc.PowerStateOn, Off or Unknown).
func (c *Client) GetPowerState() (string, error) {
	c.SessionMu.Lock()
	defer c.SessionMu.Unlock()

	if !c.IsLoggedIn || c.ST1Token == "" {
		return bmc.PowerStateUnknown, fmt.Errorf("client not logged in")
	}

	urlStr := fmt.Sprintf("%s/data?get=pwState,&ST1=%s", bmc.BaseURL(c.Host), c.ST1Token)
	req, err := http.NewRequest("POST", urlStr, strings.NewReader(""))
	if err != nil {
		return bmc.PowerStateUnknown, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.ST2Token != "" {
		req.Header.Set("ST2", c.ST2Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return bmc.PowerStateUnknown, bmc.FormatError("Dell GetPowerState", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return bmc.PowerStateUnknown, err
	}

	body := string(bodyBytes)
	if strings.Contains(body, "<pwState>1</pwState>") {
		return bmc.PowerStateOn, nil
	} else if strings.Contains(body, "<pwState>0</pwState>") {
		return bmc.PowerStateOff, nil
	}
	return bmc.PowerStateUnknown, nil
}

// PowerState implements bmc.PowerReader.
func (c *Client) PowerState() (string, error) { return c.GetPowerState() }

// PowerControl commands the chassis power state via Dell's AppWeb HTTP API using POST.
func (c *Client) PowerControl(action bmc.PowerAction) error {
	stateStr, err := pwStateCode(action)
	if err != nil {
		return err
	}

	st1, st2 := c.csrfTokens()
	urlStr := fmt.Sprintf("%s/data?ST1=%s&set=pwState:%s", bmc.BaseURL(c.Host), st1, stateStr)
	req, err := http.NewRequest("POST", urlStr, strings.NewReader(""))
	if err != nil {
		return bmc.FormatError("Dell PowerControl NewRequest", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if st2 != "" {
		req.Header.Set("ST2", st2)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return bmc.FormatError("Dell PowerControl POST", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
	body := string(bodyBytes)
	log.Printf("[Dell] PowerControl %s (code %s) -> %s", action, stateStr, strings.TrimSpace(body))

	if !strings.Contains(body, "<status>ok</status>") {
		return fmt.Errorf("power control rejected by BMC: %s", strings.TrimSpace(body))
	}

	return nil
}

// pwStateCodes maps power actions to AppWeb's set=pwState:<code> values, which follow the IPMI
// Chassis Control command: 0 down, 1 up, 2 cycle, 3 hard reset, 4 diagnostic interrupt (NMI),
// 5 soft (ACPI) shutdown. NMI is deliberately not exposed.
var pwStateCodes = map[bmc.PowerAction]string{
	bmc.PowerOff:     "0",
	bmc.PowerOn:      "1",
	bmc.PowerCycle:   "2",
	bmc.PowerReset:   "3",
	bmc.PowerSoftOff: "5",
}

func pwStateCode(action bmc.PowerAction) (string, error) {
	code, ok := pwStateCodes[action]
	if !ok {
		return "", fmt.Errorf("unsupported power action: %s", action)
	}
	return code, nil
}
