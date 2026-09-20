//go:build unix

package authsync

import (
	"os"

	"golang.org/x/sys/unix"
)

// openAuthFile avoids blocking on a FIFO before readBoundedFile can reject it.
// O_NONBLOCK has no effect on ordinary files and symlinks continue to resolve.
func openAuthFile(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), nil
}
