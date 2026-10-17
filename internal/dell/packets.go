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
	pktScreenRefresh uint16 = 769    // o:  "ScreenRefresh", forces a full-frame resend
	pktSetResolution uint16 = 770    // y:  "Set Display Area"
	pktSetScale1to1  uint16 = 772    // p:  "SetScaleMode1to1", sent once when enabling video
	pktEnableVideo   uint16 = 782    // fb
	pktHeartbeat     uint16 = 0x0400 // q:  keeps the 30s control-channel watchdog quiet

	pktRequestVMCredentials uint16 = 0x0427 // s: asks for one-time virtual media tokens (payload u32 30)
	pktVMCredentials        uint16 = 0x8434 // k: user and password tokens, 32 bytes each, NUL-padded
)

// APCP channel modes sent in the 53-byte greeting.
const (
	channelModeControl byte = 3 // auth, input, heartbeats
	channelModeVideo   byte = 4 // ASPEED bitstream and ACKs
	channelModeVMedia  byte = 2 // AVMP virtual media
)

// serverPacketNames are the class names decrypted from the C6220's viewer, for logging.
var serverPacketNames = map[uint16]string{
	0x8102: "MouseAccelResponse",
	0x8200: "Input Resolution Response",
	0x8202: "VideoSetupData",
	0x8204: "DVCColorModeResponse",
	0x8300: "User Login Response",
	0x8401: "SharingRequest",
	0x8431: "SelectedServerUpdate",
	0x8434: "RequestCredentialsMessage",
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
