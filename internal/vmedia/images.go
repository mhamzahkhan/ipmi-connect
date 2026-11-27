package vmedia

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mhamzahkhan/ipmi-connect/internal/bmc"
)

// floppyMaxBytes is the vendor's size limit for the floppy drive.
const floppyMaxBytes = 2_880_000

type image struct {
	id, name, path string
	size           int64
	kind           string // "iso" or "img"
	readOnly       bool
	info           os.FileInfo // identifies the file across links and path spellings
}

// targets lists the drives an image can go to, as the vendor client decides it: ISOs on the
// CD; IMGs on the removable drive, or the floppy when small enough.
func (im *image) targets() []bmc.MediaTarget {
	if im.kind == "iso" {
		return []bmc.MediaTarget{bmc.TargetCD}
	}
	if im.size < floppyMaxBytes {
		return []bmc.MediaTarget{bmc.TargetRemovable, bmc.TargetFloppy}
	}
	return []bmc.MediaTarget{bmc.TargetRemovable}
}

func (im *image) canMapTo(t bmc.MediaTarget) bool {
	for _, x := range im.targets() {
		if x == t {
			return true
		}
	}
	return false
}

// inspectImage validates a path the user added.
func inspectImage(path string, readOnly bool) (*image, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("give the image's full path")
	}
	path = filepath.Clean(path)
	kind := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if kind != "iso" && kind != "img" {
		return nil, errors.New("the image must be an .iso or .img file")
	}
	// Open (without following a final symlink) and inspect the open file, so what is checked is
	// what was opened; openFile later insists on the same file.
	f, err := openNoFollow(path, os.O_RDONLY)
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	st, err := f.Stat()
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return nil, errors.New("the image must be a non-empty regular file")
	}
	id := make([]byte, 6)
	_, _ = rand.Read(id)
	return &image{
		id: hex.EncodeToString(id), name: filepath.Base(path), path: path,
		size: st.Size(), kind: kind, readOnly: readOnly || kind == "iso", info: st,
	}, nil
}

// ErrImageReplaced means the path now names a different file than the one the user added.
var ErrImageReplaced = errors.New("the image file was replaced since it was added; remove it and add it again")

// openFile opens the image for serving: read-write only when the image is writable. It refuses
// a path that has been swapped for another file (or a symlink) since the image was added, so a
// replaced file is never served to the host, or written.
func (im *image) openFile() (*os.File, error) {
	flag := os.O_RDONLY
	if !im.readOnly {
		flag = os.O_RDWR
	}
	f, err := openNoFollow(im.path, flag)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", im.name, err)
	}
	st, err := f.Stat()
	if err != nil || !os.SameFile(st, im.info) || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, ErrImageReplaced
	}
	return f, nil
}
