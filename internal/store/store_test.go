package store

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func auth(id, token string) []byte {
	return userAuth("user-"+id, id, token)
}
func userAuth(user, workspace, token string) []byte {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + user + `","https://api.openai.com/auth":{"chatgpt_account_id":"` + workspace + `","chatgpt_user_id":"` + user + `"}}`))
	return []byte(`{"tokens":{"account_id":"` + workspace + `","id_token":"header.` + payload + `.signature","refresh_token":"` + token + `"},"unknown":9007199254740993}`)
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
	keyA, err := IdentifyAuth([]byte(`{"OPENAI_API_KEY":"sk-synthetic-a"}`))
	if err != nil {
		t.Fatal(err)
	}
	keyACopy, _ := IdentifyAuth([]byte(`{"OPENAI_API_KEY":"sk-synthetic-a"}`))
	keyB, _ := IdentifyAuth([]byte(`{"OPENAI_API_KEY":"sk-synthetic-b"}`))
	chatTeamA, _ := IdentifyAuth(userAuth("same-user", "team-a", "first"))
	chatTeamB, _ := IdentifyAuth(userAuth("same-user", "team-b", "second"))
	if !keyA.Equal(keyACopy) || keyA.Equal(keyB) || keyA.Equal(chatTeamA) || chatTeamA.Equal(chatTeamB) {
		t.Fatal("composite identity did not distinguish login method, API key, user, and workspace")
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
func TestCompositeIdentityAndReconciliation(t *testing.T) {
	s := setup(t)
	alphaState, err := s.AddAccount("Alpha", string(userAuth("user-a", "team", "alpha")))
	if err != nil {
		t.Fatal(err)
	}
	alpha := alphaState.Accounts[0].ID
	betaState, err := s.AddAccount("Beta", string(userAuth("user-b", "team", "beta")))
	if err != nil {
		t.Fatal(err)
	}
	beta := betaState.Accounts[1].ID
	if _, err = s.AddAccount("Duplicate", string(userAuth("user-a", "team", "other"))); err == nil {
		t.Fatal("duplicate composite identity accepted")
	}
	writes := 0
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		writes++
		return write(path, b, mode)
	}
	r, err := s.ReconcileTargetAuth(userAuth("user-a", "team", "alpha"))
	if err != nil || r.Outcome != ReconcileUpToDate || writes != 0 {
		t.Fatalf("unchanged active account should not write: outcome=%s writes=%d err=%v", r.Outcome, writes, err)
	}
	if e := os.WriteFile(s.TargetAuthPath, userAuth("user-b", "team", "new"), 0600); e != nil {
		t.Fatal(e)
	}
	r, err = s.ReconcileTargetAuth(userAuth("user-b", "team", "new"))
	if err != nil || r.Outcome != ReconcileFollowed || r.State == nil || r.State.ActiveAccountID == nil || *r.State.ActiveAccountID != beta {
		t.Fatal("did not safely follow another member in the same workspace")
	}
	savedBeta, _ := os.ReadFile(s.authPath(beta))
	if !bytes.Contains(savedBeta, []byte("new")) {
		t.Fatal("matched account was not refreshed")
	}
	r, err = s.ReconcileTargetAuth(userAuth("user-b", "team", "newer"))
	if err != nil || r.Outcome != ReconcileSynced {
		t.Fatal("active account was not synchronized")
	}
	r, err = s.ReconcileTargetAuth(userAuth("user-c", "team", "unknown"))
	if err != nil || r.Outcome != ReconcileUnknown {
		t.Fatal("unknown member was not isolated")
	}
	r, err = s.ReconcileTargetAuth([]byte(`{"tokens":{"account_id":"team","refresh_token":"legacy"}}`))
	if err != nil || r.Outcome != ReconcileIdentityMissing {
		t.Fatal("credential without a user identity was accepted")
	}
	if _, err = s.SwitchAccount(alpha); err != nil {
		t.Fatal(err)
	}
	if e := os.WriteFile(s.authPath(beta), userAuth("user-a", "team", "duplicate"), 0600); e != nil {
		t.Fatal(e)
	}
	r, err = s.ReconcileTargetAuth(userAuth("user-a", "team", "alpha"))
	if err != nil || r.Outcome != ReconcileAmbiguous {
		t.Fatal("historical duplicate identities were not isolated")
	}
}

func TestPersistRefreshedAuthCoordinatesActiveTarget(t *testing.T) {
	s := setup(t)
	alpha := addAccount(t, s, "Alpha", "a")
	original, _ := os.ReadFile(s.authPath(alpha))
	refreshed := auth("a", "refreshed")
	if err := s.PersistRefreshedAuth(alpha, original, refreshed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.authPath(alpha), s.TargetAuthPath} {
		current, _ := os.ReadFile(path)
		if !bytes.Contains(current, []byte("refreshed")) {
			t.Fatalf("active refresh did not update %s", path)
		}
	}
	if err := s.PersistRefreshedAuth(alpha, original, auth("a", "stale")); err == nil {
		t.Fatal("stale refresh overwrote newer credentials")
	}
	current, _ := os.ReadFile(s.authPath(alpha))
	if err := s.PersistRefreshedAuth(alpha, current, userAuth("other-user", "a", "wrong")); err == nil {
		t.Fatal("accepted wrong user in the same workspace")
	}
	beta := addAccount(t, s, "Beta", "b")
	betaOriginal, _ := os.ReadFile(s.authPath(beta))
	targetBefore, _ := os.ReadFile(s.TargetAuthPath)
	if err := s.PersistRefreshedAuth(beta, betaOriginal, auth("b", "beta-refreshed")); err != nil {
		t.Fatal(err)
	}
	targetAfter, _ := os.ReadFile(s.TargetAuthPath)
	if !bytes.Equal(targetBefore, targetAfter) {
		t.Fatal("inactive refresh changed the active target")
	}
	snapshots, _ := s.Snapshots()
	if _, err := s.RemoveAccount(alpha); err != nil {
		t.Fatal(err)
	}
	if err := s.PersistRefreshedAuth(alpha, current, auth("a", "newer")); err == nil {
		t.Fatal("resurrected removed account")
	}
	raw, _ := snapshots[0].Credentials()
	if !strings.Contains(string(raw), "refreshed") {
		t.Fatal("snapshot changed after deletion")
	}
}

func TestReconcileAndActiveRefreshRollbackWhenIndexSaveFails(t *testing.T) {
	s := setup(t)
	alpha := addAccount(t, s, "Alpha", "a")
	original, err := os.ReadFile(s.authPath(alpha))
	if err != nil {
		t.Fatal(err)
	}
	write := s.write
	s.write = func(path string, b []byte, mode os.FileMode) error {
		if path == s.indexPath() {
			return errors.New("simulated index failure")
		}
		return write(path, b, mode)
	}

	external := auth("a", "external")
	if err = os.WriteFile(s.TargetAuthPath, external, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReconcileTargetAuth(external); err == nil {
		t.Fatal("reconciliation unexpectedly survived index failure")
	}
	saved, _ := os.ReadFile(s.authPath(alpha))
	if !bytes.Equal(saved, original) {
		t.Fatal("reconciliation did not roll back saved credentials")
	}
	target, _ := os.ReadFile(s.TargetAuthPath)
	if !bytes.Equal(target, external) {
		t.Fatal("reconciliation modified the target auth")
	}

	if err = os.WriteFile(s.TargetAuthPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.PersistRefreshedAuth(alpha, original, auth("a", "scheduled")); err == nil {
		t.Fatal("active refresh unexpectedly survived index failure")
	}
	for _, path := range []string{s.authPath(alpha), s.TargetAuthPath} {
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, original) {
			t.Fatalf("active refresh did not roll back %s", path)
		}
	}
}
