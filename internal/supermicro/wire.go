package supermicro

import (
	"crypto/rand"
	"encoding/binary"
)

// Client-to-server ATEN iKVM messages. Layouts follow the X7SB3's own client library
// and agree with smkvm's live-verified encoders.

// Power codes for the in-band power message (0x1a).
const (
	powerOff     = 0
	powerOn      = 1
	powerReset   = 2
	powerSoftOff = 3
)

// keyEvent: 04 | enc 00 | down | 00 00 | u32 BE USB HID usage | 9 zero bytes (18 bytes).
func keyEvent(down bool, usage uint32) []byte {
	b := make([]byte, 18)
	b[0] = 0x04
	if down {
		b[2] = 1
	}
	binary.BigEndian.PutUint32(b[5:9], usage)
	return b
}

// pointerEvent: 05 | enc 00 | buttons | u16 BE x | u16 BE y | 11 zero bytes (18 bytes).
// Buttons: left 1, middle 2, right 4; wheel up 8, down 16.
func pointerEvent(buttons byte, x, y uint16) []byte {
	b := make([]byte, 18)
	b[0] = 0x05
	b[2] = buttons
	binary.BigEndian.PutUint16(b[3:5], x)
	binary.BigEndian.PutUint16(b[5:7], y)
	return b
}

// encryptedPointerEvent is the form used when the BMC's mouse-info reply sets the encryption
// flag: 05 | 01 | AES block of [buttons, x, y, 11 random bytes].
func encryptedPointerEvent(buttons byte, x, y uint16) []byte {
	var plain [16]byte
	plain[0] = buttons
	binary.BigEndian.PutUint16(plain[1:3], x)
	binary.BigEndian.PutUint16(plain[3:5], y)
	_, _ = rand.Read(plain[5:]) // crypto/rand.Read never fails (Go 1.24+)
	cipher := encryptMouseBlock(plain)
	return append([]byte{0x05, 0x01}, cipher[:]...)
}

// framebufferUpdateRequest: 03 | incremental | u16 x | u16 y | u16 w | u16 h.
func framebufferUpdateRequest(incremental bool, w, h uint16) []byte {
	b := make([]byte, 10)
	b[0] = 0x03
	if incremental {
		b[1] = 1
	}
	binary.BigEndian.PutUint16(b[6:8], w)
	binary.BigEndian.PutUint16(b[8:10], h)
	return b
}

// keepAlive is what the vendor client sends every 3 seconds.
func keepAlive() []byte { return []byte{0x16, 0x01} }

// mouseInfoRequest asks for the 0x37 reply: mouse encryption flag, mouse mode, power status.
func mouseInfoRequest() []byte { return []byte{0x37} }

func powerCommand(code byte) []byte { return []byte{0x1a, code} }

// credentials: user and password fields of 24 bytes each, NUL-padded, values capped at 23 bytes.
func credentials(user, password string) []byte {
	b := make([]byte, 48)
	copy(b[0:23], user)
	copy(b[24:47], password)
	return b
}
