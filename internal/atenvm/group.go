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
	plugging  map[uint16]bool // drives between the slot check and the end of setup
	lastEvent string
	closed    bool
	done      chan struct{}
}

var _ vmedia.Session = (*Group)(nil)

// NewGroup returns a Group that opens a fresh connection (with fresh credentials) per mapped drive.
func NewGroup(connect Connector) *Group {
	return &Group{
		connect: connect, devices: map[uint16]*deviceSession{}, plugging: map[uint16]bool{},
		done: make(chan struct{}),
	}
}

// SetKeepaliveInterval overrides the keepalive cadence (used in tests).
func (g *Group) SetKeepaliveInterval(d time.Duration) {
	g.mu.Lock()
	g.keepalive = d
	g.mu.Unlock()
}

// slotFor maps a drive target to its device class and USB descriptors. Every drive asks for port
// index 0: the BMC picks the free slot itself, and on the X7SB3 a drive set up with port 1 stalls
// the host right after INQUIRY.
func slotFor(t vmedia.Target) (class byte, descriptors []byte, err error) {
	switch t {
	case vmedia.TargetCD:
		return ClassCDROM, CDROMDescriptors(), nil
	case vmedia.TargetRemovable:
		return ClassRemovable, DiskDescriptors(), nil
	case vmedia.TargetFloppy:
		return ClassFloppy, DiskDescriptors(), nil
	}
	return 0, nil, fmt.Errorf("unsupported virtual media target %s", t)
}

// Map attaches an image on its own connection and returns a drive id (the target value).
func (g *Group) Map(ctx context.Context, img vmedia.Image, opts vmedia.MapOptions) (uint16, error) {
	class, descriptors, err := slotFor(opts.Target)
	if err != nil {
		return 0, err
	}
	drive, err := vmedia.NewDrive(img, opts)
	if err != nil {
		return 0, err
	}
	disk := uint16(opts.Target)
	if err := g.reserve(disk, opts.Target); err != nil {
		return 0, err
	}
	ds, err := g.plugIn(ctx, drive, disk, setupOpts{class: class, descriptors: descriptors})

	g.mu.Lock()
	delete(g.plugging, disk)
	closed := g.closed
	if err == nil && !closed {
		g.devices[disk] = ds
	}
	g.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if closed { // Close ran while the drive was plugging in
		ds.close()
		return 0, errors.New("virtual media session is closed")
	}
	log.Printf("[Supermicro] Virtual media: %s attached", opts.Target)
	return disk, nil
}

// reserve claims the drive's slot until Map finishes. The firmware has two slots: at most one CD
// and one disk (floppy or removable) at a time.
func (g *Group) reserve(disk uint16, t vmedia.Target) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return errors.New("virtual media session is closed")
	}
	taken := func(d uint16) bool { return g.devices[d] != nil || g.plugging[d] }
	if taken(disk) {
		return fmt.Errorf("%s drive is already mapped", t)
	}
	if t != vmedia.TargetCD && (taken(uint16(vmedia.TargetRemovable)) || taken(uint16(vmedia.TargetFloppy))) {
		return errors.New("only one floppy or removable drive can be mapped at a time")
	}
	g.plugging[disk] = true
	return nil
}

// plugIn opens a connection with fresh credentials and plugs the drive in on it.
func (g *Group) plugIn(ctx context.Context, drive *vmedia.Drive, disk uint16, o setupOpts) (*deviceSession, error) {
	conn, creds, err := g.connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect virtual media: %w", err)
	}
	g.mu.Lock()
	keepalive := g.keepalive
	g.mu.Unlock()
	ds := newDeviceSession(conn, drive, disk, keepalive)
	o.username, o.password, o.isSessionID, o.encrypt = creds.Username, creds.Password, creds.IsSessionID, creds.Encrypt
	if err := ds.open(ctx, o); err != nil {
		_ = conn.Close()
		if ctx.Err() != nil {
			err = ctx.Err() // report the cancellation, not the I/O error it caused
		}
		return nil, fmt.Errorf("plug in %s drive: %w", drive.Target, err)
	}
	return ds, nil
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
				g.lastEvent = fmt.Sprintf("%s drive disconnected: %v", ds.drive.Target, err)
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
