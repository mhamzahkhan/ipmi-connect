package atenvm

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"ipmi-connect/internal/vmedia"
)

const (
	defaultKeepalive = 10 * time.Second // client sends keepalive; the BMC idle timeout is ~30 s
	writeTimeout     = 10 * time.Second
	setupTimeout     = 15 * time.Second
	detachAckTimeout = 2 * time.Second
	// maxBulkChunk bounds one bulk-IN data frame (the BMC's ring is ~260 KB).
	maxBulkChunk = 64 * 1024
	// controlOutMax bounds a non-write OUT transfer (e.g. MODE SELECT) to a read-only device.
	controlOutMax = 64 * 1024
)

// deviceSession is one TCP connection carrying one emulated USB device (one drive). The ATEN
// firmware allows exactly one device per connection, so a Group (below) opens one of these per
// mapped drive.
type deviceSession struct {
	conn           net.Conn
	drive          *vmedia.Drive
	target         *scsiTarget
	disk           uint16
	keepaliveEvery time.Duration

	writeMu sync.Mutex

	stop      context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	err       error
}

// setupOpts carries what Open needs to plug the device in.
type setupOpts struct {
	username, password string
	isSessionID        bool
	portIndex          byte
	class              byte
	descriptors        []byte
	encrypt            bool
}

func newDeviceSession(conn net.Conn, drive *vmedia.Drive, disk uint16, keepalive time.Duration) *deviceSession {
	if keepalive <= 0 {
		keepalive = defaultKeepalive
	}
	return &deviceSession{
		conn: conn, drive: drive, target: newTarget(drive), disk: disk,
		keepaliveEvery: keepalive, done: make(chan struct{}),
	}
}

// open performs Device Setup + EP Setup, then starts the serve and keepalive goroutines.
func (s *deviceSession) open(ctx context.Context, o setupOpts) error {
	deadline := time.Now().Add(setupTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.conn.SetDeadline(deadline)
	// Cancelling ctx (an eject while plugging in) unblocks the setup I/O at once.
	stopCancel := context.AfterFunc(ctx, func() { _ = s.conn.SetDeadline(time.Now()) })
	defer stopCancel()

	hSetup, setupPayload, err := BuildDeviceSetupPacket(SetupOptions{
		Username: o.username, Password: o.password, IsSessionID: o.isSessionID,
		PortIndex: o.portIndex, Class: o.class, Descriptors: o.descriptors, Encrypt: o.encrypt,
	})
	if err != nil {
		return fmt.Errorf("build setup packet: %w", err)
	}
	if err := WriteFrame(s.conn, hSetup, setupPayload); err != nil {
		return fmt.Errorf("send setup packet: %w", err)
	}
	hResp, respPayload, err := ReadFrame(s.conn)
	if err != nil {
		return fmt.Errorf("read setup response: %w", err)
	}
	if hResp.Tag != TagDeviceSetupResp {
		return fmt.Errorf("unexpected setup response tag 0x%02x", hResp.Tag)
	}
	status, _, err := ParseDeviceSetupResponse(respPayload)
	if err != nil {
		return err
	}
	if status != StatusSetupOK {
		return fmt.Errorf("device setup refused (status 0x%02x)", status)
	}
	hEP, epPayload := BuildEPSetupPacket()
	if err := WriteFrame(s.conn, hEP, epPayload); err != nil {
		return fmt.Errorf("send EP setup: %w", err)
	}
	if !stopCancel() {
		return ctx.Err() // cancelled just as setup finished; the deadline is already poisoned
	}
	_ = s.conn.SetDeadline(time.Time{})

	runCtx, stop := context.WithCancel(context.Background())
	s.stop = stop
	go s.serve(runCtx)
	go s.keepalive(runCtx)
	return nil
}

func (s *deviceSession) close() {
	s.closeOnce.Do(func() {
		if s.stop != nil {
			s.stop()
		}
		// Best-effort detach, then close regardless of the ack.
		hDetach, _ := BuildDetachPacket()
		_ = s.writeFrameWithin(hDetach, nil, detachAckTimeout)
		_ = s.conn.Close()
		close(s.done)
	})
}

func (s *deviceSession) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.close()
}

func (s *deviceSession) writeFrame(h Header, payload []byte) error {
	return s.writeFrameWithin(h, payload, writeTimeout)
}

func (s *deviceSession) writeFrameWithin(h Header, payload []byte, timeout time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(timeout))
	defer func() { _ = s.conn.SetWriteDeadline(time.Time{}) }()
	return WriteFrame(s.conn, h, payload)
}

func (s *deviceSession) keepalive(ctx context.Context) {
	t := time.NewTicker(s.keepaliveEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			// The client is the keepalive initiator: send tag 0x04, the BMC replies 0x03 (ignored).
			h, payload := BuildKeepaliveRequest()
			if err := s.writeFrame(h, payload); err != nil {
				s.fail(err) // a dead peer: close so the serve reader unblocks instead of leaking
				return
			}
		}
	}
}

// serve reads BMC frames and dispatches them. Control frames may interleave with a CBW's OUT data.
func (s *deviceSession) serve(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		h, payload, err := ReadFrame(s.conn)
		if err != nil {
			s.fail(err)
			return
		}
		switch h.Tag {
		case TagEnqueue, TagEnqueueAndGo:
			if h.Endpoint == EndpointBulkOut {
				if err := s.handleCBW(payload); err != nil {
					s.fail(err)
					return
				}
			}
		case TagPingResp:
			// BMC's keepalive reply; nothing to do.
		case TagPingReq:
			// Some firmware may ping; answer to be safe.
			h2, p2 := BuildKeepaliveResponse(payload)
			_ = s.writeFrame(h2, p2)
		case TagDetachResp:
			s.close()
			return
		}
	}
}

// handleCBW parses a CBW and services the command. Reads and writes are streamed a frame at a
// time, so a transfer length the BMC claims never sizes an allocation.
func (s *deviceSession) handleCBW(payload []byte) error {
	cbw, err := ParseCBW(payload)
	if err != nil {
		// No CSW is possible without a tag; log and resync on the next frame.
		log.Printf("[Supermicro] Dropping malformed CBW: %v", err)
		return nil
	}
	cdb := cbw.CDB[:cbw.CDBLength]
	switch {
	case cbw.Flags&0x80 == 0 && cbw.DataLen > 0:
		return s.handleOut(cbw, cdb)
	case isRead(cdb):
		return s.streamRead(cbw, cdb)
	}
	data, ok := s.target.Execute(cdb, cbw.DataLen)
	status := CSWStatusGood
	if !ok {
		status = CSWStatusCommandFail
	}
	if err := s.sendData(data); err != nil {
		return err
	}
	return s.sendCSW(cbw.Tag, cbw.DataLen-uint32(len(data)), status)
}

// streamRead sends a READ's data in maxBulkChunk frames read straight from the image.
func (s *deviceSession) streamRead(cbw CBW, cdb []byte) error {
	off, n, ok := s.target.readRange(cdb, cbw.DataLen)
	status := CSWStatusCommandFail
	var sent int64
	if ok {
		status = CSWStatusGood
		buf := make([]byte, min(n, maxBulkChunk))
		for sent < n {
			chunk := buf[:min(int64(len(buf)), n-sent)]
			if _, err := s.drive.ReadAt(chunk, off+sent); err != nil {
				s.target.sense.set(senseMediumError, ascUnrecoveredRead, 0)
				status = CSWStatusCommandFail
				break
			}
			if err := s.sendData(chunk); err != nil {
				return err
			}
			sent += int64(len(chunk))
		}
	}
	return s.sendCSW(cbw.Tag, cbw.DataLen-uint32(sent), status)
}

// handleOut takes the host's write data frame by frame, writing each straight to the image, or
// draining it when the command is refused, then answers with one CSW.
func (s *deviceSession) handleOut(cbw CBW, cdb []byte) error {
	// A writable drive accepts up to its size, a read-only one only a small control transfer
	// (MODE SELECT etc.). A larger claim is a protocol violation from an untrusted BMC, so drop the
	// session rather than wait for data that will not fit.
	limit := int64(controlOutMax)
	if !s.drive.ReadOnly {
		limit = s.drive.Size()
	}
	if int64(cbw.DataLen) > limit {
		return fmt.Errorf("oversized %d-byte OUT transfer (limit %d)", cbw.DataLen, limit)
	}
	off, ok := s.target.writeRange(cdb, cbw.DataLen)
	status := CSWStatusGood
	if !ok {
		status = CSWStatusCommandFail
	}
	err := s.receiveOut(cbw.DataLen, func(p []byte, at int64) {
		if status != CSWStatusGood {
			return // refused or failed: drain the rest
		}
		if _, err := s.drive.WriteAt(p, off+at); err != nil {
			s.target.sense.set(senseMediumError, ascWriteError, 0)
			status = CSWStatusCommandFail
		}
	})
	if err != nil {
		return err
	}
	return s.sendCSW(cbw.Tag, 0, status)
}

// receiveOut passes dataLen bytes of host write data to sink as the frames arrive, skipping
// keepalives that interleave so reassembly never blocks.
func (s *deviceSession) receiveOut(dataLen uint32, sink func(p []byte, at int64)) error {
	for got := uint32(0); got < dataLen; {
		h, payload, err := ReadFrame(s.conn)
		if err != nil {
			return err
		}
		switch {
		case h.Endpoint == EndpointBulkOut && (h.Tag == TagEnqueue || h.Tag == TagEnqueueAndGo):
			payload = payload[:min(uint32(len(payload)), dataLen-got)]
			sink(payload, int64(got))
			got += uint32(len(payload))
		case h.Tag == TagPingReq || h.Tag == TagPingResp:
		default:
			return fmt.Errorf("unexpected frame (tag 0x%02x) while reading write data", h.Tag)
		}
	}
	return nil
}

// sendData sends bulk-IN data in frames of at most maxBulkChunk bytes.
func (s *deviceSession) sendData(data []byte) error {
	for len(data) > 0 {
		n := len(data)
		if n > maxBulkChunk {
			n = maxBulkChunk
		}
		h := Header{Endpoint: EndpointBulkIn, Tag: TagEnqueue, PayloadLen: uint32(n)}
		if err := s.writeFrame(h, data[:n]); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (s *deviceSession) sendCSW(tag, residue uint32, status byte) error {
	csw := CSW{Tag: tag, Residue: residue, Status: status}
	h := Header{Endpoint: EndpointBulkIn, Tag: TagEnqueueAndGo, PayloadLen: CSWLength}
	return s.writeFrame(h, csw.Encode())
}

func (s *deviceSession) status() vmedia.DriveStatus { return s.drive.Status(s.disk) }

func (s *deviceSession) lastErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
