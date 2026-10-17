// Package web serves the local browser console: embedded static UI, a small REST API
// for power and virtual media, and a WebSocket that streams video frames out and
// carries keyboard and mouse input back.
//
// Binary frames sent to the browser start with a type byte: 0x02 raw RGBA
// (u16 width, u16 height, pixels) or 0x03 ASPEED frame (mode420, luma table,
// chroma table, 4 reserved bytes, u16 width, height, bitstream).
package web
