package main

import (
	"testing"
	"time"
)

func TestNextBackoff(t *testing.T) {
	tests := []struct {
		in, want time.Duration
	}{
		{2 * time.Second, 4 * time.Second},
		{16 * time.Second, 32 * time.Second},
		{32 * time.Second, maxReconnectDelay},
		{maxReconnectDelay, maxReconnectDelay},
	}
	for _, tt := range tests {
		if got := nextBackoff(tt.in); got != tt.want {
			t.Errorf("nextBackoff(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
