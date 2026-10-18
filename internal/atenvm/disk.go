package atenvm

import (
	"encoding/binary"
	"io"

	"ipmi-connect/internal/vmedia"
)

// SectorSizeDisk is the standard sector size for floppy and removable disk media.
const SectorSizeDisk = 512

// DiskTarget implements SCSITarget for a floppy or removable disk image (read-only or writable).
type DiskTarget struct {
	image    vmedia.Image
	size     int64
	sectors  uint32
	readOnly bool
}

// NewDiskTarget creates a DiskTarget wrapping a disk image.
func NewDiskTarget(image vmedia.Image, size int64, readOnly bool) *DiskTarget {
	sectors := uint32(size / SectorSizeDisk)
	if size%SectorSizeDisk != 0 {
		sectors++
	}
	if sectors == 0 {
		sectors = 1
	}
	return &DiskTarget{
		image:    image,
		size:     size,
		sectors:  sectors,
		readOnly: readOnly,
	}
}

// Sectors returns the total sector count of the disk image.
func (t *DiskTarget) Sectors() uint32 {
	return t.sectors
}

// ExecuteCDB processes an incoming SCSI CDB from the host.
func (t *DiskTarget) ExecuteCDB(cdb []byte, allocLen uint32) ([]byte, byte, uint32) {
	if len(cdb) == 0 {
		return nil, CSWStatusCommandFail, allocLen
	}

	switch cdb[0] {
	case CmdTestUnitReady:
		return nil, CSWStatusGood, 0

	case CmdInquiry:
		// Disk INQUIRY response (36 bytes):
		// Device type: 0x00 (Direct Access Block Device / Disk)
		// Removable: 0x80
		// Vendor: "IPMI    ", Product: "Virtual Disk    ", Rev: "3000"
		inq := []byte{
			0x00, 0x80, 0x00, 0x21, 0x1F, 0x00, 0x00, 0x00,
			'I', 'P', 'M', 'I', ' ', ' ', ' ', ' ',
			'V', 'i', 'r', 't', 'u', 'a', 'l', ' ', 'D', 'i', 's', 'k', ' ', ' ', ' ', ' ',
			'3', '0', '0', '0',
		}
		return truncateData(inq, allocLen)

	case CmdReadCapacity10:
		capData := make([]byte, 8)
		lastLBA := t.sectors - 1
		binary.BigEndian.PutUint32(capData[0:4], lastLBA)
		binary.BigEndian.PutUint32(capData[4:8], SectorSizeDisk)
		return truncateData(capData, allocLen)

	case CmdReadFormatCapacities:
		capList := make([]byte, 12)
		binary.BigEndian.PutUint32(capList[0:4], 8)         // Capacity list length
		binary.BigEndian.PutUint32(capList[4:8], t.sectors) // Number of blocks
		capList[8] = 0x02                                   // Formatted media
		capList[9] = 0x00                                   // Block length (512 = 0x000200)
		capList[10] = 0x02
		capList[11] = 0x00
		return truncateData(capList, allocLen)

	case CmdModeSense6:
		// 4-byte header: modeDataLen 3, mediumType 0, deviceSpecific (bit 7 = write protect), blockDescLen 0
		devSpec := byte(0x00)
		if t.readOnly {
			devSpec = 0x80
		}
		modeData := []byte{0x03, 0x00, devSpec, 0x00}
		return truncateData(modeData, allocLen)

	case CmdModeSense10:
		// 8-byte header: modeDataLen 6, mediumType 0, deviceSpecific (bit 7 = write protect), reserved 0, blockDescLen 0
		devSpec := byte(0x00)
		if t.readOnly {
			devSpec = 0x80
		}
		modeData := []byte{0x00, 0x06, 0x00, devSpec, 0x00, 0x00, 0x00, 0x00}
		return truncateData(modeData, allocLen)

	case CmdPreventAllowMediumRemoval, CmdStartStopUnit:
		return nil, CSWStatusGood, 0

	case CmdRead10:
		if len(cdb) < 9 {
			return nil, CSWStatusCommandFail, allocLen
		}
		lba := binary.BigEndian.Uint32(cdb[2:6])
		blocks := uint32(binary.BigEndian.Uint16(cdb[7:9]))
		return t.readBlocks(lba, blocks, allocLen)

	case CmdRead12:
		if len(cdb) < 10 {
			return nil, CSWStatusCommandFail, allocLen
		}
		lba := binary.BigEndian.Uint32(cdb[2:6])
		blocks := binary.BigEndian.Uint32(cdb[6:10])
		return t.readBlocks(lba, blocks, allocLen)

	case CmdWrite10, CmdWrite12:
		if t.readOnly {
			return nil, CSWStatusCommandFail, allocLen
		}
		// Write commands do not return data; payload arrives on Bulk-OUT
		return nil, CSWStatusGood, 0

	case CmdRequestSense:
		sense := make([]byte, 18)
		sense[0] = 0x70
		sense[7] = 0x0A
		return truncateData(sense, allocLen)

	default:
		return nil, CSWStatusCommandFail, allocLen
	}
}

// WriteData handles the incoming Bulk-OUT write payload for WRITE(10) and WRITE(12).
func (t *DiskTarget) WriteData(cdb []byte, data []byte) byte {
	if t.readOnly || len(cdb) < 6 {
		return CSWStatusCommandFail
	}
	lba := binary.BigEndian.Uint32(cdb[2:6])
	offset := int64(lba) * SectorSizeDisk
	if _, err := t.image.WriteAt(data, offset); err != nil {
		return CSWStatusCommandFail
	}
	return CSWStatusGood
}

// readBlocks reads `blocks` sectors starting at `lba` from the underlying image.
func (t *DiskTarget) readBlocks(lba, blocks, allocLen uint32) ([]byte, byte, uint32) {
	bytesToRead := blocks * SectorSizeDisk
	if allocLen > 0 && bytesToRead > allocLen {
		bytesToRead = allocLen
	}

	buf := make([]byte, bytesToRead)
	offset := int64(lba) * SectorSizeDisk
	_, err := t.image.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return nil, CSWStatusCommandFail, allocLen
	}

	residue := uint32(0)
	if allocLen > uint32(len(buf)) {
		residue = allocLen - uint32(len(buf))
	}
	return buf, CSWStatusGood, residue
}
