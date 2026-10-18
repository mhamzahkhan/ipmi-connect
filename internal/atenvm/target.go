package atenvm

import "ipmi-connect/internal/vmedia"

// Sector sizes.
const (
	SectorSizeCD   = 2048
	SectorSizeDisk = 512
)

type targetKind int

const (
	kindCD targetKind = iota
	kindDisk
)

// scsiTarget is the SCSI/MMC target for one emulated drive (a read-only ISO CD-ROM, or a
// floppy/removable disk that may be writable). It validates every command against the medium and
// keeps sense for REQUEST SENSE, as the BMC's own target does.
type scsiTarget struct {
	scsiBase
	kind     targetKind
	image    vmedia.Image // writable backing (disk); for CD only its ReaderAt is used
	readOnly bool
}

// NewISOTarget creates a read-only CD-ROM target over a disc image.
func NewISOTarget(r vmedia.Image, size int64) SCSITarget {
	return newTarget(kindCD, r, size, true)
}

// NewDiskTarget creates a floppy/removable disk target.
func NewDiskTarget(img vmedia.Image, size int64, readOnly bool) SCSITarget {
	return newTarget(kindDisk, img, size, readOnly)
}

func newTarget(kind targetKind, img vmedia.Image, size int64, readOnly bool) *scsiTarget {
	blockSize := uint32(SectorSizeCD)
	if kind == kindDisk {
		blockSize = SectorSizeDisk
	}
	sectors := uint32(size / int64(blockSize))
	if size%int64(blockSize) != 0 {
		sectors++
	}
	if sectors == 0 {
		sectors = 1
	}
	return &scsiTarget{
		scsiBase: scsiBase{sectors: sectors, blockSize: blockSize, newMedia: true},
		kind:     kind,
		image:    img,
		readOnly: kind == kindCD || readOnly,
	}
}

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
		return t.readLBA(t.image, be32(cdb[2:6]), uint32(be16(cdb[7:9])), dataLen)
	case CmdRead12:
		if len(cdb) < 10 {
			return t.fail(senseIllegalRequest, ascInvalidField, 0)
		}
		return t.readLBA(t.image, be32(cdb[2:6]), be32(cdb[6:10]), dataLen)
	case CmdModeSense6:
		return t.modeSense(cdb, dataLen, false)
	case CmdModeSense10:
		return t.modeSense(cdb, dataLen, true)
	case CmdPreventAllowMediumRemoval, CmdStartStopUnit, CmdSetCDSpeed:
		return nil, true // accepted no-ops
	}
	if t.kind == kindCD {
		if data, ok, handled := t.executeMMC(cdb, dataLen); handled {
			return data, ok
		}
	}
	if t.kind == kindDisk && cdb[0] == CmdReadFormatCapacities {
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
	if t.readOnly {
		t.sense.set(senseDataProtect, ascWriteProtected, 0)
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
	if uint64(lba)+uint64(blocks) > uint64(t.sectors) {
		t.sense.set(senseIllegalRequest, ascLBAOutOfRange, 0)
		return CSWStatusCommandFail
	}
	if uint64(len(data)) != uint64(blocks)*uint64(t.blockSize) {
		t.sense.set(senseIllegalRequest, ascInvalidField, 0)
		return CSWStatusCommandFail
	}
	if _, err := t.image.WriteAt(data, int64(lba)*int64(t.blockSize)); err != nil {
		t.sense.set(senseMediumError, 0x0C, 0) // write error
		return CSWStatusCommandFail
	}
	return CSWStatusGood
}
