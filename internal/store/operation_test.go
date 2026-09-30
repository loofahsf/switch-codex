package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reopen(t *testing.T, s *Store) *Store {
	t.Helper()
	next := New(s.DataDir, s.TargetAuthPath)
	if err := next.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	if err := next.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	return next
}

func crash(t *testing.T, run func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected simulated crash")
		}
	}()
	run()
}

func TestRevisionAndLegacyIndex(t *testing.T) {
	s := setup(t)
	if err := os.WriteFile(s.indexPath(), []byte(`{"activeAccountId":null,"accounts":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	state, err := s.ListAccounts()
	if err != nil || state.Revision != 0 {
		t.Fatalf("legacy state: %+v %v", state, err)
	}
	state, err = s.AddAccount("Alpha", string(auth("a", "one")))
	if err != nil || state.Revision != 1 {
		t.Fatalf("add revision: %+v %v", state, err)
	}
	state, err = s.SwitchAccount(state.Accounts[0].ID)
	if err != nil || state.Revision != 2 {
		t.Fatalf("switch revision: %+v %v", state, err)
	}
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		if path == s.indexPath() {
			return errors.New("index fail")
		}
		return write(path, b, mode)
	}
	if _, err = s.AddAccount("Beta", string(auth("b", "two"))); err == nil {
		t.Fatal("expected failure")
	}
	s.write = write
	state, err = s.ListAccounts()
	if err != nil || state.Revision != 2 {
		t.Fatalf("failed write advanced revision: %+v %v", state, err)
	}
}

func TestQuotaSnapshotsUseCompositeIdentityAndRemainImmutable(t *testing.T) {
	s := setup(t)
	state, err := s.AddAccount("Alpha", string(userAuth("user-a", "team", "saved")))
	if err != nil {
		t.Fatal(err)
	}
	id := state.Accounts[0].ID
	if err = os.WriteFile(s.TargetAuthPath, userAuth("user-b", "team", "other"), 0600); err != nil {
		t.Fatal(err)
	}
	quota, err := s.QuotaSnapshots()
	if err != nil || quota.Revision != state.Revision {
		t.Fatalf("snapshot: %+v %v", quota, err)
	}
	raw, _ := quota.Accounts[0].Credentials()
	if !bytes.Contains(raw, []byte("saved")) || bytes.Contains(raw, []byte("other")) {
		t.Fatal("selected another user's target")
	}
	if err = os.WriteFile(s.TargetAuthPath, userAuth("user-a", "team", "new"), 0600); err != nil {
		t.Fatal(err)
	}
	quota, err = s.QuotaSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = quota.Accounts[0].Credentials()
	if !bytes.Contains(raw, []byte("new")) {
		t.Fatal("did not select same identity's new token")
	}
	if _, err = s.RemoveAccount(id); err != nil {
		t.Fatal(err)
	}
	again, _ := quota.Accounts[0].Credentials()
	if !bytes.Equal(raw, again) {
		t.Fatal("snapshot changed after deletion")
	}
}

func TestStaleReconcileAndAddCurrent(t *testing.T) {
	s := setup(t)
	addAccount(t, s, "Alpha", "a")
	old, _ := os.ReadFile(s.TargetAuthPath)
	newer := auth("a", "newer")
	if err := os.WriteFile(s.TargetAuthPath, newer, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReconcileTargetAuth(old); !errors.Is(err, ErrTargetAuthChanged) {
		t.Fatalf("stale reconcile: %v", err)
	}
	if _, err := s.AddCurrentAccount("Beta", old); !errors.Is(err, ErrTargetAuthChanged) {
		t.Fatalf("stale add: %v", err)
	}
	state, _ := s.ListAccounts()
	if state.Revision != 1 || len(state.Accounts) != 1 {
		t.Fatal("stale operation changed index")
	}
}

func TestSwitchCrashRecovery(t *testing.T) {
	for _, point := range []string{"before-index", "after-index"} {
		t.Run(point, func(t *testing.T) {
			s := setup(t)
			addAccount(t, s, "Alpha", "a")
			beta := addAccount(t, s, "Beta", "b")
			old, _ := os.ReadFile(s.TargetAuthPath)
			write := s.write
			s.write = func(path string, b []byte, mode os.FileMode) error {
				if point == "before-index" && path == s.indexPath() {
					panic("crash")
				}
				err := write(path, b, mode)
				if point == "after-index" && path == s.indexPath() && err == nil {
					panic("crash")
				}
				return err
			}
			crash(t, func() { _, _ = s.SwitchAccount(beta) })
			next := reopen(t, s)
			state, _ := next.ListAccounts()
			current, _ := os.ReadFile(next.TargetAuthPath)
			if point == "before-index" && (!bytes.Equal(current, old) || state.Revision != 2) {
				t.Fatal("uncommitted switch not restored")
			}
			if point == "after-index" && (bytes.Equal(current, old) || state.Revision != 3) {
				t.Fatal("committed switch lost")
			}
		})
	}
}

func TestFirstAddCrashRemovesUncommittedCredential(t *testing.T) {
	s := setup(t)
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		if path == s.indexPath() {
			panic("crash")
		}
		return write(path, b, mode)
	}
	crash(t, func() { _, _ = s.AddAccount("Alpha", string(auth("a", "one"))) })
	next := reopen(t, s)
	state, err := next.ListAccounts()
	if err != nil || len(state.Accounts) != 0 || state.Revision != 0 {
		t.Fatalf("uncommitted add survived: %+v %v", state, err)
	}
	entries, err := os.ReadDir(filepath.Join(next.DataDir, "accounts"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("uncommitted credentials survived: %v %v", entries, err)
	}
}

func TestCommittedOperationSurvivesMarkerCleanupFailure(t *testing.T) {
	for _, kind := range []string{"first-add", "switch", "delete"} {
		t.Run(kind, func(t *testing.T) {
			s := setup(t)
			var id string
			if kind != "first-add" {
				addAccount(t, s, "Alpha", "a")
				id = addAccount(t, s, "Beta", "b")
			}
			s.removeMarker = func(string) error { return errors.New("cleanup denied") }
			var state AccountsState
			var err error
			switch kind {
			case "first-add":
				state, err = s.AddAccount("Alpha", string(auth("a", "one")))
			case "switch":
				state, err = s.SwitchAccount(id)
			case "delete":
				state, err = s.RemoveAccount(id)
			}
			if err != nil {
				t.Fatalf("committed operation returned failure: %v", err)
			}
			if _, err = os.Stat(s.operationPath()); err != nil {
				t.Fatal("committed marker should remain for recovery")
			}
			if kind == "first-add" {
				if len(state.Accounts) != 1 {
					t.Fatal("committed account missing from returned state")
				}
				if _, err = os.Stat(s.authPath(state.Accounts[0].ID)); err != nil {
					t.Fatal("committed credentials removed")
				}
			} else if kind == "switch" && (state.ActiveAccountID == nil || *state.ActiveAccountID != id) {
				t.Fatal("completed switch returned stale state")
			} else if kind == "delete" && len(state.Accounts) != 1 {
				t.Fatal("completed delete returned stale state")
			}
			if _, err = s.AddCurrentAccount("Blocked", auth("c", "three")); err == nil || !strings.Contains(err.Error(), "cleanup denied") {
				t.Fatalf("mutation bypassed pending transaction: %v", err)
			}
			next := reopen(t, s)
			if _, err = os.Stat(next.operationPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("restart did not clear marker")
			}
		})
	}
}

func TestUnresolvedSwitchMarkerBlocksEveryMutation(t *testing.T) {
	s := setup(t)
	alpha := addAccount(t, s, "Alpha", "a")
	beta := addAccount(t, s, "Beta", "b")
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		if filepath.Base(path) == BackupName {
			return errors.New("backup denied")
		}
		return write(path, b, mode)
	}
	if _, err := s.SwitchAccount(beta); err == nil {
		t.Fatal("expected backup failure")
	}
	if _, err := os.Stat(s.operationPath()); err != nil {
		t.Fatal("switch marker missing")
	}
	s.removeMarker = func(string) error { return errors.New("recovery denied") }
	current, _ := os.ReadFile(s.TargetAuthPath)
	backup := &AccountsBackup{payload: backupPayload{SchemaVersion: backupSchema, ExportedAt: time.Now().UTC().Format(time.RFC3339Nano), Accounts: []backupAccount{{ID: "source", Name: "Source", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Auth: auth("source", "token")}}}}
	checks := []struct {
		name string
		run  func() error
	}{
		{"add", func() error { _, err := s.AddAccount("Gamma", string(auth("c", "token"))); return err }},
		{"add-current", func() error { _, err := s.AddCurrentAccount("Gamma", current); return err }},
		{"switch", func() error { _, err := s.SwitchAccount(alpha); return err }},
		{"delete", func() error { _, err := s.RemoveAccount(alpha); return err }},
		{"reconcile", func() error { _, err := s.ReconcileTargetAuth(current); return err }},
		{"refresh", func() error { return s.PersistRefreshedAuth(alpha, current, auth("a", "updated")) }},
		{"import", func() error { _, err := s.ImportAccountsBackup(backup); return err }},
	}
	for _, check := range checks {
		if err := check.run(); err == nil || !strings.Contains(err.Error(), "recovery denied") {
			t.Fatalf("%s bypassed marker: %v", check.name, err)
		}
	}
	if _, err := os.Stat(s.operationPath()); err != nil {
		t.Fatal("failed recovery removed marker")
	}
}

func TestDeleteCrashRecovery(t *testing.T) {
	for _, point := range []string{"before-index", "after-index"} {
		t.Run(point, func(t *testing.T) {
			s := setup(t)
			id := addAccount(t, s, "Alpha", "a")
			write := s.write
			s.write = func(path string, b []byte, mode os.FileMode) error {
				if point == "before-index" && path == s.indexPath() {
					panic("crash")
				}
				err := write(path, b, mode)
				if point == "after-index" && path == s.indexPath() && err == nil {
					panic("crash")
				}
				return err
			}
			crash(t, func() { _, _ = s.RemoveAccount(id) })
			next := reopen(t, s)
			state, _ := next.ListAccounts()
			_, statErr := os.Stat(next.authPath(id))
			if point == "before-index" && (len(state.Accounts) != 1 || statErr != nil) {
				t.Fatal("uncommitted delete not restored")
			}
			if point == "after-index" && (len(state.Accounts) != 0 || !errors.Is(statErr, os.ErrNotExist)) {
				t.Fatal("committed delete not cleaned")
			}
		})
	}
}

func legacyDeleteCrash(t *testing.T) (*Store, string, []byte) {
	t.Helper()
	s := setup(t)
	id := "legacy-account_01"
	authBytes := auth("legacy", "saved")
	if err := s.write(s.authPath(id), authBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.save(index{Accounts: []Account{{ID: id, Name: "Legacy"}}}); err != nil {
		t.Fatal(err)
	}
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		if path == s.indexPath() {
			panic("crash")
		}
		return write(path, b, mode)
	}
	crash(t, func() { _, _ = s.RemoveAccount(id) })
	return s, id, authBytes
}

func legacyTombCredential(t *testing.T, s *Store, id string) string {
	t.Helper()
	tombs, err := filepath.Glob(filepath.Dir(s.authPath(id)) + ".deleted-*")
	if err != nil || len(tombs) != 1 {
		t.Fatalf("tombstone missing: %v %v", tombs, err)
	}
	return filepath.Join(tombs[0], "auth.json")
}

func TestLegacyIDDeleteCrashRecovery(t *testing.T) {
	s, id, original := legacyDeleteCrash(t)
	next := reopen(t, s)
	state, err := next.ListAccounts()
	if err != nil || len(state.Accounts) != 1 || state.Accounts[0].ID != id {
		t.Fatalf("legacy account not restored: %+v %v", state, err)
	}
	got, err := os.ReadFile(next.authPath(id))
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("legacy credentials lost: %v", err)
	}
}

func TestDeleteRecoveryPreservesMarkerOnIndexMismatch(t *testing.T) {
	s, id, original := legacyDeleteCrash(t)
	tombAuth := legacyTombCredential(t, s, id)
	if err := os.WriteFile(s.indexPath(), []byte(`{"revision":99,"accounts":[{"id":"legacy-account_01","name":"Legacy"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(s.DataDir, s.TargetAuthPath).EnsureReady(); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("index mismatch accepted: %v", err)
	}
	if _, err := os.Stat(s.operationPath()); err != nil {
		t.Fatal("marker removed after index mismatch")
	}
	got, err := os.ReadFile(tombAuth)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("tombstone credentials lost: %v", err)
	}
}

func TestDeleteRecoveryPreservesMarkerOnRenameFailure(t *testing.T) {
	s, id, original := legacyDeleteCrash(t)
	tombAuth := legacyTombCredential(t, s, id)
	next := New(s.DataDir, s.TargetAuthPath)
	next.rename = func(string, string) error { return errors.New("restore denied") }
	if err := next.EnsureReady(); err == nil || !strings.Contains(err.Error(), "restore denied") {
		t.Fatalf("failed restore accepted: %v", err)
	}
	if _, err := os.Stat(s.operationPath()); err != nil {
		t.Fatal("marker removed after failed rollback")
	}
	got, err := os.ReadFile(tombAuth)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatalf("tombstone credentials lost: %v", err)
	}
	reopen(t, s)
}

func TestExternalTargetAndInvalidMarkerPreserved(t *testing.T) {
	s := setup(t)
	addAccount(t, s, "Alpha", "a")
	beta := addAccount(t, s, "Beta", "b")
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		if path == s.indexPath() {
			panic("crash")
		}
		return write(path, b, mode)
	}
	crash(t, func() { _, _ = s.SwitchAccount(beta) })
	external := auth("c", "external")
	if err := os.WriteFile(s.TargetAuthPath, external, 0600); err != nil {
		t.Fatal(err)
	}
	next := reopen(t, s)
	current, _ := os.ReadFile(next.TargetAuthPath)
	if !bytes.Equal(current, external) {
		t.Fatal("recovery overwrote external target")
	}
	marker := operationMarker{Kind: "delete", BeforeSHA256: strings.Repeat("a", 64), AfterSHA256: strings.Repeat("b", 64), ID: beta, Tomb: "../escape"}
	raw, _ := json.Marshal(marker)
	if err := os.WriteFile(next.operationPath(), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(next.DataDir, next.TargetAuthPath).EnsureReady(); err == nil {
		t.Fatal("accepted invalid marker")
	}
	if _, err := os.Stat(next.authPath(beta)); err != nil {
		t.Fatalf("invalid marker deleted credentials: %v", err)
	}
	if _, err := os.Stat(filepath.Join(next.DataDir, "accounts-operation.pending.json")); err != nil {
		t.Fatal("invalid marker removed")
	}
}
