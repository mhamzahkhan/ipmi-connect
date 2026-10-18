package atenvm

import "encoding/binary"

// respond truncates a command's full response to the smaller of the CDB allocation length and the
// host transfer length, as both SCSI and USB BOT require.
func respond(data []byte, cdbAlloc, dataLen uint32) ([]byte, bool) {
	limit := dataLen
	if cdbAlloc > 0 && cdbAlloc < limit {
		limit = cdbAlloc
	}
	return clamp(data, limit), true
}

func (t *scsiTarget) inquiry(cdb []byte, dataLen uint32) ([]byte, bool) {
	if len(cdb) >= 2 && cdb[1]&0x01 != 0 { // EVPD: no vital product data pages supported
		return t.fail(senseIllegalRequest, ascInvalidField, 0)
	}
	var inq []byte
	if t.isCD() {
		inq = []byte{
			0x05, 0x80, 0x00, 0x21, 0x1F, 0x00, 0x00, 0x00,
			'A', 'T', 'E', 'N', ' ', ' ', ' ', ' ',
			'V', 'i', 'r', 't', 'u', 'a', 'l', ' ', 'C', 'D', 'R', 'O', 'M', ' ', ' ', ' ',
			'Y', 'S', '0', 'J',
		}
	} else {
		inq = []byte{
			0x00, 0x80, 0x00, 0x21, 0x1F, 0x00, 0x00, 0x00,
			'I', 'P', 'M', 'I', ' ', ' ', ' ', ' ',
			'V', 'i', 'r', 't', 'u', 'a', 'l', ' ', 'D', 'i', 's', 'k', ' ', ' ', ' ', ' ',
			'3', '0', '0', '0',
		}
	}
	alloc := uint32(0)
	if len(cdb) >= 5 {
		alloc = uint32(be16(cdb[3:5]))
	}
	return respond(inq, alloc, dataLen)
}

func (t *scsiTarget) readCapacity(dataLen uint32) ([]byte, bool) {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:4], t.drive.Blocks-1)
	binary.BigEndian.PutUint32(b[4:8], t.drive.BlockSize)
	return respond(b, 0, dataLen)
}

func (t *scsiTarget) readFormatCapacities(dataLen uint32) ([]byte, bool) {
	b := make([]byte, 12)
	binary.BigEndian.PutUint32(b[0:4], 8)
	binary.BigEndian.PutUint32(b[4:8], t.drive.Blocks)
	binary.BigEndian.PutUint32(b[8:12], (uint32(0x02)<<24)|t.drive.BlockSize) // 0x02: formatted media
	return respond(b, 0, dataLen)
}

func (t *scsiTarget) modeSense(cdb []byte, dataLen uint32, long bool) ([]byte, bool) {
	wp := byte(0x00)
	if t.drive.ReadOnly && !t.isCD() {
		wp = 0x80
	}
	var page2A []byte
	if t.isCD() {
		page2A = []byte{
			0x2A, 0x1C, 0x3F, 0x00, 0x71, 0x77, 0x29, 0x23,
			0x1B, 0x90, 0x01, 0x00, 0x00, 0xC6, 0x1B, 0x90,
			0x00, 0x10, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x10, 0x00, 0x00, 0x00, 0x00, 0x00,
		}
	}
	var hdr, out []byte
	if long {
		if len(cdb) < 8 {
			return t.fail(senseIllegalRequest, ascInvalidField, 0)
		}
		body := page2A
		total := 6 + len(body)
		hdr = []byte{0x00, byte(total), 0x00, wp, 0x00, 0x00, 0x00, 0x00}
		out = append(hdr, body...)
		return respond(out, uint32(be16(cdb[7:9])), dataLen)
	}
	if len(cdb) < 5 {
		return t.fail(senseIllegalRequest, ascInvalidField, 0)
	}
	body := page2A
	total := 3 + len(body) // mode data length excludes its own byte
	hdr = []byte{byte(total), 0x00, wp, 0x00}
	out = append(hdr, body...)
	return respond(out, uint32(cdb[4]), dataLen)
}

// executeMMC handles the CD-ROM-only commands. handled=false means the opcode is not MMC here.
func (t *scsiTarget) executeMMC(cdb []byte, dataLen uint32) (data []byte, ok, handled bool) {
	switch cdb[0] {
	case CmdReadTOC:
		d, o := t.readTOC(cdb, dataLen)
		return d, o, true
	case CmdGetConfiguration:
		cfg := []byte{
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
		d, o := respond(cfg, allocAt(cdb, 7), dataLen)
		return d, o, true
	case CmdGetEventStatusNotification:
		event := []byte{0x00, 0x06, 0x04, 0x5E, 0x00, 0x02, 0x00, 0x00}
		if t.newMedia {
			event = []byte{0x00, 0x06, 0x04, 0x02, 0x02, 0x02, 0x00, 0x00} // new media event
		}
		d, o := respond(event, allocAt(cdb, 7), dataLen)
		return d, o, true
	case CmdReadDiscInformation:
		disc := []byte{
			0x00, 0x20, 0x0E, 0x01, 0x01, 0x01, 0x01, 0x20,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x00,
		}
		d, o := respond(disc, allocAt(cdb, 7), dataLen)
		return d, o, true
	case CmdReadTrackInformation:
		tr := make([]byte, 28)
		copy(tr[0:8], []byte{0x00, 0x1C, 0x01, 0x01, 0x00, 0x04, 0x01, 0x00})
		binary.BigEndian.PutUint32(tr[24:28], t.drive.Blocks)
		d, o := respond(tr, allocAt(cdb, 7), dataLen)
		return d, o, true
	case CmdMechanismStatus:
		d, o := respond(make([]byte, 8), allocAt(cdb, 8), dataLen)
		return d, o, true
	case CmdReadDiscStructure, CmdReadFormatCapacities:
		d, o := t.fail(senseIllegalRequest, ascInvalidCommand, 0)
		return d, o, true
	}
	return nil, false, false
}

func (t *scsiTarget) readTOC(cdb []byte, dataLen uint32) ([]byte, bool) {
	if len(cdb) < 9 {
		return t.fail(senseIllegalRequest, ascInvalidField, 0)
	}
	msf := cdb[1]&0x02 != 0
	// The format field moved between MMC revisions: modern hosts use byte 2 (bits 0-3), legacy
	// ATAPI hosts (including the captured one) use byte 9 (bits 6-7). Accept either.
	format := cdb[2] & 0x0F
	if format == 0 {
		format = cdb[9] >> 6
	}
	leadOut := t.drive.Blocks
	switch format {
	case 1: // session info
		toc := []byte{0x00, 0x0A, 0x01, 0x01, 0x00, 0x14, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00}
		return respond(toc, uint32(be16(cdb[7:9])), dataLen)
	default: // format 0: full TOC
		toc := make([]byte, 20)
		binary.BigEndian.PutUint16(toc[0:2], 18)
		toc[2], toc[3] = 0x01, 0x01
		toc[5], toc[6] = 0x14, 0x01
		putAddr(toc[8:12], 0, msf)
		toc[13], toc[14] = 0x14, 0xAA
		putAddr(toc[16:20], leadOut, msf)
		return respond(toc, uint32(be16(cdb[7:9])), dataLen)
	}
}

// putAddr writes a track address as LBA, or as MSF when the host asked for it.
func putAddr(b []byte, lba uint32, msf bool) {
	if !msf {
		binary.BigEndian.PutUint32(b, lba)
		return
	}
	total := lba + 150 // 2-second pre-gap
	b[0] = 0x00
	b[1] = byte(total / (60 * 75))
	b[2] = byte((total / 75) % 60)
	b[3] = byte(total % 75)
}

// allocAt reads a big-endian u16 allocation length at CDB offset off, or 0 if the CDB is short.
func allocAt(cdb []byte, off int) uint32 {
	if len(cdb) < off+2 {
		return 0
	}
	return uint32(be16(cdb[off : off+2]))
}
