package atenvm

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"ipmi-connect/internal/vmedia"
)

// memImage is an in-memory image that reports errors on demand.
type memImage struct {
	data     []byte
	failRead bool
}

func (m *memImage) ReadAt(p []byte, off int64) (int, error) {
	if m.failRead {
		return 0, errors.New("disk error")
	}
	if off >= int64(len(m.data)) {
		return 0, nil
	}
	n := copy(p, m.data[off:])
	return n, nil
}

func (m *memImage) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(m.data)) {
		return 0, errors.New("out of range")
	}
	return copy(m.data[off:], p), nil
}

func isoTarget(t *testing.T, img vmedia.Image, size int64) SCSITarget {
	t.Helper()
	return mustTarget(t, img, vmedia.MapOptions{Target: vmedia.TargetCD, Size: size})
}

func diskTarget(t *testing.T, img vmedia.Image, size int64, readOnly bool) SCSITarget {
	t.Helper()
	return mustTarget(t, img, vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: size, ReadOnly: readOnly})
}

func mustTarget(t *testing.T, img vmedia.Image, o vmedia.MapOptions) SCSITarget {
	t.Helper()
	d, err := vmedia.NewDrive(img, o)
	if err != nil {
		t.Fatal(err)
	}
	return NewTarget(d)
}

func cdb16(b ...byte) []byte {
	out := make([]byte, 16)
	copy(out, b)
	return out
}

// exec runs a full 16-byte CDB and returns data+status.
func exec(target SCSITarget, dataLen uint32, b ...byte) ([]byte, byte) {
	data, ok := target.Execute(cdb16(b...), dataLen)
	if !ok {
		return nil, CSWStatusCommandFail
	}
	return data, CSWStatusGood
}

func TestISOReadBounds(t *testing.T) {
	const sectors = 4
	img := &memImage{data: bytes.Repeat([]byte{0xAB}, sectors*SectorSizeCD)}
	tgt := isoTarget(t, img, int64(len(img.data)))

	// In-range READ(10): 2 sectors at LBA 1.
	data, st := exec(tgt, 2*SectorSizeCD, CmdRead10, 0, 0, 0, 0, 1, 0, 0, 2)
	if st != CSWStatusGood || len(data) != 2*SectorSizeCD {
		t.Fatalf("in-range read: status %#x len %d", st, len(data))
	}

	// Past the end must fail with a sense, not return zeros.
	if _, st := exec(tgt, SectorSizeCD, CmdRead10, 0, 0, 0, 0, byte(sectors), 0, 0, 1); st != CSWStatusCommandFail {
		t.Error("read past end returned success")
	}
	if sk, asc := tgt.(*scsiTarget).sense.peek(); sk != senseIllegalRequest || asc != ascLBAOutOfRange {
		t.Errorf("sense %d/%02x, want illegal-request/LBA-out-of-range", sk, asc)
	}

	// Overflow: huge block count must not allocate or wrap.
	if _, st := exec(tgt, 0xFFFFFFFF, CmdRead12, 0, 0, 0, 0, 0, 0x20, 0, 0); st != CSWStatusCommandFail {
		t.Error("overflowing READ(12) returned success")
	}

	// A real read error must fail, never silently return zeros.
	img.failRead = true
	if _, st := exec(tgt, SectorSizeCD, CmdRead10, 0, 0, 0, 0, 0, 0, 0, 1); st != CSWStatusCommandFail {
		t.Error("read error returned success")
	}
}

func TestISONoDataWhenDataLenZero(t *testing.T) {
	img := &memImage{data: make([]byte, 4*SectorSizeCD)}
	tgt := isoTarget(t, img, int64(len(img.data)))
	data, _ := exec(tgt, 0, CmdRead10, 0, 0, 0, 0, 0, 0, 0, 1)
	if len(data) != 0 {
		t.Errorf("returned %d bytes for a zero-length transfer", len(data))
	}
}

func TestShortCDBDoesNotPanic(t *testing.T) {
	img := &memImage{data: make([]byte, 2*SectorSizeCD)}
	tgt := isoTarget(t, img, int64(len(img.data)))
	for _, op := range []byte{CmdReadTOC, CmdRead10, CmdRead12, CmdModeSense10, CmdInquiry} {
		// A 1-byte CDB (just the opcode) must be rejected, not panic.
		if _, ok := tgt.Execute([]byte{op}, 64); ok {
			t.Errorf("opcode %#x accepted a 1-byte CDB", op)
		}
	}
}

func TestUnsupportedOpcodeSetsSense(t *testing.T) {
	img := &memImage{data: make([]byte, 2*SectorSizeCD)}
	tgt := isoTarget(t, img, int64(len(img.data)))
	if _, ok := tgt.Execute(cdb16(0xFC), 0); ok {
		t.Error("unknown opcode 0xFC accepted")
	}
	if sk, asc := tgt.(*scsiTarget).sense.peek(); sk != senseIllegalRequest || asc != ascInvalidCommand {
		t.Errorf("sense %d/%02x, want illegal-request/invalid-command", sk, asc)
	}
	// REQUEST SENSE then reports it.
	sense, ok := tgt.Execute(cdb16(CmdRequestSense, 0, 0, 0, 18), 18)
	if !ok || sense[2]&0x0f != senseIllegalRequest || sense[12] != ascInvalidCommand {
		t.Errorf("request sense returned %x", sense)
	}
}

func TestISOVendorNoOpsAccepted(t *testing.T) {
	img := &memImage{data: make([]byte, 2*SectorSizeCD)}
	tgt := isoTarget(t, img, int64(len(img.data)))
	for _, op := range []byte{CmdSetCDSpeed, CmdMechanismStatus, CmdStartStopUnit, CmdPreventAllowMediumRemoval} {
		if _, ok := tgt.Execute(cdb16(op), 0); !ok {
			t.Errorf("vendor no-op opcode %#x rejected", op)
		}
	}
}

func TestDiskWriteBoundsAndProtect(t *testing.T) {
	const sectors = 8
	img := &memImage{data: make([]byte, sectors*SectorSizeDisk)}
	tgt := diskTarget(t, img, int64(len(img.data)), false)

	// In-range WRITE(10): 1 sector at LBA 3.
	payload := bytes.Repeat([]byte{0x5A}, SectorSizeDisk)
	if st := tgt.WriteData(cdb16(CmdWrite10, 0, 0, 0, 0, 3, 0, 0, 1), payload); st != CSWStatusGood {
		t.Fatalf("in-range write status %#x", st)
	}
	if !bytes.Equal(img.data[3*SectorSizeDisk:4*SectorSizeDisk], payload) {
		t.Error("in-range write did not land")
	}

	// Out-of-range WRITE must fail and not touch the image.
	before := append([]byte(nil), img.data...)
	if st := tgt.WriteData(cdb16(CmdWrite10, 0xFF, 0xFF, 0xFF, 0xFF, 0, 0, 1), payload); st != CSWStatusCommandFail {
		t.Error("out-of-range write returned success")
	}
	if !bytes.Equal(img.data, before) {
		t.Error("out-of-range write modified the image")
	}

	// A non-write OUT command (MODE SELECT) must not write to the image.
	if st := tgt.WriteData(cdb16(0x15, 0x10, 0, 0, 12), make([]byte, 12)); st != CSWStatusCommandFail {
		t.Error("MODE SELECT was accepted as a write")
	}

	// Mismatched data length (2 sectors claimed, 1 sector of data) must fail.
	if st := tgt.WriteData(cdb16(CmdWrite10, 0, 0, 0, 0, 0, 0, 0, 2), payload); st != CSWStatusCommandFail {
		t.Error("short write data accepted")
	}

	// Read-only target refuses writes with DATA PROTECT.
	ro := diskTarget(t, img, int64(len(img.data)), true)
	if st := ro.WriteData(cdb16(CmdWrite10, 0, 0, 0, 0, 0, 0, 0, 1), payload); st != CSWStatusCommandFail {
		t.Error("read-only target accepted a write")
	}
	if sk, asc := ro.(*scsiTarget).sense.peek(); sk != senseDataProtect || asc != ascWriteProtected {
		t.Errorf("sense %d/%02x, want data-protect/write-protected", sk, asc)
	}
}

func TestReadOfUnalignedImageTail(t *testing.T) {
	tests := []struct {
		name      string
		target    vmedia.Target
		size      int
		lastLBA   byte
		blockSize int
		wantTail  int // image bytes in the last block; the rest must read as zeros
	}{
		{"cd pads a partial last sector", vmedia.TargetCD, SectorSizeCD + 100, 1, SectorSizeCD, 100},
		{"disk hides a partial last block", vmedia.TargetRemovable, 2*SectorSizeDisk + 100, 1, SectorSizeDisk, SectorSizeDisk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := &fileImage{data: bytes.Repeat([]byte{0x77}, tt.size)}
			tgt := mustTarget(t, img, vmedia.MapOptions{Target: tt.target, Size: int64(tt.size)})
			data, st := exec(tgt, uint32(tt.blockSize), CmdRead10, 0, 0, 0, 0, tt.lastLBA, 0, 0, 1)
			if st != CSWStatusGood || len(data) != tt.blockSize {
				t.Fatalf("READ(10) of the last block: status %d, %d bytes", st, len(data))
			}
			want := append(bytes.Repeat([]byte{0x77}, tt.wantTail), make([]byte, tt.blockSize-tt.wantTail)...)
			if !bytes.Equal(data, want) {
				t.Error("last block is not the image tail padded with zeros")
			}
			// One block past the end is out of range, so the tail is never written either.
			payload := make([]byte, tt.blockSize)
			if st := tgt.WriteData(cdb16(CmdWrite10, 0, 0, 0, 0, tt.lastLBA+1, 0, 0, 1), payload); st != CSWStatusCommandFail {
				t.Error("write past the last block was accepted")
			}
			if len(img.data) != tt.size {
				t.Errorf("image grew to %d bytes", len(img.data))
			}
		})
	}
}

// fileImage behaves like an *os.File: a short read returns io.EOF and a write past the end grows it.
type fileImage struct{ data []byte }

func (f *fileImage) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fileImage) WriteAt(p []byte, off int64) (int, error) {
	if end := off + int64(len(p)); end > int64(len(f.data)) {
		f.data = append(f.data, make([]byte, end-int64(len(f.data)))...)
	}
	return copy(f.data[off:], p), nil
}
