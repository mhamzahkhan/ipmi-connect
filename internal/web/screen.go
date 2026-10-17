package web

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"log"
	"net/http"

	"github.com/coder/websocket"
)

// Binary messages to the browser.
const (
	// msgFullFrame: [0x02][u16 width][u16 height][width*height RGBA].
	msgFullFrame = 0x02
	// msgRects: [0x04][u16 count] then per rect [u16 x][u16 y][u16 w][u16 h][w*h RGBA].
	msgRects = 0x04
)

// tileSize is the granularity of change detection; it matches ASPEED's 16x16 macroblocks.
const tileSize = 16

type rect struct{ x, y, w, h int }

// UpdateScreen takes the host's current picture (RGBA, width*height*4 bytes) and sends browsers
// what changed: the whole frame on the first update or a resolution change, otherwise only the
// changed tiles. pix is copied, so callers may reuse their buffer.
func (s *Server) UpdateScreen(width, height int, pix []byte) {
	s.screenMu.Lock()
	if s.screen == nil || width != s.screenW || height != s.screenH {
		s.screen = append([]byte(nil), pix...)
		s.screenW, s.screenH = width, height
		full := encodeFullFrame(width, height, s.screen)
		s.screenMu.Unlock()
		s.SetScreenSize(width, height)
		s.broadcastFull(full)
		return
	}
	rects := diffRects(s.screen, pix, width, height)
	if len(rects) == 0 {
		s.screenMu.Unlock()
		return
	}
	copy(s.screen, pix)
	msg := encodeRects(s.screen, width, rects)
	s.screenMu.Unlock()
	s.broadcast(msg)
}

// screenSnapshot returns a copy of the current picture, or nil before the first frame.
func (s *Server) screenSnapshot() (width, height int, pix []byte) {
	s.screenMu.Lock()
	defer s.screenMu.Unlock()
	if s.screen == nil {
		return 0, 0, nil
	}
	return s.screenW, s.screenH, append([]byte(nil), s.screen...)
}

// fullFrameMessage encodes the current picture, or returns nil before the first frame.
func (s *Server) fullFrameMessage() []byte {
	s.screenMu.Lock()
	defer s.screenMu.Unlock()
	if s.screen == nil {
		return nil
	}
	return encodeFullFrame(s.screenW, s.screenH, s.screen)
}

func encodeFullFrame(width, height int, pix []byte) []byte {
	msg := make([]byte, 5+len(pix))
	msg[0] = msgFullFrame
	binary.BigEndian.PutUint16(msg[1:3], uint16(width))
	binary.BigEndian.PutUint16(msg[3:5], uint16(height))
	copy(msg[5:], pix)
	return msg
}

// diffRects returns the tiles that differ between two pictures, merging horizontal runs of
// changed tiles into one rect. Edge tiles are clipped to the picture.
func diffRects(prev, next []byte, width, height int) []rect {
	var rects []rect
	for ty := 0; ty < height; ty += tileSize {
		th := min(tileSize, height-ty)
		start := -1
		for tx := 0; tx < width; tx += tileSize {
			tw := min(tileSize, width-tx)
			if tileDiffers(prev, next, width, tx, ty, tw, th) {
				if start < 0 {
					start = tx
				}
				continue
			}
			if start >= 0 {
				rects = append(rects, rect{start, ty, tx - start, th})
				start = -1
			}
		}
		if start >= 0 {
			rects = append(rects, rect{start, ty, width - start, th})
		}
	}
	return rects
}

func tileDiffers(prev, next []byte, width, x, y, w, h int) bool {
	for row := y; row < y+h; row++ {
		off := (row*width + x) * 4
		if !bytes.Equal(prev[off:off+w*4], next[off:off+w*4]) {
			return true
		}
	}
	return false
}

func encodeRects(pix []byte, width int, rects []rect) []byte {
	size := 3
	for _, r := range rects {
		size += 8 + r.w*r.h*4
	}
	msg := make([]byte, 3, size)
	msg[0] = msgRects
	binary.BigEndian.PutUint16(msg[1:3], uint16(len(rects)))
	for _, r := range rects {
		msg = binary.BigEndian.AppendUint16(msg, uint16(r.x))
		msg = binary.BigEndian.AppendUint16(msg, uint16(r.y))
		msg = binary.BigEndian.AppendUint16(msg, uint16(r.w))
		msg = binary.BigEndian.AppendUint16(msg, uint16(r.h))
		for row := r.y; row < r.y+r.h; row++ {
			off := (row*width + r.x) * 4
			msg = append(msg, pix[off:off+r.w*4]...)
		}
	}
	return msg
}

// handleScreenshot serves the current picture as PNG.
func (s *Server) handleScreenshot(w http.ResponseWriter, _ *http.Request) {
	width, height, pix := s.screenSnapshot()
	if pix == nil {
		http.Error(w, "no video received yet", http.StatusServiceUnavailable)
		return
	}
	img := &image.NRGBA{Pix: pix, Stride: width * 4, Rect: image.Rect(0, 0, width, height)}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Printf("[Server] Screenshot: %v", err)
		http.Error(w, "encoding failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(buf.Bytes())
}

// queueFullFrame replaces whatever a browser has queued with the current full picture.
func (c *wsClient) queueFullFrame(full []byte) {
	if full == nil {
		return
	}
	c.drain()
	select {
	case c.out <- outMessage{typ: websocket.MessageBinary, data: full}:
	default:
	}
}
