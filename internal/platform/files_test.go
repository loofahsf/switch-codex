package platform

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAtomicReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	for _, text := range []string{"old", "new"} {
		if e := WriteAtomic(path, []byte(text), 0600); e != nil {
			t.Fatal(e)
		}
	}
	b, _ := os.ReadFile(path)
	if string(b) != "new" {
		t.Fatal("not replaced")
	}
	info, _ := os.Stat(path)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("credentials not private")
	}
	target := filepath.Join(dir, "directory")
	if e := os.Mkdir(target, 0700); e != nil {
		t.Fatal(e)
	}
	if e := WriteAtomic(target, []byte("failure"), 0600); e == nil {
		t.Fatal("expected replacement failure")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if len(matches) != 0 {
		t.Fatal("temporary files leaked")
	}
}
func TestLockHelper(t *testing.T) {
	if path := os.Getenv("SWITCH_CODEX_TEST_LOCK"); path != "" {
		f, e := Lock(path)
		if e != nil {
			os.Exit(23)
		}
		f.Close()
		os.Exit(0)
	}
}
func TestLockAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "scheduler.lock")
	f, e := Lock(path)
	if e != nil {
		t.Fatal(e)
	}
	exe, _ := os.Executable()
	run := func() error {
		cmd := exec.Command(exe, "-test.run=^TestLockHelper$")
		cmd.Env = append(os.Environ(), "SWITCH_CODEX_TEST_LOCK="+path)
		return cmd.Run()
	}
	if e = run(); e == nil {
		t.Fatal("second process acquired lock")
	}
	f.Close()
	if e = run(); e != nil {
		t.Fatalf("lock not released: %v", e)
	}
}

func TestDataDirPlatformPaths(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "workspace")
	home := filepath.Join(base, "home", "tester")
	xdg := filepath.Join(base, "data-home")
	override := filepath.Join(base, "custom", "switch-codex")

	got, err := dataDirForOS("linux", false, root, home, override, "", xdg)
	if err != nil || got != override {
		t.Fatalf("override path = %q, %v; want %q", got, err, override)
	}
	got, err = dataDirForOS("linux", false, root, home, "", "", xdg)
	want := filepath.Join(xdg, AppIdentifier, "data")
	if err != nil || got != want {
		t.Fatalf("XDG path = %q, %v; want %q", got, err, want)
	}
	got, err = dataDirForOS("linux", false, root, home, "", "", "")
	want = filepath.Join(home, ".local", "share", AppIdentifier, "data")
	if err != nil || got != want {
		t.Fatalf("default Linux path = %q, %v; want %q", got, err, want)
	}
	localAppData := filepath.Join(base, "LocalAppData")
	got, err = dataDirForOS("windows", false, root, home, "", localAppData, "")
	want = filepath.Join(localAppData, AppIdentifier, "data")
	if err != nil || got != want {
		t.Fatalf("Windows path = %q, %v; want %q", got, err, want)
	}
	got, err = dataDirForOS("darwin", false, root, home, "", "", "")
	want = filepath.Join(home, "Library", "Application Support", AppIdentifier, "data")
	if err != nil || got != want {
		t.Fatalf("macOS path = %q, %v; want %q", got, err, want)
	}
}
func legacyData(t *testing.T, root string) {
	t.Helper()
	for name, b := range map[string]string{"accounts.json": `{"activeAccountId":"a","accounts":[{"id":"a"}]}`, "accounts/a/auth.json": `{"tokens":{"refresh_token":"synthetic"}}`, "settings.json": `{"settings":{"enabled":false}}`} {
		if e := WriteAtomic(filepath.Join(root, "src-tauri/data", name), []byte(b), 0600); e != nil {
			t.Fatal(e)
		}
	}
}
func TestDevelopmentMigrationAndConflict(t *testing.T) {
	root := t.TempDir()
	legacyData(t, root)
	dir, e := DevelopmentDataDir(root)
	if e != nil {
		t.Fatal(e)
	}
	old, _ := os.ReadFile(filepath.Join(root, "src-tauri/data/accounts/a/auth.json"))
	copy, _ := os.ReadFile(filepath.Join(dir, "accounts/a/auth.json"))
	if !bytes.Equal(old, copy) {
		t.Fatal("copy differs")
	}
	if _, e = DevelopmentDataDir(root); e != nil {
		t.Fatal("completed migration not idempotent", e)
	}
	conflict := t.TempDir()
	legacyData(t, conflict)
	if e = WriteAtomic(filepath.Join(conflict, "data/accounts.json"), []byte(`{"accounts":[{"id":"b"}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = DevelopmentDataDir(conflict); e == nil {
		t.Fatal("merged independent indexes")
	}
}
func TestMigrationRejectsInvalidSourceAndBusyDirectory(t *testing.T) {
	root := t.TempDir()
	legacyData(t, root)
	f, e := Lock(filepath.Join(root, "src-tauri/data/scheduler.lock"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = DevelopmentDataDir(root); e == nil {
		t.Fatal("copied a live store")
	}
	f.Close()
	if e = os.WriteFile(filepath.Join(root, "src-tauri/data/settings.json"), []byte("invalid"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = DevelopmentDataDir(root); e == nil {
		t.Fatal("copied invalid settings")
	}
	if exists(filepath.Join(root, "data/accounts.json")) {
		t.Fatal("partially published migration")
	}

	invalidAuth := t.TempDir()
	legacyData(t, invalidAuth)
	if e = os.WriteFile(filepath.Join(invalidAuth, "src-tauri/data/accounts/a/auth.json"), []byte(`{}`), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = DevelopmentDataDir(invalidAuth); e == nil {
		t.Fatal("copied invalid credentials")
	}
}

func TestMigrationRollbackAndFinderMetadata(t *testing.T) {
	root := t.TempDir()
	legacyData(t, root)
	empty := []byte(`{"activeAccountId":null,"accounts":[]}`)
	if e := WriteAtomic(filepath.Join(root, "data/accounts.json"), empty, 0600); e != nil {
		t.Fatal(e)
	}
	if e := WriteAtomic(filepath.Join(root, "data/accounts/.DS_Store"), []byte("finder"), 0600); e != nil {
		t.Fatal(e)
	}
	// A directory at the marker path forces the final commit write to fail.
	if e := os.Mkdir(filepath.Join(root, "data/.migrated-from-tauri"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := DevelopmentDataDir(root); e == nil {
		t.Fatal("expected marker commit failure")
	}
	got, e := os.ReadFile(filepath.Join(root, "data/accounts.json"))
	if e != nil || !bytes.Equal(got, empty) {
		t.Fatal("empty destination index was not restored")
	}
	if exists(filepath.Join(root, "data/accounts/a/auth.json")) {
		t.Fatal("partially copied credentials remained after rollback")
	}
	if !exists(filepath.Join(root, "data/accounts/.DS_Store")) {
		t.Fatal("Finder metadata was removed")
	}
}
