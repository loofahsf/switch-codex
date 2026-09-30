package usage

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"switch-codex/internal/store"
	"sync/atomic"
	"testing"
	"time"
)

func usageAuth(user, team, access string) string {
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(
		`{"sub":%q,"https://api.openai.com/auth":{"chatgpt_account_id":%q,"chatgpt_user_id":%q}}`, user, team, user)))
	return fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":"refresh","account_id":%q,"id_token":"header.%s.signature"}}`, access, team, payload)
}

func usageStore(t *testing.T) *store.Store {
	t.Helper()
	root := t.TempDir()
	s := store.New(filepath.Join(root, "data"), filepath.Join(root, "home", ".codex", "auth.json"))
	if err := s.EnsureReady(); err != nil {
		t.Fatal(err)
	}
	return s
}

func addUsageAccount(t *testing.T, s *store.Store, name, auth string) string {
	t.Helper()
	state, err := s.AddAccount(name, auth)
	if err != nil {
		t.Fatal(err)
	}
	return state.Accounts[len(state.Accounts)-1].ID
}

func quotaSnapshot(t *testing.T, s *store.Store) store.QuotaSnapshot {
	t.Helper()
	snapshot, err := s.QuotaSnapshots()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func writeTarget(t *testing.T, s *store.Store, auth string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(s.TargetAuthPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.TargetAuthPath, []byte(auth), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaAndResetUseSelectedUserWithinSameTeam(t *testing.T) {
	s := usageStore(t)
	id := addUsageAccount(t, s, "A", usageAuth("user-a", "team", "saved-a"))
	if _, err := s.SwitchAccount(id); err != nil {
		t.Fatal(err)
	}
	writeTarget(t, s, usageAuth("user-b", "team", "other-b"))
	var gets, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer saved-a" || r.Header.Get("ChatGPT-Account-Id") != "team" {
			t.Errorf("wrong user's credential: %s / %s", r.Header.Get("Authorization"), r.Header.Get("ChatGPT-Account-Id"))
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			w.Write([]byte(`{"code":"reset","windows_reset":2}`))
		} else {
			gets.Add(1)
			w.Write([]byte(`{"plan_type":"plus"}`))
		}
	}))
	defer server.Close()
	c := NewClient()
	c.QuotaInterval = 0
	c.QuotaEndpoint, c.ResetCreditConsumeEndpoint = server.URL, server.URL
	snapshot := quotaSnapshot(t, s)
	if q, err := c.AccountQuota(context.Background(), snapshot, id); err != nil || !q.Accounts[0].Ok {
		t.Fatalf("quota: %+v %v", q, err)
	}
	if _, err := c.ConsumeResetCredit(context.Background(), snapshot, id, "credit", "attempt"); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 1 || posts.Load() != 1 {
		t.Fatalf("GET=%d POST=%d", gets.Load(), posts.Load())
	}
}

func TestQuotaSnapshotSurvivesSwitchAndDelete(t *testing.T) {
	s := usageStore(t)
	idA := addUsageAccount(t, s, "A", usageAuth("user-a", "team", "saved-a"))
	idB := addUsageAccount(t, s, "B", usageAuth("user-b", "team", "saved-b"))
	snapshot := quotaSnapshot(t, s)
	if _, err := s.SwitchAccount(idB); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RemoveAccount(idA); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer saved-a" {
			t.Errorf("snapshot changed: %s", r.Header.Get("Authorization"))
		}
		if r.Method == http.MethodPost {
			w.Write([]byte(`{"code":"reset","windows_reset":1}`))
		} else {
			w.Write([]byte(`{"plan_type":"plus"}`))
		}
	}))
	defer server.Close()
	c := NewClient()
	c.QuotaInterval = 0
	c.QuotaEndpoint, c.ResetCreditConsumeEndpoint = server.URL, server.URL
	if q, err := c.AccountQuota(context.Background(), snapshot, idA); err != nil || !q.Accounts[0].Ok {
		t.Fatalf("quota: %+v %v", q, err)
	}
	if _, err := c.ConsumeResetCredit(context.Background(), snapshot, idA, "credit", "attempt"); err != nil {
		t.Fatal(err)
	}
}

func waitForFlight(t *testing.T, c *Client, id string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		c.quotaMu.Lock()
		found := false
		for key := range c.quotaFlights {
			if key.id == id {
				found = true
			}
		}
		c.quotaMu.Unlock()
		if found {
			return
		}
		select {
		case <-deadline:
			t.Fatal("flight did not start")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

type notifiedContext struct {
	context.Context
	observed chan struct{}
	once     atomic.Bool
}

func (c *notifiedContext) Done() <-chan struct{} {
	if c.once.CompareAndSwap(false, true) {
		close(c.observed)
	}
	return c.Context.Done()
}

func TestQuotaSingleflightAndInvalidation(t *testing.T) {
	s := usageStore(t)
	id := addUsageAccount(t, s, "A", usageAuth("a", "team", "token-a"))
	snapshot := quotaSnapshot(t, s)
	entered := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		first := calls.Add(1) == 1
		entered <- struct{}{}
		if first {
			<-releaseFirst
		}
		w.Write([]byte(`{"plan_type":"plus"}`))
	}))
	defer server.Close()
	c := NewClient()
	c.QuotaInterval = 0
	c.QuotaEndpoint = server.URL
	results := make(chan AccountQuota, 3)
	go func() { q, _ := c.AccountQuota(context.Background(), snapshot, id); results <- q.Accounts[0] }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first HTTP did not start")
	}
	joined := &notifiedContext{Context: context.Background(), observed: make(chan struct{})}
	go func() { q, _ := c.AccountQuota(joined, snapshot, id); results <- q.Accounts[0] }()
	select {
	case <-joined.observed:
	case <-time.After(2 * time.Second):
		t.Fatal("second caller did not join")
	}
	if calls.Load() != 1 {
		t.Fatalf("same credential caused %d HTTP calls", calls.Load())
	}
	c.InvalidateQuotas([]string{id})
	go func() { q, _ := c.AccountQuota(context.Background(), snapshot, id); results <- q.Accounts[0] }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("new epoch did not start HTTP")
	}
	close(releaseFirst)
	a, b, newer := <-results, <-results, <-results
	if calls.Load() != 2 {
		t.Fatalf("expected two HTTP calls, got %d", calls.Load())
	}
	ids := []uint64{a.RequestID, b.RequestID, newer.RequestID}
	if ids[0] == ids[1] && ids[2] > ids[0] {
		return
	}
	if ids[0] == ids[2] && ids[1] > ids[0] {
		return
	}
	if ids[1] == ids[2] && ids[0] > ids[1] {
		return
	}
	t.Fatalf("unexpected request IDs: %v", ids)
}

func TestQuotaWaiterCancellationAndIntervalStop(t *testing.T) {
	s := usageStore(t)
	idA := addUsageAccount(t, s, "A", usageAuth("a", "team-a", "token-a"))
	addUsageAccount(t, s, "B", usageAuth("b", "team-b", "token-b"))
	snapshot := quotaSnapshot(t, s)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		w.Write([]byte(`{"plan_type":"plus"}`))
	}))
	defer server.Close()
	c := NewClient()
	c.QuotaInterval = time.Hour
	c.QuotaEndpoint = server.URL
	first := make(chan AccountQuota, 1)
	go func() { q, _ := c.AccountQuota(context.Background(), snapshot, idA); first <- q.Accounts[0] }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first HTTP did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	joined := &notifiedContext{Context: ctx, observed: make(chan struct{})}
	waiter := make(chan AccountQuota, 1)
	go func() { q, _ := c.AccountQuota(joined, snapshot, idA); waiter <- q.Accounts[0] }()
	select {
	case <-joined.observed:
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not join flight")
	}
	cancel()
	select {
	case q := <-waiter:
		if q.Ok || q.Error == nil || !strings.Contains(*q.Error, "取消") {
			t.Fatalf("waiter: %+v", q)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter blocked after cancel")
	}
	close(release)
	<-first
	ctx2, cancel2 := context.WithCancel(context.Background())
	all := make(chan AccountQuotas, 1)
	go func() { all <- c.AccountQuotas(ctx2, snapshot) }()
	waitForFlight(t, c, idA)
	cancel2()
	select {
	case <-all:
	case <-time.After(2 * time.Second):
		t.Fatal("interval wait ignored cancel")
	}
	if calls.Load() != 1 {
		t.Fatalf("queried further accounts after cancellation: %d", calls.Load())
	}
}

type quotaRoundTripFunc func(*http.Request) (*http.Response, error)

func (f quotaRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestMissingQuotaCredentialBypassesFlightAndInterval(t *testing.T) {
	s := usageStore(t)
	id := addUsageAccount(t, s, "A", usageAuth("a", "team", "token-a"))
	state, err := s.ListAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(state.Accounts[0].AuthPath); err != nil {
		t.Fatal(err)
	}
	snapshot := quotaSnapshot(t, s)
	if _, err := snapshot.Accounts[0].Credentials(); err == nil {
		t.Fatal("expected snapshot to contain the credential read error")
	}
	var calls atomic.Int32
	c := NewClient()
	c.HTTP = &http.Client{Transport: quotaRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("unexpected HTTP request")
	})}
	c.QuotaEndpoint = "http://quota.invalid/usage"
	c.QuotaInterval = time.Hour
	c.nextQuotaStart = time.Now().Add(time.Hour)
	c.quotaGate <- struct{}{}
	defer func() { <-c.quotaGate }()

	query := func() AccountQuota {
		t.Helper()
		result := make(chan AccountQuota, 1)
		go func() {
			q, err := c.AccountQuota(context.Background(), snapshot, id)
			if err == nil && len(q.Accounts) == 1 {
				result <- q.Accounts[0]
			}
		}()
		select {
		case q := <-result:
			return q
		case <-time.After(250 * time.Millisecond):
			t.Fatal("credential error waited for quota slot")
			return AccountQuota{}
		}
	}
	first, second := query(), query()
	if first.Ok || second.Ok || first.Error == nil || second.Error == nil {
		t.Fatalf("missing credential did not produce errors: %+v %+v", first, second)
	}
	if first.RequestID == 0 || second.RequestID <= first.RequestID {
		t.Fatalf("request IDs did not advance: %d, %d", first.RequestID, second.RequestID)
	}
	if calls.Load() != 0 {
		t.Fatalf("credential errors made %d HTTP requests", calls.Load())
	}
	c.quotaMu.Lock()
	flights := len(c.quotaFlights)
	c.quotaMu.Unlock()
	if flights != 0 {
		t.Fatalf("credential errors entered %d flights", flights)
	}
}
