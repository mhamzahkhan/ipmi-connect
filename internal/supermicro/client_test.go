package supermicro

import "testing"

func TestNewClient(t *testing.T) {
	c := NewClient("192.0.2.20", "ADMIN", "ADMIN")
	if c.Host != "192.0.2.20" || c.User != "ADMIN" || c.Password != "ADMIN" {
		t.Errorf("NewClient initialization mismatch: %+v", c)
	}
	if c.HTTPClient == nil {
		t.Error("expected non-nil HTTPClient")
	}
}
