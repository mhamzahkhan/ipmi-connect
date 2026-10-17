// Package web serves the local browser console: embedded static UI, a small REST API
// for power and virtual media, and a WebSocket that streams video frames out and
// carries keyboard and mouse input back.
//
// The server decodes BMC video itself and keeps the current picture. Binary
// messages to the browser are RGBA: 0x02 a full frame (u16 width, u16 height,
// pixels) and 0x04 changed rectangles (u16 count, then per rect u16 x, y, w, h
// and its pixels). GET /api/screenshot.png returns the current picture.
package web
