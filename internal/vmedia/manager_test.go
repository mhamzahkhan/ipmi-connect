package vmedia_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/avmp"
	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

// fakeBMC answers an AVMP connection automatically: login OK, the C6220's three free drives,
// and nothing for maps. It records the packet types the client sends.
type fakeBMC struct {
	mu    sync.Mutex
	conn  net.Conn
	seen  []uint16
	maps  map[uint16]bool // drives the client mapped
	opens int
}

func (b *fakeBMC) serve(conn net.Conn) {
	for {
		hdr := make([]byte, 10)
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(hdr[4:8])-10)
		if _, err := io.ReadFull(conn, body); err != nil {
			return
		}
		typ := binary.BigEndian.Uint16(hdr[8:10])
		b.mu.Lock()
		b.seen = append(b.seen, typ)
		switch typ {
		case 0x0210, 0x0211:
			b.maps[binary.BigEndian.Uint16(body[0:2])] = true
		case 0x0220:
			delete(b.maps, binary.BigEndian.Uint16(body[0:2]))
		}
		b.mu.Unlock()
		switch typ {
		case 0x0100:
			b.reply(conn, 0x8100, make([]byte, 101))
		case 0x0200:
			b.reply(conn, 0x8200, []byte{0xff, 0xff, 0, 3, 2, 0, 0, 1, 1, 0, 0, 1, 4, 0, 0, 1})
		}
	}
}

func (b *fakeBMC) reply(conn net.Conn, typ uint16, body []byte) {
	p := []byte("AVMP")
	p = binary.BigEndian.AppendUint32(p, uint32(10+len(body)))
	p = binary.BigEndian.AppendUint16(p, typ)
	_, _ = conn.Write(append(p, body...))
}

// opener connects sessions to the fake BMC.
func (b *fakeBMC) opener(ctx context.Context) (vmedia.Session, error) {
	server, client := net.Pipe()
	b.mu.Lock()
	b.conn = server
	b.opens++
	b.mu.Unlock()
	go b.serve(server)
	s := avmp.NewSession(client, "u", "p")
	if err := s.Open(ctx); err != nil {
		return nil, err
	}
	return s, nil
}

// send pushes a raw BMC message on the current connection.
func (b *fakeBMC) send(typ uint16, body []byte) {
	b.mu.Lock()
	conn := b.conn
	b.mu.Unlock()
	b.reply(conn, typ, body)
}

func (b *fakeBMC) mapped() map[uint16]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[uint16]bool{}
	for k, v := range b.maps {
		out[k] = v
	}
	return out
}

func newManager(t *testing.T) (*vmedia.Manager, *fakeBMC) {
	t.Helper()
	b := &fakeBMC{maps: map[uint16]bool{}}
	m := vmedia.NewManager(b.opener)
	t.Cleanup(m.Close)
	return m, b
}

func writeFile(t *testing.T, name string, size int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.Mkdir(p, 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAddImageValidation(t *testing.T) {
	m, _ := newManager(t)
	dir := t.TempDir()
	tests := []struct {
		name, path, want string
	}{
		{"relative path", "disk.img", "full path"},
		{"missing file", filepath.Join(dir, "nope.iso"), "open"},
		{"directory", mkdir(t, filepath.Join(dir, "folder.iso")), "regular file"},
		{"wrong extension", writeFile(t, "notes.txt", 4096), ".iso or .img"},
		{"empty", writeFile(t, "empty.iso", 0), "regular file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := m.AddImage(tt.path, false); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSameFileCannotBeAddedTwice(t *testing.T) {
	m, _ := newManager(t)
	path := writeFile(t, "a.img", 4096)
	if _, err := m.AddImage(path, false); err != nil {
		t.Fatal(err)
	}
	dir, base := filepath.Split(path)
	link := filepath.Join(t.TempDir(), "link.img")
	_ = os.Symlink(path, link)
	hard := filepath.Join(dir, "hard.img")
	_ = os.Link(path, hard)
	for _, p := range []string{filepath.Join(dir, ".", base), link, hard} {
		if _, err := m.AddImage(p, false); err == nil {
			t.Errorf("%s was added although it is the same file", p)
		}
	}
}

func TestImageTargets(t *testing.T) {
	m, _ := newManager(t)
	iso, _ := m.AddImage(writeFile(t, "a.iso", 4096), false)
	small, _ := m.AddImage(writeFile(t, "f.img", 1474560), false)
	big, _ := m.AddImage(writeFile(t, "d.img", 4<<20), false)
	if !iso.ReadOnly || len(iso.Targets) != 1 || iso.Targets[0] != bmc.TargetCD {
		t.Errorf("iso %+v", iso)
	}
	if len(small.Targets) != 2 {
		t.Errorf("1.44 MB image targets %v, want removable and floppy", small.Targets)
	}
	if len(big.Targets) != 1 || big.Targets[0] != bmc.TargetRemovable {
		t.Errorf("4 MB image targets %v, want removable only", big.Targets)
	}
	if _, err := m.AddImage(iso.Path, false); err == nil {
		t.Error("the same file was added twice")
	}
}

func TestMapUnmapLifecycle(t *testing.T) {
	m, b := newManager(t)
	iso, _ := m.AddImage(writeFile(t, "a.iso", 4096), false)
	img, _ := m.AddImage(writeFile(t, "f.img", 1474560), false)

	if err := m.MapImage(iso.ID, bmc.TargetCD); err != nil {
		t.Fatalf("map CD: %v", err)
	}
	if err := m.MapImage(img.ID, bmc.TargetFloppy); err != nil {
		t.Fatalf("map floppy: %v", err)
	}
	// The fake BMC records a map after reading it off the pipe, so it can trail MapImage.
	waitFor(t, "CD and floppy map", func() bool { got := b.mapped(); return got[0] && got[2] })
	st := m.MediaStatus()
	if len(st.Drives) != 2 || st.Images[0].MappedTo != bmc.TargetCD || st.Images[1].MappedTo != bmc.TargetFloppy {
		t.Errorf("status %+v", st)
	}

	// Rules while mapped.
	if err := m.MapImage(iso.ID, bmc.TargetCD); err == nil {
		t.Error("mapped the same image twice")
	}
	if err := m.RemoveImage(iso.ID); err == nil {
		t.Error("removed a mapped image")
	}
	if err := m.SetImageReadOnly(img.ID, true); err == nil {
		t.Error("changed read-only while mapped")
	}
	if err := m.MapImage(img.ID, bmc.TargetCD); err == nil {
		t.Error("mapped an IMG to the CD drive")
	}

	if err := m.UnmapDrive(bmc.TargetCD); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "CD release", func() bool { return !b.mapped()[0] })
	if err := m.UnmapDrive(bmc.TargetFloppy); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "floppy release", func() bool { return len(b.mapped()) == 0 })
	if len(m.MediaStatus().Drives) != 0 {
		t.Error("drives listed after unmapping everything")
	}
	if err := m.RemoveImage(iso.ID); err != nil {
		t.Errorf("remove after unmap: %v", err)
	}

	// The session closes with the last drive and reopens for the next map.
	if err := m.MapImage(img.ID, bmc.TargetRemovable); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	opens := b.opens
	b.mu.Unlock()
	if opens != 2 {
		t.Errorf("sessions opened: %d, want 2 (closed after the last drive)", opens)
	}
}

func TestWritesReachTheImageFile(t *testing.T) {
	m, b := newManager(t)
	path := writeFile(t, "f.img", 1474560)
	img, _ := m.AddImage(path, false)
	if err := m.MapImage(img.ID, bmc.TargetFloppy); err != nil {
		t.Fatal(err)
	}
	body := []byte{0, 2, 0, 0, 0, 1, 0, 0, 0, 1}
	body = append(body, []byte(strings.Repeat("Z", 512))...)
	b.send(0x8310, body)
	waitFor(t, "write stats", func() bool {
		st := m.MediaStatus()
		return len(st.Drives) == 1 && st.Drives[0].BytesWritten == 512
	})
	data, _ := os.ReadFile(path)
	if string(data[512:516]) != "ZZZZ" {
		t.Error("block 1 of the image file was not written")
	}
}

func TestReadOnlyImageIsOpenedReadOnly(t *testing.T) {
	m, b := newManager(t)
	path := writeFile(t, "f.img", 1474560)
	img, _ := m.AddImage(path, true)
	if err := m.MapImage(img.ID, bmc.TargetFloppy); err != nil {
		t.Fatal(err)
	}
	body := append([]byte{0, 2, 0, 0, 0, 0, 0, 0, 0, 1}, make([]byte, 512)...)
	body[10] = 'X'
	b.send(0x8310, body)
	time.Sleep(50 * time.Millisecond)
	data, _ := os.ReadFile(path)
	if data[0] == 'X' {
		t.Error("a read-only image was modified")
	}
}

func TestHistorySamples(t *testing.T) {
	m, b := newManager(t)
	m.SetSampleInterval(20 * time.Millisecond)
	img, _ := m.AddImage(writeFile(t, "a.iso", 8192), false)
	if err := m.MapImage(img.ID, bmc.TargetCD); err != nil {
		t.Fatal(err)
	}
	b.send(0x8300, []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 2, 0, 0, 0, 2})
	waitFor(t, "a sample with reads", func() bool {
		for _, s := range m.MediaStatus().History {
			if s.Read == 4096 {
				return true
			}
		}
		return false
	})
}

func TestBMCReleaseIsReflected(t *testing.T) {
	m, b := newManager(t)
	m.SetSampleInterval(20 * time.Millisecond)
	img, _ := m.AddImage(writeFile(t, "a.iso", 4096), false)
	if err := m.MapImage(img.ID, bmc.TargetCD); err != nil {
		t.Fatal(err)
	}
	b.send(0x8410, []byte{0, 0, 0, 0, 0, 4}) // USB not connected
	waitFor(t, "drive dropped", func() bool { return len(m.MediaStatus().Drives) == 0 })
	if st := m.MediaStatus(); !strings.Contains(st.Error, "USB not connected") || st.Images[0].MappedTo != "" {
		t.Errorf("status %+v", st)
	}
}

func TestCancelAMapInProgress(t *testing.T) {
	block := make(chan struct{})
	m := vmedia.NewManager(func(ctx context.Context) (vmedia.Session, error) {
		select {
		case <-block:
		case <-ctx.Done():
		}
		return nil, errors.New("cancelled")
	})
	defer m.Close()
	img, _ := m.AddImage(writeFile(t, "a.iso", 4096), false)
	res := make(chan error, 1)
	go func() { res <- m.MapImage(img.ID, bmc.TargetCD) }()
	waitFor(t, "mounting state", func() bool {
		d := m.MediaStatus().Drives
		return len(d) == 1 && d[0].Mounting
	})
	if err := m.UnmapDrive(bmc.TargetCD); err != nil {
		t.Errorf("UnmapDrive while mounting: %v", err)
	}
	select {
	case err := <-res:
		if err == nil {
			t.Error("MapImage succeeded after being cancelled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("MapImage did not return")
	}
	if len(m.MediaStatus().Drives) != 0 {
		t.Error("a cancelled drive is still listed")
	}
}

func TestCloseReleasesEverything(t *testing.T) {
	m, b := newManager(t)
	img, _ := m.AddImage(writeFile(t, "a.iso", 4096), false)
	if err := m.MapImage(img.ID, bmc.TargetCD); err != nil {
		t.Fatal(err)
	}
	m.Close()
	waitFor(t, "release", func() bool { return len(b.mapped()) == 0 })
	if err := m.MapImage(img.ID, bmc.TargetCD); err == nil {
		t.Error("mapped after Close")
	}
}
