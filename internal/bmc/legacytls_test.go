package bmc

import (
	"crypto/tls"
	"testing"
)

func TestLegacyTLSConfig(t *testing.T) {
	cfg := LegacyTLSConfig()
	if cfg == nil {
		t.Fatal("expected non-nil tls.Config")
	}
	if !cfg.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify=true for legacy BMCs")
	}
	if cfg.MinVersion != tls.VersionTLS10 {
		t.Errorf("expected MinVersion=TLS1.0, got %x", cfg.MinVersion)
	}
	if len(cfg.CipherSuites) == 0 {
		t.Error("expected non-empty cipher suites")
	}
}
