package aspeed

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
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

func readRecording(t testing.TB, path string) []frameMessage {
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

// A live C6220 stream (1024x768 console test pattern: colour bars, coloured text, an animated
// bar) must decode to exactly what Aspeed's JS decoder produced for every frame, both in
// reference mode and with the default (production) deviations, which only engage on truncated
// input or resizes.
func TestDecoderMatchesReferenceOnRecordedStream(t *testing.T) {
	frames := readRecording(t, "testdata/c6220-pattern-1024x768.bin")
	golden := readGolden(t, "testdata/c6220-pattern-1024x768.golden")
	if len(frames) != len(golden) {
		t.Fatalf("%d frames but %d golden lines", len(frames), len(golden))
	}

	for _, reference := range []bool{true, false} {
		t.Run(fmt.Sprintf("reference=%v", reference), func(t *testing.T) {
			d := NewDecoder()
			d.ReferenceSemantics = reference
			for i, f := range frames {
				if err := d.Decode(f.header, f.bitstream); err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				want := fmt.Sprintf("%d %dx%d %s", i, f.header.Width, f.header.Height, digest(d.Frame()))
				if want != golden[i] {
					t.Fatalf("frame %d differs from the reference:\n got  %s\n want %s", i, want, golden[i])
				}
			}
		})
	}
}

// BenchmarkRecordedStream decodes the recorded C6220 stream; ns/op is per frame.
func BenchmarkRecordedStream(b *testing.B) {
	frames := readRecording(b, "testdata/c6220-pattern-1024x768.bin")
	d := NewDecoder()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := frames[i%len(frames)]
		_ = d.Decode(f.header, f.bitstream)
	}
}

func TestDecodeRejectsOversizedFrames(t *testing.T) {
	d := NewDecoder()
	err := d.Decode(Header{Width: 65535, Height: 65535}, make([]byte, 64))
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("err = %v, want ErrFrameTooLarge", err)
	}
	if d.out != nil {
		t.Error("allocated a buffer for a rejected frame")
	}
}

// painted counts pixels that are no longer the initial white.
func painted(pix []byte) int {
	n := 0
	for i := 0; i < len(pix); i += 4 {
		if pix[i] != 0xff || pix[i+1] != 0xff || pix[i+2] != 0xff {
			n++
		}
	}
	return n
}

// A truncated frame (no frame-end code) makes the reference decode zero words past the data,
// painting blocks it never received. The default decoder stops when the data runs out.
func TestDecoderStopsAtEndOfTruncatedData(t *testing.T) {
	h := Header{Width: 720, Height: 400, Mode420: 1, LumaSelector: 4, ChromaSelector: 7}
	data := make([]byte, 64) // header 0 everywhere: JPEG blocks, never a frame-end code

	ref := NewDecoder()
	ref.ReferenceSemantics = true
	if err := ref.Decode(h, data); err != nil {
		t.Fatal(err)
	}
	d := NewDecoder()
	if err := d.Decode(h, data); err != nil {
		t.Fatal(err)
	}

	if got, refGot := painted(d.Frame()), painted(ref.Frame()); got >= refGot {
		t.Errorf("default decoder painted %d pixels, reference %d: it should stop at the end of the data", got, refGot)
	}
}

// After a resolution change the old picture must not be reinterpreted at the new stride.
func TestDecoderStartsCleanAfterResize(t *testing.T) {
	frameEndOnly := []byte{0, 0, 0, 0x90, 0, 0, 0, 0} // first word's top nibble is 9: frame end
	d := NewDecoder()
	if err := d.Decode(Header{Width: 64, Height: 64}, make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	if painted(d.Frame()) == 0 {
		t.Fatal("setup: first frame painted nothing")
	}
	if err := d.Decode(Header{Width: 32, Height: 16}, frameEndOnly); err != nil {
		t.Fatal(err)
	}
	if n := painted(d.Frame()); n != 0 {
		t.Errorf("%d pixels carried over from the previous mode", n)
	}
}
