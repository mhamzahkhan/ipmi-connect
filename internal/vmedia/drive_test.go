package vmedia_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

// fileImage behaves like an *os.File: a short read at the end returns io.EOF, and a write past
// the end grows the file.
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

func TestNewDriveGeometry(t *testing.T) {
	tests := []struct {
		name      string
		opts      vmedia.MapOptions
		blockSize uint32
		blocks    uint32
		readOnly  bool
		wantErr   bool
	}{
		{"cd rounds a partial last sector up", vmedia.MapOptions{Target: vmedia.TargetCD, Size: 2048 + 100}, 2048, 2, true, false},
		{"cd is always read-only", vmedia.MapOptions{Target: vmedia.TargetCD, Size: 4096}, 2048, 2, true, false},
		{"disk ignores a partial last block", vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: 1024 + 100}, 512, 2, false, false},
		{"disk keeps read-only", vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: 1024, ReadOnly: true}, 512, 2, true, false},
		{"floppy under the limit", vmedia.MapOptions{Target: vmedia.TargetFloppy, Size: 1_474_560}, 512, 2880, false, false},
		{"floppy at the limit is refused", vmedia.MapOptions{Target: vmedia.TargetFloppy, Size: 2_880_000}, 0, 0, false, true},
		{"image smaller than a block is refused", vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: 100}, 0, 0, false, true},
		{"image over 2^32 blocks is refused", vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: 512 << 32}, 0, 0, false, true},
		{"unknown target is refused", vmedia.MapOptions{Target: 8, Size: 4096}, 0, 0, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := vmedia.NewDrive(&fileImage{}, tt.opts)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewDrive succeeded, want an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if d.BlockSize != tt.blockSize || d.Blocks != tt.blocks || d.ReadOnly != tt.readOnly {
				t.Errorf("got %d blocks of %d, read-only %v; want %d of %d, %v",
					d.Blocks, d.BlockSize, d.ReadOnly, tt.blocks, tt.blockSize, tt.readOnly)
			}
		})
	}
}

func TestDriveReadZeroPadsPastImageEnd(t *testing.T) {
	img := &fileImage{data: bytes.Repeat([]byte{7}, 2048+100)}
	d, err := vmedia.NewDrive(img, vmedia.MapOptions{Target: vmedia.TargetCD, Size: int64(len(img.data))})
	if err != nil {
		t.Fatal(err)
	}
	buf := bytes.Repeat([]byte{0xEE}, 2048)
	if n, err := d.ReadAt(buf, 2048); err != nil || n != 2048 {
		t.Fatalf("ReadAt last sector = %d, %v", n, err)
	}
	want := append(bytes.Repeat([]byte{7}, 100), make([]byte, 1948)...)
	if !bytes.Equal(buf, want) {
		t.Error("last sector is not the image tail padded with zeros")
	}
	if st := d.Status(3); st.BytesRead != 2048 || st.Disk != 3 || st.Blocks != 2 {
		t.Errorf("status %+v", st)
	}
}

func TestDriveWriteRules(t *testing.T) {
	tests := []struct {
		name     string
		readOnly bool
		off      int64
		n        int
		wantErr  error
	}{
		{"write inside the drive", false, 512, 512, nil},
		{"write to a read-only drive", true, 0, 512, vmedia.ErrReadOnly},
		{"write into the ignored partial tail", false, 1024, 512, vmedia.ErrOutOfRange},
		{"negative offset", false, -512, 512, vmedia.ErrOutOfRange},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			img := &fileImage{data: make([]byte, 1024+100)}
			d, err := vmedia.NewDrive(img, vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: int64(len(img.data)), ReadOnly: tt.readOnly})
			if err != nil {
				t.Fatal(err)
			}
			_, err = d.WriteAt(bytes.Repeat([]byte{9}, tt.n), tt.off)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("WriteAt error %v, want %v", err, tt.wantErr)
			}
			if len(img.data) != 1024+100 {
				t.Errorf("image grew to %d bytes", len(img.data))
			}
			wantWritten := int64(0)
			if tt.wantErr == nil {
				wantWritten = int64(tt.n)
			}
			if got := d.Status(0).BytesWritten; got != wantWritten {
				t.Errorf("BytesWritten %d, want %d", got, wantWritten)
			}
		})
	}
}
