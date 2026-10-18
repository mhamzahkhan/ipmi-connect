package web

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"ipmi-connect/internal/bmc"
)

func init() {
	_ = mime.AddExtensionType(".js", "application/javascript")
}

//go:embed static/*
var staticFS embed.FS

// Server hosts the local web UI, REST API, and WebSocket streaming bridge.
type Server struct {
	State   bmc.SessionState
	StateMu sync.RWMutex
	Console bmc.Console // set once before Run; nil until the BMC is connected
	// After a power action the power state is re-read this often, for this long.
	powerFollowEvery time.Duration
	powerFollowFor   time.Duration
	typeMu           sync.Mutex  // one paste typed at a time
	typing           atomic.Bool // a paste is being typed: live input from every browser is held off
	followMu         sync.Mutex  // guards followUntil and following
	followUntil      time.Time
	following        bool
	typeDelay        time.Duration // between typed key events
	clients          map[*wsClient]struct{}
	clientsMu        sync.Mutex // guards clients
	Port             int        // guarded by sessionMu once Run starts
	HTTPServer       *http.Server
	token            string     // per-run secret required by the API and WebSocket
	launchCode       string     // one-time code in the launch link, exchanged for token
	sessionMu        sync.Mutex // guards Port and launchCodeUsed
	launchCodeUsed   bool

	// Sockets are hijacked, so HTTPServer.Shutdown does not stop them: Run cancels connCtx and
	// waits on connWG so each socket releases its held input before main logs out.
	connCtx    context.Context
	connCancel context.CancelFunc
	connWG     sync.WaitGroup

	// The host's current picture (RGBA), kept so new browsers, slow browsers and screenshots
	// get a complete frame without asking the BMC.
	screenMu         sync.Mutex
	screen           []byte
	screenW, screenH int
}

// NewServer initializes a new Web UI server.
func NewServer(port int, state bmc.SessionState) *Server {
	connCtx, connCancel := context.WithCancel(context.Background())
	return &Server{
		connCtx:    connCtx,
		connCancel: connCancel,
		token:      newToken(),
		launchCode: newToken(),
		State:      state,
		Port:       port,
		clients:    make(map[*wsClient]struct{}),

		powerFollowEvery: powerFollowInterval,
		powerFollowFor:   powerFollowDuration,
		typeDelay:        typeKeyDelay,
	}
}

// routes wires the embedded UI, REST API, and WebSocket endpoint.
func (s *Server) routes() (http.Handler, error) {
	subFS, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(subFS)))
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/power", s.handlePower)
	s.mediaRoutes(mux)
	mux.HandleFunc("/api/screenshot.png", s.handleScreenshot)
	mux.HandleFunc("/ws", s.handleWS)
	return s.guard(mux), nil
}

// Run serves the console until ctx is cancelled, then shuts the HTTP server down.
func (s *Server) Run(ctx context.Context, openBrowser bool) error {
	mux, err := s.routes()
	if err != nil {
		return err
	}

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.Port))
	if err != nil {
		// Try dynamic port
		listener, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
	}
	s.sessionMu.Lock()
	s.Port = listener.Addr().(*net.TCPAddr).Port
	s.sessionMu.Unlock()

	s.HTTPServer = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
	}

	// The link's one-time code signs in the first browser that opens it.
	url := s.LaunchURL()
	log.Printf("==================================================================")
	log.Printf("🚀 IPMI Modern Console active at: %s", url)
	log.Printf("==================================================================")

	if openBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			s.launchBrowser(url)
		}()
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- s.HTTPServer.Serve(listener) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	err = s.HTTPServer.Shutdown(shutdownCtx)

	s.connCancel()
	sockets := make(chan struct{})
	go func() { s.connWG.Wait(); close(sockets) }()
	select {
	case <-sockets:
	case <-shutdownCtx.Done():
		log.Printf("[Server] Timed out waiting for browser connections to close")
	}

	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// boundPort returns the listening port once Run has bound it (0 before).
func (s *Server) boundPort() int {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	return s.Port
}

// shutdownTimeout bounds how long Run waits for in-flight HTTP requests on exit.
const shutdownTimeout = 3 * time.Second

// launchBrowser opens the URL in the system's default web browser.
func (s *Server) launchBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if cmd != nil {
		_ = cmd.Start()
	}
}
