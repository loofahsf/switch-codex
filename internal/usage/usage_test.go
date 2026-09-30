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
	"sync/atomic"
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

func TestOversizedSessionLineIsSkippedWithoutLosingFollowingEvents(t *testing.T) {
	dir := t.TempDir()
	valid := `{"type":"event_msg","timestamp":"2026-09-09T09:00:00Z","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":100,"output_tokens":20}}}}`
	oversized := `{"ignored":"` + strings.Repeat("x", 512) + `"}`
	if err := os.WriteFile(filepath.Join(dir, "rollout.jsonl"), []byte(oversized+"\n"+valid+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, "2026-09-09T10:00:00Z")
	r, err := aggregateWithMaxLine(context.Background(), dir, 0, catalog{}, now, 256)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary.TotalTokens != 120 || r.Summary.ModelCalls != 1 {
		t.Fatalf("valid event after oversized line was lost: %+v", r.Summary)
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
	s := usageStore(t)
	id := addUsageAccount(t, s, "A", usageAuth("user-a", "account", "stored"))
	if _, err := s.SwitchAccount(id); err != nil {
		t.Fatal(err)
	}
	writeTarget(t, s, usageAuth("user-a", "account", "active"))
	var expectedToken atomic.Value
	expectedToken.Store("active")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expectedToken.Load().(string) || r.Header.Get("ChatGPT-Account-Id") != "account" {
			t.Error("wrong credential source")
		}
		if r.URL.Path == "/reset-credits" {
			w.Write([]byte(`{"available_count":1,"credits":[{"id":"credit-1","reset_type":"codex_rate_limits","status":"available","granted_at":"2026-09-01T00:00:00Z","expires_at":"2026-10-01T00:00:00Z","title":"Full reset"}]}`))
			return
		}
		w.Write([]byte(`{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":105,"limit_window_seconds":61},"secondary_window":{"used_percent":2,"limit_window_seconds":604800},"tertiary_window":{"used_percent":3,"limit_window_seconds":2592000}},"rate_limit_reset_credits":{"available_count":1},"credits":{"has_credits":true,"balance":12.5}}`))
	}))
	defer server.Close()
	c := NewClient()
	c.QuotaEndpoint = server.URL + "/usage"
	c.ResetCreditsEndpoint = server.URL + "/reset-credits"
	c.QuotaInterval = 0
	r := c.AccountQuotas(context.Background(), quotaSnapshot(t, s))
	q := r.Accounts[0]
	if !q.Ok || q.Primary.UsedPercent != 105 || *q.Primary.WindowMinutes != 2 || q.Weekly == nil || q.Monthly == nil || *q.Credits.Balance != "12.5" || q.ResetCredits.AvailableCount != 1 || len(q.ResetCredits.Credits) != 1 || q.ResetCredits.Credits[0].ID != "credit-1" {
		t.Fatalf("window contract differs: %+v", q)
	}
	if _, e := c.AccountQuota(context.Background(), quotaSnapshot(t, s), "missing"); e == nil {
		t.Fatal("missing account accepted")
	}
	writeTarget(t, s, `{"OPENAI_API_KEY":"synthetic"}`)
	expectedToken.Store("stored")
	if q = c.AccountQuotas(context.Background(), quotaSnapshot(t, s)).Accounts[0]; !q.Ok {
		t.Fatal("different identity did not fall back to saved credential")
	}
	writeTarget(t, s, `{"tokens":{"access_token":"identity-missing","refresh_token":"refresh","account_id":"account"}}`)
	if q = c.AccountQuotas(context.Background(), quotaSnapshot(t, s)).Accounts[0]; !q.Ok {
		t.Fatal("missing identity did not fall back to saved credential")
	}
	writeTarget(t, s, `invalid`)
	if q = c.AccountQuotas(context.Background(), quotaSnapshot(t, s)).Accounts[0]; !q.Ok {
		t.Fatal("invalid global credential did not fall back to saved credential")
	}
	apiID := addUsageAccount(t, s, "API", `{"OPENAI_API_KEY":"synthetic"}`)
	apiResult, err := c.AccountQuota(context.Background(), quotaSnapshot(t, s), apiID)
	if err != nil || apiResult.Accounts[0].Ok || apiResult.Accounts[0].Error == nil || !strings.Contains(*apiResult.Accounts[0].Error, "API Key") {
		t.Fatalf("API key account was queried: %+v %v", apiResult, err)
	}
}

func TestConsumeRateLimitResetCredit(t *testing.T) {
	s := usageStore(t)
	id := addUsageAccount(t, s, "A", usageAuth("user-a", "account", "stored"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer stored" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong reset request")
		}
		var body struct {
			RedeemRequestID string `json:"redeem_request_id"`
			CreditID        string `json:"credit_id"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.RedeemRequestID != "attempt-1" || body.CreditID != "credit-1" {
			t.Fatalf("wrong reset payload: %+v", body)
		}
		w.Write([]byte(`{"code":"reset","windows_reset":2}`))
	}))
	defer server.Close()
	c := NewClient()
	c.ResetCreditConsumeEndpoint = server.URL
	snapshot := quotaSnapshot(t, s)
	result, err := c.ConsumeResetCredit(context.Background(), snapshot, id, "credit-1", "attempt-1")
	if err != nil || result.Code != "reset" || result.WindowsReset != 2 {
		t.Fatalf("reset failed: result=%+v err=%v", result, err)
	}
	if _, err = c.ConsumeResetCredit(context.Background(), snapshot, "missing", "", "attempt-2"); err == nil {
		t.Fatal("missing account accepted")
	}
}
func TestQuotaHTTPFailures(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			s := usageStore(t)
			addUsageAccount(t, s, "A", usageAuth("user-a", "account", "synthetic"))
			c := NewClient()
			c.QuotaInterval = 0
			c.QuotaEndpoint = server.URL
			r := c.AccountQuotas(context.Background(), quotaSnapshot(t, s))
			if r.Accounts[0].Ok || r.Accounts[0].Error == nil {
				t.Fatal("HTTP failure was hidden")
			}
		})
	}
}
