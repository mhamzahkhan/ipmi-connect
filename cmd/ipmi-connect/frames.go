package main

import "github.com/mhamzahkhan/ipmi-connect/internal/dell"

// frameAssembler joins ib chunks into whole ASPEED frames. A frame starts with a chunk whose
// first-chunk flag is set; continuation chunks extend it. Chunks that arrive with no frame in
// progress (after a reconnect, or after a frame was flushed) are dropped: decoded on their own
// they paint garbage over the whole screen.
type frameAssembler struct {
	frame *dell.VideoPacket // frame being assembled; Bitstream accumulates its chunks
}

// add consumes one chunk and returns the previous frame if this chunk completed it.
func (a *frameAssembler) add(pkt *dell.VideoPacket) *dell.VideoPacket {
	if pkt.Flags&1 == 0 {
		if a.frame != nil {
			a.frame.Bitstream = append(a.frame.Bitstream, pkt.Bitstream...)
		}
		return nil
	}

	done := a.take()
	next := *pkt
	next.Bitstream = append([]byte(nil), pkt.Bitstream...)
	a.frame = &next
	return done
}

// take returns the frame in progress, if any, and resets the assembler.
func (a *frameAssembler) take() *dell.VideoPacket {
	f := a.frame
	a.frame = nil
	return f
}

func (a *frameAssembler) inProgress() bool { return a.frame != nil }
