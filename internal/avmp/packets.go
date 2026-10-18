package avmp

import (
	"encoding/binary"

	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

// Message types. Bit 0x8000 marks BMC-to-client messages.
const (
	typeLogin           uint16 = 0x0100
	typePreemptResponse uint16 = 0x0120
	typeGetVDiskInfo    uint16 = 0x0200
	typeMapCD           uint16 = 0x0210 // VDISK_REQUEST: CD with a TOC
	typeMapDisk         uint16 = 0x0211 // VDISK_REQUEST2: floppy or removable, with geometry
	typeRelease         uint16 = 0x0220 // VDISK_RELEASE: unmap / eject
	typeReadData        uint16 = 0x0300
	typeHeartbeat       uint16 = 0x0400
	typeClientStatus    uint16 = 0x0410
	typeUSBReset        uint16 = 0x0420

	typeLoginStatus    uint16 = 0x8100
	typeDisconnect     uint16 = 0x8110
	typeVDiskInfo      uint16 = 0x8200
	typeRequestRelease uint16 = 0x8240
	typeVDiskRead      uint16 = 0x8300
	typeVDiskWrite     uint16 = 0x8310
	typeDeviceStatus   uint16 = 0x8410
	// typeDeviceConfigOption carries a BMC policy; option 1 (READ_ONLY) forces later maps read-only.
	typeDeviceConfigOption uint16 = 0x8430
)

const (
	headerLen = 10 // "AVMP", u32 total length (header included), u16 type

	cdBlockSize = vmedia.BlockSizeCD

	allDrives = 0xFFFF
	loginLen  = 153
	userField = 96
	passField = 32
)

// CLIENT_STATUS values.
const (
	statusOK          uint32 = 0
	statusReadFailed  uint32 = 2
	statusWriteFailed uint32 = 3
)

// cdTOC is the READ TOC (format 0) the vendor client sends for every image: one data track at
// LBA 0 and a fixed lead-out at 1,144,544 blocks (about 2.2 GiB) whatever the image size. The BMC
// sizes the device from the block count, so this is copied verbatim.
var cdTOC = []byte{
	0x00, 0x12, 0x01, 0x01,
	0x00, 0x14, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x00, 0x14, 0xAA, 0x00, 0x00, 0x11, 0x76, 0xE0,
}

func packet(typ uint16, bodyLen int) []byte {
	b := make([]byte, headerLen+bodyLen)
	copy(b, "AVMP")
	binary.BigEndian.PutUint32(b[4:8], uint32(len(b)))
	binary.BigEndian.PutUint16(b[8:10], typ)
	return b
}

func diskPacket(typ uint16, disk uint16) []byte {
	b := packet(typ, 2)
	binary.BigEndian.PutUint16(b[10:12], disk)
	return b
}

// login carries the one-time tokens minted over the KVM control channel, in plain text: the
// connection is already TLS.
func login(user, password string, preempt bool) []byte {
	b := packet(typeLogin, loginLen-headerLen)
	u := user[:min(len(user), userField)]
	b[12] = byte(len(u))
	copy(b[13:13+userField], u)
	copy(b[13+userField:13+userField+passField], password)
	if preempt {
		b[loginLen-1] = 1
	}
	return b
}

func getVDiskInfo() []byte        { return diskPacket(typeGetVDiskInfo, allDrives) }
func heartbeat() []byte           { return diskPacket(typeHeartbeat, 0) }
func releaseDisk(d uint16) []byte { return diskPacket(typeRelease, d) }

func clientStatus(disk uint16, status uint32) []byte {
	b := packet(typeClientStatus, 6)
	binary.BigEndian.PutUint16(b[10:12], disk)
	binary.BigEndian.PutUint32(b[12:16], status)
	return b
}

// preemptResponse answers another user's request for the drive: 0 yields, 52 refuses.
func preemptResponse(accept bool) []byte {
	b := packet(typePreemptResponse, 3)
	if !accept {
		b[12] = 52
	}
	return b
}

// mapCD asks the BMC to attach a read-only CD of blocks 2048-byte blocks.
func mapCD(disk uint16, blocks uint32) []byte {
	b := packet(typeMapCD, 14+len(cdTOC))
	binary.BigEndian.PutUint16(b[10:12], disk)
	binary.BigEndian.PutUint32(b[12:16], cdBlockSize)
	binary.BigEndian.PutUint32(b[16:20], blocks)
	b[20] = 1 // read-only
	b[21] = byte(min(blocks, 0xFF))
	binary.BigEndian.PutUint16(b[22:24], uint16(len(cdTOC)))
	copy(b[24:], cdTOC)
	return b
}

func readData(disk uint16, start, n uint32, data []byte) []byte {
	b := packet(typeReadData, 10+len(data))
	binary.BigEndian.PutUint16(b[10:12], disk)
	binary.BigEndian.PutUint32(b[12:16], start)
	binary.BigEndian.PutUint32(b[16:20], n)
	copy(b[20:], data)
	return b
}

// mapDisk asks the BMC to attach a floppy or removable disk with the given geometry.
func mapDisk(disk uint16, blockSize, blocks uint32, readOnly bool, cyl uint16, heads, sectors byte) []byte {
	b := packet(typeMapDisk, 16)
	binary.BigEndian.PutUint16(b[10:12], disk)
	binary.BigEndian.PutUint32(b[12:16], blockSize)
	binary.BigEndian.PutUint32(b[16:20], blocks)
	if readOnly {
		b[20] = 1
	}
	b[21] = byte(min(blocks, 0xFF))
	binary.BigEndian.PutUint16(b[22:24], cyl)
	b[24], b[25] = heads, sectors
	return b
}

// usbReset makes the BMC disconnect and reconnect its virtual mass-storage device on the host
// (about a second on a C6220; keyboard and mouse are not affected, mapped drives stay mapped).
// The BMC sends no reply.
func usbReset() []byte { return diskPacket(typeUSBReset, allDrives) }
