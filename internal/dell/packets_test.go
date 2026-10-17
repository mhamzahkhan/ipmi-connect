package dell

import (
	"bytes"
	"testing"
)

// Expected bytes are the literal packets the client sent before the builder existed;
// they pin the wire format so refactors cannot change what the BMC receives.
func TestBeefPacketEncoding(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{
			name: "heartbeat q (1024)",
			got:  beefPacket(pktHeartbeat, [8]byte{}),
			want: []byte{'B', 'E', 'E', 'F', 0x04, 0x00, 0x00, 0x10, 0, 0, 0, 0, 0, 0, 0, 0},
		},
		{
			name: "enable video fb (782)",
			got:  beefPacket(pktEnableVideo, [8]byte{1, 1}),
			want: []byte{'B', 'E', 'E', 'F', 0x03, 0x0E, 0x00, 0x10, 1, 1, 0, 0, 0, 0, 0, 0},
		},
		{
			name: "set resolution y 1024x768 (770)",
			got:  beefPacket(pktSetResolution, [8]byte{0x04, 0x00, 0x03, 0x00}),
			want: []byte{'B', 'E', 'E', 'F', 0x03, 0x02, 0x00, 0x10, 0x04, 0x00, 0x03, 0x00, 0, 0, 0, 0},
		},
		{
			name: "frame refresh p (772)",
			got:  beefPacket(pktFrameRefresh, [8]byte{}),
			want: []byte{'B', 'E', 'E', 'F', 0x03, 0x04, 0x00, 0x10, 0, 0, 0, 0, 0, 0, 0, 0},
		},
		{
			name: "video ack eb (0) with batch count 1",
			got:  beefPacket(pktVideoAck, [8]byte{1}),
			want: []byte{'B', 'E', 'E', 'F', 0x00, 0x00, 0x00, 0x10, 1, 0, 0, 0, 0, 0, 0, 0},
		},
		{
			name: "pointer jc (513) left button at 0x0123,0x0456",
			got:  pointerPacket(1, 0x0123, 0x0456),
			want: []byte{'B', 'E', 'E', 'F', 0x02, 0x01, 0x00, 0x10, 0, 1, 0x01, 0x23, 0x04, 0x56, 0, 0},
		},
		{
			name: "key e (512) press uses 0 for down",
			got:  keyPacket(true, 0x00E0),
			want: []byte{'B', 'E', 'E', 'F', 0x02, 0x00, 0x00, 0x10, 0, 0, 0x00, 0xE0, 0, 0, 0, 0},
		},
		{
			name: "key e (512) release uses 1 for up",
			got:  keyPacket(false, 0x004C),
			want: []byte{'B', 'E', 'E', 'F', 0x02, 0x00, 0x00, 0x10, 0, 1, 0x00, 0x4C, 0, 0, 0, 0},
		},
		{
			name: "video auth dc (1) carries random and session token",
			got:  videoAuthPacket(0x11223344, 0xAABBCCDD),
			want: []byte{'B', 'E', 'E', 'F', 0x00, 0x01, 0x00, 0x10, 0x11, 0x22, 0x33, 0x44, 0xAA, 0xBB, 0xCC, 0xDD},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !bytes.Equal(tt.got, tt.want) {
				t.Errorf("got  % x\nwant % x", tt.got, tt.want)
			}
		})
	}
}
