package main

import (
	"context"
	"log"
	"time"

	"ipmi-connect/internal/bmc"
	"ipmi-connect/internal/supermicro"
	"ipmi-connect/internal/web"
)

const smPowerPollInterval = 15 * time.Second

// runSupermicro logs in, then keeps an ATEN iKVM session up in the background. It returns the
// function that ends the BMC session (after the KVM socket has closed).
func runSupermicro(ctx context.Context, server *web.Server, host, user, password string) (logout func()) {
	client := supermicro.NewClient(host, user, password)
	server.Console = client

	server.StateMu.Lock()
	server.State.Model = "Supermicro (ATEN iKVM)"
	server.StateMu.Unlock()

	log.Printf("[Supermicro] Initiating login sequence with %s...", host)
	if err := client.Login(); err != nil {
		log.Fatalf("[Supermicro] Login failed: %v", err)
	}

	go pollSupermicroPower(ctx, server, client)
	client.Start(ctx, server)
	return client.Logout
}

// pollSupermicroPower refreshes the reported power state.
func pollSupermicroPower(ctx context.Context, server *web.Server, client *supermicro.Client) {
	update := func() {
		state, err := client.PowerStatus()
		if err != nil {
			log.Printf("[Supermicro] Power status: %v", err)
			state = bmc.PowerStateUnknown // a stale value could hide the real state
		}
		server.StateMu.Lock()
		server.State.PowerState = state
		server.StateMu.Unlock()
	}
	update()
	ticker := time.NewTicker(smPowerPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
	}
}
