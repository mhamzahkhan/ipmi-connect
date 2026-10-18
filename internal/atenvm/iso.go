package atenvm

import (
	"encoding/binary"
	"io"
)

// SectorSizeCD is the standard sector size for CD-ROM / MMC media.
const SectorSizeCD = 2048

// ISOTarget implements SCSITarget for a read-only ISO 9660 image.
type ISOTarget struct {
	reader  io.ReaderAt
	size    int64
	sectors uint32
}

// NewISOTarget creates an ISOTarget wrapping the given reader and file size.
func NewISOTarget(reader io.ReaderAt, size int64) *ISOTarget {
	sectors := uint32(size / SectorSizeCD)
	if size%SectorSizeCD != 0 {
		sectors++
	}
	if sectors == 0 {
		sectors = 1
	}
	return &ISOTarget{
		reader:  reader,
		size:    size,
		sectors: sectors,
	}
}

// Sectors returns the total sector count of the image.
func (t *ISOTarget) Sectors() uint32 {
	return t.sectors
}

// ExecuteCDB processes an incoming SCSI CDB from the host and generates the
// appropriate data response, CSW status, and CSW data residue.
func (t *ISOTarget) ExecuteCDB(cdb []byte, allocLen uint32) ([]byte, byte, uint32) {
	if len(cdb) == 0 {
		return nil, CSWStatusCommandFail, allocLen
	}

	switch cdb[0] {
	case CmdTestUnitReady:
		return nil, CSWStatusGood, 0

	case CmdInquiry:
		// Standard INQUIRY response (36 bytes):
		// Device type: 0x05 (CD-ROM), RMB: 0x80 (Removable), ANSI: 0x00, Response format: 0x21
		// Additional length: 31 (0x1F), Vendor: "ATEN    ", Product: "Virtual CDROM   ", Rev: "YS0J"
		inq := []byte{
			0x05, 0x80, 0x00, 0x21, 0x1F, 0x00, 0x00, 0x00,
			'A', 'T', 'E', 'N', ' ', ' ', ' ', ' ',
			'V', 'i', 'r', 't', 'u', 'a', 'l', ' ', 'C', 'D', 'R', 'O', 'M', ' ', ' ', ' ',
			'Y', 'S', '0', 'J',
		}
		return truncateData(inq, allocLen)

	case CmdReadCapacity10:
		capData := make([]byte, 8)
		lastLBA := t.sectors - 1
		binary.BigEndian.PutUint32(capData[0:4], lastLBA)
		binary.BigEndian.PutUint32(capData[4:8], SectorSizeCD)
		return truncateData(capData, allocLen)

	case CmdReadTOC:
		// Format is in cdb[9] >> 6 (or cdb[2] & 0x0F)
		format := cdb[9] >> 6
		if format == 1 {
			// Session info (12 bytes):
			toc := []byte{
				0x00, 0x0A, 0x01, 0x01,
				0x00, 0x14, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
			}
			return truncateData(toc, allocLen)
		}

		// Standard TOC (format 0):
		// Header (4 bytes): length 18 (0x0012), first track 1, last track 1
		// Track 1 descriptor (8 bytes): reserved 0, ADR/Ctrl 0x14, track 1, reserved 0, start LBA 0
		// Lead-out descriptor (8 bytes): reserved 0, ADR/Ctrl 0x14, track 0xAA, reserved 0, lead-out LBA
		toc := make([]byte, 20)
		binary.BigEndian.PutUint16(toc[0:2], 18)
		toc[2] = 0x01 // First track
		toc[3] = 0x01 // Last track

		// Track 1 descriptor
		toc[4] = 0x00
		toc[5] = 0x14 // Data track
		toc[6] = 0x01 // Track number
		toc[7] = 0x00
		binary.BigEndian.PutUint32(toc[8:12], 0)

		// Lead-out track descriptor
		toc[12] = 0x00
		toc[13] = 0x14
		toc[14] = 0xAA // Lead-out track
		toc[15] = 0x00
		binary.BigEndian.PutUint32(toc[16:20], t.sectors)

		return truncateData(toc, allocLen)

	case CmdModeSense10:
		// MM Capabilities and Mechanical Status Page (0x2A)
		// Mode parameter header (8 bytes): modeDataLen 0x0024 (36), mediumType 0x01
		// Page 0x2A (30 bytes)
		modeData := []byte{
			0x00, 0x24, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x2A, 0x1C, 0x3F, 0x00, 0x71, 0x77, 0x29, 0x23,
			0x1B, 0x90, 0x01, 0x00, 0x00, 0xC6, 0x1B, 0x90,
			0x00, 0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x10, 0x00, 0x00, 0x00, 0x00, 0x00,
		}
		return truncateData(modeData, allocLen)

	case CmdModeSense6:
		// 6-byte header equivalent
		modeData := []byte{
			0x22, 0x01, 0x00, 0x00,
			0x2A, 0x1C, 0x3F, 0x00, 0x71, 0x77, 0x29, 0x23,
			0x1B, 0x90, 0x01, 0x00, 0x00, 0xC6, 0x1B, 0x90,
			0x00, 0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x10, 0x00, 0x00, 0x00, 0x00, 0x00,
		}
		return truncateData(modeData, allocLen)

	case CmdGetConfiguration:
		// Full 152-byte MMC profile list (Profile 0x0008 = CD-ROM current)
		configData := []byte{
			0x00, 0x00, 0x00, 0x98, 0x00, 0x00, 0x00, 0x08,
			0x00, 0x00, 0x03, 0x08, 0x00, 0x10, 0x00, 0x00,
			0x00, 0x08, 0x01, 0x00, 0x00, 0x01, 0x03, 0x04,
			0x00, 0x00, 0x00, 0x02, 0x00, 0x02, 0x07, 0x04,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x03, 0x03, 0x04,
			0x29, 0x00, 0x00, 0x00, 0x00, 0x10, 0x01, 0x08,
			0x00, 0x00, 0x08, 0x00, 0x00, 0x00, 0x01, 0x00,
			0x00, 0x1D, 0x01, 0x00, 0x00, 0x1E, 0x05, 0x04,
			0x03, 0x00, 0x00, 0x00, 0x00, 0x1F, 0x00, 0x04,
			0x00, 0x00, 0x01, 0x00, 0x00, 0x24, 0x00, 0x04,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x2A, 0x00, 0x04,
			0x00, 0x01, 0x00, 0x00, 0x00, 0x2B, 0x00, 0x04,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x3B, 0x00, 0x04,
			0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x03, 0x00,
			0x01, 0x03, 0x00, 0x04, 0x07, 0x00, 0x01, 0x00,
			0x01, 0x04, 0x03, 0x00, 0x01, 0x05, 0x07, 0x04,
			0x00, 0x00, 0x00, 0x00, 0x01, 0x06, 0x00, 0x04,
			0x00, 0x00, 0x00, 0x01, 0x01, 0x07, 0xC0, 0x04,
			0x08, 0x00, 0x00, 0x00, 0x01, 0x0B, 0x00, 0x04,
		}
		return truncateData(configData, allocLen)

	case CmdGetEventStatusNotification:
		// 8-byte response: no change, media present
		eventData := []byte{0x00, 0x06, 0x04, 0x5E, 0x00, 0x02, 0x00, 0x00}
		return truncateData(eventData, allocLen)

	case CmdReadDiscInformation:
		discInfo := []byte{
			0x00, 0x20, 0x0E, 0x01, 0x01, 0x01, 0x01, 0x20,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00,
		}
		return truncateData(discInfo, allocLen)

	case CmdReadTrackInformation:
		trackInfo := make([]byte, 28)
		copy(trackInfo[0:8], []byte{0x00, 0x1C, 0x01, 0x01, 0x00, 0x04, 0x01, 0x00})
		binary.BigEndian.PutUint32(trackInfo[24:28], t.sectors)
		return truncateData(trackInfo, allocLen)

	case CmdPreventAllowMediumRemoval, CmdStartStopUnit:
		return nil, CSWStatusGood, 0

	case CmdReadFormatCapacities:
		capList := make([]byte, 12)
		binary.BigEndian.PutUint32(capList[0:4], 8)         // Capacity list length
		binary.BigEndian.PutUint32(capList[4:8], t.sectors) // Number of blocks
		capList[8] = 0x02                                   // Formatted media
		capList[9] = 0x00                                   // Block length (2048 = 0x000800)
		capList[10] = 0x08
		capList[11] = 0x00
		return truncateData(capList, allocLen)

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

	case CmdRequestSense:
		// Standard 18-byte Sense Data (No Sense)
		sense := make([]byte, 18)
		sense[0] = 0x70 // Current error
		sense[7] = 0x0A // Additional sense length = 10
		return truncateData(sense, allocLen)

	default:
		// Unsupported opcode: return Command Failed
		return nil, CSWStatusCommandFail, allocLen
	}
}

// readBlocks reads `blocks` sectors starting at `lba` from the underlying reader.
func (t *ISOTarget) readBlocks(lba, blocks, allocLen uint32) ([]byte, byte, uint32) {
	bytesToRead := blocks * SectorSizeCD
	if allocLen > 0 && bytesToRead > allocLen {
		bytesToRead = allocLen
	}

	buf := make([]byte, bytesToRead)
	offset := int64(lba) * SectorSizeCD
	_, _ = t.reader.ReadAt(buf, offset)

	residue := uint32(0)
	if allocLen > uint32(len(buf)) {
		residue = allocLen - uint32(len(buf))
	}
	return buf, CSWStatusGood, residue
}

// WriteData returns CSWStatusCommandFail because an ISO is read-only.
func (t *ISOTarget) WriteData(cdb []byte, data []byte) byte {
	return CSWStatusCommandFail
}

// truncateData trims data to allocLen if allocLen is smaller, and calculates CSW residue.
func truncateData(data []byte, allocLen uint32) ([]byte, byte, uint32) {
	if allocLen == 0 {
		return nil, CSWStatusGood, 0
	}
	if uint32(len(data)) > allocLen {
		data = data[:allocLen]
	}
	residue := uint32(0)
	if allocLen > uint32(len(data)) {
		residue = allocLen - uint32(len(data))
	}
	return data, CSWStatusGood, residue
}
