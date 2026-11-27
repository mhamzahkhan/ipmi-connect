//go:build unix

package vmedia

import (
	"os"
	"syscall"
)

// openNoFollow opens path, failing if its final component is a symlink.
func openNoFollow(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag|syscall.O_NOFOLLOW, 0)
}
