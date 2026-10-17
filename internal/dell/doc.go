// Package dell is a client for the Avocent firmware on Dell C6220 II BMCs (ASPEED AST2300).
//
// The KVM uses two TLS sockets on port 7578: a control channel (mode 3) for auth,
// input and heartbeats, and a video channel (mode 4) that streams ASPEED bitstream
// packets. Power control and session keepalive go through the AppWeb HTTP API.
// Wire formats are documented in docs/protocols.md.
package dell
