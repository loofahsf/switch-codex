// Package platform contains the small OS boundary used by the business packages.
package platform

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteAtomic never removes the destination before replacing it. The temporary
// file lives on the same filesystem and is removed on every failure path.
func WriteAtomic(path string, contents []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = f.Close(); _ = os.Remove(tmp) }()
	if err = f.Chmod(mode); err != nil {
		return err
	}
	if _, err = f.Write(contents); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = replaceFile(tmp, path); err != nil {
		return err
	}
	if d, openErr := os.Open(dir); openErr == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

// Lock uses the same scheduler.lock and overlapping OS lock range as Rust's
// std::fs::File::try_lock. A PID file is insufficient for cross-version exclusion.
func Lock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("无法打开任务锁: %w", err)
	}
	if err = lockFile(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("该数据目录已由另一个 Switch Codex 实例使用: %w", err)
	}
	return f, nil
}
