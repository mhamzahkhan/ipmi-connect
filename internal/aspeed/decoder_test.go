package aspeed

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"
)

// frameMessage is the 0x03 WebSocket message ipmi-connect used to send to the browser:
// [0x03][mode420][luma][chroma][4 reserved][u16 width][u16 height][bitstream].
type frameMessage struct {
	header    Header
	bitstream []byte
}

func readRecording(t *testing.T, path string) []frameMessage {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var frames []frameMessage
	for len(data) > 0 {
		n := int(binary.BigEndian.Uint32(data))
		msg := data[4 : 4+n]
		data = data[4+n:]
		frames = append(frames, frameMessage{
			header: Header{
				Mode420:        msg[1],
				LumaSelector:   msg[2],
				ChromaSelector: msg[3],
				Width:          int(binary.BigEndian.Uint16(msg[8:10])),
				Height:         int(binary.BigEndian.Uint16(msg[10:12])),
			},
			bitstream: msg[12:],
		})
	}
	return frames
}

func readGolden(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The live C6220 stream (720x400 BIOS text, YUV420, luma table 4, chroma table 7) must decode
// to exactly what Aspeed's JS decoder produced for every frame.
func TestDecoderMatchesReferenceOnRecordedStream(t *testing.T) {
	frames := readRecording(t, "testdata/c6220-text-720x400.bin")
	golden := readGolden(t, "testdata/c6220-text-720x400.golden")
	if len(frames) != len(golden) {
		t.Fatalf("%d frames but %d golden lines", len(frames), len(golden))
	}

	d := NewDecoder()
	for i, f := range frames {
		if err := d.Decode(f.header, f.bitstream); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		want := fmt.Sprintf("%d %dx%d %s", i, f.header.Width, f.header.Height, digest(d.Frame()))
		if want != golden[i] {
			t.Fatalf("frame %d differs from the reference:\n got  %s\n want %s", i, want, golden[i])
		}
	}
}

// BenchmarkRecordedStream decodes the recorded C6220 stream; ns/op is per frame.
func BenchmarkRecordedStream(b *testing.B) {
	data, err := os.ReadFile("testdata/c6220-text-720x400.bin")
	if err != nil {
		b.Fatal(err)
	}
	var frames []frameMessage
	for len(data) > 0 {
		n := int(binary.BigEndian.Uint32(data))
		msg := data[4 : 4+n]
		data = data[4+n:]
		frames = append(frames, frameMessage{
			header:    Header{Mode420: msg[1], LumaSelector: msg[2], ChromaSelector: msg[3], Width: int(binary.BigEndian.Uint16(msg[8:10])), Height: int(binary.BigEndian.Uint16(msg[10:12]))},
			bitstream: msg[12:],
		})
	}
	d := NewDecoder()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := frames[i%len(frames)]
		_ = d.Decode(f.header, f.bitstream)
	}
}
