package supermicro

import (
	"crypto/aes"
	"crypto/cipher"
)

// The vendor client encrypts mouse events with AES-128-CBC under a fixed key and IV
// (found in the X7SB3's viewer library). They are the NIST SP 800-38A example
// values: this is obfuscation, not security.
var (
	mouseKey = [16]byte{0x2b, 0x7e, 0x15, 0x16, 0x28, 0xae, 0xd2, 0xa6, 0xab, 0xf7, 0x15, 0x88, 0x09, 0xcf, 0x4f, 0x3c}
	mouseIV  = [16]byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}
)

func encryptMouseBlock(plain [16]byte) [16]byte {
	block, err := aes.NewCipher(mouseKey[:])
	if err != nil {
		panic(err) // a 16-byte key is always valid
	}
	var out [16]byte
	cipher.NewCBCEncrypter(block, mouseIV[:]).CryptBlocks(out[:], plain[:])
	return out
}
