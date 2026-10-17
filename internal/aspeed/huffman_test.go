package aspeed

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// The Go builder must produce byte-identical lookup tables to Aspeed's JS builder.
func TestHuffmanTablesMatchReference(t *testing.T) {
	sources := map[string]huffmanSource{
		"dcLuminance":   dcLuminanceSource,
		"acLuminance":   acLuminanceSource,
		"dcChrominance": dcChrominanceSource,
		"acChrominance": acChrominanceSource,
	}
	for name, src := range sources {
		t.Run(name, func(t *testing.T) {
			ht := buildHuffmanTable(src)
			h := sha256.New()
			_ = binary.Write(h, binary.LittleEndian, ht.v[:])
			_ = binary.Write(h, binary.LittleEndian, ht.len[:])
			_ = binary.Write(h, binary.LittleEndian, ht.minorCode[:])
			if got, want := hex.EncodeToString(h.Sum(nil)), huffmanFingerprints[name]; got != want {
				t.Errorf("fingerprint %s, want %s", got, want)
			}
		})
	}
}
