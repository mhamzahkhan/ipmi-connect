package diskimage

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/filesystem"
)

// openFS reads back the filesystem in an image we created.
func openFS(t *testing.T, path string) (filesystem.FileSystem, int) {
	t.Helper()
	d, err := diskfs.Open(path, diskfs.WithOpenMode(diskfs.ReadOnly))
	if err != nil {
		t.Fatalf("open image: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, part := range []int{0, 1} {
		if fsys, err := d.GetFilesystem(part); err == nil {
			return fsys, part
		}
	}
	t.Fatal("no filesystem found in the image")
	return nil, 0
}

func TestCreateBlank(t *testing.T) {
	tests := []struct {
		name      string
		size      int64
		wantType  filesystem.Type
		wantPart  int
		wantBytes int64
	}{
		{"1.44 MB floppy is a bare FAT12 volume", FloppySize, filesystem.TypeFat12, 0, FloppySize},
		{"64 MiB stick is FAT16 in an MBR partition", 64 << 20, filesystem.TypeFat16, 1, 64 << 20},
		{"1 GiB stick is FAT32", 1 << 30, filesystem.TypeFat32, 1, 1 << 30},
		{"odd sizes round up to whole MiB", 10<<20 + 123, filesystem.TypeFat16, 1, 11 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "blank.img")
			if err := CreateBlank(path, tt.size, "RESCUE"); err != nil {
				t.Fatalf("CreateBlank: %v", err)
			}
			st, _ := os.Stat(path)
			if st.Size() != tt.wantBytes {
				t.Errorf("file is %d bytes, want %d", st.Size(), tt.wantBytes)
			}
			fsys, part := openFS(t, path)
			if fsys.Type() != tt.wantType || part != tt.wantPart {
				t.Errorf("filesystem %v in partition %d, want %v in %d", fsys.Type(), part, tt.wantType, tt.wantPart)
			}
			if label := strings.TrimSpace(fsys.Label()); label != "RESCUE" {
				t.Errorf("label %q", label)
			}
		})
	}
}

func TestCreateBlankValidation(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists.img")
	_ = os.WriteFile(existing, []byte("x"), 0o600)
	tests := []struct {
		name, path string
		size       int64
		want       string
	}{
		{"relative path", "blank.img", FloppySize, "full path"},
		{"wrong extension", filepath.Join(dir, "blank.iso"), FloppySize, ".img"},
		{"existing file is never overwritten", existing, FloppySize, "exists"},
		{"too small", filepath.Join(dir, "a.img"), 1000, "size"},
		{"too large", filepath.Join(dir, "b.img"), MaxSize + 1, "size"},
		{"missing folder", filepath.Join(dir, "nope", "c.img"), FloppySize, "folder"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CreateBlank(tt.path, tt.size, ""); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
	if data, _ := os.ReadFile(existing); string(data) != "x" {
		t.Error("an existing file was modified")
	}
}

func TestCreateFromFolder(t *testing.T) {
	src := t.TempDir()
	files := map[string][]byte{
		"readme.txt":                       []byte("drivers for the RAID card\n"),
		"drivers/storage/hba.ko":          bytes.Repeat([]byte{0x7f}, 300_000),
		"drivers/firmware-2026.bin":        bytes.Repeat([]byte("fw"), 50_000),
		"A Long File Name With Spaces.txt": []byte("long names survive"),
	}
	for name, data := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.Mkdir(filepath.Join(src, "empty"), 0o700)

	path := filepath.Join(t.TempDir(), "drivers.img")
	if err := CreateFromFolder(path, src, "DRIVERS"); err != nil {
		t.Fatalf("CreateFromFolder: %v", err)
	}
	fsys, _ := openFS(t, path)
	for name, want := range files {
		got, err := fs.ReadFile(fsys, "/"+name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes, want %d", name, len(got), len(want))
		}
	}
	root, err := fsys.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir /: %v", err)
	}
	var names []string
	for _, e := range root {
		names = append(names, e.Name())
	}
	t.Logf("root: %v", names)
	if _, err := fsys.ReadDir("empty"); err != nil {
		t.Errorf("empty folder missing: %v", err)
	}
}

func TestCreateFromFolderFitsAFloppy(t *testing.T) {
	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("small"), 0o600)
	path := filepath.Join(t.TempDir(), "small.img")
	if err := CreateFromFolder(path, src, ""); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(path); st.Size() != FloppySize {
		t.Errorf("small folder made a %d-byte image, want a 1.44 MB floppy", st.Size())
	}
}

func TestCreateFromFolderRejectsATargetInsideTheFolder(t *testing.T) {
	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o600)
	_ = os.Mkdir(filepath.Join(src, "sub"), 0o700)
	if err := CreateFromFolder(filepath.Join(src, "sub", "..", "self.img"), src, ""); err == nil || !strings.Contains(err.Error(), "inside") {
		t.Errorf("err = %v, want a refusal to create an image inside the folder it copies", err)
	}
	if _, err := os.Stat(filepath.Join(src, "self.img")); err == nil {
		t.Error("left a file behind")
	}
}

func TestCreateFromFolderRejectsNonFolders(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file.txt")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	if err := CreateFromFolder(filepath.Join(t.TempDir(), "x.img"), f, ""); err == nil {
		t.Error("accepted a file as the source folder")
	}
}
