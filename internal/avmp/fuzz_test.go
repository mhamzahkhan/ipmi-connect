package avmp

import (
	"bytes"
	"context"
	"io"
	"log"
	"net"
	"os"
	"testing"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

// fuzzImageSize keeps fuzzed drives small: 16 CD sectors or 64 disk blocks.
const fuzzImageSize = 16 * cdBlockSize

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

// streamConn is a net.Conn that reads a fixed stream and discards writes.
type streamConn struct {
	net.Conn // nil: unused methods panic
	r        *bytes.Reader
}

func (c *streamConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *streamConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *streamConn) Close() error                     { return nil }
func (c *streamConn) SetReadDeadline(time.Time) error  { return nil }
func (c *streamConn) SetWriteDeadline(time.Time) error { return nil }

func msg(typ uint16, body []byte) []byte { return append(header(typ, headerLen+len(body)), body...) }

// FuzzReadMessage reads AVMP packets from arbitrary BMC bytes.
func FuzzReadMessage(f *testing.F) {
	f.Add(msg(typeLoginStatus, loginStatus(0)))
	f.Add(append(msg(typeVDiskInfo, standardDrives()), msg(typeVDiskRead, readRequest(0, 0, 1, 16))...))
	f.Add(header(typeVDiskWrite, maxPacket))
	f.Add(header(typeVDiskWrite, headerLen-1))
	f.Fuzz(func(t *testing.T, data []byte) {
		r := bytes.NewReader(data)
		for {
			m, err := readMessage(r)
			if err != nil {
				return
			}
			if len(m.body) > maxPacket-headerLen {
				t.Fatalf("body of %d bytes", len(m.body))
			}
			_ = loginHolder(m.body)
		}
	})
}

// FuzzSession logs in and serves arbitrary BMC packets with a CD (drive 0), a writable disk
// (drive 1) and a read-only disk (drive 2) mapped, until the stream ends.
func FuzzSession(f *testing.F) {
	login := msg(typeLoginStatus, loginStatus(0))
	f.Add(append(login, msg(typeVDiskRead, readRequest(0, 1, 2, 1))...))
	f.Add(append(login, msg(typeVDiskRead, readRequest(1, 0xFFFFFFFF, maxReadBlocks, 0))...))
	f.Add(append(login, msg(typeVDiskWrite, writeRequest(1, 3, 1, make([]byte, vmedia.BlockSizeDisk)))...))
	f.Add(append(login, msg(typeVDiskWrite, writeRequest(2, 0, 1, make([]byte, vmedia.BlockSizeDisk)))...))
	f.Add(append(login, msg(typeVDiskWrite, writeRequest(1, 0, 0x00800001, make([]byte, vmedia.BlockSizeDisk)))...))
	f.Add(append(login, msg(typeVDiskInfo, standardDrives())...))
	f.Add(append(msg(typeDeviceConfigOption, []byte{0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1}), login...))
	f.Add(append(login, msg(typeDisconnect, []byte{0, 0, 4, 0, 0, 200, 'x'})...))
	f.Add(append(login, msg(typeDeviceStatus, []byte{0, 1, 0, 0, 0, 2})...))
	f.Add(append(login, msg(typeRequestRelease, []byte{0, 2})...))
	f.Add(msg(typeLoginStatus, []byte{0, 0, 6, 0, 200, 'h', 'o', 'l', 'd'}))
	log.SetOutput(io.Discard) // refused requests log
	f.Cleanup(func() { log.SetOutput(os.Stderr) })
	f.Fuzz(func(t *testing.T, stream []byte) {
		s := NewSession(&streamConn{r: bytes.NewReader(stream)}, "user", "pass")
		var images []*strictImage
		var before [][]byte
		for disk, o := range []vmedia.MapOptions{
			{Target: vmedia.TargetCD, Size: fuzzImageSize},
			{Target: vmedia.TargetRemovable, Size: fuzzImageSize},
			{Target: vmedia.TargetRemovable, Size: fuzzImageSize, ReadOnly: true},
		} {
			img := &strictImage{t: t, data: bytes.Repeat([]byte{0xa5}, fuzzImageSize)}
			d, err := vmedia.NewDrive(img, o)
			if err != nil {
				t.Fatal(err)
			}
			s.drives[uint16(disk)] = d
			images, before = append(images, img), append(before, bytes.Clone(img.data))
		}
		_ = s.login(context.Background())
		s.run() // returns once the stream ends or the BMC breaks protocol
		select {
		case <-s.done:
		default:
			t.Fatal("run returned without ending the session")
		}
		select {
		case body := <-s.info:
			_, _ = s.pickDrive(body, TargetCD)
		default:
		}
		for i, img := range images {
			if len(img.data) != fuzzImageSize || (i != 1 && !bytes.Equal(img.data, before[i])) {
				t.Fatalf("read-only drive %d was written", i)
			}
		}
	})
}
