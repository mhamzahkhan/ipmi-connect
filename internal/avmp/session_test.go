package avmp

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBMC is the BMC end of an AVMP connection.
type fakeBMC struct {
	t    *testing.T
	conn net.Conn
}

func (b *fakeBMC) send(typ uint16, body []byte) {
	b.t.Helper()
	if _, err := b.conn.Write(append(header(typ, 10+len(body)), body...)); err != nil {
		b.t.Errorf("fake BMC write: %v", err)
	}
}

// next reads the client's next packet, skipping heartbeats.
func (b *fakeBMC) next() (uint16, []byte) {
	b.t.Helper()
	_ = b.conn.SetReadDeadline(time.Now().Add(3 * time.Second)) // overall: heartbeats must not extend it
	for {
		hdr := make([]byte, 10)
		if _, err := io.ReadFull(b.conn, hdr); err != nil {
			b.t.Fatalf("fake BMC read: %v", err)
		}
		body := make([]byte, binary.BigEndian.Uint32(hdr[4:8])-10)
		if _, err := io.ReadFull(b.conn, body); err != nil {
			b.t.Fatalf("fake BMC read body: %v", err)
		}
		if typ := binary.BigEndian.Uint16(hdr[8:10]); typ != typeHeartbeat {
			return typ, body
		}
	}
}

// expect reads the next packet and checks its type.
func (b *fakeBMC) expect(typ uint16) []byte {
	b.t.Helper()
	got, body := b.next()
	if got != typ {
		b.t.Fatalf("client sent %#04x, want %#04x", got, typ)
	}
	return body
}

func loginStatus(status byte) []byte {
	b := make([]byte, 101)
	b[2] = status
	return b
}

func vdiskInfo(drives ...[2]byte) []byte {
	b := []byte{0xff, 0xff}
	b = binary.BigEndian.AppendUint16(b, uint16(len(drives)))
	for _, d := range drives {
		b = append(b, d[0], d[1], 0, 1)
	}
	return b
}

// standardDrives is what the C6220 reports: CD, removable, floppy, all free.
func standardDrives() []byte { return vdiskInfo([2]byte{2, 0}, [2]byte{1, 0}, [2]byte{4, 0}) }

func readRequest(disk uint16, start, n, factor uint32) []byte {
	b := binary.BigEndian.AppendUint16(nil, disk)
	b = binary.BigEndian.AppendUint32(b, start)
	b = binary.BigEndian.AppendUint32(b, n)
	return binary.BigEndian.AppendUint32(b, factor)
}

func writeRequest(disk uint16, start, n uint32, data []byte) []byte {
	b := binary.BigEndian.AppendUint16(nil, disk)
	b = binary.BigEndian.AppendUint32(b, start)
	b = binary.BigEndian.AppendUint32(b, n)
	return append(b, data...)
}

// memImage is an in-memory disk image.
type memImage struct {
	mu   sync.Mutex
	data []byte
}

func (m *memImage) ReadAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (m *memImage) WriteAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return copy(m.data[off:], p), nil
}

func (m *memImage) bytes() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.data...)
}

// iso is a 3-block CD image whose blocks are filled with 1, 2 and 3.
func iso() *memImage {
	return &memImage{data: bytes.Join([][]byte{
		bytes.Repeat([]byte{1}, cdBlockSize), bytes.Repeat([]byte{2}, cdBlockSize), bytes.Repeat([]byte{3}, cdBlockSize),
	}, nil)}
}

// open starts a session against a fake BMC and completes the login.
func open(t *testing.T) (*fakeBMC, *Session) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	bmc := &fakeBMC{t: t, conn: server}
	s := NewSession(client, "user", "pass")
	s.heartbeatEvery = 20 * time.Millisecond
	s.driveWait, s.driveRetry = 300*time.Millisecond, 10*time.Millisecond
	errc := make(chan error, 1)
	go func() { errc <- s.Open(context.Background()) }()
	if body := bmc.expect(typeLogin); string(body[3:7]) != "user" {
		t.Fatalf("login body % x", body[:8])
	}
	bmc.send(typeLoginStatus, loginStatus(0))
	if err := <-errc; err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		go func() { _, _ = io.Copy(io.Discard, server) }()
		_ = s.Close()
	})
	return bmc, s
}

type mapResult struct {
	disk uint16
	err  error
}

// mapAsync starts a Map and plays the BMC's drive-info answer.
func mapAsync(t *testing.T, bmc *fakeBMC, s *Session, img Image, o MapOptions, info []byte) <-chan mapResult {
	t.Helper()
	res := make(chan mapResult, 1)
	go func() {
		d, err := s.Map(context.Background(), img, o)
		res <- mapResult{d, err}
	}()
	bmc.expect(typeGetVDiskInfo)
	bmc.send(typeVDiskInfo, info)
	return res
}

func TestMapCDAndServeReads(t *testing.T) {
	bmc, s := open(t)
	img := iso()
	res := mapAsync(t, bmc, s, img, MapOptions{Target: TargetCD, Size: 3 * cdBlockSize}, standardDrives())
	body := bmc.expect(typeMapCD)
	if disk := binary.BigEndian.Uint16(body[0:2]); disk != 0 || binary.BigEndian.Uint32(body[6:10]) != 3 {
		t.Errorf("map body % x", body[:10])
	}
	if r := <-res; r.err != nil || r.disk != 0 {
		t.Fatalf("Map = %d, %v", r.disk, r.err)
	}

	bmc.send(typeVDiskRead, readRequest(0, 0, 3, 2))
	var got []byte
	for _, wantStart := range []uint32{0, 2} {
		body := bmc.expect(typeReadData)
		if st := binary.BigEndian.Uint32(body[2:6]); st != wantStart {
			t.Errorf("chunk start %d, want %d", st, wantStart)
		}
		got = append(got, body[10:]...)
	}
	if !bytes.Equal(got, img.bytes()) {
		t.Error("served data does not match the image")
	}
	if body := bmc.expect(typeClientStatus); binary.BigEndian.Uint32(body[2:6]) != statusOK {
		t.Errorf("status % x", body)
	}
	if d := s.Drives(); len(d) != 1 || d[0].BytesRead != 3*cdBlockSize || !d[0].ReadOnly {
		t.Errorf("drives %+v", d)
	}

	// Past the end of the image: zeros.
	bmc.send(typeVDiskRead, readRequest(0, 5, 1, 4))
	if body := bmc.expect(typeReadData); !bytes.Equal(body[10:], make([]byte, cdBlockSize)) {
		t.Error("past-EOF read was not zeros")
	}
	bmc.expect(typeClientStatus)

	unmapped := make(chan error, 1)
	go func() { unmapped <- s.Unmap(0) }() // net.Pipe writes block until the BMC reads
	if body := bmc.expect(typeRelease); !bytes.Equal(body, []byte{0, 0}) {
		t.Errorf("release body % x", body)
	}
	if err := <-unmapped; err != nil {
		t.Fatal(err)
	}
	if len(s.Drives()) != 0 {
		t.Error("drive still listed after unmap")
	}
}

func TestMapFloppyAndRemovableTogether(t *testing.T) {
	bmc, s := open(t)
	floppy := &memImage{data: make([]byte, 2880*512)}
	disk := &memImage{data: make([]byte, 204800*512)}

	res := mapAsync(t, bmc, s, floppy, MapOptions{Target: TargetFloppy, Size: int64(len(floppy.data))}, standardDrives())
	body := bmc.expect(typeMapDisk)
	if !bytes.Equal(body, h("0002"+"00000200"+"00000b40"+"00"+"ff"+"0050"+"02"+"12")) {
		t.Errorf("floppy map body % x", body)
	}
	if r := <-res; r.err != nil || r.disk != 2 {
		t.Fatalf("floppy Map = %d, %v", r.disk, r.err)
	}

	// The BMC now reports the floppy as attached; the removable drive is still free.
	res = mapAsync(t, bmc, s, disk, MapOptions{Target: TargetRemovable, Size: int64(len(disk.data)), ReadOnly: true},
		vdiskInfo([2]byte{2, 0}, [2]byte{1, 0}, [2]byte{4, 1}))
	body = bmc.expect(typeMapDisk)
	if binary.BigEndian.Uint16(body[0:2]) != 1 || body[10] != 1 {
		t.Errorf("removable map body % x, want drive 1 read-only", body)
	}
	if r := <-res; r.err != nil || r.disk != 1 {
		t.Fatalf("removable Map = %d, %v", r.disk, r.err)
	}

	// Writes reach the right image; reads are routed by drive.
	payload := bytes.Repeat([]byte{0xab}, 1024)
	bmc.send(typeVDiskWrite, writeRequest(2, 3, 2, payload))
	if body := bmc.expect(typeClientStatus); binary.BigEndian.Uint16(body[0:2]) != 2 || binary.BigEndian.Uint32(body[2:6]) != statusOK {
		t.Errorf("write status % x", body)
	}
	if !bytes.Equal(floppy.bytes()[3*512:5*512], payload) {
		t.Error("write did not land at block 3 of the floppy")
	}

	// The removable drive is read-only: the write fails and the image is untouched.
	bmc.send(typeVDiskWrite, writeRequest(1, 0, 1, payload[:512]))
	if body := bmc.expect(typeClientStatus); binary.BigEndian.Uint32(body[2:6]) != statusWriteFailed {
		t.Errorf("read-only write status % x", body)
	}
	if !bytes.Equal(disk.bytes()[:512], make([]byte, 512)) {
		t.Error("read-only image was modified")
	}

	// 512-byte drives get packets of at most 32 KiB (64 blocks), whatever the blocking factor.
	bmc.send(typeVDiskRead, readRequest(1, 0, 100, 200))
	if body := bmc.expect(typeReadData); binary.BigEndian.Uint32(body[6:10]) != 64 {
		t.Errorf("first chunk %d blocks, want 64", binary.BigEndian.Uint32(body[6:10]))
	}
	bmc.expect(typeReadData)
	bmc.expect(typeClientStatus)

	var written int64
	for _, d := range s.Drives() {
		written += d.BytesWritten
	}
	if written != 1024 {
		t.Errorf("bytes written %d, want 1024", written)
	}

	// The BMC releasing the floppy leaves the removable drive mapped.
	bmc.send(typeRequestRelease, []byte{0, 2})
	bmc.expect(typeRelease)
	waitFor(t, func() bool { return len(s.Drives()) == 1 })
	if d := s.Drives(); d[0].Disk != 1 {
		t.Errorf("remaining drive %+v", d)
	}
}

func TestMapValidation(t *testing.T) {
	_, s := open(t)
	tests := []struct {
		name string
		o    MapOptions
		want string
	}{
		{"floppy too big", MapOptions{Target: TargetFloppy, Size: 2_880_000}, "floppy"},
		{"empty image", MapOptions{Target: TargetCD, Size: 0}, "empty"},
		{"image smaller than a block", MapOptions{Target: TargetRemovable, Size: 100}, "empty"},
		{"unknown target", MapOptions{Target: 9, Size: 4096}, "target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := s.Map(context.Background(), &memImage{}, tt.o); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestMapWaitsForAFreeDrive(t *testing.T) {
	bmc, s := open(t)
	res := mapAsync(t, bmc, s, iso(), MapOptions{Target: TargetCD, Size: 3 * cdBlockSize},
		vdiskInfo([2]byte{0, 2}, [2]byte{0, 2}, [2]byte{0, 2})) // just after another session
	bmc.expect(typeGetVDiskInfo)
	bmc.send(typeVDiskInfo, standardDrives())
	bmc.expect(typeMapCD)
	if r := <-res; r.err != nil {
		t.Fatalf("Map = %v", r.err)
	}
}

func TestMapGivesUpWhenNoDriveFrees(t *testing.T) {
	bmc, s := open(t)
	res := make(chan error, 1)
	go func() {
		_, err := s.Map(context.Background(), iso(), MapOptions{Target: TargetCD, Size: 3 * cdBlockSize})
		res <- err
	}()
	for {
		select {
		case err := <-res:
			if err == nil || !strings.Contains(err.Error(), "no free") {
				t.Errorf("err = %v", err)
			}
			return
		default:
		}
		_ = bmc.conn.SetReadDeadline(time.Now().Add(time.Second))
		hdr := make([]byte, 12)
		if _, err := io.ReadFull(bmc.conn, hdr); err != nil {
			t.Fatalf("read: %v", err)
		}
		if binary.BigEndian.Uint16(hdr[8:10]) == typeGetVDiskInfo {
			bmc.send(typeVDiskInfo, vdiskInfo([2]byte{2, 1}, [2]byte{1, 1}, [2]byte{4, 1}))
		}
	}
}

func TestPerDriveDeviceStatus(t *testing.T) {
	bmc, s := open(t)
	res := mapAsync(t, bmc, s, iso(), MapOptions{Target: TargetCD, Size: 3 * cdBlockSize}, standardDrives())
	bmc.expect(typeMapCD)
	<-res
	bmc.send(typeDeviceStatus, []byte{0, 2, 0, 0, 0, 6}) // floppy "disabled": not ours
	bmc.send(typeDeviceStatus, []byte{0, 0, 0, 0, 0, 4}) // our CD: USB not connected
	bmc.expect(typeRelease)
	waitFor(t, func() bool { return len(s.Drives()) == 0 })
	if ev := s.LastEvent(); !strings.Contains(ev, "USB not connected") {
		t.Errorf("last event %q", ev)
	}
	select {
	case <-s.Done():
		t.Error("a drive failure ended the whole session")
	default:
	}
}

func TestReadOnlyOptionForcesLaterMaps(t *testing.T) {
	bmc, s := open(t)
	opt := make([]byte, 11)
	binary.BigEndian.PutUint32(opt[2:6], 1) // option 1: READ_ONLY
	opt[10] = 1
	bmc.send(typeDeviceConfigOption, opt)
	img := &memImage{data: make([]byte, 2880*512)}
	waitFor(t, func() bool { return s.forcedReadOnly() })
	res := mapAsync(t, bmc, s, img, MapOptions{Target: TargetRemovable, Size: int64(len(img.data))}, standardDrives())
	if body := bmc.expect(typeMapDisk); body[10] != 1 {
		t.Error("map was not read-only after the BMC's READ_ONLY option")
	}
	<-res
}

func TestShortRequestsStillGetAStatus(t *testing.T) {
	bmc, _ := open(t)
	bmc.send(typeVDiskRead, []byte{0, 0, 0, 0})
	if body := bmc.expect(typeClientStatus); binary.BigEndian.Uint32(body[2:6]) != statusReadFailed {
		t.Errorf("short read: status % x", body)
	}
	bmc.send(typeVDiskWrite, []byte{0, 1, 0})
	if body := bmc.expect(typeClientStatus); binary.BigEndian.Uint16(body[0:2]) != 1 || binary.BigEndian.Uint32(body[2:6]) != statusWriteFailed {
		t.Errorf("short write: status % x", body)
	}
}

func TestReadOnlyOptionDuringLogin(t *testing.T) {
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	bmc := &fakeBMC{t: t, conn: server}
	s := NewSession(client, "user", "pass")
	t.Cleanup(func() {
		go func() { _, _ = io.Copy(io.Discard, server) }()
		_ = s.Close()
	})
	errc := make(chan error, 1)
	go func() { errc <- s.Open(context.Background()) }()
	bmc.expect(typeLogin)
	opt := make([]byte, 11)
	binary.BigEndian.PutUint32(opt[2:6], 1)
	opt[10] = 1
	bmc.send(typeDeviceConfigOption, opt)
	bmc.send(typeLoginStatus, loginStatus(0))
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if !s.forcedReadOnly() {
		t.Error("a READ_ONLY option sent before the login reply was lost")
	}
}

func TestUSBReset(t *testing.T) {
	bmc, s := open(t)
	reset := make(chan error, 1)
	go func() { reset <- s.USBReset() }()
	if body := bmc.expect(typeUSBReset); !bytes.Equal(body, []byte{0xff, 0xff}) {
		t.Errorf("body % x", body)
	}
	if err := <-reset; err != nil {
		t.Fatal(err)
	}
}

func TestPreemptionReleasesEverythingAndEnds(t *testing.T) {
	bmc, s := open(t)
	res := mapAsync(t, bmc, s, iso(), MapOptions{Target: TargetCD, Size: 3 * cdBlockSize}, standardDrives())
	bmc.expect(typeMapCD)
	<-res
	bmc.send(typeDisconnect, []byte{0, 0, 4, 0, 30, 5, 'a', 'd', 'm', 'i', 'n'})
	if body := bmc.expect(typePreemptResponse); body[2] != 0 {
		t.Errorf("preempt response % x", body)
	}
	bmc.expect(typeRelease)
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session did not end")
	}
	if err := s.Err(); err == nil || !strings.Contains(err.Error(), "admin") {
		t.Errorf("Err = %v", err)
	}
}

func TestCloseReleasesMappedDrives(t *testing.T) {
	bmc, s := open(t)
	res := mapAsync(t, bmc, s, iso(), MapOptions{Target: TargetCD, Size: 3 * cdBlockSize}, standardDrives())
	bmc.expect(typeMapCD)
	<-res
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	bmc.expect(typeRelease)
	go func() { _, _ = io.Copy(io.Discard, bmc.conn) }()
	if err := <-closed; err != nil {
		t.Errorf("Close = %v", err)
	}
}

func TestOpenFailures(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	s := NewSession(client, "u", "p")
	errc := make(chan error, 1)
	go func() { errc <- s.Open(context.Background()) }()
	bmc := &fakeBMC{t: t, conn: server}
	bmc.next()
	reply := loginStatus(6)
	reply[4] = 4
	copy(reply[5:], "root")
	bmc.send(typeLoginStatus, reply)
	if err := <-errc; err == nil || !strings.Contains(err.Error(), "root") {
		t.Errorf("err = %v, want it to name the holder", err)
	}
}

func TestOversizedPacketEndsSession(t *testing.T) {
	bmc, s := open(t)
	_, _ = bmc.conn.Write(header(typeVDiskRead, 1<<30))
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("session survived a 1 GiB packet header")
	}
}

func TestReadNearTheBlockLimitIsRefused(t *testing.T) {
	bmc, s := open(t)
	res := mapAsync(t, bmc, s, iso(), MapOptions{Target: TargetCD, Size: 3 * cdBlockSize}, standardDrives())
	bmc.expect(typeMapCD)
	<-res
	bmc.send(typeVDiskRead, readRequest(0, 0xFFFFFFFF, 2, 2))
	if body := bmc.expect(typeClientStatus); binary.BigEndian.Uint32(body[2:6]) != statusReadFailed {
		t.Errorf("status % x", body)
	}
}

func TestHeartbeats(t *testing.T) {
	bmc, _ := open(t)
	_ = bmc.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	hdr := make([]byte, 12)
	if _, err := io.ReadFull(bmc.conn, hdr); err != nil || binary.BigEndian.Uint16(hdr[8:10]) != typeHeartbeat {
		t.Errorf("got % x (%v), want a heartbeat", hdr, err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
