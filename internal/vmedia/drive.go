package vmedia

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sync/atomic"
)

// Block sizes the BMCs use for each drive type.
const (
	BlockSizeCD   = 2048
	BlockSizeDisk = 512 // floppy and removable
)

// Errors a Drive returns for writes it refuses.
var (
	ErrReadOnly   = errors.New("the drive is read-only")
	ErrOutOfRange = errors.New("the access is past the end of the drive")
)

// Drive serves an image as a block device, the part every virtual media protocol shares: the
// geometry and read-only rules for a target, reads that pad past the image end with zeros, bounded
// writes, and transfer counters. It is an io.ReaderAt and io.WriterAt over the drive's bytes.
type Drive struct {
	Target    Target
	BlockSize uint32
	Blocks    uint32
	ReadOnly  bool

	image         Image
	read, written atomic.Int64
}

// NewDrive checks that an image of o.Size bytes fits o.Target and works out its geometry.
func NewDrive(image Image, o MapOptions) (*Drive, error) {
	d := &Drive{Target: o.Target, BlockSize: BlockSizeDisk, ReadOnly: o.ReadOnly, image: image}
	switch o.Target {
	case TargetCD:
		d.BlockSize, d.ReadOnly = BlockSizeCD, true
	case TargetRemovable:
	case TargetFloppy:
		if o.Size >= floppyMaxBytes {
			return nil, fmt.Errorf("a floppy image must be under %d bytes; map it as a removable disk", floppyMaxBytes)
		}
	default:
		return nil, fmt.Errorf("unknown drive target %d", o.Target)
	}
	blocks := o.Size / int64(d.BlockSize)
	if o.Target == TargetCD && o.Size%int64(d.BlockSize) != 0 {
		blocks++ // a padded last sector reads as zeros
	}
	if blocks < 1 {
		return nil, errors.New("the image is empty")
	}
	if blocks > math.MaxUint32 {
		return nil, errors.New("the image is too large")
	}
	d.Blocks = uint32(blocks)
	return d, nil
}

// Size is the drive's size in bytes.
func (d *Drive) Size() int64 { return int64(d.Blocks) * int64(d.BlockSize) }

// Contains reports whether n blocks starting at lba lie on the drive.
func (d *Drive) Contains(lba, n uint32) bool { return uint64(lba)+uint64(n) <= uint64(d.Blocks) }

// ReadAt fills p from byte offset off. Bytes past the end of the image read as zeros.
func (d *Drive) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, ErrOutOfRange
	}
	clear(p)
	if off < d.Size() {
		end := min(int64(len(p)), d.Size()-off)
		if _, err := d.image.ReadAt(p[:end], off); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
	}
	d.read.Add(int64(len(p)))
	return len(p), nil
}

// WriteAt writes p at byte offset off. It refuses a read-only drive and anything past the end of
// the drive, so a write never grows the image.
func (d *Drive) WriteAt(p []byte, off int64) (int, error) {
	if d.ReadOnly {
		return 0, ErrReadOnly
	}
	if off < 0 || off+int64(len(p)) > d.Size() {
		return 0, ErrOutOfRange
	}
	n, err := d.image.WriteAt(p, off)
	d.written.Add(int64(n))
	return n, err
}

// Status reports the drive as disk id.
func (d *Drive) Status(disk uint16) DriveStatus {
	return DriveStatus{
		Disk: disk, Target: d.Target, ReadOnly: d.ReadOnly, BlockSize: d.BlockSize, Blocks: d.Blocks,
		BytesRead: d.read.Load(), BytesWritten: d.written.Load(),
	}
}
