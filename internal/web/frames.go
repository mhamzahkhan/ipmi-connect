package web

import "encoding/binary"

// SendRawRGBA encodes width, height, and raw RGBA pixels and broadcasts to browsers.
func (s *Server) SendRawRGBA(width, height int, rgba []byte) {
	buf := make([]byte, 5+len(rgba))
	buf[0] = 0x02 // raw RGBA marker
	binary.BigEndian.PutUint16(buf[1:3], uint16(width))
	binary.BigEndian.PutUint16(buf[3:5], uint16(height))
	copy(buf[5:], rgba)

	s.LastFrameMu.Lock()
	s.LastFrame = buf
	s.LastFrameMu.Unlock()

	s.broadcastFull(buf)
}

// SendAspeedFrame packages an Aspeed video bitstream chunk and broadcasts to browsers.
func (s *Server) SendAspeedFrame(mode420, lumaSelector, chromaSelector byte, w, h uint16, bitstream []byte) {
	buf := make([]byte, 12+len(bitstream))
	buf[0] = 0x03 // Aspeed Frame marker
	buf[1] = mode420
	buf[2] = lumaSelector
	buf[3] = chromaSelector
	// Bytes 4..7 are reserved (zero): frames always cover the screen from the origin.
	binary.BigEndian.PutUint16(buf[8:10], w)
	binary.BigEndian.PutUint16(buf[10:12], h)
	copy(buf[12:], bitstream)

	// ASPEED frames are deltas, so none is cached for new browsers: they request a refresh.
	s.broadcast(buf)
}

// SetScreenSize records the host's current video mode so /api/status and newly connected
// browsers see the real resolution.
func (s *Server) SetScreenSize(width, height int) {
	s.StateMu.Lock()
	defer s.StateMu.Unlock()
	s.State.ScreenWidth = width
	s.State.ScreenHeight = height
}
