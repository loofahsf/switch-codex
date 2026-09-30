package authsync

import (
	"bytes"
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

func TestCheckRereadsSnapshotAfterConcurrentSwitchOrRefresh(t *testing.T) {
	for _, action := range []string{"switch", "refresh"} {
		t.Run(action, func(t *testing.T) {
			st, initial := testStore(t)
			state, err := st.AddAccount("Beta", string(testAuth("user-b", "team", "beta")))
			if err != nil {
				t.Fatal(err)
			}
			observed, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			service := New(st, Options{
				Enabled: true,
				Wait:    func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil },
				ReadFile: func(path string, max int64) ([]byte, error) {
					raw, err := readBoundedFile(path, max)
					once.Do(func() { close(observed); <-release })
					return raw, err
				},
			})
			defer service.Close()
			done := make(chan Status, 1)
			go func() { done <- service.CheckNow() }()
			<-observed
			if action == "switch" {
				_, err = st.SwitchAccount(state.Accounts[1].ID)
			} else {
				original, readErr := os.ReadFile(initial.Accounts[0].AuthPath)
				if readErr != nil {
					err = readErr
				} else {
					err = st.PersistRefreshedAuth(initial.Accounts[0].ID, original, testAuth("user-a", "team", "refreshed"))
				}
			}
			close(release)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case status := <-done:
				if status.State != UpToDate {
					t.Fatalf("check did not re-read the winning update: %+v", status)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("interleaved check did not finish")
			}
			current, err := st.ListAccounts()
			if err != nil {
				t.Fatal(err)
			}
			wanted := state.Accounts[1].ID
			if action == "refresh" {
				wanted = initial.Accounts[0].ID
			}
			if current.ActiveAccountID == nil || *current.ActiveAccountID != wanted {
				t.Fatal("obsolete snapshot reverted the active account")
			}
			if action == "refresh" {
				for _, path := range []string{initial.Accounts[0].AuthPath, st.TargetAuthPath} {
					raw, err := os.ReadFile(path)
					if err != nil || !bytes.Contains(raw, []byte("refreshed")) {
						t.Fatal("obsolete snapshot overwrote the refreshed credential")
					}
				}
			}
		})
	}
}
