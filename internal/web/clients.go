package web

import (
	"context"
	"time"

	"github.com/coder/websocket"
)

const (
	// clientQueueLen bounds how far one browser may fall behind before it is resynchronised.
	clientQueueLen = 16
	// clientWriteTimeout drops a browser that stops reading.
	clientWriteTimeout = 5 * time.Second
)

type outMessage struct {
	typ  websocket.MessageType
	data []byte
}

// wsClient is one connected browser. A dedicated writer drains out, so a slow browser only
// delays itself.
type wsClient struct {
	out  chan outMessage // video and frame updates; may be drained when the browser falls behind
	ctrl chan outMessage // replies to the browser (e.g. paste results); never drained, sent first
}

// ctrlQueueLen bounds pending control replies; they are rare and small.
const ctrlQueueLen = 8

func newWSClient() *wsClient {
	return &wsClient{out: make(chan outMessage, clientQueueLen), ctrl: make(chan outMessage, ctrlQueueLen)}
}

// writeLoop sends queued messages until ctx ends or a write fails; failure calls cancel so the
// connection's reader stops too.
func (c *wsClient) writeLoop(ctx context.Context, cancel context.CancelFunc, conn *websocket.Conn) {
	defer cancel()
	write := func(m outMessage) bool {
		wctx, wcancel := context.WithTimeout(ctx, clientWriteTimeout)
		defer wcancel()
		return conn.Write(wctx, m.typ, m.data) == nil
	}
	for {
		select { // control replies first
		case m := <-c.ctrl:
			if !write(m) {
				return
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return
		case m := <-c.ctrl:
			if !write(m) {
				return
			}
		case m := <-c.out:
			if !write(m) {
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

// broadcast queues a change (rects) for every browser. A browser whose queue is full has fallen
// behind; changes only make sense on top of everything before them, so its backlog is replaced
// with the current full picture.
func (s *Server) broadcast(frame []byte) {
	msg := outMessage{typ: websocket.MessageBinary, data: frame}
	var full []byte
	for _, c := range s.clientList() {
		select {
		case c.out <- msg:
		default:
			if full == nil {
				full = s.fullFrameMessage()
			}
			c.queueFullFrame(full)
		}
	}
}

// broadcastFull queues a full frame. Each one supersedes everything before it, so a browser keeps
// only the newest instead of a backlog of multi-megabyte frames.
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
