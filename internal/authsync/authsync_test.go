package authsync

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"switch-codex/internal/store"
	"sync"
	"testing"
	"time"
)

func testAuth(user, workspace, refresh string) []byte {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + user + `","https://api.openai.com/auth":{"chatgpt_account_id":"` + workspace + `","chatgpt_user_id":"` + user + `"}}`))
	return []byte(`{"tokens":{"account_id":"` + workspace + `","id_token":"header.` + payload + `.signature","refresh_token":"` + refresh + `"}}`)
}

func testStore(t *testing.T) (*store.Store, store.AccountsState) {
	t.Helper()
	root := t.TempDir()
	st := store.New(filepath.Join(root, "data"), filepath.Join(root, "home", ".codex", "auth.json"))
	if err := st.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	state, err := st.AddAccount("Alpha", string(testAuth("user-a", "team", "first")))
	if err != nil {
		t.Fatal(err)
	}
	return st, state
}

func TestCheckReadsOnlyTargetAndSynchronizesCompositeIdentity(t *testing.T) {
	st, state := testStore(t)
	var mu sync.Mutex
	var reads []string
	service := New(st, Options{
		Enabled: true,
		ReadFile: func(path string, max int64) ([]byte, error) {
			mu.Lock()
			reads = append(reads, path)
			mu.Unlock()
			return readBoundedFile(path, max)
		},
	})
	defer service.Close()
	if status := service.CheckNow(); status.State != UpToDate {
		t.Fatalf("unexpected initial state: %s", status.State)
	}
	if err := os.WriteFile(st.TargetAuthPath, testAuth("user-a", "team", "newer"), 0600); err != nil {
		t.Fatal(err)
	}
	if status := service.CheckNow(); status.State != Synced {
		t.Fatalf("credential was not synchronized: %s", status.State)
	}
	saved, err := os.ReadFile(state.Accounts[0].AuthPath)
	if err != nil || !contains(saved, "newer") {
		t.Fatal("saved credential did not change")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, path := range reads {
		if path != st.TargetAuthPath {
			t.Fatalf("poller accessed an unrelated path: %s", path)
		}
	}
}

func TestFollowsKnownMemberAndAddsUnknownCurrentAccount(t *testing.T) {
	st, _ := testStore(t)
	state, err := st.AddAccount("Beta", string(testAuth("user-b", "team", "beta")))
	if err != nil {
		t.Fatal(err)
	}
	var changed store.AccountsState
	service := New(st, Options{Enabled: true, AccountsChanged: func(next store.AccountsState) { changed = next }})
	defer service.Close()
	if err = os.WriteFile(st.TargetAuthPath, testAuth("user-b", "team", "beta-new"), 0600); err != nil {
		t.Fatal(err)
	}
	status := service.CheckNow()
	if status.State != Followed || status.AccountID == nil || *status.AccountID != state.Accounts[1].ID || changed.ActiveAccountID == nil {
		t.Fatal("did not follow the uniquely matched team member")
	}
	if err = os.WriteFile(st.TargetAuthPath, testAuth("user-c", "team", "unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	status = service.CheckNow()
	if status.State != Unknown || status.PendingID == nil {
		t.Fatal("unknown account did not create a pending action")
	}
	added, err := service.AddPendingCurrentAccount(*status.PendingID, "Gamma")
	if err != nil || len(added.Accounts) != 3 || added.ActiveAccountID == nil || *added.ActiveAccountID != added.Accounts[2].ID {
		t.Fatal("pending current account was not safely added")
	}
}

func TestPendingAccountRejectsChangedTarget(t *testing.T) {
	st, _ := testStore(t)
	service := New(st, Options{Enabled: true})
	defer service.Close()
	if err := os.WriteFile(st.TargetAuthPath, testAuth("user-c", "team", "unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	status := service.CheckNow()
	if status.PendingID == nil {
		t.Fatal("missing pending action")
	}
	if err := os.WriteFile(st.TargetAuthPath, testAuth("user-d", "team", "changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddPendingCurrentAccount(*status.PendingID, "Wrong"); err == nil {
		t.Fatal("changed target was added using a stale pending action")
	}
}

func TestContentDigestDoesNotDependOnFileMetadata(t *testing.T) {
	st, state := testStore(t)
	service := New(st, Options{Enabled: true})
	defer service.Close()
	service.check(true, false)
	info, err := os.Stat(st.TargetAuthPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := store.NormalizeAuth(testAuth("user-a", "team", "other"))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(st.TargetAuthPath)
	if len(before) != len(replacement) {
		t.Fatal("test credentials must have the same size")
	}
	if err = os.WriteFile(st.TargetAuthPath, replacement, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chtimes(st.TargetAuthPath, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	service.check(false, false)
	if service.Status().State != Synced {
		t.Fatal("same-size, same-mtime content change was missed")
	}
	saved, _ := os.ReadFile(state.Accounts[0].AuthPath)
	if !contains(saved, "other") {
		t.Fatal("replacement was not saved")
	}
}

func TestMissingInvalidOversizedAndReadFailureAreIsolated(t *testing.T) {
	st, _ := testStore(t)
	waitNow := func(context.Context, time.Duration) bool { return true }
	if err := os.Remove(st.TargetAuthPath); err != nil {
		t.Fatal(err)
	}
	service := New(st, Options{Enabled: true, Wait: waitNow})
	if status := service.CheckNow(); status.State != Missing {
		t.Fatalf("missing file reported as %s", status.State)
	}
	service.Close()
	if err := os.WriteFile(st.TargetAuthPath, []byte(`{"broken"`), 0600); err != nil {
		t.Fatal(err)
	}
	service = New(st, Options{Enabled: true, Wait: waitNow})
	if status := service.CheckNow(); status.State != Invalid {
		t.Fatalf("invalid file reported as %s", status.State)
	}
	service.Close()
	if err := os.WriteFile(st.TargetAuthPath, make([]byte, MaxAuthBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	service = New(st, Options{Enabled: true, Wait: waitNow})
	if status := service.CheckNow(); status.State != Invalid {
		t.Fatalf("actual oversized file reported as %s", status.State)
	}
	service.Close()
	service = New(st, Options{Enabled: true, ReadFile: func(string, int64) ([]byte, error) { return nil, errTooLarge }})
	if status := service.CheckNow(); status.State != Invalid {
		t.Fatalf("oversized file reported as %s", status.State)
	}
	service.Close()
	service = New(st, Options{Enabled: true, ReadFile: func(string, int64) ([]byte, error) { return nil, errors.New("denied") }})
	if status := service.CheckNow(); status.State != Failed {
		t.Fatalf("read failure reported as %s", status.State)
	}
	service.Close()
}

func TestTransientInvalidWriteRetriesAndFailureRecovery(t *testing.T) {
	st, _ := testStore(t)
	reads := 0
	service := New(st, Options{
		Enabled: true,
		Wait:    func(context.Context, time.Duration) bool { return true },
		ReadFile: func(path string, max int64) ([]byte, error) {
			reads++
			if reads < 3 {
				return []byte(`{"tokens":`), nil
			}
			return readBoundedFile(path, max)
		},
	})
	if status := service.CheckNow(); status.State != UpToDate || reads != 3 {
		t.Fatalf("transient write was not retried: state=%s reads=%d", status.State, reads)
	}
	service.Close()

	service = New(st, Options{Enabled: true, Wait: func(context.Context, time.Duration) bool { return true }})
	defer service.Close()
	if status := service.CheckNow(); status.State != UpToDate {
		t.Fatalf("unexpected initial state: %s", status.State)
	}
	previous, err := os.ReadFile(st.TargetAuthPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(st.TargetAuthPath); err != nil {
		t.Fatal(err)
	}
	if status := service.CheckNow(); status.State != Missing {
		t.Fatalf("missing file reported as %s", status.State)
	}
	if err = os.WriteFile(st.TargetAuthPath, previous, 0600); err != nil {
		t.Fatal(err)
	}
	service.check(false, false)
	if status := service.Status(); status.State != UpToDate {
		t.Fatalf("restored previous content was skipped after failure: %s", status.State)
	}

	if err = os.WriteFile(st.TargetAuthPath, testAuth("unknown-user", "team", "unknown"), 0600); err != nil {
		t.Fatal(err)
	}
	if status := service.CheckNow(); status.State != Unknown || status.PendingID == nil {
		t.Fatalf("expected pending unknown account, got %s", status.State)
	}
	indexPath := filepath.Join(st.DataDir, "accounts.json")
	index, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(indexPath, []byte(`{"broken":`), 0600); err != nil {
		t.Fatal(err)
	}
	if status := service.CheckNow(); status.State != Failed || service.pending != nil {
		t.Fatalf("store failure did not invalidate pending state: %s", status.State)
	}
	if err = os.WriteFile(indexPath, index, 0600); err != nil {
		t.Fatal(err)
	}
	service.check(false, false)
	if status := service.Status(); status.State != Unknown || status.PendingID == nil {
		t.Fatalf("same digest was skipped after store recovery: %s", status.State)
	}
}

func TestPollingLifecycleAndEnablement(t *testing.T) {
	st, _ := testStore(t)
	ticks := make(chan time.Time, 1)
	checked := make(chan Status, 4)
	service := New(st, Options{Enabled: false, Ticks: ticks, Emit: func(status Status) { checked <- status }})
	service.Start()
	if service.Status().State != Disabled {
		t.Fatal("service did not start disabled")
	}
	service.SetEnabled(true)
	waitForState(t, service, UpToDate)
	ticks <- time.Now()
	service.SetEnabled(false)
	if service.Status().State != Disabled {
		t.Fatal("service did not stop polling when disabled")
	}
	service.Close()
}

func waitForState(t *testing.T, service *Service, state State) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if service.Status().State == state {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; got %s", state, service.Status().State)
}

func contains(raw []byte, value string) bool {
	for i := 0; i+len(value) <= len(raw); i++ {
		if string(raw[i:i+len(value)]) == value {
			return true
		}
	}
	return false
}
