package atenvm

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log"
	"net"
	"os"
	"testing"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

// fuzzImageSize keeps fuzzed drives small: 16 CD sectors or 64 disk blocks.
const fuzzImageSize = 16 * SectorSizeCD

// strictImage fails the test on any access outside it.
type strictImage struct {
	t    *testing.T
	data []byte
}

func (m *strictImage) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off > int64(len(m.data)) {
		m.t.Fatalf("read at %d of a %d-byte image", off, len(m.data))
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *strictImage) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(m.data)) {
		m.t.Fatalf("write of %d bytes at %d past a %d-byte image", len(p), off, len(m.data))
	}
	return copy(m.data[off:], p), nil
}

// fuzzDrive maps a strictImage as a read-only CD or a disk.
func fuzzDrive(t *testing.T, cd, readOnly bool) (*vmedia.Drive, *strictImage) {
	img := &strictImage{t: t, data: bytes.Repeat([]byte{0xa5}, fuzzImageSize)}
	o := vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: fuzzImageSize, ReadOnly: readOnly}
	if cd {
		o.Target = vmedia.TargetCD
	}
	d, err := vmedia.NewDrive(img, o)
	if err != nil {
		t.Fatal(err)
	}
	return d, img
}

// cbwFrame frames a CBW as the BMC sends it on the bulk-OUT endpoint.
func cbwFrame(tag, dataLen uint32, in bool, cdb ...byte) []byte {
	b := make([]byte, CBWLength)
	binary.LittleEndian.PutUint32(b[0:4], CBWSignature)
	binary.LittleEndian.PutUint32(b[4:8], tag)
	binary.LittleEndian.PutUint32(b[8:12], dataLen)
	if in {
		b[12] = 0x80
	}
	b[14] = byte(len(cdb))
	copy(b[15:], cdb)
	return append(Header{Endpoint: EndpointBulkOut, Flags: FlagBMCQueue, Tag: TagEnqueue, PayloadLen: CBWLength}.Encode(), b...)
}

func bulkOut(data []byte) []byte {
	return append(Header{Endpoint: EndpointBulkOut, Tag: TagEnqueue, PayloadLen: uint32(len(data))}.Encode(), data...)
}

// streamConn is a net.Conn that reads a fixed stream and discards writes.
type streamConn struct {
	net.Conn // nil: unused methods panic
	r        *bytes.Reader
	written  int
}

func (c *streamConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *streamConn) Write(p []byte) (int, error)      { c.written += len(p); return len(p), nil }
func (c *streamConn) Close() error                     { return nil }
func (c *streamConn) SetDeadline(time.Time) error      { return nil }
func (c *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

// FuzzReadFrame reads frames from arbitrary BMC bytes and parses each payload as the session and
// setup code would.
func FuzzReadFrame(f *testing.F) {
	f.Add(cbwFrame(1, 36, true, CmdInquiry, 0, 0, 0, 36, 0))
	f.Add(append(Header{Tag: TagDeviceSetupResp, PayloadLen: 5}.Encode(), 0, 1, 2, 3, 4))
	f.Add(append(Header{Tag: TagPingReq, PayloadLen: 4}.Encode(), 1, 2, 3, 4))
	f.Add(Header{PayloadLen: 16 << 20}.Encode())
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		for {
			h, payload, err := ReadFrame(r)
			if err != nil {
				return
			}
			if uint32(len(payload)) != h.PayloadLen {
				t.Fatalf("payload of %d bytes for a length of %d", len(payload), h.PayloadLen)
			}
			if cbw, err := ParseCBW(payload); err == nil && cbw.CDBLength > 16 {
				t.Fatalf("CDB length %d", cbw.CDBLength)
			}
			_, _ = ParseCSW(payload)
			_, _, _ = ParseDeviceSetupResponse(payload)
			_, _ = BuildKeepaliveResponse(payload)
		}
	})
}

// FuzzSCSITarget runs arbitrary CDBs against a CD and a disk.
func FuzzSCSITarget(f *testing.F) {
	f.Add(true, false, []byte{CmdRead10, 0, 0, 0, 0, 1, 0, 0, 2, 0}, uint32(2*SectorSizeCD), []byte(nil))
	f.Add(true, false, []byte{CmdRead12, 0, 0, 0, 0, 0, 0x20, 0, 0, 0, 0, 0}, uint32(0xFFFFFFFF), []byte(nil))
	f.Add(true, false, []byte{CmdReadTOC, 2, 0, 0, 0, 0, 0, 0, 0x0c, 0x40}, uint32(0x0c), []byte(nil))
	f.Add(true, false, []byte{CmdGetConfiguration, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 0}, uint32(0xffff), []byte(nil))
	f.Add(false, false, []byte{CmdWrite10, 0, 0, 0, 0, 3, 0, 0, 1, 0}, uint32(SectorSizeDisk), make([]byte, SectorSizeDisk))
	f.Add(false, false, []byte{CmdWrite10, 0, 0xff, 0xff, 0xff, 0xff, 0, 0, 1, 0}, uint32(SectorSizeDisk), make([]byte, SectorSizeDisk))
	f.Add(false, true, []byte{CmdWrite12, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0}, uint32(SectorSizeDisk), make([]byte, SectorSizeDisk))
	f.Add(false, false, []byte{CmdModeSense10, 0, 0x3f, 0, 0, 0, 0, 0xff, 0xff, 0}, uint32(0xffff), []byte(nil))
	f.Add(false, false, []byte{CmdRequestSense, 0, 0, 0, 18, 0}, uint32(18), []byte(nil))
	f.Add(false, false, []byte{CmdReadFormatCapacities, 0, 0, 0, 0, 0, 0, 0, 0xfc, 0}, uint32(0xfc), []byte(nil))
	f.Fuzz(func(t *testing.T, cd, readOnly bool, cdb []byte, dataLen uint32, data []byte) {
		d, img := fuzzDrive(t, cd, readOnly)
		before := bytes.Clone(img.data)
		tgt := newTarget(d)
		if out, _ := tgt.Execute(cdb, dataLen); uint64(len(out)) > uint64(dataLen) {
			t.Fatalf("returned %d bytes for a %d-byte transfer", len(out), dataLen)
		}
		_ = tgt.WriteData(cdb, data)
		if len(img.data) != fuzzImageSize || (d.ReadOnly && !bytes.Equal(img.data, before)) {
			t.Fatal("the image changed though the drive is read-only")
		}
	})
}

// FuzzDeviceSession serves arbitrary BMC frames on a plugged-in device until the stream ends.
func FuzzDeviceSession(f *testing.F) {
	f.Add(false, false, cbwFrame(1, 36, true, CmdInquiry, 0, 0, 0, 36, 0))
	f.Add(true, false, append(cbwFrame(2, 2*SectorSizeCD, true, CmdRead10, 0, 0, 0, 0, 1, 0, 0, 2, 0),
		cbwFrame(3, 0xFFFFFFFF, true, CmdRead12, 0, 0, 0, 0, 0, 0, 0, 0, 0x10, 0)...))
	write := cbwFrame(4, 2*SectorSizeDisk, false, CmdWrite10, 0, 0, 0, 0, 2, 0, 0, 2, 0)
	write = append(write, bulkOut(make([]byte, SectorSizeDisk))...)
	write = append(write, append(Header{Tag: TagPingReq}.Encode(), bulkOut(make([]byte, SectorSizeDisk))...)...)
	f.Add(false, false, write)
	f.Add(false, true, append(cbwFrame(5, SectorSizeDisk, false, CmdWrite10, 0, 0, 0, 0, 0, 0, 0, 1, 0), bulkOut(make([]byte, SectorSizeDisk))...))
	f.Add(true, false, append(cbwFrame(6, 12, false, 0x55, 0x10, 0, 0, 0, 0, 0, 0, 12, 0), bulkOut(make([]byte, 12))...))
	f.Add(false, false, cbwFrame(7, 0xFFFFFFFF, false, CmdWrite12, 0, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0xff, 0))
	// A 9-byte READ TOC and an 8-byte MODE SENSE(10) once panicked the serve goroutine.
	f.Add(true, false, cbwFrame(9, 0x14, true, CmdReadTOC, 0, 0, 0, 0, 0, 0, 0, 0x14))
	f.Add(false, false, cbwFrame(10, 0xff, true, CmdModeSense10, 0, 0x3f, 0, 0, 0, 0, 0))
	f.Add(false, false, append(Header{Tag: TagDetachResp}.Encode(), cbwFrame(8, 0, true, CmdTestUnitReady, 0, 0, 0, 0, 0)...))
	log.SetOutput(io.Discard) // malformed CBWs log
	f.Cleanup(func() { log.SetOutput(os.Stderr) })
	f.Fuzz(func(t *testing.T, cd, readOnly bool, stream []byte) {
		d, img := fuzzDrive(t, cd, readOnly)
		before := bytes.Clone(img.data)
		s := newDeviceSession(&streamConn{r: bytes.NewReader(stream)}, d, 0, time.Hour)
		s.serve(context.Background()) // returns once the stream ends or the BMC breaks protocol
		select {
		case <-s.done:
		default:
			t.Fatal("serve returned without closing the session")
		}
		if len(img.data) != fuzzImageSize || (d.ReadOnly && !bytes.Equal(img.data, before)) {
			t.Fatal("the image changed though the drive is read-only")
		}
	})
}
