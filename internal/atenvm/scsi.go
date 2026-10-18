package atenvm

import "encoding/binary"

// SCSI opcode constants.
const (
	CmdTestUnitReady              byte = 0x00
	CmdRequestSense               byte = 0x03
	CmdInquiry                    byte = 0x12
	CmdModeSense6                 byte = 0x1A
	CmdStartStopUnit              byte = 0x1B
	CmdPreventAllowMediumRemoval  byte = 0x1E
	CmdReadFormatCapacities       byte = 0x23
	CmdReadCapacity10             byte = 0x25
	CmdRead10                     byte = 0x28
	CmdWrite10                    byte = 0x2A
	CmdReadTOC                    byte = 0x43
	CmdGetConfiguration           byte = 0x46
	CmdGetEventStatusNotification byte = 0x4A
	CmdReadDiscInformation        byte = 0x51
	CmdReadTrackInformation       byte = 0x52
	CmdModeSense10                byte = 0x5A
	CmdRead12                     byte = 0xA8
	CmdWrite12                    byte = 0xAA
	CmdMechanismStatus            byte = 0xBD
	CmdSetCDSpeed                 byte = 0xBB
	CmdReadDiscStructure          byte = 0xAD
)

// CSW status constants (USB Bulk-Only Transport).
const (
	CSWStatusGood        byte = 0x00
	CSWStatusCommandFail byte = 0x01
	CSWStatusPhaseError  byte = 0x02
)

// SCSITarget executes SCSI/MMC commands against one emulated drive. It is the SCSI target; the
// host (via the BMC) is the initiator.
type SCSITarget interface {
	// Execute runs a command that may return data to the host. dataLen is the host's expected
	// transfer length (dCBWDataTransferLength). It returns the IN data (never longer than dataLen)
	// and ok=false for CHECK CONDITION, in which case the sense is set for the next REQUEST SENSE.
	Execute(cdb []byte, dataLen uint32) (data []byte, ok bool)
	// WriteData applies a host OUT transfer (WRITE(10)/(12)); it returns a CSW status.
	WriteData(cdb []byte, data []byte) byte
}

// scsiBase holds state shared by the CD-ROM and disk targets.
type scsiBase struct {
	sectors   uint32
	blockSize uint32
	sense     senseState
	newMedia  bool // reported once via GET EVENT STATUS / cleared after the first TEST UNIT READY
}

// readLBA validates an LBA/block count against the medium and returns the data, bounded by the
// host transfer length. A short or malformed request, an out-of-range LBA, or a read error sets
// sense and returns ok=false.
func (b *scsiBase) readLBA(r reader, lba, blocks, dataLen uint32) ([]byte, bool) {
	if dataLen == 0 || blocks == 0 {
		return nil, true // nothing to transfer
	}
	// uint64 throughout: blocks and sectors are uint32, so lba+blocks can exceed 2^32.
	end := uint64(lba) + uint64(blocks)
	if end > uint64(b.sectors) {
		b.sense.set(senseIllegalRequest, ascLBAOutOfRange, 0)
		return nil, false
	}
	want := uint64(blocks) * uint64(b.blockSize)
	if want > uint64(dataLen) {
		want = uint64(dataLen) // the host asked for fewer bytes than a whole sector run
	}
	buf := make([]byte, want)
	off := int64(lba) * int64(b.blockSize)
	if _, err := r.ReadAt(buf, off); err != nil {
		b.sense.set(senseMediumError, ascUnrecoveredRead, 0)
		return nil, false
	}
	return buf, true
}

// requestSense returns the pending sense, truncated to the host transfer length.
func (b *scsiBase) requestSense(dataLen uint32) ([]byte, bool) {
	return clamp(b.sense.take(), dataLen), true
}

// fail sets sense and returns a CHECK CONDITION result.
func (b *scsiBase) fail(key, asc, ascq byte) ([]byte, bool) {
	b.sense.set(key, asc, ascq)
	return nil, false
}

// reader is the read side of an image.
type reader interface {
	ReadAt(p []byte, off int64) (int, error)
}

// clamp trims data to at most dataLen bytes (the SCSI/USB transfer length).
func clamp(data []byte, dataLen uint32) []byte {
	if uint32(len(data)) > dataLen {
		return data[:dataLen]
	}
	return data
}

// be32 / be16 read big-endian fields from a CDB (SCSI is big-endian).
func be32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }
func be16(b []byte) uint16 { return binary.BigEndian.Uint16(b) }
