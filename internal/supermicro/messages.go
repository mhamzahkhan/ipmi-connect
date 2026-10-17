package supermicro

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"strings"
)

// maxCursorSide bounds cursor bitmaps (the BMC uses 32x32 or 64x64).
const maxCursorSide = 256

// maxRectData bounds one rectangle's payload (a 1920x1200 frame at 2 bytes per pixel is 4.6 MB).
const maxRectData = 64 << 20

// Server-to-client messages.
type (
	FramebufferUpdate struct{ Rects []Rect }
	Rect              struct {
		X, Y, W, H uint16
		Encoding   int32
		Data       []byte
	}
	ServerKeepAlive struct{}
	// MouseInfo is the 0x37 reply. PowerOn is only meaningful when HasPower (3-byte form).
	MouseInfo struct {
		Encrypted, PowerOn, HasPower bool
		Mode                         byte
	}
	// Privilege is the 0x39 notice; A=1, B=4 means another user has control (view only).
	Privilege struct {
		A, B uint32
		Text string
	}
	// Ignored is a message read and discarded.
	Ignored struct{ Type byte }
)

// readMessage reads one server message. Lengths follow the X7SB3's own client; mouseInfoLen is
// 3 on this firmware (2 on later X9 firmware). An unknown type cannot be skipped safely, so it
// is an error carrying the next bytes for diagnosis.
func readMessage(r *bufio.Reader, mouseInfoLen int) (any, error) {
	typ, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	switch typ {
	case 0x00:
		return readFramebufferUpdate(r)
	case 0x04: // cursor: x, y, w, h, type as u32; type 1 adds a u32 and a w*h*2 bitmap
		hdr, err := readBytes(r, 20)
		if err != nil {
			return nil, err
		}
		if binary.BigEndian.Uint32(hdr[16:20]) == 1 {
			w, h := binary.BigEndian.Uint32(hdr[8:12]), binary.BigEndian.Uint32(hdr[12:16])
			if w > maxCursorSide || h > maxCursorSide {
				return nil, fmt.Errorf("cursor %dx%d is not plausible", w, h)
			}
			if _, err := readBytes(r, 4+int(w*h*2)); err != nil {
				return nil, err
			}
		}
		return Ignored{Type: typ}, nil
	case 0x16:
		_, err := r.ReadByte()
		return ServerKeepAlive{}, err
	case 0x37:
		b, err := readBytes(r, mouseInfoLen)
		if err != nil {
			return nil, err
		}
		mi := MouseInfo{Encrypted: b[0] != 0, Mode: b[1]}
		if len(b) >= 3 {
			mi.HasPower, mi.PowerOn = true, b[2] != 0
		}
		return mi, nil
	case 0x39:
		b, err := readBytes(r, 264)
		if err != nil {
			return nil, err
		}
		return Privilege{
			A:    binary.BigEndian.Uint32(b[0:4]),
			B:    binary.BigEndian.Uint32(b[4:8]),
			Text: strings.TrimRight(string(b[8:]), "\x00"),
		}, nil
	}
	if n, ok := map[byte]int{0x33: 4, 0x35: 5, 0x3c: 8}[typ]; ok {
		_, err := readBytes(r, n)
		return Ignored{Type: typ}, err
	}
	next, _ := r.Peek(min(r.Buffered(), 32))
	return nil, fmt.Errorf("%w %#x (next bytes % x)", errUnknownMessage, typ, next)
}

func readFramebufferUpdate(r *bufio.Reader) (FramebufferUpdate, error) {
	hdr, err := readBytes(r, 3) // padding, u16 rect count
	if err != nil {
		return FramebufferUpdate{}, err
	}
	n := int(binary.BigEndian.Uint16(hdr[1:3]))
	u := FramebufferUpdate{Rects: make([]Rect, 0, n)}
	for i := 0; i < n; i++ {
		b, err := readBytes(r, 20) // x, y, w, h u16; encoding s32; frame mode u32; data length u32
		if err != nil {
			return FramebufferUpdate{}, err
		}
		length := binary.BigEndian.Uint32(b[16:20])
		if length > maxRectData {
			return FramebufferUpdate{}, fmt.Errorf("rect data length %d is not plausible", length)
		}
		data, err := readBytes(r, int(length))
		if err != nil {
			return FramebufferUpdate{}, err
		}
		u.Rects = append(u.Rects, Rect{
			X: binary.BigEndian.Uint16(b[0:2]), Y: binary.BigEndian.Uint16(b[2:4]),
			W: binary.BigEndian.Uint16(b[4:6]), H: binary.BigEndian.Uint16(b[6:8]),
			Encoding: int32(binary.BigEndian.Uint32(b[8:12])),
			Data:     data,
		})
	}
	return u, nil
}
