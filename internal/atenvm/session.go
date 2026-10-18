package atenvm

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
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
)

// deviceSession is one TCP connection carrying one emulated USB device (one drive). The ATEN
// firmware allows exactly one device per connection, so a Group (below) opens one of these per
// mapped drive.
type deviceSession struct {
	conn           net.Conn
	target         SCSITarget
	disk           uint16
	tgtType        vmedia.Target
	readOnly       bool
	keepaliveEvery time.Duration

	writeMu      sync.Mutex
	bytesRead    atomic.Int64
	bytesWritten atomic.Int64

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

func newDeviceSession(conn net.Conn, target SCSITarget, disk uint16, tgtType vmedia.Target, readOnly bool, keepalive time.Duration) *deviceSession {
	if keepalive <= 0 {
		keepalive = defaultKeepalive
	}
	return &deviceSession{
		conn: conn, target: target, disk: disk, tgtType: tgtType, readOnly: readOnly,
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

// handleCBW parses a CBW and services the command. For an OUT command it first gathers the host's
// write data, which may span several frames with control frames interleaved.
func (s *deviceSession) handleCBW(payload []byte) error {
	cbw, err := ParseCBW(payload)
	if err != nil {
		// No CSW is possible without a tag; log and resync on the next frame.
		log.Printf("[Supermicro] Dropping malformed CBW: %v", err)
		return nil
	}
	cdb := cbw.CDB[:cbw.CDBLength]
	out := cbw.Flags&0x80 == 0 && cbw.DataLen > 0

	if out {
		data, err := s.readOutData(cbw.DataLen)
		if err != nil {
			return err
		}
		status := s.target.WriteData(cdb, data)
		if status == CSWStatusGood {
			s.bytesWritten.Add(int64(len(data)))
		}
		return s.sendCSW(cbw.Tag, cbw.DataLen, 0, status)
	}

	data, ok := s.target.Execute(cdb, cbw.DataLen)
	status := CSWStatusGood
	if !ok {
		status = CSWStatusCommandFail
	}
	if len(data) > 0 {
		if err := s.sendData(data); err != nil {
			return err
		}
		s.bytesRead.Add(int64(len(data)))
	}
	residue := cbw.DataLen - uint32(len(data))
	return s.sendCSW(cbw.Tag, cbw.DataLen, residue, status)
}

// readOutData collects dataLen bytes of host write data, answering control frames that interleave.
func (s *deviceSession) readOutData(dataLen uint32) ([]byte, error) {
	buf := make([]byte, 0, dataLen)
	for uint32(len(buf)) < dataLen {
		h, payload, err := ReadFrame(s.conn)
		if err != nil {
			return nil, err
		}
		switch {
		case h.Endpoint == EndpointBulkOut && (h.Tag == TagEnqueue || h.Tag == TagEnqueueAndGo):
			need := dataLen - uint32(len(buf))
			if uint32(len(payload)) > need {
				payload = payload[:need]
			}
			buf = append(buf, payload...)
		case h.Tag == TagPingReq || h.Tag == TagPingResp:
			// A keepalive may interleave; skip it without writing, so reassembly never blocks.
		default:
			return nil, fmt.Errorf("unexpected frame (tag 0x%02x) while reading write data", h.Tag)
		}
	}
	return buf, nil
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

func (s *deviceSession) sendCSW(tag, dataLen, residue uint32, status byte) error {
	csw := CSW{Tag: tag, Residue: residue, Status: status}
	h := Header{Endpoint: EndpointBulkIn, Tag: TagEnqueueAndGo, PayloadLen: CSWLength}
	return s.writeFrame(h, csw.Encode())
}

func (s *deviceSession) status() vmedia.DriveStatus {
	blockSize := uint32(SectorSizeCD)
	if s.tgtType != vmedia.TargetCD {
		blockSize = SectorSizeDisk
	}
	return vmedia.DriveStatus{
		Disk: s.disk, Target: s.tgtType, ReadOnly: s.readOnly, BlockSize: blockSize,
		BytesRead: s.bytesRead.Load(), BytesWritten: s.bytesWritten.Load(),
	}
}

func (s *deviceSession) lastErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
