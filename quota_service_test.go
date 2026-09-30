package main

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"switch-codex/internal/scheduler"
	"switch-codex/internal/store"
	"switch-codex/internal/usage"
	"sync/atomic"
	"testing"
	"time"
)

type quotaTestTransport func(*http.Request) (*http.Response, error)

func (f quotaTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type warmupTestRunner struct{}

func (warmupTestRunner) Run(context.Context, *exec.Cmd, time.Duration) (scheduler.ProcessOutput, error) {
	return scheduler.ProcessOutput{Success: true, Stdout: []byte("{\"type\":\"turn.completed\"}\n")}, nil
}

func TestSingleAccountWarmupReturnsBeforeOnlyItsQuotaRefresh(t *testing.T) {
	root := t.TempDir()
	st := store.New(filepath.Join(root, "data"), filepath.Join(root, "home", "auth.json"))
	if err := st.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	var firstID string
	for _, name := range []string{"alpha", "beta"} {
		claims := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + name + `","https://api.openai.com/auth":{"chatgpt_account_id":"team"}}`))
		auth := `{"tokens":{"account_id":"team","access_token":"` + name + `","id_token":"h.` + claims + `.s","refresh_token":"refresh-` + name + `"}}`
		state, err := st.AddAccount(name, auth)
		if err != nil {
			t.Fatal(err)
		}
		if name == "alpha" {
			firstID = state.Accounts[0].ID
		}
	}
	scheduled, err := scheduler.New(st, scheduler.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer scheduled.Close()
	manual, err := scheduler.NewManualWarmup(st, scheduler.Options{
		Runner:      warmupTestRunner{},
		ResolveCLI:  func(*string) (string, error) { return "/synthetic/codex", nil },
		ValidateCLI: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manual.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release := make(chan string, 2), make(chan struct{})
	var calls atomic.Int32
	client := usage.NewClient()
	client.QuotaInterval = 0
	client.HTTP = &http.Client{Transport: quotaTestTransport(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		started <- req.Header.Get("Authorization")
		select {
		case <-release:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"plan_type":"plus"}`))}, nil
	})}
	svc := &AppService{ctx: ctx, store: st, usage: client, scheduler: scheduled, manualWarmup: manual, emitEvent: func(string, any) {}}
	emitted := make(chan usage.AccountQuotas, 1)
	svc.quotaRefresh = newQuotaRefresher(ctx, svc.refreshQuotaAccounts, func(result usage.AccountQuotas) { emitted <- result })
	defer svc.quotaRefresh.Close()
	completed := make(chan error, 1)
	go func() { completed <- svc.WarmupAccount(firstID) }()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("warmup waited for the quota HTTP request")
	}
	select {
	case authorization := <-started:
		if authorization != "Bearer alpha" {
			t.Fatalf("refreshed another account: %s", authorization)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("warmup did not queue a quota refresh")
	}
	close(release)
	select {
	case result := <-emitted:
		if len(result.Accounts) != 1 || result.Accounts[0].AccountID != firstID || result.Accounts[0].RequestID == 0 {
			t.Fatal("refresh was not a versioned partial account update")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("background refresh did not emit")
	}
	if calls.Load() != 1 {
		t.Fatalf("single account warmup made %d requests", calls.Load())
	}
}
