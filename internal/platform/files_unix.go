//go:build unix

package platform

import (
	"golang.org/x/sys/unix"
	"os"
)

func replaceFile(source, target string) error { return os.Rename(source, target) }
func lockFile(f *os.File) error               { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
