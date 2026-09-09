package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func auth(id, token string) []byte {
	return []byte(`{"tokens":{"account_id":"` + id + `","refresh_token":"` + token + `"},"unknown":9007199254740993}`)
}
func setup(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	s := New(filepath.Join(root, "data"), filepath.Join(root, "home", ".codex", "auth.json"))
	if err := s.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	return s
}
func addAccount(t *testing.T, s *Store, name, id string) string {
	t.Helper()
	v, e := s.AddAccount(name, string(auth(id, "synthetic")))
	if e != nil {
		t.Fatal(e)
	}
	return v.Accounts[len(v.Accounts)-1].ID
}
func TestAuthValidationAndIdentity(t *testing.T) {
	for _, v := range []string{"", `[]`, `null`, `{}`, `{"tokens":{"refresh_token":"  "}}`, `{"OPENAI_API_KEY":null}`} {
		if _, e := NormalizeAuth([]byte(v)); e == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	for _, v := range [][]byte{auth("  account  ", "refresh"), []byte(`{"OPENAI_API_KEY":"synthetic"}`)} {
		b, e := NormalizeAuth(v)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.HasSuffix(b, []byte{'\n'}) {
			t.Fatal("missing newline")
		}
	}
	b, _ := NormalizeAuth(auth("  account  ", "refresh"))
	if !bytes.Contains(b, []byte("9007199254740993")) {
		t.Fatal("lost unknown integer")
	}
	if id := AccountID(b); id == nil || *id != "account" {
		t.Fatal("identity not trimmed")
	}
	if AccountID([]byte(`{}`)) != nil {
		t.Fatal("missing identity matched")
	}
}
func TestAccountLifecycleAndBackup(t *testing.T) {
	s := setup(t)
	a := addAccount(t, s, "Alpha", "a")
	first, _ := os.ReadFile(s.TargetAuthPath)
	b := addAccount(t, s, "Beta", "b")
	if _, e := s.AddAccount(" aLPHa ", string(auth("c", "token"))); e == nil {
		t.Fatal("duplicate name accepted")
	}
	state, e := s.SwitchAccount(b)
	if e != nil {
		t.Fatal(e)
	}
	if state.ActiveAccountID == nil || *state.ActiveAccountID != b {
		t.Fatal("not active")
	}
	backup, _ := os.ReadFile(filepath.Join(filepath.Dir(s.TargetAuthPath), BackupName))
	if !bytes.Equal(first, backup) {
		t.Fatal("backup changed")
	}
	current, _ := os.ReadFile(s.TargetAuthPath)
	state, e = s.RemoveAccount(b)
	if e != nil {
		t.Fatal(e)
	}
	if state.ActiveAccountID != nil || len(state.Accounts) != 1 {
		t.Fatal("invalid removal state")
	}
	after, _ := os.ReadFile(s.TargetAuthPath)
	if !bytes.Equal(current, after) {
		t.Fatal("deleting active account changed global auth")
	}
	if _, e = s.SwitchAccount(a); e != nil {
		t.Fatal(e)
	}
	if _, e = s.SwitchAccount("missing"); e == nil {
		t.Fatal("missing account accepted")
	}
}
func TestIndexFailureRollsBackAuth(t *testing.T) {
	s := setup(t)
	addAccount(t, s, "Alpha", "a")
	b := addAccount(t, s, "Beta", "b")
	before, _ := os.ReadFile(s.TargetAuthPath)
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		if path == s.indexPath() {
			return errors.New("simulated index failure")
		}
		return write(path, b, mode)
	}
	if _, e := s.SwitchAccount(b); e == nil {
		t.Fatal("expected failure")
	}
	after, _ := os.ReadFile(s.TargetAuthPath)
	if !bytes.Equal(before, after) {
		t.Fatal("old auth not restored")
	}
	if _, e := s.RemoveAccount(b); e == nil {
		t.Fatal("expected removal failure")
	}
	if _, e := os.Stat(s.authPath(b)); e != nil {
		t.Fatal("removed credentials despite index failure")
	}
}
func TestBackupFailureDoesNotReplaceTarget(t *testing.T) {
	s := setup(t)
	addAccount(t, s, "Alpha", "a")
	b := addAccount(t, s, "Beta", "b")
	before, _ := os.ReadFile(s.TargetAuthPath)
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		if filepath.Base(path) == BackupName {
			return errors.New("backup denied")
		}
		return write(path, b, mode)
	}
	if _, e := s.SwitchAccount(b); e == nil {
		t.Fatal("expected backup failure")
	}
	after, _ := os.ReadFile(s.TargetAuthPath)
	if !bytes.Equal(before, after) {
		t.Fatal("target overwritten")
	}
}
func TestUpdateMismatchAndRefreshConflict(t *testing.T) {
	s := setup(t)
	id := addAccount(t, s, "Alpha", "a")
	original, _ := os.ReadFile(s.authPath(id))
	if e := os.WriteFile(s.TargetAuthPath, auth("b", "new"), 0600); e != nil {
		t.Fatal(e)
	}
	r, e := s.UpdateAccountAuth(id, false)
	if e != nil || r.Updated || r.State != nil {
		t.Fatal("mismatch confirmation lost")
	}
	r, e = s.UpdateAccountAuth(id, true)
	if e != nil || !r.Updated {
		t.Fatal("confirmed update failed")
	}
	if e = s.PersistRefreshedAuth(id, original, auth("a", "refreshed")); e == nil {
		t.Fatal("overwrote user changes")
	}
	current, _ := os.ReadFile(s.authPath(id))
	if e = s.PersistRefreshedAuth(id, current, auth("x", "refreshed")); e == nil {
		t.Fatal("accepted wrong identity")
	}
	if e = s.PersistRefreshedAuth(id, current, auth("b", "refreshed")); e != nil {
		t.Fatal(e)
	}
	snapshots, _ := s.Snapshots()
	if _, e = s.RemoveAccount(id); e != nil {
		t.Fatal(e)
	}
	if e = s.PersistRefreshedAuth(id, current, auth("b", "newer")); e == nil {
		t.Fatal("resurrected removed account")
	}
	raw, _ := snapshots[0].Credentials()
	if !strings.Contains(string(raw), "refreshed") {
		t.Fatal("snapshot changed after deletion")
	}
}
