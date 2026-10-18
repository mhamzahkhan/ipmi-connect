package bmc

import (
	"net"
	"net/url"
	"strings"
)

// BaseURL returns the https:// origin for a BMC host (name, IPv4, IPv6, optionally with a
// port), bracketing IPv6 literals so a path can be appended.
func BaseURL(host string) string {
	h := host
	if name, port, err := net.SplitHostPort(host); err == nil {
		h = net.JoinHostPort(name, port)
	} else if bare := strings.Trim(host, "[]"); strings.Contains(bare, ":") {
		h = "[" + bare + "]"
	}
	return (&url.URL{Scheme: "https", Host: h}).String()
}
