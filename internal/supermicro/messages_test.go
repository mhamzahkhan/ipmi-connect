package supermicro

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func fbu(rects ...[]byte) []byte {
	b := []byte{0x00, 0x00, 0, byte(len(rects))}
	for _, r := range rects {
		b = append(b, r...)
	}
	return b
}

func rect(x, y, w, h uint16, enc int32, data []byte) []byte {
	b := make([]byte, 20)
	binary.BigEndian.PutUint16(b[0:], x)
	binary.BigEndian.PutUint16(b[2:], y)
	binary.BigEndian.PutUint16(b[4:], w)
	binary.BigEndian.PutUint16(b[6:], h)
	binary.BigEndian.PutUint32(b[8:], uint32(enc))
	binary.BigEndian.PutUint32(b[16:], uint32(len(data)))
	return append(b, data...)
}

func TestReadMessage(t *testing.T) {
	cursor := make([]byte, 21)
	cursor[0] = 0x04
	tests := []struct {
		name string
		in   []byte
		want any
	}{
		{"framebuffer update", fbu(rect(0, 0, 800, 600, 0x59, []byte{1, 2, 3})),
			FramebufferUpdate{Rects: []Rect{{W: 800, H: 600, Encoding: 0x59, Data: []byte{1, 2, 3}}}}},
		{"server keep-alive", []byte{0x16, 0x00}, ServerKeepAlive{}},
		{"mouse info (3 bytes on Hermon)", []byte{0x37, 1, 2, 1}, MouseInfo{Encrypted: true, Mode: 2, PowerOn: true, HasPower: true}},
		{"privilege", append([]byte{0x39, 0, 0, 0, 1, 0, 0, 0, 4}, append([]byte("1 ADMIN 192.0.2.1"), make([]byte, 256-17)...)...),
			Privilege{A: 1, B: 4, Text: "1 ADMIN 192.0.2.1"}},
		{"cursor without bitmap", cursor, Ignored{Type: 0x04}},
		{"keyboard info", []byte{0x35, 0, 0, 0, 0, 0}, Ignored{Type: 0x35}},
		{"language", []byte{0x3c, 0, 0, 0, 0, 0, 0, 0, 0}, Ignored{Type: 0x3c}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readMessage(bufio.NewReader(bytes.NewReader(tt.in)), 3)
			if err != nil {
				t.Fatalf("readMessage: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got  %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestReadMessageRejects(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"unknown type", []byte{0x7f, 1, 2, 3}},
		{"oversized rect", fbu(rect(0, 0, 1, 1, 0x59, nil)[:16], []byte{0xff, 0xff, 0xff, 0xff})},
		{"cursor size overflowing u32", func() []byte {
			b := make([]byte, 21)
			b[0] = 0x04
			binary.BigEndian.PutUint32(b[9:], 65536)
			binary.BigEndian.PutUint32(b[13:], 65537)
			binary.BigEndian.PutUint32(b[17:], 1)
			return b
		}()},
		{"truncated", fbu(rect(0, 0, 8, 8, 0x59, []byte{1, 2, 3}))[:20]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := readMessage(bufio.NewReader(bytes.NewReader(tt.in)), 3); err == nil {
				t.Error("want error")
			}
		})
	}
}
