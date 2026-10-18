// Command ipmi-connect is a browser-based replacement for the Java Web Start
// KVM consoles shipped with legacy Dell (Avocent) and Supermicro (ATEN) BMCs.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
	"github.com/mhamzahkhan/ipmi-connect/internal/web"
)

var version = "dev"

func main() {
	opts := parseArgs(os.Args[1:])
	if opts.ShowVersion {
		fmt.Printf("ipmi-connect %s\n", version)
		os.Exit(0)
	}
	if opts.ShowHelp || opts.Host == "" {
		printUsage()
		os.Exit(0)
	}
	if opts.PortIgnored {
		log.Printf("Ignoring --port: BMC ports are fixed by each vendor's protocol. Use --web-port for the local console.")
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
		PowerState:   bmc.PowerStateUnknown,
		ScreenWidth:  1024,
		ScreenHeight: 768,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first Ctrl+C, restore default handling so a second one exits immediately, even
	// while setup is blocked on the network.
	go func() {
		<-ctx.Done()
		log.Printf("[Signal] Caught shutdown signal, cleaning up BMC session (Ctrl+C again to force quit)...")
		stop()
	}()

	var logout func()
	switch targetType {
	case bmc.TargetDell:
		logout = runDell(ctx, server, opts.Host, user, password)
	case bmc.TargetSupermicro:
		logout = runSupermicro(ctx, server, opts.Host, user, password)
	default:
		log.Fatalf("Unknown or unsupported target type: %s", targetType)
	}
	// The Dell allows a single GUI session: whatever happens from here, log out on the way out.
	defer logout()

	if ctx.Err() != nil {
		return // interrupted during setup; the deferred logout runs
	}

	if err := server.Run(ctx, !opts.NoBrowser); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("Server error: %v", err)
	}
}
