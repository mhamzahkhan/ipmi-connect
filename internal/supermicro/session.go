package supermicro

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"
)

// Status texts shown to the user.
const (
	statusNoSignal    = "No video signal from the host"
	statusViewOnly    = "View only: another user has keyboard and mouse control"
	statusOtherVideo  = "Another session holds the video"
	statusUnsupported = "Unsupported video encoding"
)

// Screen-off marker: the BMC reports a -640x-480 rect while the host has no video signal.
const (
	screenOffW = 0xFD80
	screenOffH = 0xFE20
)

const (
	keepAliveInterval = 3 * time.Second  // the vendor client's keep-alive period
	idlePoke          = 5 * time.Second  // ask for a full frame after this much silence
	idleDead          = 15 * time.Second // reconnect after this much silence
	noSignalPoll      = time.Second
	writeTimeout      = 5 * time.Second
	sendQueueLen      = 64
	inputTimeout      = time.Second
	mouseInfoLen      = 3 // on this firmware; 2 on later X9 firmware
	initW, initH      = 0x27f, 0x1df
	// maxScreenSide rejects implausible resolutions before allocating a picture for them.
	maxScreenSide = 4096
)

// Sink receives a session's output.
type Sink interface {
	UpdateScreen(width, height int, pix []byte)
	SetStatus(text string)
}

// Session is one authenticated ATEN iKVM connection.
type Session struct {
	conn net.Conn
	r    *bufio.Reader
	info ServerInfo

	out            chan []byte
	done           chan struct{}
	encryptMouse   atomic.Bool
	viewOnly       atomic.Bool  // status only: input is still sent and the BMC decides
	powerOn        atomic.Int32 // -1 unknown, 0 off, 1 on
	keepAliveEvery time.Duration

	frame    *frame
	noSignal bool
	status   string
}

func newSession(conn net.Conn, r *bufio.Reader, info ServerInfo) *Session {
	s := &Session{
		conn: conn, r: r, info: info,
		out: make(chan []byte, sendQueueLen), done: make(chan struct{}),
		keepAliveEvery: keepAliveInterval,
	}
	s.viewOnly.Store(!info.KeyboardMouse)
	s.powerOn.Store(-1)
	return s
}

type readResult struct {
	msg any
	err error
}

// Run serves the session until ctx ends or the connection fails. It owns the connection: one
// reader goroutine (no read deadlines, which could split a message) and one writer goroutine.
func (s *Session) Run(ctx context.Context, sink Sink) error {
	defer close(s.done)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() { _ = s.conn.Close() }()

	writeErr := make(chan error, 1)
	go func() { writeErr <- s.writeLoop(ctx) }()

	msgs := make(chan readResult)
	go func() {
		for {
			m, err := readMessage(s.r, mouseInfoLen)
			select {
			case msgs <- readResult{m, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	if !s.info.Video {
		s.setStatus(sink, statusOtherVideo)
	} else if s.viewOnly.Load() {
		s.setStatus(sink, statusViewOnly)
	}
	s.enqueue(mouseInfoRequest())
	s.enqueue(framebufferUpdateRequest(false, initW, initH))

	keepAliveTick := time.NewTicker(s.keepAliveEvery)
	defer keepAliveTick.Stop()
	poll := time.NewTicker(noSignalPoll)
	defer poll.Stop()
	lastMsg, poked := time.Now(), false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-writeErr:
			return fmt.Errorf("write: %w", err)
		case <-keepAliveTick.C:
			s.enqueue(keepAlive())
		case <-poll.C:
			idle := time.Since(lastMsg)
			switch {
			case idle > idleDead:
				return errors.New("BMC silent for 15s")
			case s.noSignal:
				s.enqueue(framebufferUpdateRequest(false, initW, initH))
			case idle > idlePoke && !poked:
				poked = true
				s.requestFull()
			}
		case res := <-msgs:
			if res.err != nil {
				return res.err
			}
			lastMsg, poked = time.Now(), false
			s.handle(res.msg, sink)
		}
	}
}

func (s *Session) writeLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case b := <-s.out:
			_ = s.conn.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, err := s.conn.Write(b); err != nil {
				return err
			}
		}
	}
}

// enqueue queues a message for the writer; internal messages never block the session loop.
func (s *Session) enqueue(b []byte) {
	select {
	case s.out <- b:
	default:
		log.Printf("[Supermicro] Send queue full, dropping a %#x message", b[0])
	}
}

func (s *Session) setStatus(sink Sink, text string) {
	if text != s.status {
		s.status = text
		sink.SetStatus(text)
		if text != "" {
			log.Printf("[Supermicro] %s", text)
		}
	}
}

func (s *Session) requestFull() {
	w, h := initW, initH
	if s.frame != nil {
		w, h = s.frame.w, s.frame.h
	}
	s.enqueue(framebufferUpdateRequest(false, uint16(w), uint16(h)))
}

func (s *Session) handle(msg any, sink Sink) {
	switch m := msg.(type) {
	case FramebufferUpdate:
		s.handleUpdate(m, sink)
	case MouseInfo:
		s.encryptMouse.Store(m.Encrypted)
		if m.HasPower {
			s.powerOn.Store(map[bool]int32{false: 0, true: 1}[m.PowerOn])
		}
	case Privilege:
		log.Printf("[Supermicro] Session notice: %q", m.Text)
		if m.A == 1 && m.B == 4 {
			s.viewOnly.Store(true)
			s.setStatus(sink, statusViewOnly)
		}
	}
}

func (s *Session) handleUpdate(u FramebufferUpdate, sink Sink) {
	changed, resized := false, false
	for _, r := range u.Rects {
		if r.W == screenOffW && r.H == screenOffH {
			// No reply: the BMC answers every request instantly while the screen is off. The
			// once-a-second poll checks for the signal coming back.
			s.noSignal = true
			s.setStatus(sink, statusNoSignal)
			return
		}
		if r.W == 1 && r.H == 1 {
			s.setStatus(sink, statusOtherVideo)
			continue
		}
		if r.W == 0 || r.H == 0 {
			continue
		}
		if r.W > maxScreenSide || r.H > maxScreenSide {
			log.Printf("[Supermicro] Ignoring an update for an implausible %dx%d screen", r.W, r.H)
			continue
		}
		s.noSignal = false
		if s.frame == nil || s.frame.w != int(r.W) || s.frame.h != int(r.H) {
			s.frame = newFrame(int(r.W), int(r.H))
			resized = true
			log.Printf("[Supermicro] Host video mode is %dx%d", r.W, r.H)
		}
		switch r.Encoding {
		case 0x59, 0x00: // Hermon (older firmware reports 0)
			if err := decodeHermon(r.Data, s.frame); err != nil {
				log.Printf("[Supermicro] Dropping undecodable update: %v", err)
				continue
			}
			changed = true
			s.setStatus(sink, s.baseStatus())
		default:
			s.setStatus(sink, fmt.Sprintf("%s (%#x)", statusUnsupported, r.Encoding))
		}
	}
	if changed {
		sink.UpdateScreen(s.frame.w, s.frame.h, s.frame.pix)
	}
	if s.frame != nil {
		// After a mode change the new picture starts black: ask for all of it, not just changes.
		s.enqueue(framebufferUpdateRequest(!resized, uint16(s.frame.w), uint16(s.frame.h)))
	}
}

// baseStatus is the status while video is flowing normally.
func (s *Session) baseStatus() string {
	if s.viewOnly.Load() {
		return statusViewOnly
	}
	return ""
}

// send queues user input, waiting briefly rather than dropping it: a lost key-up would leave a
// key held on the host.
func (s *Session) send(b []byte) error {
	// Checked first: with both ready, select would pick at random and could report success
	// for a message no writer will ever send.
	select {
	case <-s.done:
		return errors.New("supermicro KVM session closed")
	default:
	}
	select {
	case s.out <- b:
		return nil
	case <-s.done:
		return errors.New("supermicro KVM session closed")
	case <-time.After(inputTimeout):
		return errors.New("supermicro KVM send queue stalled")
	}
}

// SendKeyEvent sends a USB HID key event.
func (s *Session) SendKeyEvent(down bool, usage uint16) error {
	return s.send(keyEvent(down, uint32(usage)))
}

// SendPointerEvent sends an absolute pointer event (buttons: left 1, middle 2, right 4).
func (s *Session) SendPointerEvent(buttons byte, x, y uint16) error {
	if s.encryptMouse.Load() {
		return s.send(encryptedPointerEvent(buttons, x, y))
	}
	return s.send(pointerEvent(buttons, x, y))
}

// RequestFrameRefresh asks for a complete frame.
func (s *Session) RequestFrameRefresh() error {
	return s.send(framebufferUpdateRequest(false, initW, initH))
}

// power sends an in-band power command and asks for the updated status.
func (s *Session) power(code byte) error {
	if err := s.send(powerCommand(code)); err != nil {
		return err
	}
	return s.send(mouseInfoRequest())
}
