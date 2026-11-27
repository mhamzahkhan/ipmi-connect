package supermicro

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

const (
	dialTimeout       = 5 * time.Second
	handshakeTimeout  = 15 * time.Second
	reconnectMin      = 2 * time.Second
	reconnectMax      = 30 * time.Second
	stableSession     = 30 * time.Second // a session this long resets the backoff
	logoutWaitTimeout = 5 * time.Second

	// After a session that had keyboard and mouse control is lost (say the BMC dropped off the
	// network while the host rebooted on a shared NIC), the X7SB3 keeps it as the owner for a
	// while and makes the reconnect view only. Reconnect this often, for this long, to get
	// control back; after that a view-only session is accepted (it may be a real user).
	reclaimDelay  = 3 * time.Second
	reclaimWindow = 2 * time.Minute
)

// statusReclaiming is shown while waiting for the BMC to drop a lost session's control.
const statusReclaiming = "Waiting for the BMC to release keyboard and mouse from the previous connection…"

// errReclaim ends a view-only connection made while reclaiming control.
var errReclaim = errors.New("view only: the lost session still holds keyboard and mouse")

// inBandPower maps power actions to the KVM socket's 0x1a codes. Power cycle has none; it goes
// through the web CGI.
var inBandPower = map[bmc.PowerAction]byte{
	bmc.PowerOff: powerOff, bmc.PowerOn: powerOn, bmc.PowerReset: powerReset, bmc.PowerSoftOff: powerSoftOff,
}

// Client keeps a Supermicro KVM session up, reconnecting with a fresh one-time ticket as needed,
// and implements bmc.Console on whichever session is current.
type Client struct {
	host string
	web  *WebSession
	dial func(ctx context.Context, addr string) (net.Conn, error)

	// Retry timing; fields so tests can shorten them.
	retryMin, reclaimDelay, reclaimWindow time.Duration

	mu      sync.Mutex
	session *Session
	running bool
	stopped chan struct{} // closed when Run returns

	// Manager provides virtual media (bmc.VirtualMedia) over ATEN Virtual Storage sessions.
	*vmedia.Manager
}

// NewClient prepares a client; call Login, then Run.
func NewClient(host, user, password string) *Client {
	c := &Client{
		host:     host,
		web:      NewWebSession(host, user, password),
		stopped:  make(chan struct{}),
		retryMin: reconnectMin, reclaimDelay: reclaimDelay, reclaimWindow: reclaimWindow,
		dial: func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{Timeout: dialTimeout}
			return d.DialContext(ctx, "tcp", addr)
		},
	}
	c.Manager = vmedia.NewManager(c.openVMedia)
	return c
}

// Login authenticates to the web interface; call it before Run so bad credentials fail fast.
func (c *Client) Login() error { return c.web.Login() }

func nextDelay(d time.Duration) time.Duration { return min(2*d, reconnectMax) }

// Start runs the client in the background. It marks the client running before returning, so a
// Logout straight after still waits for the KVM socket to close.
func (c *Client) Start(ctx context.Context, sink Sink) {
	c.markRunning()
	go c.Run(ctx, sink)
}

func (c *Client) markRunning() {
	c.mu.Lock()
	c.running = true
	c.mu.Unlock()
}

// Run keeps a KVM session up until ctx ends. Call it once.
func (c *Client) Run(ctx context.Context, sink Sink) {
	c.markRunning()
	defer close(c.stopped)

	delay := c.retryMin
	var reclaimUntil time.Time // while in the future, a view-only connection is retried
	for {
		started := time.Now()
		hadControl, err := c.serveOnce(ctx, sink, time.Now().Before(reclaimUntil))
		if ctx.Err() != nil {
			return
		}
		var wait time.Duration
		if errors.Is(err, errReclaim) {
			log.Printf("[Supermicro] %v; retrying in %v", err, c.reclaimDelay)
			sink.SetStatus(statusReclaiming)
			wait = c.reclaimDelay
		} else {
			if hadControl {
				reclaimUntil = time.Now().Add(c.reclaimWindow)
			}
			if time.Since(started) > stableSession {
				delay = c.retryMin
			}
			log.Printf("[Supermicro] KVM session ended (%v); reconnecting in %v", err, delay)
			sink.SetStatus("Reconnecting to the KVM…")
			wait, delay = delay, nextDelay(delay)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// serveOnce fetches a ticket, connects, authenticates and serves one session. hadControl
// reports whether the session still had keyboard and mouse control when it ended. With
// reclaim, a view-only connection is closed at once with errReclaim.
func (c *Client) serveOnce(ctx context.Context, sink Sink, reclaim bool) (hadControl bool, err error) {
	ticket, err := c.web.FetchTicket(c.host)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err() // shutting down: never open a new web session
		}
		// The web session may have expired: log in again once.
		if lerr := c.web.Login(); lerr != nil {
			return false, fmt.Errorf("re-login: %w", lerr)
		}
		if ticket, err = c.web.FetchTicket(c.host); err != nil {
			return false, err
		}
	}
	if ticket.TLS {
		return false, errors.New(`the BMC has "KVM SSL" enabled, which is not supported; disable it in the BMC web UI`)
	}

	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	conn, err := c.dial(ctx, net.JoinHostPort(c.host, strconv.Itoa(ticket.Port)))
	if err != nil {
		return false, fmt.Errorf("KVM port %d: %w", ticket.Port, err)
	}
	_ = conn.SetDeadline(time.Now().Add(handshakeTimeout))
	r := bufio.NewReader(conn)
	info, err := handshake(r, conn, ticket)
	if err != nil {
		_ = conn.Close()
		return false, fmt.Errorf("KVM handshake: %w", err)
	}
	if reclaim && !info.KeyboardMouse {
		_ = conn.Close()
		return false, errReclaim
	}
	_ = conn.SetDeadline(time.Time{})
	log.Printf("[Supermicro] KVM connected to %q (session %d, video %v, keyboard/mouse %v)",
		info.Name, info.SessionID, info.Video, info.KeyboardMouse)
	sink.SetStatus("")

	s := newSession(conn, r, info)
	c.mu.Lock()
	c.session = s
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.session = nil
		c.mu.Unlock()
		hadControl = !s.viewOnly.Load()
	}()
	return false, s.Run(ctx, sink)
}

// Logout ends the web session once the KVM socket has closed (a web logout would kick it).
func (c *Client) Logout() {
	if c.Manager != nil {
		c.Close()
	}
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running {
		select {
		case <-c.stopped:
		case <-time.After(logoutWaitTimeout):
			log.Printf("[Supermicro] KVM session did not close in time; logging out anyway")
		}
	}
	c.web.Logout()
}

func (c *Client) current() (*Session, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.session == nil {
		return nil, errors.New("supermicro KVM not connected")
	}
	return c.session, nil
}

// SendKeyEvent implements bmc.Console.
func (c *Client) SendKeyEvent(down bool, usage uint16) error {
	s, err := c.current()
	if err != nil {
		return err
	}
	return s.SendKeyEvent(down, usage)
}

// SendPointerEvent implements bmc.Console.
func (c *Client) SendPointerEvent(buttons byte, x, y uint16) error {
	s, err := c.current()
	if err != nil {
		return err
	}
	return s.SendPointerEvent(buttons, x, y)
}

// RequestFrameRefresh implements bmc.Console.
func (c *Client) RequestFrameRefresh() error {
	s, err := c.current()
	if err != nil {
		return err
	}
	return s.RequestFrameRefresh()
}

// PowerControl implements bmc.Console: in-band when a KVM session is up, otherwise via the web CGI.
func (c *Client) PowerControl(action bmc.PowerAction) error {
	if code, ok := inBandPower[action]; ok {
		if s, err := c.current(); err == nil {
			log.Printf("[Supermicro] Power %s sent over the KVM connection", action)
			return s.power(code)
		}
	}
	return c.web.PowerCGI(action)
}

// PowerStatus reports "ON" or "OFF" from the web interface.
func (c *Client) PowerStatus() (string, error) { return c.web.PowerStatus() }

// PowerState implements bmc.PowerReader.
func (c *Client) PowerState() (string, error) { return c.web.PowerStatus() }
