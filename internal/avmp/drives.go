package avmp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/mhamzahkhan/ipmi-connect/internal/vmedia"
)

// Target is a BMC virtual drive type, as VDISK_INFO reports it.
type Target = vmedia.Target

// Drive types.
const (
	TargetRemovable = vmedia.TargetRemovable
	TargetCD        = vmedia.TargetCD
	TargetFloppy    = vmedia.TargetFloppy
)

const (
	// maxChunkBytes caps data per packet at the vendor's 32 KiB read size.
	maxChunkBytes = 32 << 10
	// maxReadBlocks bounds one read request; the host asks for far less.
	maxReadBlocks = 1 << 17
	driveIdle     = 0
)

// Image is a disk image. Writes are only made to drives mapped writable.
type Image = vmedia.Image

// MapOptions describes an image to attach.
type MapOptions = vmedia.MapOptions

// DriveStatus describes a mapped drive.
type DriveStatus = vmedia.DriveStatus

// Map attaches image as a drive of the requested type and returns its drive ID. While the BMC
// still holds its drives from an earlier session, Map waits for one to free up.
func (s *Session) Map(ctx context.Context, image Image, o MapOptions) (uint16, error) {
	d, err := vmedia.NewDrive(image, o)
	if err != nil {
		return 0, err
	}
	s.mapMu.Lock()
	defer s.mapMu.Unlock()

	disk, err := s.waitForDrive(ctx, o.Target)
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	d.ReadOnly = d.ReadOnly || s.readOnly
	s.drives[disk] = d // before the request: the host may read as soon as the BMC attaches it
	s.mu.Unlock()

	if err := s.write(mapPacket(d, disk)); err != nil {
		s.mu.Lock()
		delete(s.drives, disk)
		s.mu.Unlock()
		return 0, err
	}
	log.Printf("[VMedia] Attached %s as drive %d (%d blocks of %d bytes, read-only %v)",
		o.Target, disk, d.Blocks, d.BlockSize, d.ReadOnly)
	return disk, nil
}

func mapPacket(d *vmedia.Drive, disk uint16) []byte {
	if d.Target == TargetCD {
		return mapCD(disk, d.Blocks)
	}
	c, h, sec := geometry(d.Blocks)
	return mapDisk(disk, d.BlockSize, d.Blocks, d.ReadOnly, c, h, sec)
}

// waitForDrive asks for the drive list until a drive of the wanted type is free.
func (s *Session) waitForDrive(ctx context.Context, want Target) (uint16, error) {
	deadline := time.Now().Add(s.driveWait)
	last := ""
	for {
		select { // a reply already queued may be stale
		case <-s.info:
		default:
		}
		if err := s.write(getVDiskInfo()); err != nil {
			return 0, err
		}
		var body []byte
		select {
		case body = <-s.info:
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-s.done:
			return 0, fmt.Errorf("virtual media session ended: %w", s.Err())
		case <-time.After(setupTimeout):
			return 0, errors.New("BMC did not report its drives")
		}
		disk, err := s.pickDrive(body, want)
		if err == nil {
			return disk, nil
		}
		if msg := err.Error(); msg != last {
			last = msg
			log.Printf("[VMedia] Waiting for a free %s drive: %v", want, err)
		}
		if time.Now().After(deadline) {
			return 0, err
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-s.done:
			return 0, fmt.Errorf("virtual media session ended: %w", s.Err())
		case <-time.After(s.driveRetry):
		}
	}
}

// pickDrive returns the first idle drive of the wanted type that we have not mapped.
func (s *Session) pickDrive(body []byte, want Target) (uint16, error) {
	if len(body) < 4 {
		return 0, errors.New("short drive info")
	}
	n := int(binary.BigEndian.Uint16(body[2:4]))
	s.mu.Lock()
	defer s.mu.Unlock()
	var seen []string
	for i := 0; i < n && 4+4*i+2 <= len(body); i++ {
		typ, status := Target(body[4+4*i]), body[5+4*i]
		if _, ours := s.drives[uint16(i)]; typ == want && status == driveIdle && !ours {
			return uint16(i), nil
		}
		seen = append(seen, fmt.Sprintf("%s status %d", typ, status))
	}
	return 0, fmt.Errorf("no free %s drive on the BMC (drives: %s); is another virtual media session open?",
		want, strings.Join(seen, ", "))
}

// Unmap ejects a drive.
func (s *Session) Unmap(disk uint16) error {
	s.mu.Lock()
	_, ok := s.drives[disk]
	delete(s.drives, disk)
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("drive %d is not mapped", disk)
	}
	log.Printf("[VMedia] Ejected drive %d", disk)
	return s.writeWithin(releaseDisk(disk), ejectTimeout)
}

// dropDrive handles the BMC releasing a drive or reporting it failed.
func (s *Session) dropDrive(disk uint16, why string) {
	s.mu.Lock()
	d, ok := s.drives[disk]
	delete(s.drives, disk)
	if ok {
		s.lastEvent = fmt.Sprintf("%s drive: %s", d.Target, why)
	}
	s.mu.Unlock()
	if !ok {
		return // the vendor only unmaps drives it has mapped
	}
	log.Printf("[VMedia] Drive %d: %s", disk, why)
	_ = s.writeWithin(releaseDisk(disk), ejectTimeout)
}

// Drives lists the mapped drives, ordered by ID.
func (s *Session) Drives() []DriveStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DriveStatus, 0, len(s.drives))
	for id, d := range s.drives {
		out = append(out, d.Status(id))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Disk < out[j].Disk })
	return out
}

// LastEvent describes the latest drive the BMC released or failed, if any.
func (s *Session) LastEvent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastEvent
}

func (s *Session) forcedReadOnly() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readOnly
}

func (s *Session) mappedDrives() []uint16 {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]uint16, 0, len(s.drives))
	for id := range s.drives {
		ids = append(ids, id)
	}
	return ids
}

func (s *Session) lookup(disk uint16) *vmedia.Drive {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.drives[disk]
}

// serveRead sends the requested blocks in packets of at most the BMC's blocking factor and
// 32 KiB, then exactly one CLIENT_STATUS. Blocks past the end of the image read as zeros.
func (s *Session) serveRead(body []byte) error {
	if len(body) < 14 {
		return s.answerMalformed(body, statusReadFailed)
	}
	disk := binary.BigEndian.Uint16(body[0:2])
	start := binary.BigEndian.Uint32(body[2:6])
	n := binary.BigEndian.Uint32(body[6:10])
	factor := binary.BigEndian.Uint32(body[10:14])
	d := s.lookup(disk)
	if d == nil || n > maxReadBlocks || uint64(start)+uint64(n) > math.MaxUint32 {
		log.Printf("[VMedia] Refusing a %d-block read at block %d of drive %d", n, start, disk)
		return s.write(clientStatus(disk, statusReadFailed))
	}
	perPacket := maxChunkBytes / d.BlockSize
	if factor > 0 {
		perPacket = min(perPacket, factor)
	}

	status := statusOK
	buf := make([]byte, int(perPacket*d.BlockSize))
	for done := uint32(0); done < n; {
		count := min(perPacket, n-done)
		chunk := buf[:int(count*d.BlockSize)]
		if _, err := d.ReadAt(chunk, (int64(start)+int64(done))*int64(d.BlockSize)); err != nil {
			log.Printf("[VMedia] Image read failed: %v", err)
			status = statusReadFailed
			break
		}
		if err := s.write(readData(disk, start+done, count, chunk)); err != nil {
			return err
		}
		done += count
	}
	return s.write(clientStatus(disk, status))
}

// serveWrite applies one VDISK_WRITE and answers with exactly one CLIENT_STATUS.
func (s *Session) serveWrite(body []byte) error {
	if len(body) < 10 {
		return s.answerMalformed(body, statusWriteFailed)
	}
	disk := binary.BigEndian.Uint16(body[0:2])
	start := binary.BigEndian.Uint32(body[2:6])
	n := binary.BigEndian.Uint32(body[6:10])
	data := body[10:]
	d := s.lookup(disk)
	switch {
	case d == nil:
		return s.write(clientStatus(disk, statusWriteFailed))
	case d.ReadOnly:
		log.Printf("[VMedia] Refused a write to read-only drive %d", disk)
		return s.write(clientStatus(disk, statusWriteFailed))
	case !d.Contains(start, n) || uint64(len(data)) < uint64(n)*uint64(d.BlockSize):
		log.Printf("[VMedia] Refused an out-of-range write (%d blocks at %d) to drive %d", n, start, disk)
		return s.write(clientStatus(disk, statusWriteFailed))
	}
	length := int(n * d.BlockSize)
	if _, err := d.WriteAt(data[:length], int64(start)*int64(d.BlockSize)); err != nil {
		log.Printf("[VMedia] Image write failed: %v", err)
		return s.write(clientStatus(disk, statusWriteFailed))
	}
	return s.write(clientStatus(disk, statusOK))
}

// answerMalformed fails a request too short to serve. The BMC waits for a status per request,
// so answer whenever the drive ID is readable.
func (s *Session) answerMalformed(body []byte, status uint32) error {
	if len(body) < 2 {
		return nil
	}
	return s.write(clientStatus(binary.BigEndian.Uint16(body[0:2]), status))
}

func deviceFatal(code uint32) bool {
	switch code {
	case 2, 4, 5, 6, 8, 0xFFFFFFFF:
		return true
	}
	return false
}

func deviceError(code uint32) string {
	msgs := map[uint32]string{
		1: "command unsupported", 2: "read failed", 3: "write failed", 4: "USB not connected",
		5: "drive already attached", 6: "drive disabled", 7: "unknown option", 8: "VDISK request failed",
	}
	if m, ok := msgs[code]; ok {
		return m
	}
	return fmt.Sprintf("status %d", int32(code))
}
