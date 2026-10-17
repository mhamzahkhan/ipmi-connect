package supermicro

import (
	"os"
	"testing"
)

func TestParseTicketFromRecordedJNLP(t *testing.T) {
	data, err := os.ReadFile("testdata/ikvm.jnlp")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := parseTicket(string(data), "192.0.2.20")
	if err != nil {
		t.Fatalf("parseTicket: %v", err)
	}
	if tk.Port != 5900 || tk.TLS {
		t.Errorf("port %d tls %v, want 5900 plaintext", tk.Port, tk.TLS)
	}
	if len(tk.User) != 16 || tk.User != tk.Password {
		t.Errorf("token %q/%q, want the same 16-character token twice", tk.User, tk.Password)
	}
}

func TestParseTicket(t *testing.T) {
	jnlp := func(args ...string) string {
		s := "<jnlp><application-desc>"
		for _, a := range args {
			s += "<argument>" + a + "</argument>"
		}
		return s + "</application-desc></jnlp>"
	}
	tests := []struct {
		name    string
		body    string
		user    string
		port    int
		tls     bool
		wantErr bool
	}{
		{"X7SB3 layout", jnlp("192.0.2.20", "abc", "abc", "null", "5900", "623", "2", "0"), "abc", 5900, false, false},
		{"XML escapes", jnlp("192.0.2.20", "a&amp;b", "x", "null", "5901"), "a&b", 5901, false, false},
		{"leading extra arguments", jnlp("lang=en", "192.0.2.20", "tok", "tok", "null", "5900"), "tok", 5900, false, false},
		{"KVM over TLS", jnlp("192.0.2.20", "t", "t", "null", "63630", "623", "2", "0", "1", "5900"), "t", 5900, true, false},
		{"no port defaults to 5900", jnlp("192.0.2.20", "t", "t"), "t", 5900, false, false},
		{"not a JNLP", "<html>login</html>", "", 0, false, true},
		{"too few arguments", jnlp("192.0.2.20", "t"), "", 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tk, err := parseTicket(tt.body, "192.0.2.20")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			if err == nil && (tk.User != tt.user || tk.Port != tt.port || tk.TLS != tt.tls) {
				t.Errorf("got %+v", tk)
			}
		})
	}
}
