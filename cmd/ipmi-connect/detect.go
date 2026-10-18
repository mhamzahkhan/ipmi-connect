package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"ipmi-connect/internal/bmc"
)

const (
	probeDialTimeout = 1500 * time.Millisecond
	probeHTTPTimeout = 2 * time.Second
)

// detectVendor guesses the BMC vendor from its open KVM port, then its web server banner,
// defaulting to Dell.
func detectVendor(host string) bmc.TargetType {
	if portOpen(host, 7578) { // Avocent APCP
		return bmc.TargetDell
	}
	if portOpen(host, 5900) { // ATEN RFB
		return bmc.TargetSupermicro
	}

	client := bmc.NewLegacyHTTPClient(probeHTTPTimeout)
	req, err := http.NewRequest("GET", bmc.BaseURL(host)+"/", nil)
	if err != nil {
		return bmc.TargetDell
	}
	if resp, err := client.Do(req); err == nil {
		defer resp.Body.Close()
		serverHeader := strings.ToLower(resp.Header.Get("Server"))
		if strings.Contains(serverHeader, "appweb") {
			return bmc.TargetDell
		}
		if strings.Contains(serverHeader, "lighttpd") {
			return bmc.TargetSupermicro
		}
	}

	return bmc.TargetDell
}

func portOpen(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(port)), probeDialTimeout)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
