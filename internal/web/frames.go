package web

// SetScreenSize records the host's current video mode so /api/status and newly connected
// browsers see the real resolution.
func (s *Server) SetScreenSize(width, height int) {
	s.StateMu.Lock()
	defer s.StateMu.Unlock()
	s.State.ScreenWidth = width
	s.State.ScreenHeight = height
}
