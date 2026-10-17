package avmp

import "testing"

func TestGeometry(t *testing.T) {
	tests := []struct {
		name          string
		blocks        uint32
		cyl           uint16
		heads, sector byte
	}{
		{"1.44 MB floppy", 2880, 80, 2, 18},
		{"720 KB floppy", 1440, 80, 2, 9},
		{"1.2 MB floppy", 2400, 80, 2, 15},
		{"2.88 MB floppy", 5760, 80, 2, 36},
		{"small removable uses 16 heads", 204800, 203, 16, 63},
		{"1 GiB removable (matches the vendor's 130/255/63)", 2097152, 130, 255, 63},
		{"huge removable is capped like the vendor", 40000000, 16383, 16, 63},
		{"tiny image still has one cylinder", 10, 1, 16, 63},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, h, s := geometry(tt.blocks)
			if c != tt.cyl || h != tt.heads || s != tt.sector {
				t.Errorf("got %d/%d/%d, want %d/%d/%d", c, h, s, tt.cyl, tt.heads, tt.sector)
			}
		})
	}
}
