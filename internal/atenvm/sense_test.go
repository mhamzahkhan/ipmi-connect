package atenvm

import "testing"

func TestSenseRoundTrip(t *testing.T) {
	var s senseState
	// No sense initially.
	d := s.take()
	if d[0] != 0x70 || d[2] != 0 || d[12] != 0 {
		t.Fatalf("initial sense %x, want no-sense", d)
	}
	s.set(senseIllegalRequest, ascInvalidCommand, 0)
	d = s.take()
	if d[2]&0x0f != senseIllegalRequest || d[12] != ascInvalidCommand {
		t.Errorf("sense key/asc = %d/%02x, want %d/%02x", d[2]&0x0f, d[12], senseIllegalRequest, ascInvalidCommand)
	}
	// Taking sense clears it.
	if d2 := s.take(); d2[2]&0x0f != senseNoSense {
		t.Errorf("sense not cleared after take: %x", d2)
	}
}
