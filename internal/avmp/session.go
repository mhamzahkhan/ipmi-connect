package avmp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

const (
	heartbeatInterval  = 10 * time.Second // the vendor client's idle heartbeat
	setupTimeout       = 30 * time.Second // login and drive info (the vendor's LoginTimeout)
	driveWaitTimeout   = 90 * time.Second // for the BMC to free a drive after a previous session
	driveRetryInterval = 3 * time.Second
	writeTimeout       = 30 * time.Second // a data packet may queue behind slow USB transfers
	ejectTimeout       = 2 * time.Second  // best effort: the connection usually closes next
	// maxPacket bounds BMC messages. Writes carry data, so allow a generous 1 MiB.
	maxPacket = 1 << 20
)

// Session is one AVMP connection. It carries every mapped drive: the BMC routes reads and
// writes by drive ID, and one goroutine serves them in order, as the vendor client does.
type Session struct {
	conn           net.Conn
	user, password string
	heartbeatEvery time.Duration
	driveWait      time.Duration
	driveRetry     time.Duration

	writeMu sync.Mutex // one packet at a time on the wire
	mapMu   sync.Mutex // one Map at a time: they share the drive-info replies

	mu        sync.Mutex
	drives    map[uint16]*vmedia.Drive
	readOnly  bool   // the BMC's READ_ONLY option (0x8430) applies to later maps
	lastEvent string // the latest drive-level failure, for the UI

	info chan []byte // latest VDISK_INFO body, for Map

	stop      context.CancelFunc
	done      chan struct{}
	err       error // why the session ended; set before done closes
	closeOnce sync.Once
}

var _ vmedia.Session = (*Session)(nil)

// NewSession wraps a connection that has completed the APCP greeting (and TLS). user and
// password are the one-time tokens from the KVM control channel.
func NewSession(conn net.Conn, user, password string) *Session {
	return &Session{
		conn: conn, user: user, password: password,
		heartbeatEvery: heartbeatInterval, driveWait: driveWaitTimeout, driveRetry: driveRetryInterval,
		drives: map[uint16]*vmedia.Drive{},
		info:   make(chan []byte, 1),
		done:   make(chan struct{}),
	}
}

// Open logs in and starts serving the BMC. Call Close when finished.
func (s *Session) Open(ctx context.Context) error {
	if err := s.login(ctx); err != nil {
		_ = s.conn.Close()
		return err
	}
	runCtx, stop := context.WithCancel(context.Background())
	s.stop = stop
	go s.heartbeats(runCtx)
	go s.run()
	return nil
}

func (s *Session) login(ctx context.Context) error {
	if err := s.write(login(s.user, s.password, false)); err != nil {
		return fmt.Errorf("send login: %w", err)
	}
	deadline := time.Now().Add(setupTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = s.conn.SetReadDeadline(deadline)
	defer func() { _ = s.conn.SetReadDeadline(time.Time{}) }()
	for {
		m, err := readMessage(s.conn)
		if err != nil {
			return fmt.Errorf("login: %w", err)
		}
		switch m.typ {
		case typeDisconnect:
			return s.disconnected(m.body)
		case typeDeviceConfigOption: // the BMC's policy may arrive before the login reply
			_ = s.dispatch(m)
		case typeLoginStatus:
			if len(m.body) < 3 {
				return errors.New("login: short reply")
			}
			code := m.body[2]
			if code == 0 {
				return nil
			}
			err := fmt.Errorf("virtual media login refused: %s", loginError(code))
			if holder := loginHolder(m.body); holder != "" {
				err = fmt.Errorf("%w; held by %q", err, holder)
			}
			return err
		}
	}
}

// Done is closed when the session has ended; Err says why.
func (s *Session) Done() <-chan struct{} { return s.done }

// Err is why the session ended: nil after Close.
func (s *Session) Err() error {
	select {
	case <-s.done:
		return s.err
	default:
		return nil
	}
}

// Close releases every mapped drive and ends the session.
func (s *Session) Close() error {
	s.end(nil, true)
	<-s.done
	return nil
}

// end stops the session once: optionally releasing mapped drives first, then closing the
// connection, which also stops the run loop.
func (s *Session) end(err error, release bool) {
	s.closeOnce.Do(func() {
		if release {
			for _, d := range s.mappedDrives() {
				_ = s.writeWithin(releaseDisk(d), ejectTimeout)
			}
		}
		s.mu.Lock()
		s.drives = map[uint16]*vmedia.Drive{}
		s.mu.Unlock()
		if s.stop != nil {
			s.stop()
		}
		s.err = err
		_ = s.conn.Close()
		close(s.done)
	})
}

// run reads and serves BMC messages until the connection ends.
func (s *Session) run() {
	for {
		m, err := readMessage(s.conn)
		if err != nil {
			select {
			case <-s.done: // Close
			default:
				s.end(fmt.Errorf("virtual media connection lost: %w", err), false)
			}
			return
		}
		if err := s.dispatch(m); err != nil {
			s.end(err, false)
			return
		}
	}
}

func (s *Session) dispatch(m message) error {
	switch m.typ {
	case typeVDiskInfo:
		select { // keep only the latest
		case <-s.info:
		default:
		}
		s.info <- m.body
	case typeVDiskRead:
		return s.serveRead(m.body)
	case typeVDiskWrite:
		return s.serveWrite(m.body)
	case typeRequestRelease:
		if len(m.body) >= 2 {
			s.dropDrive(binary.BigEndian.Uint16(m.body[0:2]), "released by the BMC")
		}
	case typeDeviceStatus:
		if len(m.body) >= 6 {
			if code := binary.BigEndian.Uint32(m.body[2:6]); deviceFatal(code) {
				s.dropDrive(binary.BigEndian.Uint16(m.body[0:2]), deviceError(code))
			}
		}
	case typeDeviceConfigOption:
		if len(m.body) >= 11 && binary.BigEndian.Uint32(m.body[2:6]) == 1 {
			s.mu.Lock()
			s.readOnly = m.body[10] != 0
			s.mu.Unlock()
			log.Printf("[VMedia] BMC set read-only mode: %v", m.body[10] != 0)
		}
	case typeDisconnect:
		return s.disconnected(m.body)
	}
	return nil
}

// disconnected handles a BMC DISCONNECT. Reason 4 is another user asking for virtual media,
// which we grant (releasing everything) rather than hold the media hostage.
func (s *Session) disconnected(body []byte) error {
	if len(body) < 3 {
		return errors.New("BMC ended the virtual media session")
	}
	reason := body[2]
	if reason == 4 {
		name := ""
		if len(body) >= 6 {
			name = string(body[6:min(len(body), 6+int(body[5]))])
		}
		_ = s.writeWithin(preemptResponse(true), ejectTimeout)
		for _, d := range s.mappedDrives() {
			_ = s.writeWithin(releaseDisk(d), ejectTimeout)
		}
		return fmt.Errorf("virtual media taken over by %q", name)
	}
	return fmt.Errorf("BMC ended the virtual media session: %s", disconnectReason(reason))
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
				return // the run loop sees the broken connection
			}
		}
	}
}

// USBReset reconnects the BMC's virtual mass-storage device on the host; mapped drives stay
// mapped. The BMC does not reply.
func (s *Session) USBReset() error { return s.write(usbReset()) }

func (s *Session) write(b []byte) error { return s.writeWithin(b, writeTimeout) }

func (s *Session) writeWithin(b []byte, timeout time.Duration) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(timeout))
	_, err := s.conn.Write(b)
	return err
}

type message struct {
	typ  uint16
	body []byte // after the 10-byte header
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
