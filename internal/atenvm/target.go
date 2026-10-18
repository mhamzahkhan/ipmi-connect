package atenvm

import (
	"math"

	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

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
	case CmdRead10, CmdRead12:
		off, n, ok := t.readRange(cdb, dataLen)
		if !ok {
			return nil, false
		}
		buf := make([]byte, n) // the session streams reads; this whole-buffer path serves tests
		if _, err := t.drive.ReadAt(buf, off); err != nil {
			return t.fail(senseMediumError, ascUnrecoveredRead, 0)
		}
		return buf, true
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

// WriteData applies a whole host OUT transfer. Only WRITE(10)/(12) to a writable disk is
// honoured; anything else is refused without touching the image.
func (t *scsiTarget) WriteData(cdb []byte, data []byte) byte {
	if uint64(len(data)) > math.MaxUint32 {
		t.sense.set(senseIllegalRequest, ascInvalidField, 0)
		return CSWStatusCommandFail
	}
	off, ok := t.writeRange(cdb, uint32(len(data)))
	if !ok {
		return CSWStatusCommandFail
	}
	if _, err := t.drive.WriteAt(data, off); err != nil {
		t.sense.set(senseMediumError, ascWriteError, 0)
		return CSWStatusCommandFail
	}
	return CSWStatusGood
}
