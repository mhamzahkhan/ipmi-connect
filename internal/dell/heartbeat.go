package dell

import (
	"log"
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

	// 2. Control Channel reader: consumes BMC notifications so its send buffer never fills.
	go c.drainControl(stopCh)
}

// drainControl reads framed control-channel packets until stopped or the connection fails,
// logging each packet type once so unexpected BMC behaviour is visible.
func (c *Client) drainControl(stopCh <-chan struct{}) {
	seen := map[uint16]bool{}
	for {
		select {
		case <-stopCh:
			return
		default:
		}

		c.ConnMu.Lock()
		conn := c.CtrlConn
		c.ConnMu.Unlock()
		if conn == nil {
			return
		}

		pkt, err := readPacket(conn, controlDrainPoll)
		if err != nil {
			if isTimeout(err) {
				continue
			}
			log.Printf("[Dell] Control channel closed: %v", err)
			return
		}
		if !seen[pkt.ID] {
			seen[pkt.ID] = true
			log.Printf("[Dell] Control channel: first %s (0x%04x, %d bytes)", packetName(pkt.ID), pkt.ID, len(pkt.Payload))
		}
	}
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
