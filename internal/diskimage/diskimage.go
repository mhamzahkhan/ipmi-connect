// Package diskimage creates FAT disk images for virtual media: blank ones a host can copy files
// onto (for example from a recovery environment), or ones holding a copy of a local folder.
// Floppy-sized images are bare FAT12 volumes; anything larger is a FAT16 or FAT32 partition
// behind an MBR, like a USB stick.
package diskimage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"
	"github.com/diskfs/go-diskfs/partition/mbr"
)

const (
	// FloppySize is a 1.44 MB floppy.
	FloppySize = 1_474_560
	// MinSize is the smallest image offered (a 360 KB floppy).
	MinSize = 368_640
	// MaxSize bounds what we create (and what the BMC's 32-bit block counts allow at 512 bytes).
	MaxSize = 1 << 40

	floppyMax      = 2_949_120 // a 2.88 MB floppy: the largest bare FAT12 volume we make
	sectorSize     = 512
	partitionStart = 2048      // 1 MiB alignment, as modern partitioners do
	fat16Max       = 512 << 20 // FAT32 above this
	folderMinSize  = 8 << 20   // smallest partitioned image for a folder copy
	mib            = 1 << 20
	labelMax       = 11
)

// CreateBlank makes an empty FAT-formatted image of at least size bytes. It never overwrites
// an existing file.
func CreateBlank(path string, size int64, label string) error {
	if err := checkTarget(path); err != nil {
		return err
	}
	if size < MinSize || size > MaxSize {
		return fmt.Errorf("the size must be between %d bytes and %d GiB", MinSize, MaxSize>>30)
	}
	return create(path, size, label, nil)
}

// CreateFromFolder makes a FAT image holding a copy of dir, sized to fit with room to spare.
// Symbolic links and special files are skipped.
func CreateFromFolder(path, dir, label string) error {
	if err := checkTarget(path); err != nil {
		return err
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a folder", dir)
	}
	if inside(path, dir) {
		return errors.New("the image cannot be created inside the folder it copies")
	}
	need, err := spaceNeeded(dir)
	if err != nil {
		return err
	}
	size := int64(FloppySize)
	if need > FloppySize*9/10 {
		size = max(need+need/10+4*mib, folderMinSize)
	}
	if size > MaxSize {
		return errors.New("the folder is too large for an image")
	}
	return create(path, size, label, func(fsys filesystem.FileSystem) error { return copyTree(fsys, dir) })
}

// inside reports whether path lies within dir, after resolving symbolic links.
func inside(path, dir string) bool {
	resolve := func(p string) string {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return r
		}
		return filepath.Clean(p)
	}
	rel, err := filepath.Rel(resolve(dir), resolve(filepath.Dir(path)))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func checkTarget(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("give the image's full path")
	}
	if strings.ToLower(filepath.Ext(path)) != ".img" {
		return errors.New("the image name must end in .img")
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
		return fmt.Errorf("the folder %s does not exist", filepath.Dir(path))
	}
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("%s already exists", filepath.Base(path))
	}
	return nil
}

// create formats a new image and lets fill add files. On any failure it removes the file.
func create(path string, size int64, label string, fill func(filesystem.FileSystem) error) (err error) {
	if size > floppyMax {
		size = (size + mib - 1) / mib * mib
	}
	d, err := diskfs.Create(path, size, diskfs.SectorSize512)
	if err != nil {
		// The library may fail after creating the file (e.g. no space to extend it). checkTarget
		// saw no file here, so anything left is ours, unless the failure was that it now exists.
		if !errors.Is(err, os.ErrExist) {
			_ = os.Remove(path)
		}
		return fmt.Errorf("create image: %w", err)
	}
	defer func() {
		if cerr := d.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("finish image: %w", cerr)
		}
		if err != nil {
			_ = os.Remove(path)
		}
	}()

	part, fsType := 0, filesystem.TypeFat12
	if size > floppyMax {
		part, fsType = 1, filesystem.TypeFat16
		partType := mbr.Fat16bLBA
		if size > fat16Max {
			fsType, partType = filesystem.TypeFat32, mbr.Fat32LBA
		}
		table := &mbr.Table{
			LogicalSectorSize: sectorSize, PhysicalSectorSize: sectorSize,
			Partitions: []*mbr.Partition{{Index: 1, Type: partType, Start: partitionStart, Size: uint32(size/sectorSize) - partitionStart}},
		}
		if err := d.Partition(table); err != nil {
			return fmt.Errorf("partition image: %w", err)
		}
	}
	fsys, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: part, FSType: fsType, VolumeLabel: fatLabel(label)})
	if err != nil {
		return fmt.Errorf("format image: %w", err)
	}
	if fill != nil {
		if err := fill(fsys); err != nil {
			return err
		}
	}
	return nil
}

// fatLabel makes a valid FAT volume label: upper case, at most 11 characters.
func fatLabel(label string) string {
	label = strings.ToUpper(strings.TrimSpace(label))
	if len(label) > labelMax {
		label = label[:labelMax]
	}
	return label
}

// spaceNeeded estimates the bytes a folder occupies on FAT: each file and directory rounded up
// to a 4 KiB cluster.
func spaceNeeded(dir string) (int64, error) {
	const cluster = 4096
	var total int64
	err := filepath.WalkDir(dir, func(_ string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		total += (info.Size() + cluster - 1) / cluster * cluster
		if e.IsDir() {
			total += cluster
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("read folder: %w", err)
	}
	return total, nil
}

// copyTree copies dir's directories and regular files into the image's root.
func copyTree(fsys filesystem.FileSystem, dir string) error {
	return filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		target := "/" + filepath.ToSlash(rel)
		switch {
		case e.IsDir():
			if err := fsys.Mkdir(target); err != nil {
				return fmt.Errorf("create %s in the image: %w", target, err)
			}
		case e.Type().IsRegular():
			return copyFile(fsys, p, target)
		default:
			log.Printf("[DiskImage] Skipping %s (not a regular file or folder)", p)
		}
		return nil
	})
}

func copyFile(fsys filesystem.FileSystem, src, target string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := fsys.OpenFile(target, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return fmt.Errorf("create %s in the image: %w", target, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %s: %w", src, err)
	}
	return out.Close()
}
