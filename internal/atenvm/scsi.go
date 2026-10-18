package atenvm

// SCSI Opcode Constants.
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
	CmdReadTOC                    byte = 0x43
	CmdGetConfiguration           byte = 0x46
	CmdGetEventStatusNotification byte = 0x4A
	CmdReadDiscInformation        byte = 0x51
	CmdReadTrackInformation       byte = 0x52
	CmdModeSense10                byte = 0x5A
	CmdRead12                     byte = 0xA8
)

// CSW Status Constants (for USB Bulk-Only Transport).
const (
	CSWStatusGood        byte = 0x00
	CSWStatusCommandFail byte = 0x01
	CSWStatusPhaseError  byte = 0x02
)

// SCSI Target executes SCSI Command Descriptor Blocks (CDBs) against a virtual drive.
type SCSITarget interface {
	// ExecuteCDB runs a SCSI command against the device.
	// It returns the data to send back to the host, the CSW status code, and the CSW data residue.
	ExecuteCDB(cdb []byte, allocLen uint32) (data []byte, status byte, residue uint32)
}
