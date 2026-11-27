# AGENTS.md

Guidance for AI coding agents working on ipmi-connect. Read [CONTRIBUTING.md](CONTRIBUTING.md) first: its rules apply to you too, and this file only adds what an agent needs on top.

## What this is

A single Go binary that replaces the Java Web Start consoles of old server BMCs. It speaks the BMCs' own KVM and virtual media protocols and serves the console to a browser on `127.0.0.1`. The [README](README.md) has the supported hardware and the feature status. `docs/` has the protocol notes, and CONTRIBUTING.md has the code layout.

## Commands

```bash
make            # lint, race tests, build: run before calling any change done
make test       # go test -race ./...
make lint       # go vet + golangci-lint
go test -run TestName ./internal/supermicro/   # one test
go test -fuzz FuzzReadMessage -fuzztime 30s ./internal/supermicro/   # one fuzz target
```

## Rules

1. **Clean room.** Never paste vendor code (decompiled Java, native libraries, firmware) into the repo, and never commit vendor files. Write protocol behaviour in your own words in `docs/`.
2. **No real network details.** Use `192.0.2.0/24` or `2001:db8::/32` addresses in code, tests and docs. Never commit MAC addresses, serial numbers, credentials or session tokens.
3. **Treat BMC bytes as hostile.** Bound every allocation sized from a length the BMC sends, and add or extend a fuzz target for any parser you touch.
4. **Test against fakes.** Unit tests must run without a BMC; follow the in-process fakes already in each vendor package.
5. **Be honest about hardware.** Say in the PR what was verified on a real BMC and what was only tested. Never describe a change as hardware-verified unless someone ran it on one.
6. **Live hardware is not yours to change.** If you can reach a real BMC, stay read-only unless the user asks. Power actions, BMC config changes, firmware flashes and writes to virtual media need the user's explicit OK, because a mistake on these boards can't be undone.
7. **Keep the shape.** Keep the module path `github.com/mhamzahkhan/ipmi-connect`, the package layout and the existing dependencies. Don't add a dependency for a few lines of code.
8. **Small PRs and Conventional Commits.** One topic per PR. Releases are cut from commit messages (`feat`, `fix`, `docs`, …), so get the type right.

## Credentials

The CLI reads `IPMI_CONNECT_USER` and `IPMI_CONNECT_PASSWORD`. Never print, log or commit their values. Prefer the environment variable over `-p`, which shows up in the process list.
