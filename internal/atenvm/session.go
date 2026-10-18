package atenvm

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"ipmi-connect/internal/vmedia"
)

const (
	defaultKeepaliveInterval = 3 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultSetupTimeout      = 15 * time.Second
)

// SessionOptions configures a virtual media session.
type SessionOptions struct {
	Username          string
	Password          string
	IsSessionID       bool
	PortIndex         byte
	Class             byte
	Descriptors       []byte
	Encrypt           bool
	KeepaliveInterval time.Duration
}

type drive struct {
	disk         uint16
	target       vmedia.Target
	readOnly     bool
	blockSize    uint32
	blocks       uint32
	bytesRead    int64
	bytesWritten int64
}

// Session manages a single ATEN Virtual Media TCP connection.
// It implements vmedia.Session.
type Session struct {
	conn      net.Conn
	opts      SessionOptions
	sessionID uint32
	devPort   byte
	writeMu   sync.Mutex

	mu        sync.Mutex
	drives    map[uint16]*drive
	target    SCSITarget
	lastErr   error
	lastEvent string

	stop      context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
}

var _ vmedia.Session = (*Session)(nil)

// NewSession creates an ATEN Virtual Media session over an established TCP connection.
func NewSession(conn net.Conn, opts SessionOptions) *Session {
	if opts.KeepaliveInterval == 0 {
		opts.KeepaliveInterval = defaultKeepaliveInterval
	}
	if len(opts.Descriptors) == 0 {
		opts.Descriptors = CDROMDescriptors()
	}

	return &Session{
		conn:    conn,
		opts:    opts,
		devPort: 1, // Port 1 once plugged in
		drives:  make(map[uint16]*drive),
		done:    make(chan struct{}),
	}
}

// Open performs the Device Setup handshake and arms the USB endpoints.
func (s *Session) Open(ctx context.Context) error {
	deadline := time.Now().Add(defaultSetupTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.conn.SetDeadline(deadline)
	defer func() { _ = s.conn.SetDeadline(time.Time{}) }()

	// 1. Build and send Device Setup packet (Tag 0x01)
	hSetup, setupPayload, err := BuildDeviceSetupPacket(SetupOptions{
		Username:    s.opts.Username,
		Password:    s.opts.Password,
		IsSessionID: s.opts.IsSessionID,
		PortIndex:   s.opts.PortIndex,
		Class:       s.opts.Class,
		Descriptors: s.opts.Descriptors,
		Encrypt:     s.opts.Encrypt,
	})
	if err != nil {
		return fmt.Errorf("build setup packet: %w", err)
	}
	if err := s.writeFrame(hSetup, setupPayload); err != nil {
		return fmt.Errorf("send setup packet: %w", err)
	}

	// 2. Read Device Setup Response (Tag 0x02)
	hResp, respPayload, err := ReadFrame(s.conn)
	if err != nil {
		return fmt.Errorf("read setup response: %w", err)
	}
	if hResp.Tag != TagDeviceSetupResp {
		return fmt.Errorf("unexpected setup response tag: 0x%02x (want 0x02)", hResp.Tag)
	}
	status, sid, err := ParseDeviceSetupResponse(respPayload)
	if err != nil {
		return err
	}
	if status != StatusSetupOK {
		return fmt.Errorf("setup refused with status 0x%02x", status)
	}
	s.sessionID = sid

	// 3. Build and send USB EP Setup packet (Tag 0x07)
	hEP, epPayload := BuildEPSetupPacket()
	if err := s.writeFrame(hEP, epPayload); err != nil {
		return fmt.Errorf("send EP setup: %w", err)
	}

	runCtx, stop := context.WithCancel(context.Background())
	s.stop = stop

	go s.run(runCtx)
	go s.keepalives(runCtx)

	return nil
}

// Close disconnects and cleans up the session.
func (s *Session) Close() error {
	var err error
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		// Send detach if connection is still alive
		hDetach, _ := BuildDetachPacket(s.devPort)
		_ = s.writeFrameWithin(hDetach, nil, 1*time.Second)

		err = s.conn.Close()
		close(s.done)
	})
	return err
}

// Done returns a channel that is closed when the session ends.
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Map attaches a disk image to the session.
func (s *Session) Map(ctx context.Context, img vmedia.Image, opts vmedia.MapOptions) (uint16, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	select {
	case <-s.done:
		return 0, errors.New("session closed")
	default:
	}

	disk := uint16(opts.Target)
	if _, exists := s.drives[disk]; exists {
		return 0, fmt.Errorf("drive %d is already mapped", disk)
	}

	blockSize := uint32(SectorSizeCD)
	blocks := uint32(opts.Size / int64(blockSize))
	if opts.Size%int64(blockSize) != 0 {
		blocks++
	}

	// Create and attach SCSI target
	s.target = NewISOTarget(img, opts.Size)
	s.drives[disk] = &drive{
		disk:      disk,
		target:    opts.Target,
		readOnly:  opts.ReadOnly,
		blockSize: blockSize,
		blocks:    blocks,
	}

	return disk, nil
}

// Unmap detaches a drive by its disk identifier.
func (s *Session) Unmap(disk uint16) error {
	s.mu.Lock()
	delete(s.drives, disk)
	s.target = nil
	empty := len(s.drives) == 0
	s.mu.Unlock()

	if empty {
		// No drives mapped: detach device
		hDetach, _ := BuildDetachPacket(s.devPort)
		_ = s.writeFrameWithin(hDetach, nil, 2*time.Second)
	}
	return nil
}

// USBReset arms/resets the endpoint queues.
func (s *Session) USBReset() error {
	hEP, epPayload := BuildEPSetupPacket()
	return s.writeFrame(hEP, epPayload)
}

// Err returns the error that caused the session to end.
func (s *Session) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// LastEvent returns the latest drive-level failure message.
func (s *Session) LastEvent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastEvent
}

// Drives returns the current status and I/O counters for all mapped drives.
func (s *Session) Drives() []vmedia.DriveStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	res := make([]vmedia.DriveStatus, 0, len(s.drives))
	for _, d := range s.drives {
		res = append(res, vmedia.DriveStatus{
			Disk:         d.disk,
			Target:       d.target,
			ReadOnly:     d.readOnly,
			BlockSize:    d.blockSize,
			Blocks:       d.blocks,
			BytesRead:    d.bytesRead,
			BytesWritten: d.bytesWritten,
		})
	}
	return res
}

func (s *Session) writeFrame(h Header, payload []byte) error {
	return s.writeFrameWithin(h, payload, defaultWriteTimeout)
}

func (s *Session) writeFrameWithin(h Header, payload []byte, timeout time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_ = s.conn.SetWriteDeadline(time.Now().Add(timeout))
	defer func() { _ = s.conn.SetWriteDeadline(time.Time{}) }()

	return WriteFrame(s.conn, h, payload)
}

func (s *Session) keepalives(ctx context.Context) {
	if s.opts.KeepaliveInterval <= 0 {
		return
	}
	ticker := time.NewTicker(s.opts.KeepaliveInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hPing, pingPayload := BuildKeepalivePacket(0xFFFFFFFF)
			if err := s.writeFrame(hPing, pingPayload); err != nil {
				return
			}
		}
	}
}

func (s *Session) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		h, payload, err := ReadFrame(s.conn)
		if err != nil {
			s.mu.Lock()
			s.lastErr = err
			s.mu.Unlock()
			_ = s.Close()
			return
		}

		switch h.Tag {
		case TagEnqueue:
			if h.Endpoint == EndpointBulkOut {
				// Incoming Command Block Wrapper (CBW)
				cbw, err := ParseCBW(payload)
				if err != nil {
					continue
				}
				s.serveCBW(cbw)
			}

		case TagPingReq:
			// Server keepalive ping: echo token in TagPingResp
			token := uint32(0xFFFFFFFF)
			if len(payload) >= 4 {
				token = binary.LittleEndian.Uint32(payload[0:4])
			}
			hPong, pongPayload := BuildKeepalivePacket(token)
			_ = s.writeFrame(hPong, pongPayload)

		case TagDetachResp:
			// Detach acknowledged by BMC
			_ = s.Close()
			return

		case TagStatusReq:
			// Drive status query
			s.mu.Lock()
			st := byte(0xFF)
			if len(s.drives) > 0 {
				st = 0x03
			}
			s.mu.Unlock()
			hStat := Header{
				Endpoint:   EndpointControl,
				Flags:      0x00,
				DevPort:    0x00,
				Tag:        TagStatusResp,
				PayloadLen: 3,
			}
			_ = s.writeFrame(hStat, []byte{0x02, st, 0xFF})
		}
	}
}

func (s *Session) serveCBW(cbw CBW) {
	s.mu.Lock()
	target := s.target
	s.mu.Unlock()

	var data []byte
	status := CSWStatusCommandFail
	residue := cbw.DataLen

	if target != nil {
		data, status, residue = target.ExecuteCDB(cbw.CDB[:cbw.CDBLength], cbw.DataLen)
	}

	// 1. Send Data PDU if command returned data
	if len(data) > 0 {
		hData := Header{
			Endpoint:   EndpointBulkIn,
			Flags:      0x00,
			DevPort:    s.devPort,
			Tag:        TagEnqueue,
			PayloadLen: uint32(len(data)),
		}
		if err := s.writeFrame(hData, data); err != nil {
			return
		}

		s.mu.Lock()
		for _, d := range s.drives {
			d.bytesRead += int64(len(data))
		}
		s.mu.Unlock()
	}

	// 2. Send Command Status Wrapper (CSW)
	csw := CSW{
		Tag:     cbw.Tag,
		Residue: residue,
		Status:  status,
	}
	hCSW := Header{
		Endpoint:   EndpointBulkIn,
		Flags:      0x00,
		DevPort:    s.devPort,
		Tag:        TagEnqueueAndGo,
		PayloadLen: CSWLength,
	}
	_ = s.writeFrame(hCSW, csw.Encode())
}
