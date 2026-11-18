//go:build !unix

package vmedia

import "os"

// openNoFollow opens path; the SameFile check in openFile still catches a swapped file.
func openNoFollow(path string, flag int) (*os.File, error) {
	return os.OpenFile(path, flag, 0)
}
