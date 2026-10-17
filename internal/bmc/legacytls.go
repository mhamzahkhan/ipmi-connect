package bmc

import (
	"crypto/tls"
	"net"
	"net/http"
	"net/http/cookiejar"
	"time"
)

// LegacyTLSConfig returns a *tls.Config tuned for ancient BMCs (TLS 1.0, permissive ciphers)
func LegacyTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS10,
		MaxVersion:         tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			tls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
			tls.TLS_ECDHE_RSA_WITH_3DES_EDE_CBC_SHA,
		},
	}
}

// NewLegacyHTTPClient returns an *http.Client configured with legacy TLS, timeouts, and cookie jar
func NewLegacyHTTPClient(timeout time.Duration) *http.Client {
	transport := &http.Transport{
		TLSClientConfig: LegacyTLSConfig(),
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:       10,
		IdleConnTimeout:    30 * time.Second,
		DisableCompression: true,
	}

	jar, _ := cookiejar.New(nil)

	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		Jar:       jar,
	}
}
