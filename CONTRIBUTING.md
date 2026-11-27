# Contributing to ipmi-connect

Thanks for looking. Pull requests are very welcome, and you don't need to own my hardware to send one.

The project only knows the two boards I have (a Dell PowerEdge C6220 II and a Supermicro X8DTL-6F). Every other board out there is somebody else's, so the most useful thing you can do is make ipmi-connect work on yours.

## What would help most

- **Support for another board or firmware.** Other ATEN, Avocent or AMI MegaRAC firmware, or a new vendor entirely. Even partial support (video only, say) is worth a PR.
- **Bug fixes**, especially on hardware I can't test.
- **Bug reports** with enough detail to reproduce (see below).
- **Anything under "Future plans"** in the [README](README.md#future-plans), or issues labelled [`help wanted`](https://github.com/mhamzahkhan/ipmi-connect/labels/help%20wanted) and [`good first issue`](https://github.com/mhamzahkhan/ipmi-connect/labels/good%20first%20issue).

Not sure if something is wanted? Open an issue first and ask. I'd rather say yes early than turn down finished work.

## Sending a pull request

1. Fork the repo and branch from `main`.
2. Make your change, with tests (see [Testing](#testing)).
3. Run `make`. It lints, runs the tests with the race detector, and builds. CI runs the same checks on your PR.
4. Open the PR. Say which board and firmware you tested on, and what you checked on real hardware versus only in tests.

Small, focused PRs get merged fastest. One topic per PR: a new vendor, a bug fix and a CI change should be three PRs, not one. If you've built up a lot of work in a fork, I'm happy to take it in pieces.

Please keep the module path `github.com/mhamzahkhan/ipmi-connect` in your PR, even if your fork renamed it.

### Commit messages

Releases are automated with semantic-release, so commit messages follow [Conventional Commits](https://www.conventionalcommits.org/):

```text
feat(supermicro): support X9 firmware 3.x
fix(dell): drop video frames of implausible geometry
docs: add notes on the ASMB8 login flow
```

Types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`. A `feat` makes a minor release and a `fix` a patch release.

## Testing

- **Unit tests** go next to the code. Table-driven tests are preferred.
- **Fake BMCs**: the vendor packages test against small in-process fakes (see `fakeRFB` in `internal/supermicro/client_test.go`). Please do the same rather than needing a real BMC for CI.
- **Fuzz targets** exist for every parser of bytes that come from a BMC (`fuzz_test.go` in each package). If you add a parser, add a fuzz target, and bound any allocation sized from a length the BMC sends.
- **Hardware**: if you changed the Dell or Supermicro code, I'll test it on my boards before merging. For anything else, your hardware testing is what counts, so describe it in the PR.

## Clean-room rules

ipmi-connect is a clean-room implementation, and it has to stay that way to be publishable (see [NOTICE](NOTICE)).

- **No vendor code.** Don't paste code from vendor Java applets, native libraries, firmware or SDKs, decompiled or otherwise.
- **No vendor files.** Don't commit firmware images, JARs, JNLP files from a real BMC, or vendor documentation.
- **Describe protocols in your own words** in `docs/`. Packet captures used as test vectors should be minimal and scrubbed.
- **No real addresses or secrets.** Use the documentation ranges (`192.0.2.0/24`, `2001:db8::/32`) for IPs. Never commit MAC addresses, serial numbers, passwords or session tokens.

Using AI tools is fine; this project was built that way (see [How it was built](README.md#how-it-was-built)). The same rules apply to whatever they produce, and you're responsible for having read and tested it.

## Reporting bugs

Open an issue with:

- the board, BMC firmware version and vendor (Dell, Supermicro, …)
- the output of `ipmi-connect -v`
- what you did, what you expected, and what happened
- the log output, with any addresses, usernames and tokens removed

## Security problems

Please don't open a public issue for a vulnerability in ipmi-connect itself. Email the maintainer instead, using the address on my [GitHub profile](https://github.com/mhamzahkhan).

The BMCs' own weaknesses (TLS 1.0, unencrypted KVM channels) are known and documented in the README's [Security](README.md#security) section.

## Code layout

```text
cmd/ipmi-connect/      CLI: flags, vendor detection, per-vendor setup and stream loops
internal/
  bmc/                 Shared types, legacy TLS 1.0 HTTP client, password prompt
  dell/                Avocent client: web session, APCP channels, heartbeat, video, input, power
  supermicro/          ATEN client: CGI session, RFB handshake and input, power
  web/                 Local HTTP/WebSocket server, key macros, frame broadcast
    static/            Embedded browser UI
  aspeed/              ASPEED video decoder (Go port of Aspeed's MPL-2.0 reference)
  vmedia/              Virtual media manager: local image library, drives, transfer stats
  avmp/                Dell (Avocent AVMP) virtual media client
  atenvm/              Supermicro (ATEN Virtual Storage) virtual media client
  diskimage/           FAT image creation (blank, or from a folder)
  textkeys/            Text to USB HID key presses (US layout)
docs/                  Hardware notes, protocol specs, per-vendor deep dives
```

A new vendor usually means a new package under `internal/` that implements `bmc.Console` (and `bmc.VirtualMedia` if it has virtual media), plus a case in the CLI's vendor detection. The existing `dell` and `supermicro` packages are the templates to follow.

```bash
make            # lint, test, build
make test       # go test -race ./...
make lint       # go vet + golangci-lint (config in .golangci.yml)
make fmt
```

You need Go 1.27.2 or newer, and golangci-lint v2 built with a Go at least that new.

## Licence

By sending a pull request you agree that your contribution is licensed under the project's [Apache License 2.0](LICENSE), the same as the rest of the code (section 5 of the licence). There's no CLA. Code under `internal/aspeed/` stays under the Mozilla Public License 2.0.
