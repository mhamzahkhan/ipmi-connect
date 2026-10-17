package bmc

// Console is the vendor-neutral remote console the web layer drives. Each BMC client adapts its
// own wire protocol to it.
type Console interface {
	// SendPointerEvent moves the pointer to absolute framebuffer coordinates. buttons uses the
	// RFB convention: left 1, middle 2, right 4.
	SendPointerEvent(buttons byte, x, y uint16) error
	// SendKeyEvent presses or releases a key, identified by its USB HID usage (keyboard page 0x07).
	SendKeyEvent(down bool, usage uint16) error
	// RequestFrameRefresh asks the BMC to resend a complete frame.
	RequestFrameRefresh() error
	// PowerControl issues a chassis power command.
	PowerControl(action PowerAction) error
}
