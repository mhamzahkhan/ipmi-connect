package dell

import (
	"net"
	"testing"
	"time"
)

func TestReadPacketIdleTimeoutConsumesNothing(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	if _, err := readPacket(client, 20*time.Millisecond); !isTimeout(err) {
		t.Fatalf("want timeout, got %v", err)
	}

	// The stream must still be aligned after the timeout.
	go func() { _, _ = server.Write(beefPacket(pktHeartbeat, [8]byte{7})) }()
	pkt, err := readPacket(client, time.Second)
	if err != nil {
		t.Fatalf("readPacket: %v", err)
	}
	if pkt.ID != pktHeartbeat || len(pkt.Payload) != 8 || pkt.Payload[0] != 7 {
		t.Errorf("got %+v", pkt)
	}
}

// The C6220 answers video auth with a zero magic; rejecting it broke the video channel.
func TestReadPacketAcceptsAnyMagic(t *testing.T) {
	for _, magic := range []string{"BEEF", "\xde\xad\xbe\xef", "\x00\x00\x00\x00"} {
		server, client := net.Pipe()
		go func() { _, _ = server.Write([]byte(magic + "\x00\x84\x00\x0a\x00\x00")) }()
		pkt, err := readPacket(client, time.Second)
		if err != nil || pkt.ID != pktVideoAuthResp || len(pkt.Payload) != 2 {
			t.Errorf("magic % x: got %+v, %v", magic, pkt, err)
		}
		server.Close()
		client.Close()
	}
}

func TestReadPacketRejectsBadLength(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{"length shorter than header", []byte("BEEF\x00\x01\x00\x04")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer server.Close()
			defer client.Close()
			go func() { _, _ = server.Write(tt.data) }()
			if _, err := readPacket(client, time.Second); err == nil || isTimeout(err) {
				t.Errorf("want protocol error, got %v", err)
			}
		})
	}
}

func TestReadPacketTimeoutMidPacketIsNotIdle(t *testing.T) {
	old := packetBodyTimeout
	packetBodyTimeout = 50 * time.Millisecond
	t.Cleanup(func() { packetBodyTimeout = old })

	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	go func() { _, _ = server.Write([]byte("BEE")) }() // header starts, then stalls

	_, err := readPacket(client, time.Second)
	if err == nil {
		t.Fatal("want error")
	}
	if isTimeout(err) {
		t.Errorf("a stall inside a packet must not look like an idle timeout: %v", err)
	}
}
