package main

import (
	"bytes"
	"testing"

	"github.com/mhamzahkhan/ipmi-connect/internal/dell"
)

func chunk(first bool, w, h uint16, data ...byte) *dell.VideoPacket {
	var flags byte
	if first {
		flags = 1
	}
	return &dell.VideoPacket{Flags: flags, Width: w, Height: h, LumaSelector: 7, Bitstream: data}
}

func TestFrameAssembler(t *testing.T) {
	t.Run("continuation without a first chunk is dropped", func(t *testing.T) {
		var a frameAssembler
		if done := a.add(chunk(false, 0, 0, 9, 9)); done != nil {
			t.Fatalf("orphan chunk produced a frame: %+v", done)
		}
		if f := a.take(); f != nil {
			t.Errorf("orphan chunk was buffered: %+v", f)
		}
	})

	t.Run("chunks join under the first chunk's geometry", func(t *testing.T) {
		var a frameAssembler
		a.add(chunk(true, 720, 400, 1, 2))
		a.add(chunk(false, 0, 0, 3))
		f := a.take()
		if f == nil || f.Width != 720 || f.Height != 400 || f.LumaSelector != 7 {
			t.Fatalf("frame = %+v", f)
		}
		if !bytes.Equal(f.Bitstream, []byte{1, 2, 3}) {
			t.Errorf("bitstream % x", f.Bitstream)
		}
		if a.take() != nil {
			t.Error("take did not reset the assembler")
		}
	})

	t.Run("a new first chunk completes the previous frame", func(t *testing.T) {
		var a frameAssembler
		a.add(chunk(true, 720, 400, 1))
		done := a.add(chunk(true, 1024, 768, 2))
		if done == nil || done.Width != 720 || !bytes.Equal(done.Bitstream, []byte{1}) {
			t.Fatalf("completed frame = %+v", done)
		}
		if f := a.take(); f == nil || f.Width != 1024 {
			t.Errorf("new frame = %+v", f)
		}
	})

	t.Run("completed frames do not alias the buffer", func(t *testing.T) {
		var a frameAssembler
		a.add(chunk(true, 1, 1, 1, 1))
		done := a.add(chunk(true, 1, 1, 2, 2))
		a.add(chunk(false, 0, 0, 3, 3))
		if !bytes.Equal(done.Bitstream, []byte{1, 1}) {
			t.Errorf("completed frame mutated to % x", done.Bitstream)
		}
	})
}
