package bmc

import "testing"

func TestBaseURL(t *testing.T) {
	tests := []struct {
		host, want string
	}{
		{"192.0.2.10", "https://192.0.2.10"},
		{"bmc.example", "https://bmc.example"},
		{"2001:db8::10", "https://[2001:db8::10]"},
		{"[2001:db8::10]", "https://[2001:db8::10]"},
		{"fe80::1%en0", "https://[fe80::1%25en0]"},
		{"192.0.2.10:8443", "https://192.0.2.10:8443"},
		{"[2001:db8::10]:8443", "https://[2001:db8::10]:8443"},
	}
	for _, tt := range tests {
		if got := BaseURL(tt.host); got != tt.want {
			t.Errorf("BaseURL(%q) = %q, want %q", tt.host, got, tt.want)
		}
	}
}
