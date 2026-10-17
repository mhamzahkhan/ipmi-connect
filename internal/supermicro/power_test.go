package supermicro

import (
	"testing"

	"ipmi-connect/internal/bmc"
)

func TestCGIPowerCode(t *testing.T) {
	tests := []struct {
		action  bmc.PowerAction
		want    string
		wantErr bool
	}{
		{action: bmc.PowerOn, want: "1"},
		{action: bmc.PowerOff, want: "2"},
		{action: bmc.PowerReset, want: "3"},
		{action: bmc.PowerCycle, want: "4"},
		{action: bmc.PowerSoftOff, want: "5"},
		{action: "power_explode", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.action), func(t *testing.T) {
			got, err := cgiPowerCode(tt.action)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
