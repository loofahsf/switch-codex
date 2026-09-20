package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const backupTestPassword = "correct horse battery staple"

func TestAccountsBackupRoundTripDoesNotActivateOrReplaceTarget(t *testing.T) {
	source := setup(t)
	addAccount(t, source, "Alpha", "alpha")
	beta := addAccount(t, source, "Beta", "beta")
	if _, err := source.SwitchAccount(beta); err != nil {
		t.Fatal(err)
	}
	raw, count, err := source.CreateAccountsBackup(backupTestPassword, "2.0.6")
	if err != nil || count != 2 {
		t.Fatalf("CreateAccountsBackup() count=%d err=%v", count, err)
	}
	decoded, err := DecodeAccountsBackup(raw, backupTestPassword)
	if err != nil || decoded.AccountCount() != 2 {
		t.Fatalf("DecodeAccountsBackup() count=%d err=%v", decoded.AccountCount(), err)
	}
	if decoded.payload.ActiveAccountID == nil || *decoded.payload.ActiveAccountID != beta {
		t.Fatal("source active account was not preserved inside encrypted payload")
	}

	root := t.TempDir()
	targetAuth := filepath.Join(root, "home", ".codex", "auth.json")
	target := New(filepath.Join(root, "data"), targetAuth)
	if err = target.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	current := []byte(`{"tokens":{"refresh_token":"existing-target"}}`)
	if err = target.write(targetAuth, current, 0600); err != nil {
		t.Fatal(err)
	}
	state, err := target.ImportAccountsBackup(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if state.ActiveAccountID != nil || len(state.Accounts) != 2 {
		t.Fatalf("unexpected imported state: %+v", state)
	}
	if state.Accounts[0].ID == decoded.payload.Accounts[0].ID {
		t.Fatal("target should allocate transaction-safe local account IDs")
	}
	after, err := os.ReadFile(targetAuth)
	if err != nil || !bytes.Equal(after, current) {
		t.Fatal("import changed target auth.json")
	}
	for pos, account := range state.Accounts {
		if account.Name != decoded.payload.Accounts[pos].Name || account.CreatedAt != decoded.payload.Accounts[pos].CreatedAt || account.UpdatedAt != decoded.payload.Accounts[pos].UpdatedAt {
			t.Fatalf("metadata changed at %d: %+v", pos, account)
		}
		auth, readErr := os.ReadFile(target.authPath(account.ID))
		if readErr != nil || !bytes.Contains(auth, []byte("9007199254740993")) {
			t.Fatalf("credential fields lost at %d: %v", pos, readErr)
		}
		if runtime.GOOS != "windows" {
			info, statErr := os.Stat(target.authPath(account.ID))
			if statErr != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("credential mode = %v err=%v", info.Mode().Perm(), statErr)
			}
		}
	}
}

func TestAccountsBackupRejectsWrongPasswordTamperingAndVersions(t *testing.T) {
	s := setup(t)
	addAccount(t, s, "Alpha", "alpha")
	raw, _, err := s.CreateAccountsBackup(backupTestPassword, "2.0.6")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := s.CreateAccountsBackup(backupTestPassword, "2.0.6")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(raw, second) || bytes.Equal(raw[len(backupMagic)+2:backupHeaderLen], second[len(backupMagic)+2:backupHeaderLen]) {
		t.Fatal("successive backups reused salt or nonce")
	}
	if _, err = DecodeAccountsBackup(raw, "wrong password"); err == nil || !strings.Contains(err.Error(), "密码错误或备份文件已损坏") {
		t.Fatalf("wrong password error = %v", err)
	}
	tampered := bytes.Clone(raw)
	tampered[len(tampered)-1] ^= 0xff
	if _, err = DecodeAccountsBackup(tampered, backupTestPassword); err == nil || !strings.Contains(err.Error(), "密码错误或备份文件已损坏") {
		t.Fatalf("tamper error = %v", err)
	}
	future := bytes.Clone(raw)
	future[len(backupMagic)] = 0
	future[len(backupMagic)+1] = 2
	if _, err = DecodeAccountsBackup(future, backupTestPassword); err == nil || !strings.Contains(err.Error(), "升级") {
		t.Fatalf("future version error = %v", err)
	}
	if _, err = DecodeAccountsBackup(raw[:10], backupTestPassword); err == nil {
		t.Fatal("truncated backup accepted")
	}
	if _, err = DecodeAccountsBackup(make([]byte, MaxBackupBytes+1), backupTestPassword); err == nil || !strings.Contains(err.Error(), "64 MiB") {
		t.Fatalf("oversized backup error = %v", err)
	}
	if _, _, err = s.CreateAccountsBackup("short", "2.0.6"); err == nil {
		t.Fatal("short passphrase accepted")
	}
}

func TestBackupPayloadValidation(t *testing.T) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	valid := backupPayload{SchemaVersion: backupSchema, ExportedAt: now, Accounts: []backupAccount{
		{ID: "a", Name: "Alpha", CreatedAt: now, UpdatedAt: now, Auth: auth("alpha", "one")},
		{ID: "b", Name: "Beta", CreatedAt: now, UpdatedAt: now, Auth: auth("beta", "two")},
	}}
	tests := []struct {
		name   string
		mutate func(*backupPayload)
	}{
		{"illegal id", func(v *backupPayload) { v.Accounts[0].ID = "../a" }},
		{"duplicate id", func(v *backupPayload) { v.Accounts[1].ID = "a" }},
		{"duplicate name", func(v *backupPayload) { v.Accounts[1].Name = " alpha " }},
		{"invalid auth", func(v *backupPayload) { v.Accounts[0].Auth = []byte(`{}`) }},
		{"invalid time", func(v *backupPayload) { v.Accounts[0].CreatedAt = "yesterday" }},
		{"dangling active", func(v *backupPayload) { missing := "missing"; v.ActiveAccountID = &missing }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			copy := valid
			copy.Accounts = append([]backupAccount(nil), valid.Accounts...)
			tc.mutate(&copy)
			if err := validateBackupPayload(&copy); err == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestImportRequiresEmptyStoreAndRollsBackWriteFailure(t *testing.T) {
	source := setup(t)
	addAccount(t, source, "Alpha", "alpha")
	raw, _, _ := source.CreateAccountsBackup(backupTestPassword, "2.0.6")
	decoded, _ := DecodeAccountsBackup(raw, backupTestPassword)
	nonempty := setup(t)
	addAccount(t, nonempty, "Existing", "existing")
	if _, err := nonempty.ImportAccountsBackup(decoded); err == nil || !strings.Contains(err.Error(), "没有账号") {
		t.Fatalf("non-empty import error = %v", err)
	}

	target := setup(t)
	write := target.write
	target.write = func(path string, data []byte, mode os.FileMode) error {
		if path == target.indexPath() {
			return errors.New("simulated index failure")
		}
		return write(path, data, mode)
	}
	if _, err := target.ImportAccountsBackup(decoded); err == nil {
		t.Fatal("expected import failure")
	}
	target.write = write
	state, err := target.ListAccounts()
	if err != nil || len(state.Accounts) != 0 {
		t.Fatalf("failed import became visible: %+v err=%v", state, err)
	}
	if _, err = os.Stat(target.pendingImportPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending marker was not removed: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(target.DataDir, "accounts"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("credential rollback failed: entries=%v err=%v", entries, err)
	}
}

func TestEnsureReadyRecoversPendingImport(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	s := New(dataDir, filepath.Join(root, "auth.json"))
	if err := s.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	id := "new-import-id"
	if err := s.write(s.authPath(id), auth("recover", "token"), 0600); err != nil {
		t.Fatal(err)
	}
	pending := pendingImport{ExpectedIndexSHA256: strings.Repeat("0", 64), AccountIDs: []string{id}}
	b, _ := json.Marshal(pending)
	if err := s.write(s.pendingImportPath(), b, 0600); err != nil {
		t.Fatal(err)
	}
	restarted := New(dataDir, filepath.Join(root, "auth.json"))
	if err := restarted.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restarted.authPath(id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned imported credential remains: %v", err)
	}

	committed := index{Accounts: []Account{{ID: "committed", Name: "Committed", CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}}}
	indexBytes, _ := marshalIndex(committed)
	if err := restarted.write(restarted.indexPath(), indexBytes, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(indexBytes)
	pending = pendingImport{ExpectedIndexSHA256: hex.EncodeToString(digest[:]), AccountIDs: []string{"committed"}}
	b, _ = json.Marshal(pending)
	if err := restarted.write(restarted.pendingImportPath(), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := New(dataDir, filepath.Join(root, "auth.json")).EnsureReady(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restarted.pendingImportPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed marker remains: %v", err)
	}
}
