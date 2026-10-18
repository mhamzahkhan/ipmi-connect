package atenvm

import "ipmi-connect/internal/vmedia"

// Sector sizes.
const (
	SectorSizeCD   = vmedia.BlockSizeCD
	SectorSizeDisk = vmedia.BlockSizeDisk
)

// scsiTarget is the SCSI/MMC target for one emulated drive (a read-only CD-ROM, or a
// floppy/removable disk that may be writable). It validates every command against the medium and
// keeps sense for REQUEST SENSE, as the BMC's own target does.
type scsiTarget struct {
	drive    *vmedia.Drive
	sense    senseState
	newMedia bool // reported once via GET EVENT STATUS / cleared after the first TEST UNIT READY
}

// NewTarget creates the SCSI target for a drive: a CD-ROM for TargetCD, otherwise a disk.
func NewTarget(d *vmedia.Drive) SCSITarget { return newTarget(d) }

func newTarget(d *vmedia.Drive) *scsiTarget { return &scsiTarget{drive: d, newMedia: true} }

func (t *scsiTarget) isCD() bool { return t.drive.Target == vmedia.TargetCD }

// Execute runs a data-in or no-data command. Writes arrive separately via WriteData.
func (t *scsiTarget) Execute(cdb []byte, dataLen uint32) ([]byte, bool) {
	if len(cdb) < 6 {
		return t.fail(senseIllegalRequest, ascInvalidField, 0)
	}
	switch cdb[0] {
	case CmdTestUnitReady:
		t.newMedia = false
		return nil, true
	case CmdRequestSense:
		return t.requestSense(dataLen)
	case CmdInquiry:
		return t.inquiry(cdb, dataLen)
	case CmdReadCapacity10:
		return t.readCapacity(dataLen)
	case CmdRead10:
		if len(cdb) < 9 {
			return t.fail(senseIllegalRequest, ascInvalidField, 0)
		}
		return t.readLBA(be32(cdb[2:6]), uint32(be16(cdb[7:9])), dataLen)
	case CmdRead12:
		if len(cdb) < 10 {
			return t.fail(senseIllegalRequest, ascInvalidField, 0)
		}
		return t.readLBA(be32(cdb[2:6]), be32(cdb[6:10]), dataLen)
	case CmdModeSense6:
		return t.modeSense(cdb, dataLen, false)
	case CmdModeSense10:
		return t.modeSense(cdb, dataLen, true)
	case CmdPreventAllowMediumRemoval, CmdStartStopUnit, CmdSetCDSpeed:
		return nil, true // accepted no-ops
	}
	if t.isCD() {
		if data, ok, handled := t.executeMMC(cdb, dataLen); handled {
			return data, ok
		}
	}
	if !t.isCD() && cdb[0] == CmdReadFormatCapacities {
		return t.readFormatCapacities(dataLen)
	}
	return t.fail(senseIllegalRequest, ascInvalidCommand, 0)
}

// WriteData applies a host OUT transfer. Only WRITE(10)/(12) to a writable disk are honoured;
// anything else is refused without touching the image.
func (t *scsiTarget) WriteData(cdb []byte, data []byte) byte {
	if len(cdb) == 0 || (cdb[0] != CmdWrite10 && cdb[0] != CmdWrite12) {
		t.sense.set(senseIllegalRequest, ascInvalidCommand, 0)
		return CSWStatusCommandFail
	}
	var lba, blocks uint32
	switch cdb[0] {
	case CmdWrite10:
		if len(cdb) < 9 {
			t.sense.set(senseIllegalRequest, ascInvalidField, 0)
			return CSWStatusCommandFail
		}
		lba, blocks = be32(cdb[2:6]), uint32(be16(cdb[7:9]))
	case CmdWrite12:
		if len(cdb) < 10 {
			t.sense.set(senseIllegalRequest, ascInvalidField, 0)
			return CSWStatusCommandFail
		}
		lba, blocks = be32(cdb[2:6]), be32(cdb[6:10])
	}
	d := t.drive
	switch {
	case d.ReadOnly:
		t.sense.set(senseDataProtect, ascWriteProtected, 0)
		return CSWStatusCommandFail
	case !d.Contains(lba, blocks):
		t.sense.set(senseIllegalRequest, ascLBAOutOfRange, 0)
		return CSWStatusCommandFail
	case uint64(len(data)) != uint64(blocks)*uint64(d.BlockSize):
		t.sense.set(senseIllegalRequest, ascInvalidField, 0)
		return CSWStatusCommandFail
	}
	if _, err := d.WriteAt(data, int64(lba)*int64(d.BlockSize)); err != nil {
		t.sense.set(senseMediumError, 0x0C, 0) // write error
		return CSWStatusCommandFail
	}
	return CSWStatusGood
}
