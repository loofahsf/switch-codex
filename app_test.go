package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"switch-codex/internal/store"
)

func TestReadBackupFileBoundsAndType(t *testing.T) {
	root := t.TempDir()
	valid := filepath.Join(root, "accounts"+store.BackupExtension)
	if err := os.WriteFile(valid, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	contents, err := readBackupFile(valid)
	if err != nil || string(contents) != "synthetic" {
		t.Fatalf("readBackupFile() = %q, %v", contents, err)
	}
	if _, err = readBackupFile(root); err == nil || !strings.Contains(err.Error(), "普通文件") {
		t.Fatalf("directory error = %v", err)
	}
	large := filepath.Join(root, "large"+store.BackupExtension)
	f, err := os.OpenFile(large, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(store.MaxBackupBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = readBackupFile(large); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatalf("large file error = %v", err)
	}
	if _, err = readBackupFile(filepath.Join(root, "missing")); err == nil {
		t.Fatalf("missing file error = %v", err)
	}
}
