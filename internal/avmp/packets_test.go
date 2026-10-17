package avmp

import (
	"bytes"
	"encoding/binary"
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

// header returns the 10-byte AVMP header for a packet of total length n.
func header(typ uint16, n int) []byte {
	b := []byte("AVMP")
	b = binary.BigEndian.AppendUint32(b, uint32(n))
	return binary.BigEndian.AppendUint16(b, typ)
}

// Layouts from avctVM.jar's ApplianceSession.cmd* and VirtualMedia.login (notes/dell-avmp-virtual-media.md).
func TestEncoders(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{"get vdisk info (all drives)", getVDiskInfo(), append(header(0x0200, 12), 0xff, 0xff)},
		{"heartbeat", heartbeat(), append(header(0x0400, 12), 0, 0)},
		{"release drive 1", releaseDisk(1), append(header(0x0220, 12), 0, 1)},
		{"client status read failed", clientStatus(1, statusReadFailed), append(header(0x0410, 16), h("0001"+"00000002")...)},
		{"accept preemption", preemptResponse(true), append(header(0x0120, 13), 0, 0, 0)},
		{"refuse preemption", preemptResponse(false), append(header(0x0120, 13), 0, 0, 52)},
		{"read data, 1 block", readData(1, 7, 1, []byte{0xaa, 0xbb}), append(header(0x0300, 22), h("0001"+"00000007"+"00000001"+"aabb")...)},
		{"map CD of 300 blocks", mapCD(1, 300), append(header(0x0210, 44), h("0001"+"00000800"+"0000012c"+"01"+"ff"+"0014"+
			"00120101"+"001401000000000000"+"14aa00001176e0")...)},
		{"map CD of 10 blocks", mapCD(0, 10)[20:22], []byte{0x01, 0x0a}},
		// The spec's worked example: 1.44 MB image on drive 2, writable.
		{"map floppy", mapDisk(2, 512, 2880, false, 80, 2, 18), h("41564d50" + "0000001a" + "0211" + "0002" + "00000200" + "00000b40" + "00" + "ff" + "0050" + "02" + "12")},
		{"map removable read-only", mapDisk(1, 512, 100, true, 1, 16, 63)[20:22], []byte{0x01, 100}},
		{"USB reset", usbReset(), h("41564d50" + "0000000c" + "0420" + "ffff")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !bytes.Equal(tt.got, tt.want) {
				t.Errorf("got  % x\nwant % x", tt.got, tt.want)
			}
		})
	}
}

func TestLoginPacket(t *testing.T) {
	p := login("719885386", "1649760492", false)
	if len(p) != 153 {
		t.Fatalf("len %d, want 153", len(p))
	}
	if !bytes.Equal(p[:12], append(header(0x0100, 153), 0, 0)) {
		t.Errorf("header % x", p[:12])
	}
	if p[12] != 9 || string(p[13:22]) != "719885386" || p[22] != 0 {
		t.Errorf("user field % x", p[12:24])
	}
	if string(p[109:119]) != "1649760492" || p[119] != 0 {
		t.Errorf("password field at 109: % x", p[109:121])
	}
	if p[152] != 0 {
		t.Error("preempt flag set")
	}
	if login("u", "p", true)[152] != 1 {
		t.Error("preempt flag not set")
	}
}

func TestLoginCapsFieldLengths(t *testing.T) {
	p := login(string(bytes.Repeat([]byte("u"), 200)), string(bytes.Repeat([]byte("p"), 200)), false)
	if len(p) != 153 || p[12] != 96 {
		t.Errorf("len %d, user length byte %d", len(p), p[12])
	}
}
