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

// readRange decodes a READ(10)/(12) into the byte range to send, bounded by the host transfer
// length. A short CDB or an out-of-range LBA sets sense and returns ok=false.
func (t *scsiTarget) readRange(cdb []byte, dataLen uint32) (off, n int64, ok bool) {
	lba, blocks, ok := t.blockRange(cdb)
	if !ok {
		return 0, 0, false
	}
	if !t.drive.Contains(lba, blocks) {
		t.sense.set(senseIllegalRequest, ascLBAOutOfRange, 0)
		return 0, 0, false
	}
	// The host may ask for fewer bytes than the whole sector run.
	n = int64(min(uint64(blocks)*uint64(t.drive.BlockSize), uint64(dataLen)))
	return int64(lba) * int64(t.drive.BlockSize), n, true
}

// writeRange validates a host OUT transfer of dataLen bytes and returns where it goes. Only
// WRITE(10)/(12) of exactly the addressed blocks to a writable drive is accepted; anything else
// sets sense and returns ok=false.
func (t *scsiTarget) writeRange(cdb []byte, dataLen uint32) (off int64, ok bool) {
	if len(cdb) == 0 || (cdb[0] != CmdWrite10 && cdb[0] != CmdWrite12) {
		t.sense.set(senseIllegalRequest, ascInvalidCommand, 0)
		return 0, false
	}
	lba, blocks, ok := t.blockRange(cdb)
	d := t.drive
	switch {
	case !ok:
		return 0, false
	case d.ReadOnly:
		t.sense.set(senseDataProtect, ascWriteProtected, 0)
		return 0, false
	case !d.Contains(lba, blocks):
		t.sense.set(senseIllegalRequest, ascLBAOutOfRange, 0)
		return 0, false
	case uint64(dataLen) != uint64(blocks)*uint64(d.BlockSize):
		t.sense.set(senseIllegalRequest, ascInvalidField, 0)
		return 0, false
	}
	return int64(lba) * int64(d.BlockSize), true
}

// blockRange decodes the LBA and block count of a READ/WRITE (10) or (12) CDB.
func (t *scsiTarget) blockRange(cdb []byte) (lba, blocks uint32, ok bool) {
	switch {
	case (cdb[0] == CmdRead10 || cdb[0] == CmdWrite10) && len(cdb) >= 9:
		return be32(cdb[2:6]), uint32(be16(cdb[7:9])), true
	case (cdb[0] == CmdRead12 || cdb[0] == CmdWrite12) && len(cdb) >= 10:
		return be32(cdb[2:6]), be32(cdb[6:10]), true
	}
	t.sense.set(senseIllegalRequest, ascInvalidField, 0)
	return 0, 0, false
}

// isRead reports whether a CDB is a READ(10)/(12), which the session streams.
func isRead(cdb []byte) bool { return len(cdb) > 0 && (cdb[0] == CmdRead10 || cdb[0] == CmdRead12) }

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
