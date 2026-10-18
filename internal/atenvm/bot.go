package atenvm

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	CBWSignature = 0x43425355 // "USBC" Little Endian
	CSWSignature = 0x53425355 // "USBS" Little Endian

	CBWLength = 31
	CSWLength = 13
)

// CBW represents a USB Bulk-Only Transport Command Block Wrapper.
type CBW struct {
	Tag       uint32   // Echoed in the CSW
	DataLen   uint32   // Expected number of data bytes to transfer
	Flags     byte     // Bit 7: 1 = Device to Host (IN), 0 = Host to Device (OUT)
	LUN       byte     // Logical Unit Number
	CDBLength byte     // Valid length of CDB (1..16)
	CDB       [16]byte // SCSI Command Descriptor Block
}

// ParseCBW parses a 31-byte slice into a CBW.
func ParseCBW(b []byte) (CBW, error) {
	if len(b) < CBWLength {
		return CBW{}, fmt.Errorf("CBW too short: %d bytes (want %d)", len(b), CBWLength)
	}
	sig := binary.LittleEndian.Uint32(b[0:4])
	if sig != CBWSignature {
		return CBW{}, fmt.Errorf("invalid CBW signature: 0x%08x (want 0x%08x)", sig, CBWSignature)
	}

	cdbLen := b[14]
	if cdbLen > 16 {
		return CBW{}, fmt.Errorf("invalid CDB length: %d (max 16)", cdbLen)
	}

	var cbw CBW
	cbw.Tag = binary.LittleEndian.Uint32(b[4:8])
	cbw.DataLen = binary.LittleEndian.Uint32(b[8:12])
	cbw.Flags = b[12]
	cbw.LUN = b[13] & 0x0F
	cbw.CDBLength = cdbLen
	copy(cbw.CDB[:], b[15:15+int(cdbLen)])

	return cbw, nil
}

// CSW represents a USB Bulk-Only Transport Command Status Wrapper.
type CSW struct {
	Tag     uint32 // Echoes CBW.Tag
	Residue uint32 // Difference between expected DataLen and transferred bytes
	Status  byte   // 0 = Good, 1 = Command Failed, 2 = Phase Error
}

// Encode serializes the CSW into a 13-byte slice.
func (c CSW) Encode() []byte {
	b := make([]byte, CSWLength)
	binary.LittleEndian.PutUint32(b[0:4], CSWSignature)
	binary.LittleEndian.PutUint32(b[4:8], c.Tag)
	binary.LittleEndian.PutUint32(b[8:12], c.Residue)
	b[12] = c.Status
	return b
}

// ParseCSW parses a 13-byte slice into a CSW.
func ParseCSW(b []byte) (CSW, error) {
	if len(b) < CSWLength {
		return CSW{}, errors.New("CSW too short")
	}
	sig := binary.LittleEndian.Uint32(b[0:4])
	if sig != CSWSignature {
		return CSW{}, fmt.Errorf("invalid CSW signature: 0x%08x", sig)
	}
	return CSW{
		Tag:     binary.LittleEndian.Uint32(b[4:8]),
		Residue: binary.LittleEndian.Uint32(b[8:12]),
		Status:  b[12],
	}, nil
}
