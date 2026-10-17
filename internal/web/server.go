package web

import (
	"embed"
	"fmt"
	"io"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"ipmi-connect/internal/bmc"
	"ipmi-connect/internal/dell"
	"ipmi-connect/internal/supermicro"
)

func init() {
	_ = mime.AddExtensionType(".wasm", "application/wasm")
	_ = mime.AddExtensionType(".js", "application/javascript")
}

//go:embed static/*
var staticFS embed.FS

// Server hosts the local web UI, REST API, and WebSocket streaming bridge.
type Server struct {
	State         bmc.SessionState
	StateMu       sync.RWMutex
	DellClient    *dell.Client
	SMClient      *supermicro.Client
	ActiveConn    net.Conn
	ConnMu        sync.Mutex
	WSConns       map[*WSConn]bool
	WSMu          sync.Mutex
	Port          int
	HTTPServer    *http.Server
	MountedISO    string
	ISOReader     io.ReadSeekCloser
	FrameChannel  chan []byte
	LastFrame     []byte
	LastFullFrame []byte
	LastFrameMu   sync.RWMutex
}

// NewServer initializes a new Web UI server.
func NewServer(port int, state bmc.SessionState) *Server {
	return &Server{
		State:        state,
		Port:         port,
		WSConns:      make(map[*WSConn]bool),
		FrameChannel: make(chan []byte, 60),
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
	mux.HandleFunc("/api/vmedia/mount", s.handleMount)
	mux.HandleFunc("/api/vmedia/unmount", s.handleUnmount)
	mux.HandleFunc("/ws", s.handleWS)
	return mux, nil
}

// Start launches the HTTP server and opens the browser.
func (s *Server) Start(openBrowser bool) error {
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
	s.Port = listener.Addr().(*net.TCPAddr).Port

	s.HTTPServer = &http.Server{
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	url := fmt.Sprintf("http://127.0.0.1:%d", s.Port)
	log.Printf("==================================================================")
	log.Printf("🚀 IPMI Modern Console active at: %s", url)
	log.Printf("==================================================================")

	if openBrowser {
		go func() {
			time.Sleep(300 * time.Millisecond)
			s.launchBrowser(url)
		}()
	}

	// Start background frame distributor
	go s.frameBroadcaster()

	return s.HTTPServer.Serve(listener)
}

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
