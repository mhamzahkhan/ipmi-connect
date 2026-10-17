package main

import (
	"context"
	"io"
	"log"
	"net"
	"time"

	"ipmi-connect/internal/supermicro"
	"ipmi-connect/internal/web"
)

const (
	smUpdateInterval = 100 * time.Millisecond
	smWriteTimeout   = 500 * time.Millisecond
	smReadPoll       = 100 * time.Millisecond
)

// runSupermicro logs in and starts the RFB connection and stream in the background.
func runSupermicro(ctx context.Context, server *web.Server, host, user, password string) {
	client := supermicro.NewClient(host, user, password)
	server.SMClient = client

	server.StateMu.Lock()
	server.State.Model = "Supermicro X7SB3 (WPCM450)"
	server.State.Firmware = "ATEN 3.13"
	server.StateMu.Unlock()

	log.Printf("[Supermicro] Initiating login sequence with %s...", host)
	if err := client.Login(); err != nil {
		log.Fatalf("[Supermicro] Login failed: %v", err)
	}

	go func() {
		conn, err := client.ConnectRFB()
		if err != nil {
			log.Printf("[Supermicro] RFB connection error: %v", err)
			return
		}
		server.ConnMu.Lock()
		server.ActiveConn = conn
		server.ConnMu.Unlock()

		server.StateMu.Lock()
		server.State.ScreenWidth = client.ScreenWidth
		server.State.ScreenHeight = client.ScreenHeight
		server.StateMu.Unlock()

		streamSupermicroVideo(ctx, server, conn, client.ScreenWidth, client.ScreenHeight)
	}()
}

// streamSupermicroVideo polls for RFB framebuffer updates.
//
// TODO: FramebufferUpdate rectangles are not decoded yet. Every update re-sends a
// solid placeholder buffer, so the Supermicro console currently shows a blank screen.
func streamSupermicroVideo(ctx context.Context, srv *web.Server, conn net.Conn, width, height int) {
	if width <= 0 || height <= 0 {
		width, height = 1024, 768
	}

	rgbaBuffer := make([]byte, width*height*4)
	for i := 0; i < len(rgbaBuffer); i += 4 {
		rgbaBuffer[i] = 16
		rgbaBuffer[i+1] = 20
		rgbaBuffer[i+2] = 28
		rgbaBuffer[i+3] = 255
	}

	srv.SendRawRGBA(width, height, rgbaBuffer)

	// FramebufferUpdateRequest: type 3, incremental flag, x, y, width, height.
	req := []byte{3, 0, 0, 0, 0, 0, byte(width >> 8), byte(width & 0xFF), byte(height >> 8), byte(height & 0xFF)}
	if _, err := conn.Write(req); err != nil {
		return
	}

	buf := make([]byte, 65536)
	ticker := time.NewTicker(smUpdateInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			req[1] = 1 // incremental from now on
			_ = conn.SetWriteDeadline(time.Now().Add(smWriteTimeout))
			if _, err := conn.Write(req); err != nil {
				return
			}
		default:
			_ = conn.SetReadDeadline(time.Now().Add(smReadPoll))
			n, err := conn.Read(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if err == io.EOF {
					return
				}
				continue
			}

			if n > 0 && buf[0] == 0 { // FramebufferUpdate
				srv.SendRawRGBA(width, height, rgbaBuffer)
			}
		}
	}
}
