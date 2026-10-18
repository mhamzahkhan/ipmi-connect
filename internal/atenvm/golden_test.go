package atenvm_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	"ipmi-connect/internal/atenvm"
)

// patternReader fills reads with a byte pattern derived from the offset (for READ tests).
type patternReader struct{ size int64 }

func (p *patternReader) ReadAt(b []byte, off int64) (int, error) {
	for i := range b {
		b[i] = byte((off + int64(i)) & 0xFF)
	}
	return len(b), nil
}
func (p *patternReader) WriteAt(b []byte, off int64) (int, error) { return len(b), nil }

// These response bytes are from the RC4-decrypted USBAnywhere capture (see
// notes/supermicro-vmedia-capture-decode.md Appendix B); they are the regression fixtures.
func TestCDGoldenResponses(t *testing.T) {
	const sectors = 8192
	tgt := atenvm.NewISOTarget(&patternReader{size: sectors * atenvm.SectorSizeCD}, sectors*atenvm.SectorSizeCD)

	// INQUIRY
	check(t, tgt, "INQUIRY", "120000002400", 36,
		"058000211f0000004154454e202020205669727475616c204344524f4d2020205953304a")
	// READ CAPACITY(10): last LBA 8191, block 2048.
	check(t, tgt, "READ CAPACITY", "25000000000000000000", 8, "00001fff00000800")
	// READ TOC format 0 (LBA), full 20 bytes.
	check(t, tgt, "READ TOC fmt0", "4300000000000000ff00", 0xff00,
		"0012010100140100000000000014aa0000002000")
	// READ TOC format 1 (session info).
	check(t, tgt, "READ TOC fmt1", "4300010000000000ff00", 0xff00, "000a01010014010000000000")
	// GET CONFIGURATION, allocation 8.
	check(t, tgt, "GET CONFIG a8", "46000000000000000800", 8, "0000009800000008")
	// GET EVENT STATUS (no change, media present) once TEST UNIT READY cleared the new-media flag.
	tgt.Execute(mustHex(t, "000000000000"), 0)
	check(t, tgt, "GET EVENT STATUS", "4a010000100000000800", 8, "0006045e00020000")
	// READ(10): 1 sector at LBA 0 → pattern bytes.
	data, ok := tgt.Execute(mustHex(t, "2800000000000000 0100"), atenvm.SectorSizeCD)
	if !ok || len(data) != atenvm.SectorSizeCD || data[0] != 0x00 || data[1] != 0x01 {
		t.Errorf("READ(10): ok=%v len=%d head=% x", ok, len(data), data[:2])
	}
}

func check(t *testing.T, tgt atenvm.SCSITarget, name, cdbHex string, dataLen uint32, wantHex string) {
	t.Helper()
	data, ok := tgt.Execute(mustHex(t, cdbHex), dataLen)
	if !ok {
		t.Errorf("%s: command failed", name)
		return
	}
	if got := hex.EncodeToString(data); got != wantHex {
		t.Errorf("%s:\n got  %s\n want %s", name, got, wantHex)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings_clean(s))
	if err != nil {
		t.Fatalf("bad hex %q: %v", s, err)
	}
	return b
}

func strings_clean(s string) string {
	return string(bytes.ReplaceAll([]byte(s), []byte(" "), nil))
}
