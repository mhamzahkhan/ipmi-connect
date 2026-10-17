package supermicro

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func h(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

const zeros9 = "000000000000000000"

// Byte layouts from the X7SB3's own client (Ghidra) and smkvm's live-verified encoders.
func TestEncoders(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		// 04 | enc 00 | down | 00 00 | u32 BE HID usage | 9 zero bytes
		{"key A down", keyEvent(true, 0x04), h("040001" + "0000" + "00000004" + zeros9)},
		{"key LCtrl up", keyEvent(false, 0xE0), h("040000" + "0000" + "000000e0" + zeros9)},
		{"pointer left at 0x123,0x45", pointerEvent(1, 0x123, 0x45), h("050001012300450000000000000000000000")},
		{"incremental FBUR 1024x768", framebufferUpdateRequest(true, 1024, 768), h("03010000000004000300")},
		{"full FBUR", framebufferUpdateRequest(false, 0x27f, 0x1df), h("030000000000027f01df")},
		{"keep-alive", keepAlive(), h("1601")},
		{"mouse info request", mouseInfoRequest(), h("37")},
		{"power soft off", powerCommand(powerSoftOff), h("1a03")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !bytes.Equal(tt.got, tt.want) {
				t.Errorf("got  % x\nwant % x", tt.got, tt.want)
			}
		})
	}
}

func TestEncodedMessageLengths(t *testing.T) {
	if n := len(keyEvent(true, 4)); n != 18 {
		t.Errorf("key event %d bytes, want 18", n)
	}
	if n := len(pointerEvent(0, 0, 0)); n != 18 {
		t.Errorf("pointer event %d bytes, want 18", n)
	}
	if n := len(encryptedPointerEvent(0, 0, 0)); n != 18 {
		t.Errorf("encrypted pointer event %d bytes, want 18", n)
	}
}

func TestCredentialsArePaddedAndCapped(t *testing.T) {
	got := credentials("abcdefghijklmnopqrstuvwxyz0123", "tok")
	if len(got) != 48 {
		t.Fatalf("len %d", len(got))
	}
	if got[23] != 0 || got[47] != 0 {
		t.Error("each 24-byte field must end in a NUL (values capped at 23 bytes)")
	}
	if string(got[:23]) != "abcdefghijklmnopqrstuvw" || string(got[24:27]) != "tok" {
		t.Errorf("got %q", got)
	}
}

// NIST SP 800-38A F.2.1 (CBC-AES128.Encrypt) uses exactly the vendor's key and IV.
func TestMouseAESKnownAnswer(t *testing.T) {
	var plain [16]byte
	copy(plain[:], h("6bc1bee22e409f96e93d7e117393172a"))
	got := encryptMouseBlock(plain)
	if want := h("7649abac8119b246cee98e9b12e9197d"); !bytes.Equal(got[:], want) {
		t.Errorf("got % x, want % x", got, want)
	}
}

func TestEncryptedPointerEventHeader(t *testing.T) {
	ev := encryptedPointerEvent(1, 2, 3)
	if ev[0] != 0x05 || ev[1] != 0x01 {
		t.Errorf("header % x, want 05 01", ev[:2])
	}
}
