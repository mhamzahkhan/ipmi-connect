package atenvm

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"ipmi-connect/internal/vmedia"
)

// Credentials authenticate a virtual media session.
type Credentials struct {
	Username    string
	Password    string
	IsSessionID bool // true: Username/Password are a one-time JNLP token
	Encrypt     bool // RC4-encrypt the Device Setup packet (the vendor default)
}

// Connector opens a TCP connection to the BMC's virtual media port and returns fresh
// credentials for it (the JNLP token is one-time, so each connection needs its own).
type Connector func(ctx context.Context) (net.Conn, Credentials, error)

// Group presents the ATEN virtual media service, where each drive needs its own TCP connection
// and emulated USB device, as a single vmedia.Session for the manager. It supports the firmware's
// two slots: one CD-ROM and one disk (floppy or removable).
type Group struct {
	connect   Connector
	keepalive time.Duration

	mu        sync.Mutex
	devices   map[uint16]*deviceSession
	lastEvent string
	closed    bool
	done      chan struct{}
}

var _ vmedia.Session = (*Group)(nil)

// NewGroup returns a Group that opens a fresh connection (with fresh credentials) per mapped drive.
func NewGroup(connect Connector) *Group {
	return &Group{connect: connect, devices: map[uint16]*deviceSession{}, done: make(chan struct{})}
}

// SetKeepaliveInterval overrides the keepalive cadence (used in tests).
func (g *Group) SetKeepaliveInterval(d time.Duration) {
	g.mu.Lock()
	g.keepalive = d
	g.mu.Unlock()
}

// slotFor maps a drive target to its device class, port index and USB descriptors.
func slotFor(t vmedia.Target) (class, port byte, descriptors []byte, err error) {
	switch t {
	case vmedia.TargetCD:
		return ClassCDROM, 0, CDROMDescriptors(), nil
	case vmedia.TargetRemovable:
		return ClassRemovable, 1, DiskDescriptors(), nil
	case vmedia.TargetFloppy:
		return ClassFloppy, 1, DiskDescriptors(), nil
	}
	return 0, 0, nil, fmt.Errorf("unsupported virtual media target %s", t)
}

// Map attaches an image on its own connection and returns a drive id (the target value).
func (g *Group) Map(ctx context.Context, img vmedia.Image, opts vmedia.MapOptions) (uint16, error) {
	if opts.Size <= 0 {
		return 0, errors.New("image size must be positive")
	}
	class, port, descriptors, err := slotFor(opts.Target)
	if err != nil {
		return 0, err
	}
	disk := uint16(opts.Target)

	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return 0, errors.New("virtual media session is closed")
	}
	if _, exists := g.devices[disk]; exists {
		g.mu.Unlock()
		return 0, fmt.Errorf("%s drive is already mapped", opts.Target)
	}
	// The firmware has two slots: at most one CD and one disk at a time.
	if port == 1 {
		for d := range g.devices {
			if d != uint16(vmedia.TargetCD) {
				g.mu.Unlock()
				return 0, errors.New("only one floppy or removable drive can be mapped at a time")
			}
		}
	}
	g.mu.Unlock()

	conn, creds, err := g.connect(ctx)
	if err != nil {
		return 0, fmt.Errorf("connect virtual media: %w", err)
	}

	var target SCSITarget
	readOnly := opts.ReadOnly || opts.Target == vmedia.TargetCD
	if opts.Target == vmedia.TargetCD {
		target = NewISOTarget(img, opts.Size)
	} else {
		target = NewDiskTarget(img, opts.Size, readOnly)
	}

	g.mu.Lock()
	keepalive := g.keepalive
	g.mu.Unlock()
	ds := newDeviceSession(conn, target, disk, opts.Target, readOnly, opts.Size, keepalive)
	if err := ds.open(ctx, setupOpts{
		username: creds.Username, password: creds.Password, isSessionID: creds.IsSessionID,
		portIndex: port, class: class, descriptors: descriptors, encrypt: creds.Encrypt,
	}); err != nil {
		_ = conn.Close()
		return 0, fmt.Errorf("plug in %s drive: %w", opts.Target, err)
	}

	g.mu.Lock()
	g.devices[disk] = ds
	g.mu.Unlock()
	log.Printf("[Supermicro] Virtual media: %s attached", opts.Target)
	return disk, nil
}

// Unmap detaches one drive.
func (g *Group) Unmap(disk uint16) error {
	g.mu.Lock()
	ds := g.devices[disk]
	delete(g.devices, disk)
	g.mu.Unlock()
	if ds == nil {
		return fmt.Errorf("drive %d is not mapped", disk)
	}
	ds.close()
	return nil
}

// Drives aggregates per-drive status and reaps any device whose connection has died.
func (g *Group) Drives() []vmedia.DriveStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	res := make([]vmedia.DriveStatus, 0, len(g.devices))
	for disk, ds := range g.devices {
		select {
		case <-ds.done: // the connection dropped
			delete(g.devices, disk)
			if err := ds.lastErr(); err != nil {
				g.lastEvent = fmt.Sprintf("%s drive disconnected: %v", ds.tgtType, err)
			}
		default:
			res = append(res, ds.status())
		}
	}
	return res
}

// USBReset has no ATEN equivalent (the device is re-plugged by remapping).
func (g *Group) USBReset() error {
	return errors.New("USB reset is not supported on Supermicro virtual media")
}

// Close detaches every drive and ends the session.
func (g *Group) Close() error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	devices := g.devices
	g.devices = map[uint16]*deviceSession{}
	g.mu.Unlock()

	for _, ds := range devices {
		ds.close()
	}
	close(g.done)
	return nil
}

// Done is closed when the session is explicitly closed. A single dropped drive does not end the
// group; the manager's sampler notices it leave Drives().
func (g *Group) Done() <-chan struct{} { return g.done }

// Err reports why the session ended; nil for a clean Close.
func (g *Group) Err() error { return nil }

// LastEvent describes the most recent drive-level disconnect.
func (g *Group) LastEvent() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lastEvent
}
