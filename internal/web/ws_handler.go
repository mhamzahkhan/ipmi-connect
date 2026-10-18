package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/coder/websocket"
)

// maxClientMessage bounds browser-to-server messages: input events are tiny, a paste to type
// is up to maxTypeRunes characters.
const maxClientMessage = 4*maxTypeRunes + 1024

// handleWS streams video to one browser and forwards its input to the console. Accept rejects
// cross-origin connections: only pages served by this server can open the socket.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	s.connWG.Add(1)
	defer s.connWG.Done() // registered first, so it runs after the input release below

	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("[WS] Rejected connection: %v", err)
		return
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxClientMessage)

	// Not r.Context(): the request is hijacked, so its context does not track the socket. Run
	// cancels connCtx on shutdown.
	ctx, cancel := context.WithCancel(s.connCtx)
	defer cancel()

	client := newWSClient()
	go client.writeLoop(ctx, cancel, conn)

	s.StateMu.RLock()
	width, height := s.State.ScreenWidth, s.State.ScreenHeight
	s.StateMu.RUnlock()
	resize, _ := json.Marshal(map[string]any{"type": "resize", "width": width, "height": height})
	client.out <- outMessage{typ: websocket.MessageText, data: resize}

	s.attachClient(client)
	defer s.removeClient(client)

	var input *inputState
	if s.Console != nil {
		input = newInputState(s.Console)
		defer input.releaseAll() // a closed tab must not leave keys or buttons held on the host
		// Ask the BMC for a complete frame even when a picture exists: reloading the page is how
		// a user recovers one damaged by a lost or corrupt frame.
		if err := s.Console.RequestFrameRefresh(); err != nil {
			log.Printf("[WS] Refresh for new client: %v", err)
		}
	}

	var typing context.CancelFunc // the paste this browser is typing, if any
	defer func() {
		if typing != nil {
			typing()
		}
	}()
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText && input != nil {
			s.handleTyping(ctx, client, input, data, &typing)
			input.handle(data)
		}
	}
}
