package atenvm

// SCSI sense keys (SPC-3 §4.5.6) and additional sense codes used by this target.
const (
	senseNoSense        byte = 0x00
	senseNotReady       byte = 0x02
	senseMediumError    byte = 0x03
	senseIllegalRequest byte = 0x05
	senseUnitAttention  byte = 0x06
	senseDataProtect    byte = 0x07
)

// Additional sense code / qualifier pairs (ASC in the high byte, ASCQ in the low).
const (
	ascInvalidCommand       byte = 0x20 // ASCQ 0x00: invalid command operation code
	ascLBAOutOfRange        byte = 0x21 // ASCQ 0x00: logical block address out of range
	ascInvalidField         byte = 0x24 // ASCQ 0x00: invalid field in CDB
	ascWriteProtected       byte = 0x27 // ASCQ 0x00: write protected
	ascNotReady             byte = 0x3A // ASCQ 0x00: medium not present
	ascUnrecoveredRead      byte = 0x11 // ASCQ 0x00: unrecovered read error
	ascWriteError           byte = 0x0C // ASCQ 0x00: write error
	ascNoAdditionalInfo     byte = 0x00
	ascMediumMayHaveChanged byte = 0x28 // ASCQ 0x00: not ready to ready, medium changed
)

// senseState holds the pending fixed-format sense for a target.
type senseState struct {
	key  byte
	asc  byte
	ascq byte
}

// set records a sense condition to be returned by the next REQUEST SENSE.
func (s *senseState) set(key, asc, ascq byte) {
	s.key, s.asc, s.ascq = key, asc, ascq
}

// peek returns the current sense key and ASC without clearing them (for tests).
func (s *senseState) peek() (byte, byte) {
	return s.key, s.asc
}

// take returns the 18-byte fixed-format sense data and clears the pending sense.
func (s *senseState) take() []byte {
	b := make([]byte, 18)
	b[0] = 0x70 // current error, fixed format
	b[2] = s.key & 0x0f
	b[7] = 0x0A // additional sense length (10)
	b[12] = s.asc
	b[13] = s.ascq
	s.key, s.asc, s.ascq = senseNoSense, ascNoAdditionalInfo, 0
	return b
}
