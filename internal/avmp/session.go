package avmp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	heartbeatInterval  = 10 * time.Second // the vendor client's idle heartbeat
	setupTimeout       = 30 * time.Second // login and drive info (the vendor's LoginTimeout)
	driveWaitTimeout   = 90 * time.Second
	driveRetryInterval = 3 * time.Second
	writeTimeout       = 30 * time.Second // a data packet may queue behind slow USB transfers
	ejectTimeout       = 2 * time.Second  // best effort: the connection closes straight after
	// maxPacket bounds BMC messages; requests are tiny, and writes to our read-only disc small.
	maxPacket = 1 << 20
	// maxChunkBlocks caps data per packet at the vendor's 32 KiB read size.
	maxChunkBlocks = 16
	// maxReadBlocks bounds one read request (64 MiB); the host asks for far less.
	maxReadBlocks = 32768
)

// Status describes a mount for the UI.
type Status struct {
	Mounted     bool  `json:"mounted"`
	BytesServed int64 `json:"bytes_served"`
}

// Session is one AVMP connection that maps a single disc image.
type Session struct {
	conn           net.Conn
	user, password string
	heartbeatEvery time.Duration
	driveWait      time.Duration // how long to wait for a free CD drive
	driveRetry     time.Duration

	writeMu sync.Mutex
	mounted atomic.Bool
	served  atomic.Int64
}

// NewSession wraps an authenticated-transport connection (after the APCP greeting and TLS).
// user and password are the one-time tokens from the KVM control channel.
func NewSession(conn net.Conn, user, password string) *Session {
	return &Session{
		conn: conn, user: user, password: password,
		heartbeatEvery: heartbeatInterval, driveWait: driveWaitTimeout, driveRetry: driveRetryInterval,
	}
}

// Status reports whether the image is attached and how much the host has read.
func (s *Session) Status() Status {
	return Status{Mounted: s.mounted.Load(), BytesServed: s.served.Load()}
}

type message struct {
	typ  uint16
	body []byte // after the 10-byte header
}

type readResult struct {
	msg message
	err error
}

// Mount logs in, attaches image (size bytes) as a read-only CD and serves the host's reads until
// ctx ends (eject) or the BMC releases the drive, both of which return nil. It closes the
// connection before returning.
func (s *Session) Mount(ctx context.Context, image io.ReaderAt, size int64) error {
	defer func() { _ = s.conn.Close() }()
	defer s.mounted.Store(false)
	if size <= 0 {
		return errors.New("the image is empty")
	}
	blocks := uint32((size + BlockSize - 1) / BlockSize)

	loopCtx, stop := context.WithCancel(ctx)
	defer stop()
	msgs := make(chan readResult)
	go s.readLoop(loopCtx, msgs)

	if err := s.write(login(s.user, s.password, false)); err != nil {
		return fmt.Errorf("send login: %w", err)
	}
	m, err := s.await(ctx, msgs, typeLoginStatus)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	if len(m.body) < 3 {
		return errors.New("login: short reply")
	}
	if code := m.body[2]; code != 0 {
		err := fmt.Errorf("virtual media login refused: %s", loginError(code))
		if holder := loginHolder(m.body); holder != "" {
			err = fmt.Errorf("%w; held by %q", err, holder)
		}
		return err
	}

	disk, err := s.waitForCD(ctx, msgs)
	if err != nil {
		return err
	}
	if err := s.write(mapCD(disk, blocks)); err != nil {
		return err
	}
	s.mounted.Store(true)
	log.Printf("[VMedia] Image attached as virtual CD %d (%d blocks)", disk, blocks)

	go s.heartbeats(loopCtx)
	return s.serve(ctx, msgs, disk, image, size)
}

// waitForCD asks for the drive list until a CD drive is free. After a session ends the BMC
// reports its drives as unassigned (type 0) or disabled for a while before freeing them.
func (s *Session) waitForCD(ctx context.Context, msgs <-chan readResult) (uint16, error) {
	deadline := time.Now().Add(s.driveWait)
	last := ""
	for {
		if err := s.write(getVDiskInfo()); err != nil {
			return 0, err
		}
		m, err := s.await(ctx, msgs, typeVDiskInfo)
		if err != nil {
			return 0, fmt.Errorf("drive info: %w", err)
		}
		disk, err := pickCD(m.body)
		if err == nil {
			return disk, nil
		}
		if msg := err.Error(); msg != last {
			last = msg
			log.Printf("[VMedia] Waiting for the BMC to free a CD drive: %v", err)
		}
		if time.Now().After(deadline) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(s.driveRetry):
		}
	}
}

// serve answers the BMC until eject, release or failure.
func (s *Session) serve(ctx context.Context, msgs <-chan readResult, disk uint16, image io.ReaderAt, size int64) error {
	eject := func() { _ = s.writeWithin(releaseDisk(disk), ejectTimeout) }
	for {
		var res readResult
		select {
		case <-ctx.Done():
			eject()
			log.Printf("[VMedia] Image ejected")
			return nil
		case res = <-msgs:
		}
		if res.err != nil {
			return res.err
		}
		m := res.msg
		switch m.typ {
		case typeVDiskRead:
			if err := s.serveRead(disk, m.body, image, size); err != nil {
				return err
			}
		case typeVDiskWrite:
			if err := s.write(clientStatus(disk, statusWriteFailed)); err != nil {
				return err
			}
		case typeRequestRelease:
			if !forDisk(m.body, disk) {
				continue // the vendor only unmaps a drive it has mapped
			}
			eject()
			log.Printf("[VMedia] BMC released the virtual CD")
			return nil
		case typeDeviceStatus:
			if len(m.body) >= 6 && forDisk(m.body, disk) {
				if code := binary.BigEndian.Uint32(m.body[2:6]); deviceFatal(code) {
					eject()
					return fmt.Errorf("virtual CD failed: %s", deviceError(code))
				}
			}
		case typeDisconnect:
			return s.disconnected(disk, true, m.body)
		}
	}
}

// forDisk reports whether a message body starts with our drive's ID.
func forDisk(body []byte, disk uint16) bool {
	return len(body) >= 2 && binary.BigEndian.Uint16(body[0:2]) == disk
}

// disconnected handles a BMC DISCONNECT; reason 4 is another user asking for the drive, which
// we grant rather than hold the media hostage. mapped says whether disk was attached.
func (s *Session) disconnected(disk uint16, mapped bool, body []byte) error {
	if len(body) < 3 {
		return errors.New("BMC ended the virtual media session")
	}
	reason := body[2]
	if reason == 4 {
		name := ""
		if len(body) >= 6 {
			n := int(body[5])
			name = string(body[6:min(len(body), 6+n)])
		}
		_ = s.writeWithin(preemptResponse(true), ejectTimeout)
		if mapped {
			_ = s.writeWithin(releaseDisk(disk), ejectTimeout)
		}
		return fmt.Errorf("virtual media taken over by %q", name)
	}
	return fmt.Errorf("BMC ended the virtual media session: %s", disconnectReason(reason))
}

// serveRead sends the requested blocks in packets of at most the BMC's blocking factor, then one
// CLIENT_STATUS. Blocks past the end of the image read as zeros.
func (s *Session) serveRead(disk uint16, body []byte, image io.ReaderAt, size int64) error {
	if len(body) < 14 {
		return s.write(clientStatus(disk, statusReadFailed))
	}
	start := binary.BigEndian.Uint32(body[2:6])
	n := binary.BigEndian.Uint32(body[6:10])
	factor := binary.BigEndian.Uint32(body[10:14])
	if factor == 0 || factor > maxChunkBlocks {
		factor = maxChunkBlocks
	}
	if n > maxReadBlocks || uint64(start)+uint64(n) > math.MaxUint32 {
		log.Printf("[VMedia] Refusing a %d-block read at block %d", n, start)
		return s.write(clientStatus(disk, statusReadFailed))
	}

	status := statusOK
	buf := make([]byte, int(factor)*BlockSize)
	for done := uint32(0); done < n; {
		count := min(factor, n-done)
		chunk := buf[:int(count)*BlockSize]
		clear(chunk)
		off := (int64(start) + int64(done)) * BlockSize
		if off < size {
			if _, err := image.ReadAt(chunk[:min(int64(len(chunk)), size-off)], off); err != nil && !errors.Is(err, io.EOF) {
				log.Printf("[VMedia] Image read failed: %v", err)
				status = statusReadFailed
				break
			}
		}
		if err := s.write(readData(disk, start+done, count, chunk)); err != nil {
			return err
		}
		s.served.Add(int64(len(chunk)))
		done += count
	}
	return s.write(clientStatus(disk, status))
}

func (s *Session) heartbeats(ctx context.Context) {
	t := time.NewTicker(s.heartbeatEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.write(heartbeat()); err != nil {
				return // the serve loop sees the broken connection
			}
		}
	}
}

func (s *Session) write(b []byte) error { return s.writeWithin(b, writeTimeout) }

func (s *Session) writeWithin(b []byte, timeout time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := s.conn.Write(b)
	return err
}

// readLoop is the only reader. It stops when ctx ends; Mount's deferred Close unblocks it.
func (s *Session) readLoop(ctx context.Context, out chan<- readResult) {
	for {
		m, err := readMessage(s.conn)
		select {
		case out <- readResult{m, err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func readMessage(r io.Reader) (message, error) {
	hdr := make([]byte, headerLen)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return message{}, err
	}
	if string(hdr[:4]) != "AVMP" {
		return message{}, fmt.Errorf("not an AVMP packet (% x)", hdr[:4])
	}
	n := binary.BigEndian.Uint32(hdr[4:8])
	if n < headerLen || n > maxPacket {
		return message{}, fmt.Errorf("AVMP packet length %d is not plausible", n)
	}
	body := make([]byte, n-headerLen)
	if _, err := io.ReadFull(r, body); err != nil {
		return message{}, err
	}
	return message{typ: binary.BigEndian.Uint16(hdr[8:10]), body: body}, nil
}

// await waits for a message of type want during setup, ignoring others.
func (s *Session) await(ctx context.Context, msgs <-chan readResult, want uint16) (message, error) {
	timeout := time.After(setupTimeout)
	for {
		select {
		case <-ctx.Done():
			return message{}, ctx.Err()
		case <-timeout:
			return message{}, errors.New("BMC did not answer")
		case res := <-msgs:
			if res.err != nil {
				return message{}, res.err
			}
			if res.msg.typ == want {
				return res.msg, nil
			}
			if res.msg.typ == typeDisconnect {
				return message{}, s.disconnected(0, false, res.msg.body)
			}
		}
	}
}

// pickCD returns the first idle CD drive from a VDISK_INFO body, as the vendor client does.
func pickCD(body []byte) (uint16, error) {
	if len(body) < 4 {
		return 0, errors.New("short drive info")
	}
	n := int(binary.BigEndian.Uint16(body[2:4]))
	var seen []string
	for i := 0; i < n && 4+4*i+2 <= len(body); i++ {
		typ, status := body[4+4*i], body[5+4*i]
		if typ == driveTypeCD && status == driveIdle {
			return uint16(i), nil
		}
		seen = append(seen, fmt.Sprintf("type %d status %d", typ, status))
	}
	return 0, fmt.Errorf("no free virtual CD drive on the BMC (drives: %s); check that virtual media is attached "+
		"(not detached) in the BMC settings and that no other virtual media session is open", strings.Join(seen, ", "))
}

func loginError(code byte) string {
	msgs := map[byte]string{
		1: "bad user", 2: "bad password", 3: "access denied", 4: "drive in use", 5: "channel not found",
		6: "in use by another user", 8: "all channels in use", 11: "in use by another user",
		22: "authentication server failure", 23: "authentication server certificate invalid", 52: "preemption refused",
	}
	if m, ok := msgs[code]; ok {
		return fmt.Sprintf("%s (%d)", m, code)
	}
	return fmt.Sprintf("status %d", code)
}

// loginHolder is the user named in a login refusal: u8 length then up to 96 bytes at body[4].
func loginHolder(body []byte) string {
	if len(body) < 5 {
		return ""
	}
	n := min(int(body[4]), len(body)-5)
	return strings.TrimRight(string(body[5:5+n]), "\x00")
}

func deviceFatal(code uint32) bool {
	switch code {
	case 2, 4, 5, 6, 8, 0xFFFFFFFF:
		return true
	}
	return false
}

func deviceError(code uint32) string {
	msgs := map[uint32]string{
		1: "command unsupported", 2: "read failed", 3: "write failed", 4: "USB not connected",
		5: "drive already attached", 6: "drive disabled", 7: "unknown option", 8: "VDISK request failed",
	}
	if m, ok := msgs[code]; ok {
		return m
	}
	return fmt.Sprintf("status %d", int32(code))
}

func disconnectReason(r byte) string {
	msgs := map[byte]string{
		0: "disconnected by an administrator", 1: "idle timeout", 2: "BMC rebooting",
		3: "firmware upgrade", 6: "KVM session closed",
	}
	if m, ok := msgs[r]; ok {
		return m
	}
	return fmt.Sprintf("reason %d", r)
}
