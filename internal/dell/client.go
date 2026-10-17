package dell

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"ipmi-connect/internal/bmc"
)

// defaultKVMPort is used for both channels until viewer.jnlp says otherwise.
const defaultKVMPort = 7578

// Timeouts and intervals. The 5s heartbeat keeps well inside the BMC's 30s control-channel watchdog.
const (
	httpTimeout        = 10 * time.Second
	dialTimeout        = 5 * time.Second
	socketWriteTimeout = 500 * time.Millisecond
	packetBodyTimeout  = 3 * time.Second
	heartbeatInterval  = 5 * time.Second
	controlDrainPoll   = 2 * time.Second
	reconnectSettle    = 1 * time.Second
)

// Client manages authentication, KVM sessions, and power controls for Dell C6220 II BMCs.
type Client struct {
	Host       string
	User       string
	Password   string
	HTTPClient *http.Client
	ST1Token   string
	ST2Token   string
	SessionMu  sync.Mutex
	IsLoggedIn bool

	// JNLP KVM credentials
	UserTokenStr string
	PassTokenStr string
	KMPort       int
	VPort        int

	// Active Channels
	CtrlConn              net.Conn
	VideoConn             net.Conn
	ClientRandom          uint32
	ApplianceSessionToken uint32
	AckCount              uint32
	heartbeatStop         chan struct{}
	ConnMu                sync.Mutex
}

// NewClient creates a new Dell BMC client.
func NewClient(host, user, pass string) *Client {
	httpClient := bmc.NewLegacyHTTPClient(httpTimeout)
	return &Client{
		Host:         host,
		User:         user,
		Password:     pass,
		HTTPClient:   httpClient,
		KMPort:       defaultKVMPort,
		VPort:        defaultKVMPort,
		ClientRandom: uint32(time.Now().UnixNano() & 0x7FFFFFFF),
	}
}

// Close closes any open control and video connections and stops the heartbeat.
func (c *Client) Close() {
	c.StopHeartbeat()

	c.ConnMu.Lock()
	defer c.ConnMu.Unlock()

	if c.VideoConn != nil {
		c.VideoConn.Close()
		c.VideoConn = nil
	}
	if c.CtrlConn != nil {
		c.CtrlConn.Close()
		c.CtrlConn = nil
	}
}

// Reconnect resets and re-establishes the Control and Video channels after a disconnect.
func (c *Client) Reconnect() error {
	log.Printf("[Dell] Reconnecting Control and Video channels...")
	c.Close()

	// Wait briefly to allow BMC socket cleanup
	time.Sleep(reconnectSettle)

	// Refresh JNLP parameters if needed
	if err := c.FetchKVMParameters(); err != nil {
		log.Printf("[Dell] Reconnect FetchKVMParameters warning: %v", err)
	}

	if err := c.ConnectControl(); err != nil {
		return fmt.Errorf("reconnect control channel failed: %w", err)
	}

	if err := c.ConnectVideo(); err != nil {
		return fmt.Errorf("reconnect video channel failed: %w", err)
	}

	if err := c.EnableVideo(); err != nil {
		log.Printf("[Dell] Reconnect EnableVideo warning: %v", err)
	}

	log.Printf("[Dell] Reconnection successful!")
	return nil
}
