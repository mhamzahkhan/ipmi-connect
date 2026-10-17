package aspeed

import (
	"fmt"
	"testing"
)

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
