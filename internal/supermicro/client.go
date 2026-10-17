package supermicro

import (
	"net/http"
	"sync"
	"time"

	"ipmi-connect/internal/bmc"
)

// Client manages authentication, power control, and RFB connections for Supermicro X7SB3.
type Client struct {
	Host       string
	User       string
	Password   string
	HTTPClient *http.Client
	SID        string
	SessionMu  sync.Mutex
	IsLoggedIn bool

	// Screen details from RFB ServerInit
	ScreenWidth  int
	ScreenHeight int
	ServerName   string
}

// NewClient creates a new Supermicro client.
func NewClient(host, user, pass string) *Client {
	return &Client{
		Host:       host,
		User:       user,
		Password:   pass,
		HTTPClient: bmc.NewLegacyHTTPClient(10 * time.Second),
	}
}
