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
	smReadPoll       = 100 * time.Millisecond
)

// runSupermicro logs in and starts the RFB connection and stream in the background.
// It returns the function that ends the BMC session.
func runSupermicro(ctx context.Context, server *web.Server, host, user, password string) (logout func()) {
	client := supermicro.NewClient(host, user, password)
	console := supermicro.NewConsole(client)
	server.Console = console

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
		console.AttachRFB(conn)

		server.StateMu.Lock()
		server.State.ScreenWidth = client.ScreenWidth
		server.State.ScreenHeight = client.ScreenHeight
		server.StateMu.Unlock()

		streamSupermicroVideo(ctx, server, conn, console, client.ScreenWidth, client.ScreenHeight)
	}()
	return client.Logout
}

// streamSupermicroVideo polls for RFB framebuffer updates.
//
// TODO: FramebufferUpdate rectangles are not decoded yet. Every update re-sends a
// solid placeholder buffer, so the Supermicro console currently shows a blank screen.
func streamSupermicroVideo(ctx context.Context, srv *web.Server, conn net.Conn, console *supermicro.Console, width, height int) {
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

	srv.UpdateScreen(width, height, rgbaBuffer)

	// FramebufferUpdateRequest: type 3, incremental flag, x, y, width, height.
	req := []byte{3, 0, 0, 0, 0, 0, byte(width >> 8), byte(width & 0xFF), byte(height >> 8), byte(height & 0xFF)}
	if err := console.WriteRFB(req); err != nil {
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
			if err := console.WriteRFB(req); err != nil {
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
				srv.UpdateScreen(width, height, rgbaBuffer)
			}
		}
	}
}
