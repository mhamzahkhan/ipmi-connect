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

// readLBA validates an LBA/block count against the medium and returns the data, bounded by the
// host transfer length. An out-of-range LBA or a read error sets sense and returns ok=false.
func (t *scsiTarget) readLBA(lba, blocks, dataLen uint32) ([]byte, bool) {
	if dataLen == 0 || blocks == 0 {
		return nil, true // nothing to transfer
	}
	d := t.drive
	if !d.Contains(lba, blocks) {
		return t.fail(senseIllegalRequest, ascLBAOutOfRange, 0)
	}
	// The host may ask for fewer bytes than the whole sector run.
	buf := make([]byte, min(uint64(blocks)*uint64(d.BlockSize), uint64(dataLen)))
	if _, err := d.ReadAt(buf, int64(lba)*int64(d.BlockSize)); err != nil {
		return t.fail(senseMediumError, ascUnrecoveredRead, 0)
	}
	return buf, true
}

// requestSense returns the pending sense, truncated to the host transfer length.
func (t *scsiTarget) requestSense(dataLen uint32) ([]byte, bool) {
	return clamp(t.sense.take(), dataLen), true
}

// fail sets sense and returns a CHECK CONDITION result.
func (t *scsiTarget) fail(key, asc, ascq byte) ([]byte, bool) {
	t.sense.set(key, asc, ascq)
	return nil, false
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
