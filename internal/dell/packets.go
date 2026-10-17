package dell

import "encoding/binary"

// Avocent packet IDs. Letters in comments are the obfuscated class names in avctKVM.jar.
const (
	pktVideoAck      uint16 = 0      // eb: client -> video channel, acknowledges one ib packet
	pktVideoAuth     uint16 = 1      // dc: client -> video channel, authenticates with the session token
	pktVideoAuthResp uint16 = 132    // ac: video channel -> client
	pktVideoFrame    uint16 = 134    // ib: video channel -> client, ASPEED bitstream (magic 0xDEADBEEF)
	pktControlAuth   uint16 = 258    // cb: client -> control channel, one-time JNLP credentials
	pktKey           uint16 = 512    // e:  USB HID key press/release
	pktPointer       uint16 = 513    // jc: absolute mouse position and buttons
	pktSetResolution uint16 = 770    // y
	pktFrameRefresh  uint16 = 772    // p:  forces a full-frame resend
	pktEnableVideo   uint16 = 782    // fb
	pktHeartbeat     uint16 = 0x0400 // q:  keeps the 30s control-channel watchdog quiet
)

// APCP channel modes sent in the 53-byte greeting.
const (
	channelModeControl byte = 3 // auth, input, heartbeats
	channelModeVideo   byte = 4 // ASPEED bitstream and ACKs
)

const (
	beefHeaderLen   = 8
	shortPacketLen  = 16
	controlAuthLen  = 217 // cb: 8-byte header + 209-byte payload
	videoFrameMagic = 0xDEADBEEF

	// videoFrameHeaderLen is the ib geometry header that precedes the ASPEED bitstream.
	videoFrameHeaderLen = 12
)

// beefPacket builds a 16-byte "BEEF" packet: magic, big-endian ID, length 16, 8-byte payload.
func beefPacket(id uint16, payload [8]byte) []byte {
	pkt := make([]byte, shortPacketLen)
	copy(pkt[0:4], "BEEF")
	binary.BigEndian.PutUint16(pkt[4:6], id)
	binary.BigEndian.PutUint16(pkt[6:8], shortPacketLen)
	copy(pkt[beefHeaderLen:], payload[:])
	return pkt
}

// pointerPacket encodes an absolute mouse event (jc).
func pointerPacket(buttons byte, x, y uint16) []byte {
	var p [8]byte
	p[1] = buttons
	binary.BigEndian.PutUint16(p[2:4], x)
	binary.BigEndian.PutUint16(p[4:6], y)
	return beefPacket(pktPointer, p)
}

// keyPacket encodes a USB HID key event (e). The BMC expects 0 for press and 1 for release.
func keyPacket(down bool, hidCode uint16) []byte {
	var p [8]byte
	if !down {
		p[1] = 1
	}
	binary.BigEndian.PutUint16(p[2:4], hidCode)
	return beefPacket(pktKey, p)
}

// videoAuthPacket encodes dc, which binds the video channel to the control channel's session.
func videoAuthPacket(clientRandom, sessionToken uint32) []byte {
	var p [8]byte
	binary.BigEndian.PutUint32(p[0:4], clientRandom)
	binary.BigEndian.PutUint32(p[4:8], sessionToken)
	return beefPacket(pktVideoAuth, p)
}
