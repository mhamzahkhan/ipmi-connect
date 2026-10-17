package supermicro

import (
	"encoding/binary"
	"errors"
)

// frame is the host picture as RGBA.
type frame struct {
	w, h int
	pix  []byte
}

func newFrame(w, h int) *frame {
	f := &frame{w: w, h: h, pix: make([]byte, w*h*4)}
	for i := 3; i < len(f.pix); i += 4 {
		f.pix[i] = 0xff
	}
	return f
}

const hermonTile = 16

// decodeHermon applies one Hermon (WPCM450, encoding 0x59) payload to the picture.
//
// Header: type (1 = raw full frame, else tiles), depth (0 = 15-bit RGB555 at 2 bytes per pixel,
// otherwise 6-bit RGB222 at 1 byte, which only the vendor's own decoder handles), u32 tile
// count, u32 total length. Each tile is 4 unused bytes, row, column and 16x16 pixels; tiles at
// the right and bottom edges are clipped.
func decodeHermon(data []byte, f *frame) error {
	if len(data) < 10 {
		return errors.New("hermon: payload shorter than its header")
	}
	raw, bpp := data[0] == 1, 2
	if data[1] != 0 {
		bpp = 1
	}
	tiles := int(binary.BigEndian.Uint32(data[2:6]))
	body := data[10:]

	if raw {
		n := min(len(body)/bpp, f.w*f.h)
		for i := 0; i < n; i++ {
			putPixel(f, i%f.w, i/f.w, body[i*bpp:], bpp)
		}
		return nil
	}

	tileLen := 6 + hermonTile*hermonTile*bpp
	tiles = min(tiles, len(body)/tileLen) // never trust the count beyond the data
	for t := 0; t < tiles; t++ {
		tb := body[t*tileLen : (t+1)*tileLen]
		x0, y0 := int(tb[5])*hermonTile, int(tb[4])*hermonTile
		px := tb[6:]
		for y := 0; y < hermonTile && y0+y < f.h; y++ {
			for x := 0; x < hermonTile && x0+x < f.w; x++ {
				putPixel(f, x0+x, y0+y, px[(y*hermonTile+x)*bpp:], bpp)
			}
		}
	}
	return nil
}

func putPixel(f *frame, x, y int, p []byte, bpp int) {
	var r, g, b byte
	if bpp == 2 {
		v := uint16(p[0]) | uint16(p[1])<<8
		r, g, b = expand5(v>>10), expand5(v>>5), expand5(v)
	} else {
		r, g, b = (p[0]>>4&3)*85, (p[0]>>2&3)*85, (p[0]&3)*85
	}
	i := (y*f.w + x) * 4
	f.pix[i], f.pix[i+1], f.pix[i+2], f.pix[i+3] = r, g, b, 0xff
}

// expand5 widens a 5-bit channel to 8 bits so full intensity is 255.
func expand5(v uint16) byte {
	c := byte(v & 31)
	return c<<3 | c>>2
}
