package main

import (
	"context"
	"log"
	"net"
	"time"

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
)

// runDell logs in, opens both APCP channels, and starts the keepalive and video goroutines.
func runDell(ctx context.Context, server *web.Server, host, user, password string) {
	client := dell.NewClient(host, user, password)
	server.DellClient = client

	server.StateMu.Lock()
	server.State.Model = "Dell PowerEdge C6220 II (AST2300)"
	server.State.Firmware = "Avocent 2.62.5"
	server.StateMu.Unlock()

	log.Printf("[Dell] Initiating login sequence with %s...", host)
	if err := client.Login(); err != nil {
		log.Fatalf("[Dell] Login failed: %v", err)
	}

	if err := client.FetchKVMParameters(); err != nil {
		log.Printf("[Dell] Notice: fetching JNLP tokens: %v", err)
	}

	log.Printf("[Dell] Connecting Control Channel on %s:%d...", host, client.VPort)
	if err := client.ConnectControl(); err != nil {
		log.Fatalf("[Dell] Control channel connection failed: %v", err)
	}

	log.Printf("[Dell] Connecting Video Channel on %s:%d...", host, client.VPort)
	if err := client.ConnectVideo(); err != nil {
		log.Fatalf("[Dell] Video channel connection failed: %v", err)
	}

	if err := client.EnableVideo(); err != nil {
		log.Printf("[Dell] Notice: EnableVideo: %v", err)
	}

	go pollDellSession(ctx, server, client)
	go streamDellVideo(ctx, server, client)
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

	var frameBuf []byte
	var cur dell.VideoPacket // geometry and tables of the frame being assembled
	lastPktTime := time.Now()
	seenSelectors := map[[2]byte]bool{}

	flush := func() {
		if len(frameBuf) == 0 {
			return
		}
		// Every frame covers the whole screen from the origin. Aspeed's browser decoder takes a
		// single table selector for luma and chroma, so it gets the luma table (the visually
		// dominant one) and pass-2 tables stay at 0, as in the vendor viewer.
		srv.SendAspeedFrame(cur.Mode420, cur.LumaSelector, 0, 0, 0, cur.Width, cur.Height, frameBuf)
		frameBuf = frameBuf[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		default:
		}

		readTimeout := dellIdleFrameWait
		if len(frameBuf) > 0 {
			readTimeout = dellChunkGap
		}

		pkt, err := client.ReadVideoPacketWithTimeout(readTimeout)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// A timeout while accumulating chunks means the frame is complete.
				flush()

				if time.Since(lastPktTime) > dellStaticRefreshAfter {
					_ = client.RequestFrameRefresh()
					lastPktTime = time.Now()
				}
				continue
			}
			log.Printf("[Dell] Video stream interrupted (%v). Attempting automatic reconnection...", err)
			flush()
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
			// First chunk of a new frame.
			flush()
			frameBuf = append(frameBuf[:0], pkt.Bitstream...)
			if pkt.Width != cur.Width || pkt.Height != cur.Height {
				log.Printf("[Dell] Host video mode is %dx%d", pkt.Width, pkt.Height)
				srv.SetScreenSize(int(pkt.Width), int(pkt.Height))
			}
			if sel := [2]byte{pkt.LumaSelector, pkt.ChromaSelector}; !seenSelectors[sel] {
				seenSelectors[sel] = true
				log.Printf("[Dell] Video quantisation tables: luma %d, chroma %d", sel[0], sel[1])
			}
			cur = *pkt
			cur.Bitstream = nil
		} else {
			frameBuf = append(frameBuf, pkt.Bitstream...)
		}
	}
}

// reconnectDell retries until the channels are back or ctx is cancelled (returns false).
func reconnectDell(ctx context.Context, client *dell.Client) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(dellReconnectDelay):
		}
		if err := client.Reconnect(); err != nil {
			log.Printf("[Dell] Reconnection attempt failed: %v, retrying in 2s...", err)
			continue
		}
		log.Printf("[Dell] Reconnected successfully to video channel. Resuming video stream.")
		return true
	}
}
