// Command ipmi-connect is a browser-based replacement for the Java Web Start
// KVM consoles shipped with legacy Dell (Avocent) and Supermicro (ATEN) BMCs.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ipmi-connect/internal/bmc"
	"ipmi-connect/internal/web"
)

const shutdownTimeout = 3 * time.Second

func main() {
	opts := parseArgs(os.Args[1:])
	if opts.ShowHelp || opts.Host == "" {
		printUsage()
		os.Exit(0)
	}

	user, password, err := resolveCredentials(opts, os.Getenv, bmc.ReadPassword)
	if err != nil {
		log.Fatalf("Error reading password: %v", err)
	}

	targetType := bmc.TargetType(strings.ToLower(opts.Type))
	if targetType == bmc.TargetAuto {
		log.Printf("[Probe] Detecting BMC vendor for %s...", opts.Host)
		targetType = detectVendor(opts.Host)
		log.Printf("[Probe] Target identified as: %s", strings.ToUpper(string(targetType)))
	}

	server := web.NewServer(opts.WebPort, bmc.SessionState{
		Host:         opts.Host,
		TargetType:   targetType,
		Connected:    true,
		PowerState:   "ONLINE",
		ScreenWidth:  1024,
		ScreenHeight: 768,
		StatusText:   "Connecting...",
	})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go logoutOnSignal(ctx, server)

	switch targetType {
	case bmc.TargetDell:
		runDell(ctx, server, opts.Host, user, password)
	case bmc.TargetSupermicro:
		runSupermicro(ctx, server, opts.Host, user, password)
	default:
		log.Fatalf("Unknown or unsupported target type: %s", targetType)
	}

	if err := server.Start(!opts.NoBrowser); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Server error: %v", err)
	}
}

// logoutOnSignal releases the BMC session on shutdown. The Dell allows only one GUI
// session, so exiting without logging out locks out the next login.
func logoutOnSignal(ctx context.Context, server *web.Server) {
	<-ctx.Done()
	log.Printf("\n[Signal] Caught shutdown signal, cleaning up BMC session...")
	if server.DellClient != nil {
		server.DellClient.Logout()
	}
	if server.SMClient != nil {
		server.SMClient.Logout()
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if server.HTTPServer != nil {
		_ = server.HTTPServer.Shutdown(shutdownCtx)
	}
}
