package atenvm

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"ipmi-connect/internal/vmedia"
)

// fakeBMC is the BMC side of a virtual media connection over net.Pipe.
type fakeBMC struct {
	t    *testing.T
	conn net.Conn
}

func (b *fakeBMC) read() (Header, []byte) {
	b.t.Helper()
	_ = b.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	h, p, err := ReadFrame(b.conn)
	if err != nil {
		b.t.Fatalf("BMC read: %v", err)
	}
	return h, p
}

func (b *fakeBMC) write(h Header, p []byte) {
	b.t.Helper()
	if err := WriteFrame(b.conn, h, p); err != nil {
		b.t.Fatalf("BMC write: %v", err)
	}
}

// handshake completes Device Setup + EP Setup from the BMC side.
func (b *fakeBMC) handshake() {
	b.t.Helper()
	// The setup frame's header declares only the 44-byte prefix; read the trailing 174 descriptor
	// bytes too, as the real BMC does by walking them.
	_ = b.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	hdr := make([]byte, HeaderLen)
	if _, err := io.ReadFull(b.conn, hdr); err != nil {
		b.t.Fatalf("BMC read setup header: %v", err)
	}
	h, _ := DecodeHeader(hdr)
	if h.Tag != TagDeviceSetupReq {
		b.t.Fatalf("first frame tag 0x%02x, want device setup", h.Tag)
	}
	if _, err := io.ReadFull(b.conn, make([]byte, int(h.PayloadLen)+174)); err != nil {
		b.t.Fatalf("BMC read setup body: %v", err)
	}
	b.write(Header{Tag: TagDeviceSetupResp, PayloadLen: 9}, []byte{0, 0x78, 0x56, 0x34, 0x12, 0, 0, 0, 0})
	if h, _ := b.read(); h.Tag != TagEPSetup {
		b.t.Fatalf("second frame tag 0x%02x, want EP setup", h.Tag)
	}
}

// cbw sends a CBW to the client.
func (b *fakeBMC) cbw(tag, dataLen uint32, in bool, cdb ...byte) {
	b.t.Helper()
	w := make([]byte, 31)
	binary.LittleEndian.PutUint32(w[0:4], CBWSignature)
	binary.LittleEndian.PutUint32(w[4:8], tag)
	binary.LittleEndian.PutUint32(w[8:12], dataLen)
	if in {
		w[12] = 0x80
	}
	w[14] = byte(len(cdb))
	copy(w[15:], cdb)
	b.write(Header{Endpoint: EndpointBulkOut, Tag: TagEnqueue, PayloadLen: 31}, w)
}

// readResponse collects data frames until the CSW (tag 0xFF) and returns the data and CSW.
func (b *fakeBMC) readResponse() ([]byte, CSW) {
	b.t.Helper()
	var data []byte
	for {
		h, p := b.read()
		if h.Tag == TagEnqueueAndGo {
			csw, err := ParseCSW(p)
			if err != nil {
				b.t.Fatalf("parse CSW: %v", err)
			}
			return data, csw
		}
		data = append(data, p...)
	}
}

// newGroupOverPipe returns a Group whose connector hands each Map its own pipe; the BMC side of
// each is delivered on the conns channel.
func newGroupOverPipe(t *testing.T, creds Credentials) (*Group, <-chan *fakeBMC) {
	t.Helper()
	conns := make(chan *fakeBMC, 4)
	g := NewGroup(func(ctx context.Context) (net.Conn, Credentials, error) {
		server, client := net.Pipe()
		t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
		conns <- &fakeBMC{t: t, conn: server}
		return client, creds, nil
	})
	g.SetKeepaliveInterval(50 * time.Millisecond)
	t.Cleanup(func() { _ = g.Close() })
	return g, conns
}

func TestGroupMapServesCDAndDetaches(t *testing.T) {
	g, conns := newGroupOverPipe(t, Credentials{Username: "tok", Password: "tok", IsSessionID: true, Encrypt: true})
	img := &memImage{data: bytes.Repeat([]byte{0xCC}, 4*SectorSizeCD)}

	mapErr := make(chan error, 1)
	go func() {
		_, err := g.Map(context.Background(), img, vmedia.MapOptions{Target: vmedia.TargetCD, Size: int64(len(img.data))})
		mapErr <- err
	}()
	bmc := <-conns
	bmc.handshake()
	if err := <-mapErr; err != nil {
		t.Fatalf("Map: %v", err)
	}

	// INQUIRY.
	bmc.cbw(1, 36, true, CmdInquiry, 0, 0, 0, 36, 0)
	data, csw := bmc.readResponse()
	if csw.Status != CSWStatusGood || csw.Tag != 1 || !bytes.Contains(data, []byte("Virtual CDROM")) {
		t.Errorf("inquiry: csw %+v data %q", csw, data)
	}

	// READ CAPACITY: last LBA = 3, block size 2048.
	bmc.cbw(2, 8, true, CmdReadCapacity10, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	data, _ = bmc.readResponse()
	if binary.BigEndian.Uint32(data[0:4]) != 3 || binary.BigEndian.Uint32(data[4:8]) != 2048 {
		t.Errorf("read capacity data % x", data)
	}

	// READ(10) one sector at LBA 1.
	bmc.cbw(3, SectorSizeCD, true, CmdRead10, 0, 0, 0, 0, 1, 0, 0, 1, 0)
	data, csw = bmc.readResponse()
	if csw.Status != CSWStatusGood || len(data) != SectorSizeCD || data[0] != 0xCC {
		t.Errorf("read: csw %+v len %d", csw, len(data))
	}

	// Out-of-range READ(10) fails with residue = full length, no data.
	bmc.cbw(4, SectorSizeCD, true, CmdRead10, 0, 0, 0, 0, 9, 0, 0, 1, 0)
	data, csw = bmc.readResponse()
	if csw.Status != CSWStatusCommandFail || len(data) != 0 || csw.Residue != SectorSizeCD {
		t.Errorf("out-of-range read: csw %+v data %d", csw, len(data))
	}

	// The client sends keepalive requests (tag 0x04), not unsolicited 0x03.
	if h, _ := bmc.read(); h.Tag != TagPingReq {
		t.Errorf("keepalive tag 0x%02x, want 0x04", h.Tag)
	}

	// Close triggers a detach request.
	go func() { _ = g.Close() }()
	for {
		h, _ := bmc.read()
		if h.Tag == TagDetachReq {
			break
		}
	}
}

func TestGroupDiskMultiFrameWrite(t *testing.T) {
	g, conns := newGroupOverPipe(t, Credentials{Username: "tok", Password: "tok", IsSessionID: true})
	img := &memImage{data: make([]byte, 8*SectorSizeDisk)}

	mapErr := make(chan error, 1)
	go func() {
		_, err := g.Map(context.Background(), img, vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: int64(len(img.data))})
		mapErr <- err
	}()
	bmc := <-conns
	bmc.handshake()
	if err := <-mapErr; err != nil {
		t.Fatalf("Map: %v", err)
	}

	// WRITE(10): 2 sectors at LBA 3. Send the 1024 bytes split across two frames with a keepalive
	// request interleaved, which the client must answer without corrupting the write.
	bmc.cbw(7, 2*SectorSizeDisk, false, CmdWrite10, 0, 0, 0, 0, 3, 0, 0, 2, 0)
	first := bytes.Repeat([]byte{0xA1}, SectorSizeDisk)
	second := bytes.Repeat([]byte{0xB2}, SectorSizeDisk)
	bmc.write(Header{Endpoint: EndpointBulkOut, Tag: TagEnqueue, PayloadLen: SectorSizeDisk}, first)
	bmc.write(Header{Tag: TagPingResp, PayloadLen: 4}, []byte{0xFF, 0xFF, 0xFF, 0xFF})
	bmc.write(Header{Endpoint: EndpointBulkOut, Tag: TagEnqueue, PayloadLen: SectorSizeDisk}, second)

	_, csw := bmc.readResponse()
	if csw.Status != CSWStatusGood {
		t.Errorf("write CSW status %#x", csw.Status)
	}
	if !bytes.Equal(img.data[3*SectorSizeDisk:4*SectorSizeDisk], first) ||
		!bytes.Equal(img.data[4*SectorSizeDisk:5*SectorSizeDisk], second) {
		t.Error("multi-frame write did not reassemble correctly")
	}
}

func TestGroupRejectsSecondDisk(t *testing.T) {
	g, conns := newGroupOverPipe(t, Credentials{Username: "t", Password: "t", IsSessionID: true})
	img := &memImage{data: make([]byte, 4*SectorSizeDisk)}
	mapErr := make(chan error, 1)
	go func() {
		_, err := g.Map(context.Background(), img, vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: int64(len(img.data))})
		mapErr <- err
	}()
	(<-conns).handshake()
	if err := <-mapErr; err != nil {
		t.Fatalf("first Map: %v", err)
	}
	// A floppy also needs slot 1; the group must refuse it while removable holds the slot, without
	// opening a second connection.
	if _, err := g.Map(context.Background(), img, vmedia.MapOptions{Target: vmedia.TargetFloppy, Size: int64(len(img.data))}); err == nil {
		t.Error("second disk-type drive was accepted")
	}
}

func TestOversizedOutCBWClosesSession(t *testing.T) {
	g, conns := newGroupOverPipe(t, Credentials{Username: "t", Password: "t", IsSessionID: true})
	img := &memImage{data: make([]byte, 8*SectorSizeDisk)}
	mapErr := make(chan error, 1)
	go func() {
		_, err := g.Map(context.Background(), img, vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: int64(len(img.data))})
		mapErr <- err
	}()
	bmc := <-conns
	bmc.handshake()
	if err := <-mapErr; err != nil {
		t.Fatalf("Map: %v", err)
	}
	// A WRITE claiming 4 GiB of data must not pre-allocate; the session is dropped instead.
	bmc.cbw(9, 0xFFFFFFFF, false, CmdWrite10, 0, 0, 0, 0, 0, 0, 0, 1, 0)
	deadline := time.Now().Add(2 * time.Second)
	for len(g.Drives()) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("session with an oversized OUT CBW was not dropped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestControlOutToReadOnlyCDSurvives(t *testing.T) {
	g, conns := newGroupOverPipe(t, Credentials{Username: "t", Password: "t", IsSessionID: true, Encrypt: true})
	img := &memImage{data: make([]byte, 4*SectorSizeCD)}
	mapErr := make(chan error, 1)
	go func() {
		_, err := g.Map(context.Background(), img, vmedia.MapOptions{Target: vmedia.TargetCD, Size: int64(len(img.data))})
		mapErr <- err
	}()
	bmc := <-conns
	bmc.handshake()
	if err := <-mapErr; err != nil {
		t.Fatalf("Map: %v", err)
	}
	// MODE SELECT(6): a small OUT command to the CD. It must be refused (CSW fail) without
	// killing the session.
	bmc.cbw(1, 12, false, 0x15, 0x10, 0, 0, 12, 0)
	bmc.write(Header{Endpoint: EndpointBulkOut, Tag: TagEnqueue, PayloadLen: 12}, make([]byte, 12))
	_, csw := bmc.readResponse()
	if csw.Status != CSWStatusCommandFail {
		t.Errorf("MODE SELECT to CD: status %#x, want fail", csw.Status)
	}
	// The session is still alive: a following INQUIRY works.
	bmc.cbw(2, 36, true, CmdInquiry, 0, 0, 0, 36, 0)
	if data, _ := bmc.readResponse(); !bytes.Contains(data, []byte("CDROM")) {
		t.Error("session did not survive a control OUT to the CD")
	}
}

func TestReadTOCLegacyByte9Format(t *testing.T) {
	tgt := isoTarget(t, &memImage{data: make([]byte, 4*SectorSizeCD)}, 4*SectorSizeCD)
	// Legacy MMC/ATAPI selects session-info via byte 9 top bits (0x40), byte 2 = 0.
	data, ok := tgt.Execute([]byte{CmdReadTOC, 0, 0, 0, 0, 0, 0, 0, 0x0c, 0x40}, 0x0c)
	if !ok || len(data) != 12 || data[1] != 0x0A {
		t.Errorf("legacy byte-9 format 1 not honoured: ok=%v data=% x", ok, data)
	}
}

// chunkImage is a zero-filled image that records the largest single read and write it was asked
// for, to show the session streams instead of buffering a whole transfer.
type chunkImage struct {
	mu                sync.Mutex
	data              []byte
	maxRead, maxWrite int
}

func (c *chunkImage) ReadAt(p []byte, off int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxRead = max(c.maxRead, len(p))
	return copy(p, c.data[off:]), nil
}

func (c *chunkImage) WriteAt(p []byte, off int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxWrite = max(c.maxWrite, len(p))
	return copy(c.data[off:], p), nil
}

func mapOverPipe(t *testing.T, img vmedia.Image, o vmedia.MapOptions) *fakeBMC {
	t.Helper()
	g, conns := newGroupOverPipe(t, Credentials{Username: "t", Password: "t", IsSessionID: true})
	mapErr := make(chan error, 1)
	go func() {
		_, err := g.Map(context.Background(), img, o)
		mapErr <- err
	}()
	bmc := <-conns
	bmc.handshake()
	if err := <-mapErr; err != nil {
		t.Fatalf("Map: %v", err)
	}
	return bmc
}

func TestLargeReadIsStreamedInChunks(t *testing.T) {
	const sectors = 4096 // 8 MiB
	img := &chunkImage{data: make([]byte, sectors*SectorSizeCD)}
	bmc := mapOverPipe(t, img, vmedia.MapOptions{Target: vmedia.TargetCD, Size: int64(len(img.data))})

	// READ(12) of the whole disc in one command.
	bmc.cbw(1, sectors*SectorSizeCD, true, CmdRead12, 0, 0, 0, 0, 0, 0, 0, 0x10, 0, 0, 0)
	data, csw := bmc.readResponse()
	if csw.Status != CSWStatusGood || len(data) != sectors*SectorSizeCD || csw.Residue != 0 {
		t.Fatalf("status %d, %d bytes, residue %d", csw.Status, len(data), csw.Residue)
	}
	img.mu.Lock()
	defer img.mu.Unlock()
	if img.maxRead > maxBulkChunk {
		t.Errorf("image read in a %d-byte piece, want at most %d", img.maxRead, maxBulkChunk)
	}
}

func TestWriteIsStreamedFrameByFrame(t *testing.T) {
	const frame = 8 * SectorSizeDisk
	img := &chunkImage{data: make([]byte, 64*SectorSizeDisk)}
	bmc := mapOverPipe(t, img, vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: int64(len(img.data))})

	// WRITE(10): 32 blocks at LBA 8, sent as four frames.
	bmc.cbw(2, 4*frame, false, CmdWrite10, 0, 0, 0, 0, 8, 0, 0, 32, 0)
	for i := range 4 {
		bmc.write(Header{Endpoint: EndpointBulkOut, Tag: TagEnqueue, PayloadLen: frame}, bytes.Repeat([]byte{byte(i + 1)}, frame))
	}
	if _, csw := bmc.readResponse(); csw.Status != CSWStatusGood {
		t.Fatalf("CSW status %d", csw.Status)
	}
	img.mu.Lock()
	defer img.mu.Unlock()
	for i := range 4 {
		start := 8*SectorSizeDisk + i*frame
		if !bytes.Equal(img.data[start:start+frame], bytes.Repeat([]byte{byte(i + 1)}, frame)) {
			t.Errorf("frame %d not written in place", i)
		}
	}
	if img.maxWrite > frame {
		t.Errorf("image written in a %d-byte piece, want at most one frame (%d)", img.maxWrite, frame)
	}
}

func TestRefusedWriteIsDrainedAndSessionSurvives(t *testing.T) {
	img := &chunkImage{data: make([]byte, 8*SectorSizeDisk)}
	bmc := mapOverPipe(t, img, vmedia.MapOptions{Target: vmedia.TargetRemovable, Size: int64(len(img.data)), ReadOnly: true})

	bmc.cbw(3, SectorSizeDisk, false, CmdWrite10, 0, 0, 0, 0, 0, 0, 0, 1, 0)
	bmc.write(Header{Endpoint: EndpointBulkOut, Tag: TagEnqueue, PayloadLen: SectorSizeDisk}, bytes.Repeat([]byte{0xFF}, SectorSizeDisk))
	if _, csw := bmc.readResponse(); csw.Status != CSWStatusCommandFail {
		t.Fatalf("write to a read-only drive: CSW status %d", csw.Status)
	}
	// The next command must still be served: the refused data was drained, not taken as a CBW.
	bmc.cbw(4, 0, true, CmdTestUnitReady, 0, 0, 0, 0, 0)
	if _, csw := bmc.readResponse(); csw.Status != CSWStatusGood || csw.Tag != 4 {
		t.Fatalf("after the refused write: CSW %+v", csw)
	}
	if img.maxWrite != 0 {
		t.Error("read-only image was written")
	}
}
