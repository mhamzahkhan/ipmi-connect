package atenvm

// USB Mass Storage Subclass Codes (used in Interface Descriptor, byte 87 / 0x57 of packet).
// If byte 0x57 is 0x05, /bin/usb on the BMC sets sector size = 2048 B (CD-ROM).
// Otherwise, sector size = 512 B (Floppy / Removable Disk).
const (
	SubClassSFF8070i byte = 0x05 // ATAPI / SFF-8070i (CD-ROM: sector size 2048 B)
	SubClassSCSI     byte = 0x06 // SCSI transparent command set (Disk: sector size 512 B)
)

// buildDescriptorTables generates the 8 serialized USB descriptor chunks expected by
// the ATEN Virtual Media service. subClass controls byte 87 of the Device Setup packet.
func buildDescriptorTables(subClass byte) []byte {
	// Chunk 0: Device descriptor (18 bytes, prefix 0x12)
	// USB 2.0, vendor 0x0EA0 (ATEN), product 0x1111, bcdDevice 0x0200, 1 configuration
	chunk0 := []byte{
		0x12, // chunk length
		0x12, 0x01, 0x00, 0x02, 0x00, 0x00, 0x00, 0x40,
		0xA0, 0x0E, 0x11, 0x11, 0x00, 0x02, 0x00, 0x00,
		0x00, 0x01,
	}

	// Chunk 1: Configuration descriptor bundle (39 bytes, prefix 0x27)
	// - Configuration: 9 bytes (wTotalLength = 39, 1 interface, bus-powered, 200mA)
	// - Interface: 9 bytes (class 0x08 Mass Storage, subClass, protocol 0x50 BBB, 3 endpoints)
	// - EP1 Bulk OUT: 7 bytes (addr 0x01, bulk, maxPacket 512)
	// - EP2 Bulk IN:  7 bytes (addr 0x82, bulk, maxPacket 512)
	// - EP3 Int IN:   7 bytes (addr 0x83, interrupt, maxPacket 2)
	chunk1 := []byte{
		0x27, // chunk length
		// Configuration descriptor (9 bytes)
		0x09, 0x02, 0x27, 0x00, 0x01, 0x01, 0x00, 0x80, 0x64,
		// Interface descriptor (9 bytes)
		0x09, 0x04, 0x00, 0x00, 0x03, 0x08, subClass, 0x50, 0x00,
		// Endpoint 1 Bulk-OUT (7 bytes)
		0x07, 0x05, 0x01, 0x02, 0x00, 0x02, 0xFF,
		// Endpoint 2 Bulk-IN (7 bytes)
		0x07, 0x05, 0x82, 0x02, 0x00, 0x02, 0xFF,
		// Endpoint 3 Interrupt-IN (7 bytes)
		0x07, 0x05, 0x83, 0x03, 0x02, 0x00, 0x01,
	}

	// Chunk 2: String descriptor 0 (LANGID en-US 0x0409, 4 bytes, prefix 0x04)
	chunk2 := []byte{
		0x04, // chunk length
		0x04, 0x03, 0x09, 0x04,
	}

	// Chunk 3: String descriptor 1 (iProduct "Flash Disk        " UTF-16LE, 34 bytes, prefix 0x22)
	chunk3 := []byte{
		0x22, // chunk length
		0x22, 0x03,
		'F', 0x00, 'l', 0x00, 'a', 0x00, 's', 0x00, 'h', 0x00, ' ', 0x00,
		'D', 0x00, 'i', 0x00, 's', 0x00, 'k', 0x00, ' ', 0x00, ' ', 0x00,
		' ', 0x00, ' ', 0x00, ' ', 0x00, ' ', 0x00,
	}

	// Chunk 4: String descriptor 2 (iSerial "4E8F092C3FD7F8F7" UTF-16LE, 34 bytes, prefix 0x22)
	chunk4 := []byte{
		0x22, // chunk length
		0x22, 0x03,
		'4', 0x00, 'E', 0x00, '8', 0x00, 'F', 0x00, '0', 0x00, '9', 0x00,
		'2', 0x00, 'C', 0x00, '3', 0x00, 'F', 0x00, 'D', 0x00, '7', 0x00,
		'F', 0x00, '8', 0x00, 'F', 0x00, '7', 0x00,
	}

	// Chunk 5: String descriptor 3 ("SN000PQI009 " UTF-16LE, 26 bytes, prefix 0x1A)
	chunk5 := []byte{
		0x1A, // chunk length
		0x1A, 0x03,
		'S', 0x00, 'N', 0x00, '0', 0x00, '0', 0x00, '0', 0x00, 'P', 0x00,
		'Q', 0x00, 'I', 0x00, '0', 0x00, '0', 0x00, '9', 0x00, ' ', 0x00,
	}

	// Chunk 6: Spacer chunk (1 byte, prefix 0x01)
	chunk6 := []byte{
		0x01, // chunk length
		0x00,
	}

	// Chunk 7: Device Qualifier descriptor (10 bytes, prefix 0x0A)
	chunk7 := []byte{
		0x0A, // chunk length
		0x0A, 0x06, 0x00, 0x02, 0x00, 0x00, 0x00, 0x40, 0x01, 0x00,
	}

	res := make([]byte, 0, len(chunk0)+len(chunk1)+len(chunk2)+len(chunk3)+len(chunk4)+len(chunk5)+len(chunk6)+len(chunk7))
	res = append(res, chunk0...)
	res = append(res, chunk1...)
	res = append(res, chunk2...)
	res = append(res, chunk3...)
	res = append(res, chunk4...)
	res = append(res, chunk5...)
	res = append(res, chunk6...)
	res = append(res, chunk7...)
	return res
}

// CDROMDescriptors returns the 174-byte descriptor tables configured for CD-ROM (subclass 0x05).
func CDROMDescriptors() []byte {
	return buildDescriptorTables(SubClassSFF8070i)
}

// DiskDescriptors returns the 174-byte descriptor tables configured for Disk / Floppy (subclass 0x06).
func DiskDescriptors() []byte {
	return buildDescriptorTables(SubClassSCSI)
}
