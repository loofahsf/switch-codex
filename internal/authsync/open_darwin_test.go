//go:build darwin

package authsync

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestReadBoundedFileRejectsFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := unix.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readBoundedFile(path, MaxAuthBytes)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errNotRegular) {
			t.Fatalf("FIFO error = %v, want %v", err, errNotRegular)
		}
	case <-time.After(time.Second):
		t.Fatal("FIFO read blocked")
	}
}

func TestReadBoundedFileAcceptsSymlinkToRegularFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "stored-auth.json")
	link := filepath.Join(dir, "auth.json")
	want := []byte(`{"OPENAI_API_KEY":"synthetic"}`)
	if err := os.WriteFile(target, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := readBoundedFile(link, MaxAuthBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read = %q, want %q", got, want)
	}
}
