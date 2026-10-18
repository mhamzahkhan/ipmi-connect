package dell

import "encoding/binary"

// Avocent packet IDs.
const (
	pktVideoAck      uint16 = 0      // client -> video channel, acknowledges one video frame
	pktVideoAuth     uint16 = 1      // client -> video channel, authenticates with the session token
	pktVideoAuthResp uint16 = 132    // video channel -> client
	pktVideoFrame    uint16 = 134    // video channel -> client, ASPEED bitstream (magic 0xDEADBEEF)
	pktControlAuth   uint16 = 258    // client -> control channel, one-time JNLP credentials
	pktKey           uint16 = 512    // USB HID key press/release
	pktPointer       uint16 = 513    // absolute mouse position and buttons
	pktScreenRefresh uint16 = 769    // forces a full-frame resend
	pktSetResolution uint16 = 770    // sets the display area
	pktSetScale1to1  uint16 = 772    // 1:1 scaling, sent once when enabling video
	pktEnableVideo   uint16 = 782    // turns the video stream on
	pktHeartbeat     uint16 = 0x0400 // keeps the 30s control-channel watchdog quiet

	pktRequestVMCredentials uint16 = 0x0427 // asks for one-time virtual media tokens (payload u32 30)
	pktVMCredentials        uint16 = 0x8434 // user and password tokens, 32 bytes each, NUL-padded
)

// APCP channel modes sent in the 53-byte greeting.
const (
	channelModeControl byte = 3 // auth, input, heartbeats
	channelModeVideo   byte = 4 // ASPEED bitstream and ACKs
	channelModeVMedia  byte = 2 // AVMP virtual media
)

// serverPacketNames describe server packets in log lines.
var serverPacketNames = map[uint16]string{
	0x8102: "mouse acceleration reply",
	0x8200: "input resolution reply",
	0x8202: "video setup",
	0x8204: "colour mode reply",
	0x8300: "login reply",
	0x8401: "session sharing request",
	0x8431: "selected server update",
	0x8434: "virtual media credentials",
}

func packetName(id uint16) string {
	if name, ok := serverPacketNames[id]; ok {
		return name
	}
	return "unknown packet"
}

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

// avocentButtons converts the browser's RFB-style mask (left 1, middle 2, right 4) to the
// order Avocent's viewer sends (left 1, right 2, middle 4). Other bits pass through.
func avocentButtons(rfb byte) byte {
	out := rfb &^ 0x07
	out |= rfb & 1
	if rfb&2 != 0 {
		out |= 4
	}
	if rfb&4 != 0 {
		out |= 2
	}
	return out
}

// pointerPacket encodes an absolute mouse event (jc). buttons is already in Avocent order.
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
