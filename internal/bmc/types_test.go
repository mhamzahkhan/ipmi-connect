package bmc

import "testing"

func TestTargetTypeConstants(t *testing.T) {
	if TargetAuto != "auto" {
		t.Errorf("expected TargetAuto='auto', got %s", TargetAuto)
	}
	if TargetDell != "dell" {
		t.Errorf("expected TargetDell='dell', got %s", TargetDell)
	}
	if TargetSupermicro != "supermicro" {
		t.Errorf("expected TargetSupermicro='supermicro', got %s", TargetSupermicro)
	}
}

func TestPowerActionConstants(t *testing.T) {
	actions := []PowerAction{PowerOn, PowerOff, PowerReset, PowerCycle, PowerSoftOff}
	for _, a := range actions {
		if a == "" {
			t.Error("expected non-empty power action")
		}
	}
}
