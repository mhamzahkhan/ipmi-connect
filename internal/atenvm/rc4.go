package atenvm

import (
	"crypto/rc4"
	"errors"
)

// DefaultRC4Key is the 20-byte ASCII key hardcoded in Supermicro ATEN BMC firmware
// (`mykey` at virtual address 0x1035c in /bin/usb and in libSharedLibrary).
const DefaultRC4Key = "BX80570E3110Q814A447"

// RC4Crypt encrypts or decrypts data using the RC4 stream cipher with the specified key.
// The RC4 key scheduling algorithm (KSA) is freshly initialized for each packet.
func RC4Crypt(key, data []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, errors.New("empty RC4 key")
	}
	cipher, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(data))
	cipher.XORKeyStream(out, data)
	return out, nil
}

// RC4CryptDefault encrypts or decrypts data using the DefaultRC4Key.
func RC4CryptDefault(data []byte) ([]byte, error) {
	return RC4Crypt([]byte(DefaultRC4Key), data)
}
