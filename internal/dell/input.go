package dell

// SendPointerEvent forwards mouse movement and button clicks (Packet 513 / jc) on the Control
// Channel. buttons uses the RFB convention (left 1, middle 2, right 4).
func (c *Client) SendPointerEvent(buttons byte, x, y uint16) error {
	return c.writeControl(pointerPacket(avocentButtons(buttons), x, y))
}

// SendKeyEvent forwards keyboard press and release events (Packet 512 / e) on the Control Channel.
func (c *Client) SendKeyEvent(down bool, hidCode uint16) error {
	return c.writeControl(keyPacket(down, hidCode))
}
