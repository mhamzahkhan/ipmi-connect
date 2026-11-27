package aspeed

import (
	"errors"
	"fmt"
	"testing"
)

// FuzzDecode decodes arbitrary bitstreams at arbitrary geometry, twice on one decoder so the
// second pass starts from the first's state (palette, saved YUV, picture). Every input must
// decode or fail cleanly, and a decoded picture must be exactly width*height*4 bytes.
func FuzzDecode(f *testing.F) {
	for i, fr := range readRecording(f, "testdata/c6220-pattern-1024x768.bin") {
		if i >= 4 {
			break
		}
		h := fr.header
		f.Add(uint16(h.Width), uint16(h.Height), h.Mode420, h.LumaSelector, h.ChromaSelector, h.AdvanceSelector, false, fr.bitstream)
	}
	f.Add(uint16(720), uint16(400), byte(1), byte(4), byte(7), byte(0), true, make([]byte, 64))
	f.Add(uint16(17), uint16(9), byte(0), byte(15), byte(16), byte(255), false, []byte{0, 0, 0, 0xa0, 0xff, 0xff, 0xff, 0xff, 1, 2, 3, 4})
	f.Add(uint16(800), uint16(608), byte(1), byte(3), byte(3), byte(3), false, []byte{0, 0, 0, 0x90, 0, 0, 0, 0})
	f.Add(uint16(MaxWidth+1), uint16(8), byte(0), byte(0), byte(0), byte(0), false, make([]byte, 16))
	// A fresh decoder per input, without rebuilding the Huffman tables (read-only once built)
	// or allocating a new YUV buffer each time: inputs run one at a time within a process.
	fresh, yuv := NewDecoder(), make([]byte, yuvBufferCapacity)
	f.Fuzz(func(t *testing.T, w, h uint16, mode420, luma, chroma, advance byte, reference bool, data []byte) {
		d := *fresh
		clear(yuv)
		d.yuv = yuv
		d.ReferenceSemantics = reference
		hdr := Header{Width: int(w), Height: int(h), Mode420: mode420, LumaSelector: luma, ChromaSelector: chroma, AdvanceSelector: advance}
		for pass := 0; pass < 2; pass++ {
			err := d.Decode(hdr, data)
			switch {
			case int(w) > MaxWidth || int(h) > MaxHeight:
				if !errors.Is(err, ErrFrameTooLarge) {
					t.Fatalf("%dx%d: err = %v, want ErrFrameTooLarge", w, h, err)
				}
				return
			case len(data) < 8:
				if !errors.Is(err, ErrShortBitstream) {
					t.Fatalf("%d bytes: err = %v, want ErrShortBitstream", len(data), err)
				}
				return
			case err != nil:
				t.Fatalf("Decode: %v", err)
			}
			if got := len(d.Frame()); got != int(w)*int(h)*4 {
				t.Fatalf("%dx%d picture is %d bytes", w, h, got)
			}
			hdr.Mode420 ^= 1 // the second pass reads the first's state in the other chroma mode
		}
	})
}

// fuzzSizes must match sizes in tools/gen-aspeed-fuzz.mjs (research repo).
var fuzzSizes = [][2]int{{720, 400}, {640, 480}, {800, 608}, {1024, 768}, {64, 48}, {100, 37}, {17, 9}, {8, 8}, {1280, 1024}}

// xorshift32 is the generator shared with the JS fuzz script.
type xorshift32 uint32

func (x *xorshift32) next() uint32 {
	v := uint32(*x)
	v ^= v << 13
	v ^= v >> 17
	v ^= v << 5
	*x = xorshift32(v)
	return v
}

// Random streams decoded by Aspeed's JS decoder (digests in testdata/fuzz.golden) must decode
// identically here. This reaches every block type, both chroma modes, all table selectors, edge
// macroblocks and runs past the end of the data, which the recorded stream alone does not.
func TestDecoderMatchesReferenceOnRandomStreams(t *testing.T) {
	golden := readGolden(t, "testdata/fuzz.golden")
	line := 0
	for c := 0; line < len(golden); c++ {
		seed := xorshift32(uint32(c+1) * 2654435761)
		if seed == 0 {
			seed = 1
		}
		size := fuzzSizes[seed.next()%uint32(len(fuzzSizes))]
		mode420 := byte(seed.next() & 1)
		frames := 1 + int(seed.next()%4)

		d := NewDecoder()
		d.ReferenceSemantics = true
		for f := 0; f < frames; f++ {
			h := Header{
				Width: size[0], Height: size[1], Mode420: mode420,
				LumaSelector:    byte(seed.next() % 16),
				ChromaSelector:  byte(seed.next() % 16),
				AdvanceSelector: byte(seed.next() % 16),
			}
			data := make([]byte, 8+seed.next()%4000)
			sparse := seed.next()&1 == 1
			for i := range data {
				r := seed.next()
				if sparse && r&0x300 != 0 {
					continue
				}
				data[i] = byte(r)
			}

			result := "error"
			if err := d.Decode(h, data); err == nil {
				result = digest(d.Frame())
			}
			got := fmt.Sprintf("%d %d %dx%d %s", c, f, size[0], size[1], result)
			if got != golden[line] {
				t.Fatalf("case %d frame %d (mode420=%d luma=%d chroma=%d adv=%d, %d bytes) differs:\n got  %s\n want %s",
					c, f, mode420, h.LumaSelector, h.ChromaSelector, h.AdvanceSelector, len(data), got, golden[line])
			}
			line++
		}
	}
}
