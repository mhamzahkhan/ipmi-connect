package supermicro

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func hermon(kind, depth byte, tiles int, payload []byte) []byte {
	b := make([]byte, 10)
	b[0], b[1] = kind, depth
	binary.BigEndian.PutUint32(b[2:6], uint32(tiles))
	binary.BigEndian.PutUint32(b[6:10], uint32(10+len(payload)))
	return append(b, payload...)
}

func tile(row, col byte, pixel []byte, n int) []byte {
	return append([]byte{0, 0, 0, 0, row, col}, bytes.Repeat(pixel, n)...)
}

func rgbaAt(pix []byte, w, x, y int) [4]byte {
	i := (y*w + x) * 4
	return [4]byte{pix[i], pix[i+1], pix[i+2], pix[i+3]}
}

var (
	red   = [4]byte{255, 0, 0, 255}
	white = [4]byte{255, 255, 255, 255}
	black = [4]byte{0, 0, 0, 255}
)

// Vectors from smkvm's live-verified Hermon tests.
func TestDecodeHermon(t *testing.T) {
	t.Run("tile lands at row 1, col 2", func(t *testing.T) {
		fb := newFrame(64, 32)
		if err := decodeHermon(hermon(0, 0, 1, tile(1, 2, []byte{0x00, 0x7C}, 256)), fb); err != nil {
			t.Fatal(err)
		}
		if got := rgbaAt(fb.pix, 64, 32, 16); got != red {
			t.Errorf("(32,16) = %v, want red", got)
		}
		if got := rgbaAt(fb.pix, 64, 47, 31); got != red {
			t.Errorf("(47,31) = %v, want red", got)
		}
		if got := rgbaAt(fb.pix, 64, 31, 16); got != black {
			t.Errorf("(31,16) = %v, want untouched black", got)
		}
	})

	t.Run("overhanging tile is clipped", func(t *testing.T) {
		fb := newFrame(10, 6)
		if err := decodeHermon(hermon(0, 0, 1, tile(0, 0, []byte{0xFF, 0x7F}, 256)), fb); err != nil {
			t.Fatal(err)
		}
		if got := rgbaAt(fb.pix, 10, 9, 5); got != white {
			t.Errorf("(9,5) = %v, want white", got)
		}
	})

	t.Run("raw frame, 15-bit", func(t *testing.T) {
		fb := newFrame(2, 1)
		if err := decodeHermon(hermon(1, 0, 0, []byte{0x1F, 0x00, 0xE0, 0x03}), fb); err != nil {
			t.Fatal(err)
		}
		if got := rgbaAt(fb.pix, 2, 0, 0); got != [4]byte{0, 0, 255, 255} {
			t.Errorf("pixel 0 = %v, want blue", got)
		}
		if got := rgbaAt(fb.pix, 2, 1, 0); got != [4]byte{0, 255, 0, 255} {
			t.Errorf("pixel 1 = %v, want green", got)
		}
	})

	t.Run("6-bit low-quality tile", func(t *testing.T) {
		fb := newFrame(16, 16)
		// R=3, G=0, B=1 in the 2-bit fields: 0b00110001
		if err := decodeHermon(hermon(0, 1, 1, tile(0, 0, []byte{0x31}, 256)), fb); err != nil {
			t.Fatal(err)
		}
		if got := rgbaAt(fb.pix, 16, 5, 5); got != [4]byte{255, 0, 85, 255} {
			t.Errorf("pixel = %v, want (255,0,85)", got)
		}
	})

	t.Run("tile count beyond the data is bounded", func(t *testing.T) {
		fb := newFrame(32, 16)
		if err := decodeHermon(hermon(0, 0, 1000, tile(0, 1, []byte{0x00, 0x7C}, 256)), fb); err != nil {
			t.Fatal(err)
		}
		if got := rgbaAt(fb.pix, 32, 16, 0); got != red {
			t.Errorf("the one real tile was not drawn: %v", got)
		}
	})

	t.Run("short header", func(t *testing.T) {
		if err := decodeHermon([]byte{0, 0, 0}, newFrame(8, 8)); err == nil {
			t.Error("want error")
		}
	})
}
