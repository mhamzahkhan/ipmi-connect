# ipmi-connect

A single Go binary that replaces the Java Web Start (JNLP) KVM consoles on legacy Dell (Avocent) and Supermicro (ATEN) BMCs. It speaks the vendors' proprietary KVM protocols directly and shows the console in your browser. No Java, no vendor native libraries, no TLS 1.0 browser workarounds.

Tested hardware:

| | Dell PowerEdge C6220 II | Supermicro X7SB3 |
| :--- | :--- | :--- |
| BMC | ASPEED AST2300, Avocent firmware 2.62.5 | Winbond WPCM450, ATEN firmware 3.13 |
| KVM protocol | Avocent APCP, two TLS sockets on 7578 | ATEN RFB 3.8 on 5900 |

The protocols were reverse-engineered from the vendors' JARs. Nothing from those JARs is used at build or run time; see [docs/](docs/README.md).

## Status

| Feature | Dell | Supermicro |
| :--- | :--- | :--- |
| Login / clean logout | Works | Works |
| Live video | Works (Go ASPEED decoder, verified frame-for-frame against Aspeed's reference) | **Not implemented**: shows a blank placeholder |
| Keyboard | Works (USB HID) | **Broken**: sends DOM `keyCode` as X11 keysym |
| Mouse | Works | Works (RFB PointerEvent) |
| Macros (Ctrl+Alt+Del, F-keys…) | Works | Works |
| Power control | Works | Implemented, untested |
| Virtual media | **Stub**: upload is saved locally, nothing reaches the BMC | **Stub** |

## Quick start

Requires Go 1.27+.

```bash
make build                                   # -> bin/ipmi-connect
./bin/ipmi-connect 192.0.2.10                     # user root, prompts for the password
./bin/ipmi-connect 192.0.2.20 -u admin --no-browser  # print the URL instead of opening it
```

The console runs at `http://127.0.0.1:8080` (or the next free port). Vendor is detected automatically; force it with `--type dell|supermicro`. Credentials come from flags, else environment variables, else defaults:

| | Flag | Environment | Default |
| :--- | :--- | :--- | :--- |
| User | `-u` | `IPMI_CONNECT_USER` | `root` |
| Password | `-p` | `IPMI_CONNECT_PASSWORD` | interactive prompt |

Prefer the environment variable or the prompt over `-p`, which is visible in the process list.

Press Ctrl+C to exit. The client logs out first, because the Dell allows only **one** GUI session; an unclean exit locks you out until the BMC times the session out.

## Development

```bash
make            # lint, test, build
make test       # go test -race ./...
make lint       # go vet + golangci-lint (config in .golangci.yml)
make fmt
```

## Layout

```text
cmd/ipmi-connect/      CLI: flags, vendor detection, per-vendor setup and stream loops
internal/
  bmc/                 Shared types, legacy TLS 1.0 HTTP client, password prompt
  dell/                Avocent client: web session, APCP channels, heartbeat, video, input, power
  supermicro/          ATEN client: CGI session, RFB handshake and input, power
  web/                 Local HTTP/WebSocket server, key macros, frame broadcast
    static/            Embedded UI: app.js (paints RGBA frames), keymap.js
  aspeed/              ASPEED video decoder (Go port of Aspeed's MPL-2.0 reference)
docs/                  Hardware inventory, protocol specs, per-vendor deep dives
```

## Known issues

- **The local console has no authentication or Origin checks.** Any web page open in the same browser can POST to `127.0.0.1:8080/api/power` as a no-CORS `text/plain` request, or open the `/ws` socket and type into the console. Treat it as unsafe while browsing elsewhere until this is fixed.
- Supermicro video, Supermicro keyboard, and virtual media: see the status table.
- Video frames are dropped when the browser falls behind; a dropped ASPEED delta tile leaves stale pixels until the next full frame.
- `--port` is accepted for compatibility but ignored.
