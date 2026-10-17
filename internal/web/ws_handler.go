package web

import (
	"encoding/json"
	"log"
	"net/http"
)

// handleWS manages the live WebSocket connection with the browser.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := Upgrade(w, r)
	if err != nil {
		log.Printf("[WS] Upgrade error: %v", err)
		return
	}
	defer ws.Close()

	s.WSMu.Lock()
	s.WSConns[ws] = true
	s.WSMu.Unlock()

	defer func() {
		s.WSMu.Lock()
		delete(s.WSConns, ws)
		s.WSMu.Unlock()
	}()

	s.StateMu.RLock()
	wDim, hDim := s.State.ScreenWidth, s.State.ScreenHeight
	s.StateMu.RUnlock()

	if wDim == 0 || hDim == 0 {
		wDim, hDim = 1024, 768
	}

	initMsg, _ := json.Marshal(map[string]interface{}{
		"type":   "resize",
		"width":  wDim,
		"height": hDim,
	})
	_ = ws.WriteMessage(OpText, initMsg)

	// Send cached last full frame to newly connected client
	s.LastFrameMu.RLock()
	if len(s.LastFullFrame) > 0 {
		_ = ws.WriteMessage(OpBinary, s.LastFullFrame)
	} else if len(s.LastFrame) > 0 {
		_ = ws.WriteMessage(OpBinary, s.LastFrame)
	}
	s.LastFrameMu.RUnlock()

	if s.Console == nil {
		// Drain until the browser goes away; input has nowhere to go yet.
		for {
			if _, _, err := ws.ReadMessage(); err != nil {
				return
			}
		}
	}

	// Ask for a complete frame so this browser can render without waiting for screen changes.
	if err := s.Console.RequestFrameRefresh(); err != nil {
		log.Printf("[WS] Refresh for new client: %v", err)
	}

	input := newInputState(s.Console)
	defer input.releaseAll() // a closed tab must not leave keys or buttons held on the host
	for {
		op, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		if op == OpText {
			input.handle(data)
		}
	}
}
