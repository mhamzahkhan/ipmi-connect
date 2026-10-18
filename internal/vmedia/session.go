package vmedia

import (
	"context"
	"fmt"
	"io"
)

// Target is a virtual drive type (CD, removable, floppy).
type Target byte

// Drive types.
const (
	TargetRemovable Target = 1
	TargetCD        Target = 2
	TargetFloppy    Target = 4
)

func (t Target) String() string {
	switch t {
	case TargetRemovable:
		return "removable"
	case TargetCD:
		return "cd"
	case TargetFloppy:
		return "floppy"
	}
	return fmt.Sprintf("type %d", byte(t))
}

// Image is a disk image. Writes are only made to drives mapped writable.
type Image interface {
	io.ReaderAt
	io.WriterAt
}

// MapOptions describes an image to attach.
type MapOptions struct {
	Target   Target
	Size     int64 // image size in bytes
	ReadOnly bool  // CDs are always read-only
}

// DriveStatus describes a mapped drive and its transfer counters.
type DriveStatus struct {
	Disk         uint16
	Target       Target
	ReadOnly     bool
	BlockSize    uint32
	Blocks       uint32
	BytesRead    int64
	BytesWritten int64
}

// Session is the interface implemented by virtual media backend protocols (e.g. Dell AVMP, Supermicro ATEN).
type Session interface {
	Map(ctx context.Context, image Image, o MapOptions) (disk uint16, err error)
	Unmap(disk uint16) error
	Drives() []DriveStatus
	USBReset() error
	Close() error
	Done() <-chan struct{}
	Err() error
	LastEvent() string
}

// Opener connects and logs in a new virtual media session.
type Opener func(ctx context.Context) (Session, error)
