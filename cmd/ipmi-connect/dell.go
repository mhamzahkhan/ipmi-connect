package main

import (
	"context"
	"log"
	"net"
	"os"
	"time"

	"ipmi-connect/internal/aspeed"
	"ipmi-connect/internal/dell"
	"ipmi-connect/internal/web"
)

const (
	dellSessionPollInterval = 15 * time.Second
	// dellIdleFrameWait is how long to wait for the first packet of a new frame.
	dellIdleFrameWait = 3 * time.Second
	// dellChunkGap ends a frame: no continuation chunk within this gap means it is complete.
	dellChunkGap = 40 * time.Millisecond
	// dellStaticRefreshAfter forces a full frame before the BMC's 60s idle-video disconnect.
	dellStaticRefreshAfter = 15 * time.Second
	dellReconnectDelay     = 2 * time.Second
	maxReconnectDelay      = time.Minute
)

// nextBackoff doubles the reconnect delay, capped at maxReconnectDelay.
func nextBackoff(d time.Duration) time.Duration {
	return min(2*d, maxReconnectDelay)
}

// runDell logs in, opens both APCP channels, and starts the keepalive and video goroutines.
// It returns the function that ends the BMC session.
func runDell(ctx context.Context, server *web.Server, host, user, password string) (logout func()) {
	client := dell.NewClient(host, user, password)
	server.Console = client

	server.StateMu.Lock()
	server.State.Model = "Dell PowerEdge C6220 II (AST2300)"
	server.State.Firmware = "Avocent 2.62.5"
	server.StateMu.Unlock()

	log.Printf("[Dell] Initiating login sequence with %s...", host)
	if err := client.Login(); err != nil {
		log.Fatalf("[Dell] Login failed: %v", err)
	}

	if err := client.RefreshKVMTokens(); err != nil {
		fatalAfterLogin(client, "[Dell] Could not get KVM session tokens: %v", err)
	}

	log.Printf("[Dell] Connecting Control Channel on %s:%d...", host, client.VPort)
	if err := client.ConnectControl(); err != nil {
		fatalAfterLogin(client, "[Dell] Control channel connection failed: %v", err)
	}

	log.Printf("[Dell] Connecting Video Channel on %s:%d...", host, client.VPort)
	if err := client.ConnectVideo(); err != nil {
		fatalAfterLogin(client, "[Dell] Video channel connection failed: %v", err)
	}

	if err := client.EnableVideo(); err != nil {
		log.Printf("[Dell] Notice: EnableVideo: %v", err)
	}

	go pollDellSession(ctx, server, client)
	go streamDellVideo(ctx, server, client)
	return client.Logout
}

// fatalAfterLogin logs out before exiting: the BMC allows a single GUI session, and leaving it
// open locks out the next login until the BMC times it out.
func fatalAfterLogin(client *dell.Client, format string, args ...any) {
	log.Printf(format, args...)
	client.Logout()
	os.Exit(1)
}

// pollDellSession keeps the WebGUI session alive and refreshes the reported power state.
func pollDellSession(ctx context.Context, server *web.Server, client *dell.Client) {
	ticker := time.NewTicker(dellSessionPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := client.KeepAlive(); err != nil {
				log.Printf("[Dell] KeepAlive notice: %v", err)
			}
			if pwr, err := client.GetPowerState(); err == nil {
				server.StateMu.Lock()
				server.State.PowerState = pwr
				server.StateMu.Unlock()
			}
		}
	}
}

// streamDellVideo reassembles ASPEED frames from video packets and forwards them to the browser.
func streamDellVideo(ctx context.Context, srv *web.Server, client *dell.Client) {
	log.Printf("[Dell] Starting live Aspeed video streaming loop...")

	var frames frameAssembler
	var width, height uint16
	lastPktTime := time.Now()
	seenSelectors := map[[2]byte]bool{}
	decoder := aspeed.NewDecoder()

	// send decodes a whole frame into the picture and hands it to the browsers. Every frame covers
	// the screen from the origin. The decoder keeps state across frames (and reconnects): each
	// frame only updates blocks of the previous picture.
	send := func(f *dell.VideoPacket) {
		if f == nil {
			return
		}
		h := aspeed.Header{
			Width: int(f.Width), Height: int(f.Height), Mode420: f.Mode420,
			LumaSelector: f.LumaSelector, ChromaSelector: f.ChromaSelector,
		}
		if err := decoder.Decode(h, f.Bitstream); err != nil {
			log.Printf("[Dell] Dropping undecodable frame: %v", err)
			return
		}
		srv.UpdateScreen(h.Width, h.Height, decoder.Frame())
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		readTimeout := dellIdleFrameWait
		if frames.inProgress() {
			readTimeout = dellChunkGap
		}

		pkt, err := client.ReadVideoPacketWithTimeout(readTimeout)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// A pause while accumulating chunks means the frame is complete.
				send(frames.take())

				if time.Since(lastPktTime) > dellStaticRefreshAfter {
					_ = client.RequestFrameRefresh()
					lastPktTime = time.Now()
				}
				continue
			}
			if ctx.Err() != nil {
				return // shutting down: the read failed because Logout closed the channels
			}
			log.Printf("[Dell] Video stream interrupted (%v). Attempting automatic reconnection...", err)
			frames.take() // a partial frame would decode as garbage
			if !reconnectDell(ctx, client) {
				return
			}
			lastPktTime = time.Now()
			continue
		}

		if pkt == nil {
			continue
		}
		lastPktTime = time.Now()

		if pkt.Flags&1 != 0 {
			if pkt.Width != width || pkt.Height != height {
				width, height = pkt.Width, pkt.Height
				log.Printf("[Dell] Host video mode is %dx%d", width, height)
			}
			if sel := [2]byte{pkt.LumaSelector, pkt.ChromaSelector}; !seenSelectors[sel] {
				seenSelectors[sel] = true
				log.Printf("[Dell] Video quantisation tables: luma %d, chroma %d", sel[0], sel[1])
			}
		}
		send(frames.add(pkt))
	}
}

// reconnectDell retries with exponential backoff until the channels are back or ctx is
// cancelled (returns false).
func reconnectDell(ctx context.Context, client *dell.Client) bool {
	delay := dellReconnectDelay
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
		if err := client.Reconnect(); err != nil {
			delay = nextBackoff(delay)
			log.Printf("[Dell] Reconnection attempt failed: %v; retrying in %v", err, delay)
			continue
		}
		log.Printf("[Dell] Reconnected successfully to video channel. Resuming video stream.")
		return true
	}
}
