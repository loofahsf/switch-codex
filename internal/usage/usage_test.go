package usage

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"switch-codex/internal/store"
	"testing"
	"time"
)

func TestPricingAndCostParity(t *testing.T) {
	b, _ := os.ReadFile("testdata/pricing.md")
	prices := parsePricing(string(b))
	p := findPrice("gpt-5.6", prices)
	if p == nil || p.Input != 5 || p.CacheWrite == nil || *p.CacheWrite != 6.25 || *p.LongOutput != 45 {
		t.Fatal("standard pricing differs")
	}
	p = findPrice("gpt-5.3-codex", prices)
	if p == nil || *p.CachedInput != 0.175 {
		t.Fatal("Codex pricing differs")
	}
	p = &ModelPrice{Input: 2, CachedInput: fptr(.2), CacheWrite: fptr(2.5), Output: 10, LongInput: fptr(4), LongCachedInput: fptr(.4), LongCacheWrite: fptr(5), LongOutput: fptr(15)}
	if math.Abs(estimate(tokens{Input: 100000, CachedInput: 80000, CacheWrite: 10000, Output: 5000}, p)-.111) > 1e-6 {
		t.Fatal("short cost differs")
	}
	if math.Abs(estimate(tokens{Input: 300000, Output: 10000}, p)-1.35) > 1e-6 {
		t.Fatal("long cost differs")
	}
	p = findPrice("gpt-5.4-mini-2026-01-01", []ModelPrice{{Model: "gpt-5.4"}, {Model: "gpt-5.4-mini"}})
	if p.Model != "gpt-5.4-mini" {
		t.Fatal("longest model prefix not selected")
	}
}
func TestRustRolloutFixtures(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-09-09T10:00:00Z")
	for _, name := range []string{"rollout", "performance", "aborted"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			b, _ := os.ReadFile("testdata/" + name + ".jsonl")
			if e := os.WriteFile(filepath.Join(dir, "rollout.jsonl"), b, 0600); e != nil {
				t.Fatal(e)
			}
			prices := catalog{Source: PricingSource{Kind: "test"}, Prices: []ModelPrice{}}
			if name == "rollout" {
				prices.Prices = []ModelPrice{{Model: "gpt-test", Input: 5, CachedInput: fptr(.5), Output: 30}}
			}
			r, e := aggregate(context.Background(), dir, 30, prices, now)
			if e != nil {
				t.Fatal(e)
			}
			if len(r.Models) != 1 || r.Summary.Sessions != 1 {
				t.Fatalf("invalid stats: %+v", r.Summary)
			}
			m := r.Models[0]
			switch name {
			case "rollout":
				if r.Summary.TotalTokens != 1100 || math.Abs(r.Summary.EstimatedCostUSD-.0071) > 1e-6 {
					t.Fatalf("Rust expected 1100 tokens, .0071 USD: %+v", r.Summary)
				}
			case "performance":
				if m.ModelCalls != 3 || m.TurnCount != 2 || *m.AverageTokensPerTurn != 825 || *m.AverageDurationMS != 3000 || *m.AverageTimeToFirstTokenMS != 500 {
					t.Fatalf("performance differs: %+v", m)
				}
			case "aborted":
				if m.TurnCount != 1 || *m.AverageTokensPerTurn != 200 || *m.AverageDurationMS != 3000 || m.AverageTimeToFirstTokenMS != nil {
					t.Fatalf("aborted turn included: %+v", m)
				}
			}
			serialized, _ := json.Marshal(r)
			if bytes.Contains(serialized, []byte("must not be returned")) {
				t.Fatal("message text escaped parser")
			}
		})
	}
}
func TestCumulativeCountersAndLongMalformedLines(t *testing.T) {
	dir := t.TempDir()
	line := `{"type":"event_msg","timestamp":"2026-09-09T09:00:00Z","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20}}}}`
	content := "invalid\n" + `{"type":"event_msg","payload":{"type":"user_message","message":"` + strings.Repeat("x", 100000) + `"}}` + "\n" + line + "\n" + line + "\n"
	if e := os.WriteFile(filepath.Join(dir, "rollout.jsonl"), []byte(content), 0600); e != nil {
		t.Fatal(e)
	}
	now, _ := time.Parse(time.RFC3339, "2026-09-09T10:00:00Z")
	r, e := aggregate(context.Background(), dir, 0, catalog{}, now)
	if e != nil {
		t.Fatal(e)
	}
	if r.Summary.TotalTokens != 120 || r.Summary.ModelCalls != 1 || r.Summary.UnpricedModelCount != 1 {
		t.Fatalf("duplicate cumulative value counted: %+v", r.Summary)
	}
	if r.Models[0].EstimatedCostUSD != nil {
		t.Fatal("unknown model priced")
	}
}
func TestPricesLiveCacheBundled(t *testing.T) {
	b, _ := os.ReadFile("testdata/pricing.md")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(b) }))
	defer server.Close()
	c := NewClient()
	c.PricingEndpoint = server.URL
	dir := t.TempDir()
	if v := c.loadPrices(context.Background(), dir, true); v.Source.Kind != "live" || len(v.Prices) != 2 {
		t.Fatal("live catalog failed")
	}
	server.Close()
	c.HTTP.Timeout = 100 * time.Millisecond
	if v := c.loadPrices(context.Background(), dir, true); v.Source.Kind != "cache" || v.Source.Warning == nil {
		t.Fatal("failed refresh did not retain cache")
	}
	if v := c.loadPrices(context.Background(), t.TempDir(), false); v.Source.Kind != "bundled" || len(v.Prices) == 0 {
		t.Fatal("bundled fallback missing")
	}
}
func TestQuotaCredentialSourceErrorsAndWindowParity(t *testing.T) {
	dir := t.TempDir()
	saved, current := filepath.Join(dir, "saved.json"), filepath.Join(dir, "current.json")
	os.WriteFile(saved, []byte(`{"tokens":{"access_token":"stored","account_id":"account"}}`), 0600)
	os.WriteFile(current, []byte(`{"tokens":{"access_token":"active","account_id":"account"}}`), 0600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer active" || r.Header.Get("ChatGPT-Account-Id") != "account" {
			t.Error("wrong credential source")
		}
		w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":105,"limit_window_seconds":61},"secondary_window":{"used_percent":2,"limit_window_seconds":604800},"tertiary_window":{"used_percent":3,"limit_window_seconds":2592000}},"credits":{"has_credits":true,"balance":12.5}}`))
	}))
	defer server.Close()
	c := NewClient()
	c.QuotaEndpoint = server.URL
	c.QuotaInterval = 0
	state := store.AccountsState{TargetAuthPath: current, Accounts: []store.AccountItem{{Account: store.Account{ID: "a", Name: "A"}, AuthPath: saved, IsActive: true}}}
	r := c.AccountQuotas(context.Background(), state)
	q := r.Accounts[0]
	if !q.Ok || q.Primary.UsedPercent != 105 || *q.Primary.WindowMinutes != 2 || q.Weekly == nil || q.Monthly == nil || *q.Credits.Balance != "12.5" {
		t.Fatalf("window contract differs: %+v", q)
	}
	if _, e := c.AccountQuota(context.Background(), state, "missing"); e == nil {
		t.Fatal("missing account accepted")
	}
	os.WriteFile(current, []byte(`{"OPENAI_API_KEY":"synthetic"}`), 0600)
	if q = c.AccountQuotas(context.Background(), state).Accounts[0]; q.Ok || !strings.Contains(*q.Error, "API Key") {
		t.Fatal("API key queried")
	}
	os.WriteFile(current, []byte(`invalid`), 0600)
	if q = c.AccountQuotas(context.Background(), state).Accounts[0]; q.Ok {
		t.Fatal("invalid JSON accepted")
	}
}
func TestQuotaHTTPFailures(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			path := filepath.Join(t.TempDir(), "auth.json")
			os.WriteFile(path, []byte(`{"tokens":{"access_token":"synthetic"}}`), 0600)
			c := NewClient()
			c.QuotaEndpoint = server.URL
			r := c.AccountQuotas(context.Background(), store.AccountsState{Accounts: []store.AccountItem{{AuthPath: path}}})
			if r.Accounts[0].Ok || r.Accounts[0].Error == nil {
				t.Fatal("HTTP failure was hidden")
			}
		})
	}
}
