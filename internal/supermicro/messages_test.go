package supermicro

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"io"
	"reflect"
	"runtime"
	"strings"
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

// zeros is an endless stream of zero bytes.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// rectHeader is a rect whose data length is claimed but not included.
func rectHeader(length uint32) []byte {
	b := rect(0, 0, 1024, 768, 0x59, nil)
	binary.BigEndian.PutUint32(b[16:], length)
	return b
}

func TestReadFramebufferUpdateCapsTotal(t *testing.T) {
	half := uint32(maxUpdateData/2 + 1) // each rect is plausible, the two together are not
	in := io.MultiReader(
		bytes.NewReader([]byte{0x00, 0x00, 0, 2}),
		bytes.NewReader(rectHeader(half)), io.LimitReader(zeros{}, int64(half)),
		bytes.NewReader(rectHeader(half)), zeros{},
	)
	_, err := readMessage(bufio.NewReader(in), 3)
	if err == nil || !strings.Contains(err.Error(), "framebuffer update") {
		t.Fatalf("err = %v, want the per-update cap", err)
	}
}

// allocatedBy reports how many bytes f allocates.
func allocatedBy(f func()) uint64 {
	var m runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m)
	before := m.TotalAlloc
	f()
	runtime.ReadMemStats(&m)
	return m.TotalAlloc - before
}

func TestReadFramebufferUpdateDoesNotTrustCounts(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
	}{
		{"65535 rects claimed, none sent", []byte{0x00, 0x00, 0xff, 0xff}},
		{"large rect claimed, few bytes sent", append(fbu(rectHeader(maxRectData)), 1, 2, 3)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var err error
			got := allocatedBy(func() { _, err = readMessage(bufio.NewReader(bytes.NewReader(tt.in)), 3) })
			if err == nil {
				t.Fatal("want error")
			}
			if got > 1<<20 {
				t.Errorf("allocated %d bytes for a truncated update", got)
			}
		})
	}
}

func TestReadBytes(t *testing.T) {
	data := bytes.Repeat([]byte{0xa5}, 3*readChunk+7)
	tests := []struct {
		name    string
		in      []byte
		n       int
		wantErr error
	}{
		{"small", data[:10], 10, nil},
		{"large, read in chunks", data, len(data), nil},
		{"empty stream", nil, 2 * readChunk, io.EOF},
		{"large, truncated", data[:readChunk+1], len(data), io.ErrUnexpectedEOF},
		{"small, truncated", data[:3], 10, io.ErrUnexpectedEOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := readBytes(bytes.NewReader(tt.in), tt.n)
			if err != tt.wantErr {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if err == nil && !bytes.Equal(got, tt.in[:tt.n]) {
				t.Errorf("got %d bytes, want %d", len(got), tt.n)
			}
		})
	}
	// A claimed length allocates only as far as the data that arrived.
	b, _ := readBytes(bytes.NewReader(data[:10]), maxRectData)
	if cap(b) > readChunk {
		t.Errorf("allocated %d bytes for 10 bytes of data", cap(b))
	}
}
