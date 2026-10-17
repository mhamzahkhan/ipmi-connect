package main

import (
	"errors"
	"testing"
)

func TestResolveCredentials(t *testing.T) {
	env := func(vars map[string]string) func(string) string {
		return func(k string) string { return vars[k] }
	}
	prompted := func(string) (string, error) { return "typed", nil }

	tests := []struct {
		name     string
		opts     options
		env      map[string]string
		wantUser string
		wantPass string
	}{
		{"defaults to root and prompts", options{}, nil, "root", "typed"},
		{"env supplies both", options{}, map[string]string{envUser: "admin", envPassword: "s3cret"}, "admin", "s3cret"},
		{"flags beat env", options{User: "flaguser", Password: "flagpass"}, map[string]string{envUser: "admin", envPassword: "s3cret"}, "flaguser", "flagpass"},
		{"empty env var is ignored", options{}, map[string]string{envPassword: ""}, "root", "typed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			user, pass, err := resolveCredentials(tt.opts, env(tt.env), prompted)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if user != tt.wantUser || pass != tt.wantPass {
				t.Errorf("got (%q, %q), want (%q, %q)", user, pass, tt.wantUser, tt.wantPass)
			}
		})
	}
}

func TestResolveCredentialsPromptsWithUserAndHost(t *testing.T) {
	var gotPrompt string
	prompt := func(p string) (string, error) { gotPrompt = p; return "x", nil }

	_, _, _ = resolveCredentials(options{Host: "bmc.example", User: "admin"}, func(string) string { return "" }, prompt)

	if want := "Enter password for admin@bmc.example: "; gotPrompt != want {
		t.Errorf("prompt = %q, want %q", gotPrompt, want)
	}
}

func TestResolveCredentialsReturnsPromptError(t *testing.T) {
	boom := errors.New("no tty")
	_, _, err := resolveCredentials(options{}, func(string) string { return "" }, func(string) (string, error) { return "", boom })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
}
