package atenvm_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"ipmi-connect/internal/atenvm"
)

// fakeISOReader creates a synthetic ISO reader with a known pattern.
type fakeISOReader struct {
	sectors uint32
}

func (f *fakeISOReader) ReadAt(p []byte, off int64) (n int, err error) {
	for i := range p {
		// Fill with reproducible byte pattern derived from file offset
		p[i] = byte((off + int64(i)) & 0xFF)
	}
	return len(p), nil
}

func TestSCSIGoldenVectors(t *testing.T) {
	// Golden test fixture: 8192 sectors × 2048 B = 16 MiB (matching captured session)
	sectors := uint32(8192)
	size := int64(sectors) * atenvm.SectorSizeCD
	r := &fakeISOReader{sectors: sectors}
	target := atenvm.NewISOTarget(r, size)

	tests := []struct {
		name        string
		cdbHex      string
		allocLen    uint32
		wantDataHex string
		wantStatus  byte
		wantResidue uint32
	}{
		{
			name:        "0x12 INQUIRY",
			cdbHex:      "120000002400",
			allocLen:    36,
			wantDataHex: "058000211f0000004154454e202020205669727475616c204344524f4d2020205953304a",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x00 TEST UNIT READY",
			cdbHex:      "000000000000",
			allocLen:    0,
			wantDataHex: "",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x25 READ CAPACITY(10)",
			cdbHex:      "25000000000000000000",
			allocLen:    8,
			wantDataHex: "00001fff00000800", // lastLBA = 8191 (0x1FFF), blockSize = 2048 (0x0800)
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x43 READ TOC format 1 (session info)",
			cdbHex:      "43000000000000000c40",
			allocLen:    12,
			wantDataHex: "000a01010014010000000000",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x43 READ TOC format 0 (standard)",
			cdbHex:      "43000000000000000c00",
			allocLen:    12,
			wantDataHex: "001201010014010000000000",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x5A MODE SENSE(10) page 0x2A",
			cdbHex:      "5a002a00000000008000",
			allocLen:    128,
			wantDataHex: "00240100000000002a1c3f00717729231b90010000c61b900010000000000000100000000000",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 90, // 128 - 38 = 90
		},
		{
			name:        "0x46 GET CONFIGURATION alloc 8",
			cdbHex:      "46000000000000000800",
			allocLen:    8,
			wantDataHex: "0000009800000008",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x4A GET EVENT STATUS NOTIFICATION",
			cdbHex:      "4a010000100000000800",
			allocLen:    8,
			wantDataHex: "0006045e00020000",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x51 READ DISC INFORMATION alloc 2",
			cdbHex:      "51000000000000000200",
			allocLen:    2,
			wantDataHex: "0020",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x52 READ TRACK INFORMATION alloc 8",
			cdbHex:      "52010000000100000800",
			allocLen:    8,
			wantDataHex: "001c010100040100",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
		{
			name:        "0x52 READ TRACK INFORMATION alloc 30",
			cdbHex:      "52010000000100001e00",
			allocLen:    30,
			wantDataHex: "001c0101000401000000000000000000000000000000000000002000", // 28 bytes returned (sectors 8192 = 0x00002000)
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 2, // 30 - 28 = 2
		},
		{
			name:        "0x1E PREVENT ALLOW MEDIUM REMOVAL",
			cdbHex:      "1e0000000100",
			allocLen:    0,
			wantDataHex: "",
			wantStatus:  atenvm.CSWStatusGood,
			wantResidue: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cdb, err := hex.DecodeString(tt.cdbHex)
			if err != nil {
				t.Fatalf("invalid CDB hex: %v", err)
			}
			data, status, residue := target.ExecuteCDB(cdb, tt.allocLen)
			if status != tt.wantStatus {
				t.Errorf("status = 0x%02x, want 0x%02x", status, tt.wantStatus)
			}
			if residue != tt.wantResidue {
				t.Errorf("residue = %d, want %d", residue, tt.wantResidue)
			}
			actualHex := strings.ToLower(hex.EncodeToString(data))
			if actualHex != strings.ToLower(tt.wantDataHex) {
				t.Errorf("data mismatch:\ngot:  %s\nwant: %s", actualHex, tt.wantDataHex)
			}
		})
	}
}

func TestSCSIRead10(t *testing.T) {
	sectors := uint32(100)
	size := int64(sectors) * atenvm.SectorSizeCD
	r := &fakeISOReader{sectors: sectors}
	target := atenvm.NewISOTarget(r, size)

	// Read 2 sectors (4096 B) starting at LBA 5
	// CDB: 28 00 00 00 00 05 00 00 02 00
	cdb, _ := hex.DecodeString("28000000000500000200")
	allocLen := uint32(4096)
	data, status, residue := target.ExecuteCDB(cdb, allocLen)
	if status != atenvm.CSWStatusGood {
		t.Errorf("status = 0x%02x, want 0x00", status)
	}
	if residue != 0 {
		t.Errorf("residue = %d, want 0", residue)
	}
	if len(data) != 4096 {
		t.Fatalf("read %d bytes, want 4096", len(data))
	}

	// Verify first byte matches offset 5 * 2048 = 10240
	expectedFirst := byte(10240 & 0xFF)
	if data[0] != expectedFirst {
		t.Errorf("data[0] = 0x%02x, want 0x%02x", data[0], expectedFirst)
	}
}

func TestSCSIUnsupportedCommand(t *testing.T) {
	target := atenvm.NewISOTarget(bytes.NewReader(make([]byte, 2048)), 2048)
	// Write command (0x2A) is unsupported for read-only CD
	cdb := []byte{0x2A, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00}
	_, status, residue := target.ExecuteCDB(cdb, 2048)
	if status != atenvm.CSWStatusCommandFail {
		t.Errorf("status = 0x%02x, want Command Fail (0x01)", status)
	}
	if residue != 2048 {
		t.Errorf("residue = %d, want 2048", residue)
	}
}
