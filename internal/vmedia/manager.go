package vmedia

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"ipmi-connect/internal/bmc"
)

const (
	sampleInterval = time.Second
	historyLen     = 300 // five minutes of one-second samples
	mapTimeout     = 3 * time.Minute
)

var targetTypes = map[bmc.MediaTarget]Target{
	bmc.TargetCD: TargetCD, bmc.TargetRemovable: TargetRemovable, bmc.TargetFloppy: TargetFloppy,
}

// mapping is an image attached (or being attached) to a drive.
type mapping struct {
	imageID  string
	mounting bool
	cancel   context.CancelFunc
	disk     uint16
	file     *os.File
}

// Manager implements bmc.VirtualMedia on top of a virtual media Session.
type Manager struct {
	open        Opener
	sampleEvery time.Duration

	mu       sync.Mutex
	images   []*image
	drives   map[bmc.MediaTarget]*mapping
	session  Session
	history  []bmc.MediaSample
	lastErr  string
	closed   bool
	sessMu   sync.Mutex // serialises opening and closing the session
	stopSamp context.CancelFunc
}

var _ bmc.VirtualMedia = (*Manager)(nil)

// NewManager returns a manager that opens sessions with open.
func NewManager(open Opener) *Manager {
	return &Manager{open: open, sampleEvery: sampleInterval, drives: map[bmc.MediaTarget]*mapping{}}
}

// SetSampleInterval overrides the stats sampling interval (used in tests).
func (m *Manager) SetSampleInterval(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sampleEvery = d
}

func (m *Manager) find(id string) *image {
	for _, im := range m.images {
		if im.id == id {
			return im
		}
	}
	return nil
}

func (m *Manager) mappedTarget(id string) bmc.MediaTarget {
	for t, mp := range m.drives {
		if mp.imageID == id {
			return t
		}
	}
	return ""
}

// AddImage adds a local image to the library.
func (m *Manager) AddImage(path string, readOnly bool) (bmc.MediaImage, error) {
	im, err := inspectImage(path, readOnly)
	if err != nil {
		return bmc.MediaImage{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, x := range m.images {
		// The same file under another name would let two drives write to it at once.
		if x.path == im.path || os.SameFile(x.info, im.info) {
			return bmc.MediaImage{}, fmt.Errorf("%s is already in the list (as %s)", im.name, x.name)
		}
	}
	m.images = append(m.images, im)
	return m.describe(im), nil
}

// RemoveImage takes an unmapped image out of the library.
func (m *Manager) RemoveImage(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.find(id) == nil {
		return errors.New("no such image")
	}
	if m.mappedTarget(id) != "" {
		return errors.New("eject the image first")
	}
	for i, im := range m.images {
		if im.id == id {
			m.images = append(m.images[:i:i], m.images[i+1:]...)
			break
		}
	}
	return nil
}

// SetImageReadOnly changes an unmapped image's read-only flag. ISOs are always read-only.
func (m *Manager) SetImageReadOnly(id string, readOnly bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	im := m.find(id)
	switch {
	case im == nil:
		return errors.New("no such image")
	case m.mappedTarget(id) != "":
		return errors.New("read-only cannot change while the image is mapped")
	case im.kind == "iso" && !readOnly:
		return errors.New("ISO images are always read-only")
	}
	im.readOnly = readOnly
	return nil
}

// MapImage attaches an image to a drive and returns once the BMC has accepted it.
func (m *Manager) MapImage(id string, target bmc.MediaTarget) error {
	ctx, cancel := context.WithTimeout(context.Background(), mapTimeout)
	defer cancel()

	m.mu.Lock()
	im := m.find(id)
	switch {
	case m.closed:
		m.mu.Unlock()
		return errors.New("the session is closing")
	case im == nil:
		m.mu.Unlock()
		return errors.New("no such image")
	case !im.canMapTo(target):
		m.mu.Unlock()
		return fmt.Errorf("%s cannot be mapped to the %s drive", im.name, target)
	case m.drives[target] != nil:
		m.mu.Unlock()
		return fmt.Errorf("the %s drive is in use; eject it first", target)
	case m.mappedTarget(id) != "":
		m.mu.Unlock()
		return fmt.Errorf("%s is already mapped", im.name)
	}
	mp := &mapping{imageID: id, mounting: true, cancel: cancel}
	m.drives[target] = mp
	snapshot := *im
	m.mu.Unlock()

	err := m.attach(ctx, mp, &snapshot, target)
	if err == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drives[target] == mp {
		delete(m.drives, target)
	}
	if errors.Is(err, context.Canceled) {
		err = errors.New("the mount was cancelled")
	}
	go m.closeIfIdle()
	return err
}

func (m *Manager) attach(ctx context.Context, mp *mapping, im *image, target bmc.MediaTarget) error {
	sess, err := m.ensureSession(ctx)
	if err != nil {
		return err
	}
	f, err := im.openFile()
	if err != nil {
		return err
	}
	disk, err := sess.Map(ctx, f, MapOptions{Target: targetTypes[target], Size: im.size, ReadOnly: im.readOnly})
	if err != nil {
		_ = f.Close()
		return err
	}
	// Finish under the lock that UnmapDrive, Close and watch take, so the mapping cannot be
	// cancelled, dropped or orphaned between this check and marking it attached.
	m.mu.Lock()
	if m.drives[target] != mp || m.session != sess || ctx.Err() != nil {
		m.mu.Unlock()
		_ = sess.Unmap(disk)
		_ = f.Close()
		return context.Canceled
	}
	mp.disk, mp.file, mp.mounting = disk, f, false
	m.lastErr = ""
	m.mu.Unlock()
	log.Printf("[VMedia] %s mapped to the %s drive", im.name, target)
	return nil
}

// ensureSession returns the open session, opening one if needed.
func (m *Manager) ensureSession(ctx context.Context) (Session, error) {
	m.sessMu.Lock()
	defer m.sessMu.Unlock()
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess != nil {
		return sess, nil
	}
	sess, err := m.open(ctx)
	if err != nil {
		return nil, err
	}
	sampCtx, stop := context.WithCancel(context.Background())
	m.mu.Lock()
	m.session, m.stopSamp = sess, stop
	m.mu.Unlock()
	go m.watch(sess)
	go m.sample(sampCtx, sess)
	return sess, nil
}

// watch clears every mapping when a session ends on its own.
func (m *Manager) watch(sess Session) {
	<-sess.Done()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session != sess {
		return
	}
	if err := sess.Err(); err != nil {
		m.lastErr = err.Error()
		log.Printf("[VMedia] %v", err)
	}
	m.releaseAllLocked()
}

// releaseAllLocked forgets the session and every attached drive.
func (m *Manager) releaseAllLocked() {
	for t, mp := range m.drives {
		if mp.mounting {
			mp.cancel()
			continue // MapImage cleans up
		}
		if mp.file != nil {
			_ = mp.file.Close()
		}
		delete(m.drives, t)
	}
	if m.stopSamp != nil {
		m.stopSamp()
	}
	m.session, m.stopSamp = nil, nil
}

// UnmapDrive ejects a drive, or cancels an attach in progress.
func (m *Manager) UnmapDrive(target bmc.MediaTarget) error {
	m.mu.Lock()
	mp := m.drives[target]
	if mp == nil {
		m.mu.Unlock()
		return fmt.Errorf("nothing is mapped to the %s drive", target)
	}
	if mp.mounting {
		delete(m.drives, target)
		m.mu.Unlock()
		mp.cancel()
		return nil
	}
	delete(m.drives, target)
	sess := m.session
	m.mu.Unlock()

	var err error
	if sess != nil {
		err = sess.Unmap(mp.disk)
	}
	if mp.file != nil {
		_ = mp.file.Close()
	}
	m.closeIfIdle()
	return err
}

// closeIfIdle ends the session once no drive is attached or attaching, freeing the BMC.
func (m *Manager) closeIfIdle() {
	m.sessMu.Lock()
	defer m.sessMu.Unlock()
	m.mu.Lock()
	sess := m.session
	if sess == nil || len(m.drives) > 0 {
		m.mu.Unlock()
		return
	}
	m.releaseAllLocked()
	m.mu.Unlock()
	_ = sess.Close()
}

// USBReset reconnects the BMC's virtual mass-storage device on the host.
func (m *Manager) USBReset() error {
	m.mu.Lock()
	sess := m.session
	m.mu.Unlock()
	if sess == nil {
		return errors.New("no virtual media session is open; map an image first")
	}
	log.Printf("[VMedia] USB reset requested")
	return sess.USBReset()
}

// Close ejects everything and refuses new mounts. Call it before the KVM session ends.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for t, mp := range m.drives {
		if mp.mounting {
			mp.cancel()
			delete(m.drives, t)
		}
	}
	m.mu.Unlock()
	m.sessMu.Lock()
	m.mu.Lock()
	sess := m.session
	m.releaseAllLocked()
	m.mu.Unlock()
	m.sessMu.Unlock()
	if sess != nil {
		_ = sess.Close()
	}
}

// MediaStatus implements bmc.VirtualMedia.
func (m *Manager) MediaStatus() bmc.MediaStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := bmc.MediaStatus{Images: []bmc.MediaImage{}, Drives: []bmc.MediaDrive{}, Error: m.lastErr}
	for _, im := range m.images {
		st.Images = append(st.Images, m.describe(im))
	}
	counters := map[uint16]DriveStatus{}
	if m.session != nil {
		for _, d := range m.session.Drives() {
			counters[d.Disk] = d
		}
	}
	for _, t := range []bmc.MediaTarget{bmc.TargetCD, bmc.TargetRemovable, bmc.TargetFloppy} {
		mp := m.drives[t]
		if mp == nil {
			continue
		}
		d := bmc.MediaDrive{Target: t, ImageID: mp.imageID, Mounting: mp.mounting}
		if im := m.find(mp.imageID); im != nil {
			d.Image, d.ReadOnly = im.name, im.readOnly
		}
		if c, ok := counters[mp.disk]; ok && !mp.mounting {
			d.BytesRead, d.BytesWritten, d.ReadOnly = c.BytesRead, c.BytesWritten, c.ReadOnly
		}
		st.Drives = append(st.Drives, d)
	}
	st.History = append([]bmc.MediaSample(nil), m.history...)
	return st
}

func (m *Manager) describe(im *image) bmc.MediaImage {
	return bmc.MediaImage{
		ID: im.id, Name: im.name, Path: im.path, Size: im.size, Kind: im.kind, ReadOnly: im.readOnly,
		MappedTo: m.mappedTarget(im.id), Targets: im.targets(),
	}
}
