package bmc

import (
	"strings"
	"testing"
)

func TestReadLine(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"unix newline", "secret\n", "secret"},
		{"windows newline", "secret\r\n", "secret"},
		{"no newline at EOF", "secret", "secret"},
		{"keeps inner spaces", " se cret \n", " se cret "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readLine(strings.NewReader(tt.in))
			if err != nil || got != tt.want {
				t.Errorf("readLine(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
			}
		})
	}
}

func TestReadLineEmptyInputFails(t *testing.T) {
	if _, err := readLine(strings.NewReader("")); err == nil {
		t.Error("want an error for empty input")
	}
}
