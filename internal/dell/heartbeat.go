package dell

import (
	"net"
	"time"
)

// StartHeartbeat begins sending Packet 1024 (q) on the Control Channel every 5 seconds
// and reading incoming control channel packets in the background.
// This prevents the AST2300 hardware watchdog from terminating the KVM session at 30 seconds.
func (c *Client) StartHeartbeat() {
	c.ConnMu.Lock()
	defer c.ConnMu.Unlock()

	if c.heartbeatStop != nil {
		return
	}
	if c.CtrlConn == nil {
		return
	}

	c.heartbeatStop = make(chan struct{})
	stopCh := c.heartbeatStop

	// 1. Packet 1024 (q) Heartbeat Writer
	go func() {
		ticker := time.NewTicker(heartbeatInterval)
		defer ticker.Stop()
		heartbeatPkt := beefPacket(pktHeartbeat, [8]byte{})
		for {
			select {
			case <-stopCh:
				return
			case <-ticker.C:
				c.ConnMu.Lock()
				curConn := c.CtrlConn
				if curConn != nil {
					_ = curConn.SetWriteDeadline(time.Now().Add(socketWriteTimeout))
					_, _ = curConn.Write(heartbeatPkt)
				}
				c.ConnMu.Unlock()
			}
		}
	}()

	// 2. Control Channel Reader / Drain Loop
	go func() {
		buf := make([]byte, 4096)
		for {
			select {
			case <-stopCh:
				return
			default:
				c.ConnMu.Lock()
				curConn := c.CtrlConn
				c.ConnMu.Unlock()
				if curConn == nil {
					return
				}
				_ = curConn.SetReadDeadline(time.Now().Add(controlDrainPoll))
				_, err := curConn.Read(buf)
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue
					}
					return
				}
			}
		}
	}()
}

// StopHeartbeat stops the Control Channel heartbeat and background reader.
func (c *Client) StopHeartbeat() {
	c.ConnMu.Lock()
	defer c.ConnMu.Unlock()

	if c.heartbeatStop != nil {
		close(c.heartbeatStop)
		c.heartbeatStop = nil
	}
}
