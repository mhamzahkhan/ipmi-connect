package web

import (
	"context"
	"log"
	"time"

	"github.com/coder/websocket"
)

const (
	// clientQueueLen bounds how far one browser may fall behind before it is resynchronised.
	clientQueueLen = 16
	// clientWriteTimeout drops a browser that stops reading.
	clientWriteTimeout = 5 * time.Second
	// resyncInterval rate-limits full-frame requests triggered by slow browsers.
	resyncInterval = time.Second
)

type outMessage struct {
	typ  websocket.MessageType
	data []byte
}

// wsClient is one connected browser. A dedicated writer drains out, so a slow browser only
// delays itself.
type wsClient struct {
	out chan outMessage
}

func newWSClient() *wsClient {
	return &wsClient{out: make(chan outMessage, clientQueueLen)}
}

// writeLoop sends queued messages until ctx ends or a write fails; failure calls cancel so the
// connection's reader stops too.
func (c *wsClient) writeLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-c.out:
			wctx, wcancel := context.WithTimeout(ctx, clientWriteTimeout)
			err := conn.Write(wctx, m.typ, m.data)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

// drain discards queued messages.
func (c *wsClient) drain() {
	for {
		select {
		case <-c.out:
		default:
			return
		}
	}
}

func (s *Server) addClient(c *wsClient) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	s.clients[c] = struct{}{}
}

func (s *Server) removeClient(c *wsClient) {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	delete(s.clients, c)
}

// broadcast queues a video frame for every browser. A browser whose queue is full has fallen
// behind: video frames are deltas, so its backlog is discarded and the BMC is asked for a full
// frame instead of letting it paint updates on top of a gap.
func (s *Server) broadcast(frame []byte) {
	s.clientsMu.Lock()
	clients := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.clientsMu.Unlock()

	msg := outMessage{typ: websocket.MessageBinary, data: frame}
	behind := false
	for _, c := range clients {
		select {
		case c.out <- msg:
		default:
			c.drain()
			behind = true
		}
	}
	if behind {
		s.requestResync()
	}
}

// requestResync asks the console for a full frame, at most once per resyncInterval.
func (s *Server) requestResync() {
	s.clientsMu.Lock()
	if time.Since(s.lastResync) < resyncInterval || s.Console == nil {
		s.clientsMu.Unlock()
		return
	}
	s.lastResync = time.Now()
	s.clientsMu.Unlock()

	go func() {
		if err := s.Console.RequestFrameRefresh(); err != nil {
			log.Printf("[WS] Resync refresh: %v", err)
		}
	}()
}
