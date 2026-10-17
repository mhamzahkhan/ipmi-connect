package web

import "encoding/binary"

// BroadcastFrame distributes a video frame to all connected browsers.
func (s *Server) BroadcastFrame(frame []byte) {
	select {
	case s.FrameChannel <- frame:
	default:
		// Drop frame if channel is full to preserve real-time low latency
	}
}

// frameBroadcaster pushes frames to active WebSockets without holding WSMu during network writes.
func (s *Server) frameBroadcaster() {
	for frame := range s.FrameChannel {
		s.WSMu.Lock()
		clients := make([]*WSConn, 0, len(s.WSConns))
		for ws := range s.WSConns {
			clients = append(clients, ws)
		}
		s.WSMu.Unlock()

		for _, ws := range clients {
			if err := ws.WriteMessage(OpBinary, frame); err != nil {
				s.WSMu.Lock()
				delete(s.WSConns, ws)
				s.WSMu.Unlock()
				_ = ws.Close()
			}
		}
	}
}

// SendRawRGBA encodes width, height, and raw RGBA pixels and broadcasts to browsers.
func (s *Server) SendRawRGBA(width, height int, rgba []byte) {
	buf := make([]byte, 5+len(rgba))
	buf[0] = 0x02 // raw RGBA marker
	binary.BigEndian.PutUint16(buf[1:3], uint16(width))
	binary.BigEndian.PutUint16(buf[3:5], uint16(height))
	copy(buf[5:], rgba)

	s.LastFrameMu.Lock()
	s.LastFrame = append(s.LastFrame[:0], buf...)
	s.LastFrameMu.Unlock()

	s.BroadcastFrame(buf)
}

// SendAspeedFrame packages an Aspeed video bitstream chunk and broadcasts to browsers.
func (s *Server) SendAspeedFrame(mode420, selector, advanceSelector byte, x, y, w, h uint16, bitstream []byte) {
	buf := make([]byte, 12+len(bitstream))
	buf[0] = 0x03 // Aspeed Frame marker
	buf[1] = mode420
	buf[2] = selector
	buf[3] = advanceSelector
	binary.BigEndian.PutUint16(buf[4:6], x)
	binary.BigEndian.PutUint16(buf[6:8], y)
	binary.BigEndian.PutUint16(buf[8:10], w)
	binary.BigEndian.PutUint16(buf[10:12], h)
	copy(buf[12:], bitstream)

	s.LastFrameMu.Lock()
	s.LastFrame = append(s.LastFrame[:0], buf...)
	if len(bitstream) > 1000 {
		s.LastFullFrame = append(s.LastFullFrame[:0], buf...)
	}
	s.LastFrameMu.Unlock()

	s.BroadcastFrame(buf)
}
