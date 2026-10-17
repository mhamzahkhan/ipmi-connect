package web

import (
	"encoding/json"
	"log"
	"net/http"

	"ipmi-connect/internal/supermicro"
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

	// If Dell BMC is connected, immediately request a full frame refresh so the new
	// client receives a complete screen render right away.
	if s.DellClient != nil {
		_ = s.DellClient.RequestFrameRefresh()
	}

	// Inbound message loop (keyboard/mouse/macros from browser)
	for {
		op, data, err := ws.ReadMessage()
		if err != nil {
			break
		}

		if op == OpText {
			var evt map[string]interface{}
			if err := json.Unmarshal(data, &evt); err == nil {
				s.handleClientInput(evt)
			}
		}
	}
}

// handleClientInput processes user input and forwards it to the active BMC socket.
func (s *Server) handleClientInput(evt map[string]interface{}) {
	evtType, _ := evt["type"].(string)

	switch evtType {
	case "pointer":
		mask, _ := evt["mask"].(float64)
		x, _ := evt["x"].(float64)
		y, _ := evt["y"].(float64)

		if s.DellClient != nil {
			_ = s.DellClient.SendPointerEvent(byte(mask), uint16(x), uint16(y))
		} else if s.SMClient != nil {
			s.ConnMu.Lock()
			if s.ActiveConn != nil {
				_ = supermicro.SendPointerEvent(s.ActiveConn, byte(mask), uint16(x), uint16(y))
			}
			s.ConnMu.Unlock()
		}

	case "key":
		down, _ := evt["down"].(bool)
		hidCode, hasHID := evt["hidCode"].(float64)

		if s.DellClient != nil && hasHID && hidCode > 0 {
			_ = s.DellClient.SendKeyEvent(down, uint16(hidCode))
		} else if s.SMClient != nil {
			keyCode, _ := evt["keyCode"].(float64)
			s.ConnMu.Lock()
			if s.ActiveConn != nil {
				_ = supermicro.SendKeyEvent(s.ActiveConn, down, uint32(keyCode))
			}
			s.ConnMu.Unlock()
		}

	case "macro":
		macroName, _ := evt["name"].(string)
		s.executeMacro(macroName)

	case "refresh":
		if s.DellClient != nil {
			_ = s.DellClient.RequestFrameRefresh()
		}
	}
}
