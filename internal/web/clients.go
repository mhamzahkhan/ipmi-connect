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
	clients := s.clientList()
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

// broadcastFull queues a self-contained frame (raw RGBA). Each one supersedes the last, so a
// browser keeps only the newest instead of a backlog of multi-megabyte frames.
func (s *Server) broadcastFull(frame []byte) {
	msg := outMessage{typ: websocket.MessageBinary, data: frame}
	for _, c := range s.clientList() {
		c.drain()
		select {
		case c.out <- msg:
		default: // the writer refilled the queue meanwhile; it already has a newer state
		}
	}
}

func (s *Server) clientList() []*wsClient {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	clients := make([]*wsClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	return clients
}

// requestResync asks the console for a full frame. Requests closer together than resyncInterval
// are postponed and coalesced, never dropped: a browser whose backlog was discarded has no other
// way back to a complete picture.
func (s *Server) requestResync() {
	s.clientsMu.Lock()
	if s.Console == nil || s.resyncPending {
		s.clientsMu.Unlock()
		return
	}
	wait := resyncInterval - time.Since(s.lastResync)
	if wait <= 0 {
		s.lastResync = time.Now()
		s.clientsMu.Unlock()
		go s.refreshForResync()
		return
	}
	s.resyncPending = true
	s.clientsMu.Unlock()

	time.AfterFunc(wait, func() {
		s.clientsMu.Lock()
		s.resyncPending = false
		s.lastResync = time.Now()
		s.clientsMu.Unlock()
		s.refreshForResync()
	})
}

func (s *Server) refreshForResync() {
	if err := s.Console.RequestFrameRefresh(); err != nil {
		log.Printf("[WS] Resync refresh: %v", err)
	}
}
