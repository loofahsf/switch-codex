package usage

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"switch-codex/internal/platform"
	"time"
)

const PricingURL = "https://developers.openai.com/api/docs/pricing.md"
const QuotaURL = "https://chatgpt.com/backend-api/wham/usage"

//go:embed openai_pricing_fallback.json
var fallbackJSON []byte

type Client struct {
	HTTP                           *http.Client
	PricingEndpoint, QuotaEndpoint string
	Now                            func() time.Time
	QuotaInterval                  time.Duration
}

func NewClient() *Client {
	return &Client{HTTP: &http.Client{Timeout: 12 * time.Second}, PricingEndpoint: PricingURL, QuotaEndpoint: QuotaURL, Now: time.Now, QuotaInterval: 3 * time.Second}
}

type catalog struct {
	Source PricingSource
	Prices []ModelPrice
}

func strptr(s string) *string { return &s }
func fptr(n float64) *float64 { return &n }

func (c *Client) loadPrices(ctx context.Context, dir string, refresh bool) catalog {
	path := filepath.Join(dir, "openai-model-prices.json")
	var warning *string
	if refresh {
		cached, err := c.fetchPrices(ctx)
		if err == nil {
			if b, e := json.MarshalIndent(cached, "", "  "); e == nil {
				_ = platform.WriteAtomic(path, append(b, '\n'), 0600)
			}
			return catalog{Source: PricingSource{URL: cached.SourceURL, FetchedAt: cached.FetchedAt, Kind: "live", ModelCount: len(cached.Prices)}, Prices: cached.Prices}
		}
		warning = strptr("官方价格刷新失败，已使用本地价格: " + err.Error())
	}
	var cached CachedPriceCatalog
	if b, e := os.ReadFile(path); e == nil && json.Unmarshal(b, &cached) == nil && len(cached.Prices) > 0 {
		return catalog{Source: PricingSource{URL: cached.SourceURL, FetchedAt: cached.FetchedAt, Kind: "cache", ModelCount: len(cached.Prices), Warning: warning}, Prices: cached.Prices}
	}
	if err := json.Unmarshal(fallbackJSON, &cached); err != nil {
		panic("bundled pricing catalog is invalid")
	}
	return catalog{Source: PricingSource{URL: cached.SourceURL, FetchedAt: cached.FetchedAt, Kind: "bundled", ModelCount: len(cached.Prices), Warning: warning}, Prices: cached.Prices}
}
func (c *Client) fetchPrices(ctx context.Context) (CachedPriceCatalog, error) {
	var result CachedPriceCatalog
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.PricingEndpoint, nil)
	if err != nil {
		return result, err
	}
	req.Header.Set("Accept", "text/markdown")
	req.Header.Set("User-Agent", "switch-codex/pricing")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return result, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return result, err
	}
	prices := parsePricing(string(b))
	if len(prices) == 0 {
		return result, fmt.Errorf("官方价格页格式无法识别")
	}
	return CachedPriceCatalog{SourceURL: PricingURL, FetchedAt: c.Now().UTC().Format(time.RFC3339Nano), Prices: prices}, nil
}
func parsePricing(markdown string) []ModelPrice {
	prices := map[string]ModelPrice{}
	if parts := strings.Split(markdown, "### Standard pricing data"); len(parts) > 1 {
		section := strings.SplitN(parts[1], "Regional processing", 2)[0]
		for _, line := range strings.Split(section, "\n") {
			col := columns(line)
			if len(col) < 9 {
				continue
			}
			in, out := money(col[1]), money(col[4])
			if in == nil || out == nil {
				continue
			}
			name := strings.TrimSpace(strings.SplitN(col[0], " (<", 2)[0])
			prices[name] = ModelPrice{Model: name, Input: *in, CachedInput: money(col[2]), CacheWrite: money(col[3]), Output: *out, LongInput: money(col[5]), LongCachedInput: money(col[6]), LongCacheWrite: money(col[7]), LongOutput: money(col[8])}
		}
	}
	if pos := strings.LastIndex(markdown, "Specialized models"); pos >= 0 {
		section := strings.SplitN(markdown[pos+len("Specialized models"):], "Batch", 2)[0]
		for _, line := range strings.Split(section, "\n") {
			col := columns(line)
			if len(col) < 5 || col[0] != "Codex" {
				continue
			}
			in, out := money(col[2]), money(col[4])
			if in == nil || out == nil {
				continue
			}
			prices[col[1]] = ModelPrice{Model: col[1], Input: *in, CachedInput: money(col[3]), Output: *out}
		}
	}
	result := make([]ModelPrice, 0, len(prices))
	for _, v := range prices {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Model < result[j].Model })
	return result
}
func columns(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") {
		return nil
	}
	parts := strings.Split(strings.Trim(line, "|"), "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
func money(s string) *float64 {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "free") {
		return fptr(0)
	}
	parts := strings.Fields(strings.TrimPrefix(s, "$"))
	if len(parts) == 0 {
		return nil
	}
	v, e := strconv.ParseFloat(strings.ReplaceAll(parts[0], ",", ""), 64)
	if e != nil {
		return nil
	}
	return &v
}
func findPrice(name string, prices []ModelPrice) *ModelPrice {
	if name == "gpt-5.6" {
		name = "gpt-5.6-sol"
	}
	var best *ModelPrice
	for i := range prices {
		p := &prices[i]
		if name == p.Model || strings.HasPrefix(name, p.Model+"-") {
			if best == nil || len(p.Model) > len(best.Model) {
				best = p
			}
		}
	}
	return best
}
func rate(value *float64, fallback float64) float64 {
	if value != nil {
		return *value
	}
	return fallback
}
func estimate(t tokens, p *ModelPrice) float64 {
	cached := min(t.CachedInput, t.Input)
	write := min(t.CacheWrite, t.Input-cached)
	regular := t.Input - cached - write
	in := p.Input
	cache := p.CachedInput
	wr := p.CacheWrite
	out := p.Output
	if t.Input > 272000 && p.LongInput != nil {
		in = *p.LongInput
		if p.LongCachedInput != nil {
			cache = p.LongCachedInput
		}
		if p.LongCacheWrite != nil {
			wr = p.LongCacheWrite
		}
		out = rate(p.LongOutput, out)
	}
	return (float64(regular)*in + float64(cached)*rate(cache, in) + float64(write)*rate(wr, in) + float64(t.Output)*out) / 1e6
}
