//go:build windows

package authsync

import "os"

func openAuthFile(path string) (*os.File, error) { return os.Open(path) }
