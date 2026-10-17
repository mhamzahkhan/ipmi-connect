package avmp

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
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
	for {
		_ = b.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
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

func loginStatus(status byte) []byte {
	b := make([]byte, 101)
	b[2] = status
	return b
}

func vdiskInfo(drives ...[2]byte) []byte {
	b := []byte{0xff, 0xff}
	b = binary.BigEndian.AppendUint16(b, uint16(len(drives)))
	for _, d := range drives {
		b = append(b, d[0], d[1], 0, 0)
	}
	return b
}

func readRequest(disk uint16, start, n, factor uint32) []byte {
	b := binary.BigEndian.AppendUint16(nil, disk)
	b = binary.BigEndian.AppendUint32(b, start)
	b = binary.BigEndian.AppendUint32(b, n)
	return binary.BigEndian.AppendUint32(b, factor)
}

// image is a 3-block ISO whose blocks are filled with 1, 2 and 3.
func image() *bytes.Reader {
	return bytes.NewReader(bytes.Join([][]byte{
		bytes.Repeat([]byte{1}, BlockSize), bytes.Repeat([]byte{2}, BlockSize), bytes.Repeat([]byte{3}, BlockSize),
	}, nil))
}

func start(t *testing.T) (*fakeBMC, *Session, chan error, context.CancelFunc) {
	t.Helper()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	bmc := &fakeBMC{t: t, conn: server}
	s := NewSession(client, "user", "pass")
	s.heartbeatEvery = 20 * time.Millisecond
	s.driveWait, s.driveRetry = 200*time.Millisecond, 10*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { done <- s.Mount(ctx, image(), int64(image().Len())) }()
	return bmc, s, done, cancel
}

// mapped runs the fake BMC through login, drive info and the map request.
func mapped(t *testing.T, bmc *fakeBMC) {
	t.Helper()
	if typ, body := bmc.next(); typ != typeLogin || string(body[3:7]) != "user" {
		t.Fatalf("first packet %#x % x, want login", typ, body[:8])
	}
	bmc.send(typeLoginStatus, loginStatus(0))
	if typ, _ := bmc.next(); typ != typeGetVDiskInfo {
		t.Fatalf("got %#x, want GET_VDISK_INFO", typ)
	}
	// Drive 0 is a floppy, drive 1 a CD already in use, drive 2 an idle CD.
	bmc.send(typeVDiskInfo, vdiskInfo([2]byte{4, 0}, [2]byte{2, 1}, [2]byte{2, 0}))
	typ, body := bmc.next()
	if typ != typeMapCD {
		t.Fatalf("got %#x, want VDISK_REQUEST", typ)
	}
	if disk := binary.BigEndian.Uint16(body[0:2]); disk != 2 {
		t.Errorf("mapped drive %d, want the first idle CD (2)", disk)
	}
	if n := binary.BigEndian.Uint32(body[6:10]); n != 3 {
		t.Errorf("block count %d, want 3", n)
	}
}

func TestMountServesReadsInBlockingFactorChunks(t *testing.T) {
	bmc, s, done, _ := start(t)
	mapped(t, bmc)

	bmc.send(typeVDiskRead, readRequest(2, 0, 3, 2))
	var got []byte
	for _, wantStart := range []uint32{0, 2} {
		typ, body := bmc.next()
		if typ != typeReadData {
			t.Fatalf("got %#x, want READ_DATA", typ)
		}
		if st := binary.BigEndian.Uint32(body[2:6]); st != wantStart {
			t.Errorf("chunk start %d, want %d", st, wantStart)
		}
		got = append(got, body[10:]...)
	}
	want, _ := io.ReadAll(image())
	if !bytes.Equal(got, want) {
		t.Errorf("served %d bytes not matching the image", len(got))
	}
	if typ, body := bmc.next(); typ != typeClientStatus || binary.BigEndian.Uint32(body[2:6]) != statusOK {
		t.Errorf("got %#x % x, want CLIENT_STATUS OK", typ, body)
	}
	if st := s.Status(); !st.Mounted || st.BytesServed != int64(3*BlockSize) {
		t.Errorf("status %+v", st)
	}

	// Reads past the end of the image return zeros, as for a disc padded to the TOC lead-out.
	bmc.send(typeVDiskRead, readRequest(2, 5, 1, 4))
	if typ, body := bmc.next(); typ != typeReadData || !bytes.Equal(body[10:], make([]byte, BlockSize)) {
		t.Errorf("past-EOF read: %#x", typ)
	}
	if typ, body := bmc.next(); typ != typeClientStatus || binary.BigEndian.Uint32(body[2:6]) != statusOK {
		t.Errorf("past-EOF status %#x % x", typ, body)
	}

	bmc.send(typeRequestRelease, []byte{0, 2})
	if typ, body := bmc.next(); typ != typeRelease || !bytes.Equal(body, []byte{0, 2}) {
		t.Errorf("got %#x % x, want RELEASE of drive 2", typ, body)
	}
	if err := <-done; err != nil {
		t.Errorf("Mount returned %v after a BMC release, want nil", err)
	}
}

func TestMountEjectsOnCancel(t *testing.T) {
	bmc, _, done, cancel := start(t)
	mapped(t, bmc)
	cancel()
	if typ, _ := bmc.next(); typ != typeRelease {
		t.Errorf("got %#x, want RELEASE on eject", typ)
	}
	if err := <-done; err != nil {
		t.Errorf("Mount = %v after eject, want nil", err)
	}
}

func TestMountSendsHeartbeats(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	s := NewSession(client, "user", "pass")
	s.heartbeatEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Mount(ctx, image(), 3*BlockSize) }()
	bmc := &fakeBMC{t: t, conn: server}
	mapped(t, bmc)
	_ = server.SetReadDeadline(time.Now().Add(2 * time.Second))
	hdr := make([]byte, 12)
	if _, err := io.ReadFull(server, hdr); err != nil || binary.BigEndian.Uint16(hdr[8:10]) != typeHeartbeat {
		t.Errorf("got % x (%v), want a heartbeat", hdr, err)
	}
}

func TestMountFailures(t *testing.T) {
	tests := []struct {
		name    string
		script  func(b *fakeBMC)
		wantErr string
	}{
		{"login refused, in use", func(b *fakeBMC) {
			b.next()
			b.send(typeLoginStatus, loginStatus(4))
		}, "in use"},
		{"no idle CD drive", func(b *fakeBMC) {
			b.next()
			b.send(typeLoginStatus, loginStatus(0))
			hdr := make([]byte, 12)
			for { // keep answering each retry until the client gives up and closes
				if _, err := io.ReadFull(b.conn, hdr); err != nil {
					return
				}
				b.send(typeVDiskInfo, vdiskInfo([2]byte{2, 1}, [2]byte{4, 0}))
			}
		}, "no free virtual CD"},
		{"BMC reports the map failed", func(b *fakeBMC) {
			mapped(b.t, b)
			b.send(typeDeviceStatus, []byte{0, 2, 0, 0, 0, 4})
		}, "USB not connected"},
		{"BMC disconnects", func(b *fakeBMC) {
			mapped(b.t, b)
			b.send(typeDisconnect, []byte{0, 0, 2, 0, 0, 0})
		}, "BMC rebooting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bmc, _, done, _ := start(t)
			go func() {
				tt.script(bmc)
				_, _ = io.Copy(io.Discard, bmc.conn) // keep reading, as a TCP peer's buffer would
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("err = %v, want %q", err, tt.wantErr)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Mount did not return")
			}
		})
	}
}

func TestMountWaitsForTheBMCToFreeACD(t *testing.T) {
	bmc, _, done, _ := start(t)
	bmc.next()
	bmc.send(typeLoginStatus, loginStatus(0))
	for i := 0; i < 2; i++ { // just after another session: unassigned, then disabled
		if typ, _ := bmc.next(); typ != typeGetVDiskInfo {
			t.Fatalf("got %#x, want GET_VDISK_INFO", typ)
		}
		bmc.send(typeVDiskInfo, vdiskInfo([2]byte{0, byte(2 * i)}, [2]byte{0, byte(2 * i)}))
	}
	bmc.next()
	bmc.send(typeVDiskInfo, vdiskInfo([2]byte{2, 0}, [2]byte{1, 0}))
	if typ, body := bmc.next(); typ != typeMapCD || binary.BigEndian.Uint16(body[0:2]) != 0 {
		t.Fatalf("got %#x, want the CD mapped once free", typ)
	}
	bmc.send(typeRequestRelease, []byte{0, 0})
	bmc.next()
	if err := <-done; err != nil {
		t.Errorf("Mount = %v", err)
	}
}

func TestMountIgnoresMessagesForOtherDrives(t *testing.T) {
	bmc, s, done, _ := start(t)
	mapped(t, bmc)
	bmc.send(typeDeviceStatus, []byte{0, 1, 0, 0, 0, 6}) // floppy "disabled"
	bmc.send(typeRequestRelease, []byte{0, 1})           // release the floppy
	bmc.send(typeVDiskRead, readRequest(2, 0, 1, 1))
	if typ, _ := bmc.next(); typ != typeReadData {
		t.Fatalf("got %#x: our CD was dropped because of another drive's message", typ)
	}
	bmc.next() // status
	if !s.Status().Mounted {
		t.Error("not mounted any more")
	}
	bmc.send(typeRequestRelease, []byte{0, 2})
	bmc.next()
	if err := <-done; err != nil {
		t.Errorf("Mount = %v", err)
	}
}

func TestReadNearTheBlockLimitDoesNotWrap(t *testing.T) {
	bmc, _, _, _ := start(t)
	mapped(t, bmc)
	bmc.send(typeVDiskRead, readRequest(2, 0xFFFFFFFF, 2, 2))
	if typ, body := bmc.next(); typ != typeClientStatus || binary.BigEndian.Uint32(body[2:6]) != statusReadFailed {
		t.Errorf("got %#x, want a read-failed status for a range past 2^32 blocks", typ)
	}
}

func TestPreemptionDuringLoginReleasesNothing(t *testing.T) {
	bmc, _, done, _ := start(t)
	bmc.next()
	bmc.send(typeDisconnect, []byte{0, 0, 4, 0, 30, 1, 'x'})
	if typ, _ := bmc.next(); typ != typePreemptResponse {
		t.Fatalf("got %#x, want PREEMPT_RESPONSE", typ)
	}
	// Nothing was mapped, so the next thing on the wire must be the connection closing.
	_ = bmc.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if n, err := io.ReadFull(bmc.conn, make([]byte, 10)); err == nil {
		t.Errorf("client sent another %d-byte packet (a release for a disk it never mapped?)", n)
	}
	if err := <-done; err == nil {
		t.Error("want an error")
	}
}

func TestMountAcceptsPreemption(t *testing.T) {
	bmc, _, done, _ := start(t)
	mapped(t, bmc)
	bmc.send(typeDisconnect, []byte{0, 0, 4, 0, 30, 5, 'a', 'd', 'm', 'i', 'n'})
	if typ, body := bmc.next(); typ != typePreemptResponse || body[2] != 0 {
		t.Errorf("got %#x % x, want an accepting PREEMPT_RESPONSE", typ, body)
	}
	if typ, _ := bmc.next(); typ != typeRelease {
		t.Errorf("got %#x, want RELEASE after yielding", typ)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "admin") {
		t.Errorf("err = %v, want it to name the user who took over", err)
	}
}

func TestMountRejectsOversizedPacket(t *testing.T) {
	bmc, _, done, _ := start(t)
	mapped(t, bmc)
	_, _ = bmc.conn.Write(header(typeVDiskRead, 1<<30))
	if err := <-done; err == nil || errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want a protocol error", err)
	}
}
