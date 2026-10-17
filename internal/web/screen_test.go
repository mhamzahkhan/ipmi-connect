package web

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"ipmi-connect/internal/bmc"
)

func solid(w, h int, v byte) []byte {
	p := make([]byte, w*h*4)
	for i := range p {
		p[i] = v
	}
	return p
}

func setPixel(p []byte, w, x, y int, v byte) {
	copy(p[(y*w+x)*4:], []byte{v, v, v, 255})
}

func TestDiffRects(t *testing.T) {
	const w, h = 64, 40 // 4x3 tiles of 16, the last row partial (8 rows)
	base := solid(w, h, 10)

	tests := []struct {
		name  string
		edit  func(p []byte)
		rects []rect
	}{
		{"identical", func([]byte) {}, nil},
		{"one pixel marks its tile", func(p []byte) { setPixel(p, w, 20, 5, 99) }, []rect{{16, 0, 16, 16}}},
		{"adjacent tiles in a row merge", func(p []byte) {
			setPixel(p, w, 1, 1, 99)
			setPixel(p, w, 17, 1, 99)
		}, []rect{{0, 0, 32, 16}}},
		{"separate tiles stay separate", func(p []byte) {
			setPixel(p, w, 1, 1, 99)
			setPixel(p, w, 49, 1, 99)
		}, []rect{{0, 0, 16, 16}, {48, 0, 16, 16}}},
		{"partial bottom row is clipped", func(p []byte) { setPixel(p, w, 63, 39, 99) }, []rect{{48, 32, 16, 8}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next := append([]byte(nil), base...)
			tt.edit(next)
			if got := diffRects(base, next, w, h); !reflect.DeepEqual(got, tt.rects) {
				t.Errorf("rects %v, want %v", got, tt.rects)
			}
		})
	}
}

func TestEncodeRectsCarriesPixels(t *testing.T) {
	const w, h = 32, 16
	pix := solid(w, h, 0)
	setPixel(pix, w, 17, 0, 200)
	msg := encodeRects(pix, w, []rect{{16, 0, 16, 16}})

	if msg[0] != msgRects || binary.BigEndian.Uint16(msg[1:3]) != 1 {
		t.Fatalf("header % x", msg[:3])
	}
	hdr := msg[3:11]
	if x, y, rw, rh := binary.BigEndian.Uint16(hdr[0:]), binary.BigEndian.Uint16(hdr[2:]), binary.BigEndian.Uint16(hdr[4:]), binary.BigEndian.Uint16(hdr[6:]); x != 16 || y != 0 || rw != 16 || rh != 16 {
		t.Fatalf("rect %d,%d %dx%d", x, y, rw, rh)
	}
	body := msg[11:]
	if len(body) != 16*16*4 || body[4] != 200 { // pixel (17,0) is the rect's second pixel
		t.Errorf("rect body len %d, pixel1 R=%d", len(body), body[4])
	}
}

func TestUpdateScreenSendsFullThenChanges(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{})
	c := newWSClient()
	srv.addClient(c)
	const w, h = 32, 16

	srv.UpdateScreen(w, h, solid(w, h, 1))
	if m := <-c.out; m.data[0] != msgFullFrame {
		t.Fatalf("first update sent type %#x, want full frame", m.data[0])
	}

	srv.UpdateScreen(w, h, solid(w, h, 1))
	if len(c.out) != 0 {
		t.Fatal("unchanged picture produced a message")
	}

	next := solid(w, h, 1)
	setPixel(next, w, 0, 0, 7)
	srv.UpdateScreen(w, h, next)
	if m := <-c.out; m.data[0] != msgRects {
		t.Fatalf("change sent type %#x, want rects", m.data[0])
	}

	srv.UpdateScreen(16, 16, solid(16, 16, 1)) // resolution change
	if m := <-c.out; m.data[0] != msgFullFrame {
		t.Fatalf("resize sent type %#x, want full frame", m.data[0])
	}
}

func TestUpdateScreenCopiesTheCallersBuffer(t *testing.T) {
	srv := NewServer(0, bmc.SessionState{})
	pix := solid(16, 16, 5)
	srv.UpdateScreen(16, 16, pix)
	pix[0] = 99 // the decoder reuses its buffer for the next frame
	if _, _, got := srv.screenSnapshot(); got[0] != 5 {
		t.Error("framebuffer aliases the caller's buffer")
	}
}

func TestScreenshotEndpoint(t *testing.T) {
	srv, h, _ := newGuardedServer(t)
	pix := solid(32, 16, 0)
	setPixel(pix, 32, 3, 2, 255)
	srv.UpdateScreen(32, 16, pix)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(srv, httptest.NewRequest(http.MethodGet, "/api/screenshot.png", nil)))
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("status %d type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 16 {
		t.Errorf("size %v", b)
	}
	if r, _, _, _ := img.At(3, 2).RGBA(); r>>8 != 255 {
		t.Errorf("pixel (3,2) red = %d", r>>8)
	}
}

func TestScreenshotBeforeFirstFrame(t *testing.T) {
	srv, h, _ := newGuardedServer(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, authed(srv, httptest.NewRequest(http.MethodGet, "/api/screenshot.png", nil)))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", rec.Code)
	}
}
