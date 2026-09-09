package usage

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type tokens struct {
	Input           uint64 `json:"input_tokens"`
	CachedInput     uint64 `json:"cached_input_tokens"`
	CacheWrite      uint64 `json:"cache_write_input_tokens"`
	Output          uint64 `json:"output_tokens"`
	ReasoningOutput uint64 `json:"reasoning_output_tokens"`
}

func add(a, b uint64) uint64 {
	if math.MaxUint64-a < b {
		return math.MaxUint64
	}
	return a + b
}
func sub(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}
func (t tokens) total() uint64 { return add(t.Input, t.Output) }
func (t tokens) empty() bool   { return t.Input == 0 && t.Output == 0 }
func (t tokens) delta(p tokens) tokens {
	return tokens{sub(t.Input, p.Input), sub(t.CachedInput, p.CachedInput), sub(t.CacheWrite, p.CacheWrite), sub(t.Output, p.Output), sub(t.ReasoningOutput, p.ReasoningOutput)}
}
func (t *tokens) accumulate(v tokens) {
	t.Input = add(t.Input, v.Input)
	t.CachedInput = add(t.CachedInput, v.CachedInput)
	t.CacheWrite = add(t.CacheWrite, v.CacheWrite)
	t.Output = add(t.Output, v.Output)
	t.ReasoningOutput = add(t.ReasoningOutput, v.ReasoningOutput)
}

// Message text has no destination in this schema and is never retained.
type rolloutEvent struct {
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Payload   struct {
		Type   string `json:"type"`
		TurnID string `json:"turn_id"`
		Model  string `json:"model"`
		Info   *struct {
			Total *tokens `json:"total_token_usage"`
			Last  *tokens `json:"last_token_usage"`
		} `json:"info"`
		DurationMS *uint64 `json:"duration_ms"`
		TTFT       *uint64 `json:"time_to_first_token_ms"`
	} `json:"payload"`
}
type accumulator struct {
	usage                                                                            tokens
	calls, turns, turnTokens, durationSamples, durationTotal, ttftSamples, ttftTotal uint64
	cost                                                                             float64
	priced                                                                           bool
}
type turn struct {
	model string
	usage tokens
}

func average(total, count uint64) *float64 {
	if count == 0 {
		return nil
	}
	return fptr(float64(total) / float64(count))
}

func (c *Client) UsageStats(ctx context.Context, priceDir, sessionsDir string, days uint32, refresh bool) (UsageStats, error) {
	return aggregate(ctx, sessionsDir, days, c.loadPrices(ctx, priceDir, refresh), c.Now())
}
func aggregate(ctx context.Context, dir string, days uint32, prices catalog, now time.Time) (UsageStats, error) {
	models := map[string]*accumulator{}
	daily := map[string]*accumulator{}
	var sessions uint64
	get := func(m map[string]*accumulator, key string) *accumulator {
		a := m[key]
		if a == nil {
			a = &accumulator{}
			m[key] = a
		}
		return a
	}
	cutoff := now.Add(-time.Duration(days) * 24 * time.Hour)
	validTime := func(raw string) (time.Time, bool) {
		t, e := time.Parse(time.RFC3339Nano, raw)
		return t, e == nil && (days == 0 || !t.Before(cutoff))
	}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			if path == dir && !errors.Is(walkErr, os.ErrNotExist) {
				return walkErr
			}
			return nil
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return nil
		}
		defer f.Close()
		reader := bufio.NewReader(f)
		current := "unknown"
		active := ""
		turns := map[string]*turn{}
		var previous tokens
		counted := false
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			// ReadBytes accepts long JSONL lines; Scanner's default 64 KiB limit
			// would silently truncate normal Codex sessions containing tool output.
			line, readErr := reader.ReadBytes('\n')
			if len(line) == 0 && readErr != nil {
				break
			}
			var event rolloutEvent
			if json.Unmarshal(line, &event) != nil {
				if readErr != nil {
					break
				}
				continue
			}
			p := event.Payload
			switch {
			case event.Type == "turn_context":
				if strings.TrimSpace(p.Model) != "" {
					current = p.Model
				}
				if p.TurnID != "" {
					active = p.TurnID
					if turns[active] == nil {
						turns[active] = &turn{}
					}
					turns[active].model = current
				}
			case event.Type == "event_msg" && p.Type == "task_started":
				if p.TurnID != "" {
					active = p.TurnID
					if turns[active] == nil {
						turns[active] = &turn{}
					}
				}
			case event.Type == "event_msg" && p.Type == "token_count":
				if p.Info == nil {
					continue
				}
				var usage tokens
				if p.Info.Last != nil {
					usage = *p.Info.Last
				} else if p.Info.Total != nil {
					usage = p.Info.Total.delta(previous)
				}
				if p.Info.Total != nil {
					previous = *p.Info.Total
				}
				if usage.empty() {
					continue
				}
				timestamp, valid := validTime(event.Timestamp)
				if !valid {
					continue
				}
				if t := turns[active]; t != nil {
					if t.model == "" {
						t.model = current
					}
					t.usage.accumulate(usage)
				}
				if !counted {
					sessions = add(sessions, 1)
					counted = true
				}
				price := findPrice(current, prices.Prices)
				cost := 0.0
				if price != nil {
					cost = estimate(usage, price)
				}
				m := get(models, current)
				m.usage.accumulate(usage)
				m.calls = add(m.calls, 1)
				m.cost += cost
				m.priced = m.priced || price != nil
				d := get(daily, timestamp.In(now.Location()).Format("2006-01-02"))
				d.usage.accumulate(usage)
				d.cost += cost
			case event.Type == "event_msg" && p.Type == "task_complete":
				if p.TurnID == "" {
					continue
				}
				if active == p.TurnID {
					active = ""
				}
				t := turns[p.TurnID]
				delete(turns, p.TurnID)
				if t == nil || t.usage.empty() {
					continue
				}
				if _, valid := validTime(event.Timestamp); !valid {
					continue
				}
				name := t.model
				if strings.TrimSpace(name) == "" {
					name = current
				}
				m := get(models, name)
				m.turns = add(m.turns, 1)
				m.turnTokens = add(m.turnTokens, t.usage.total())
				if p.DurationMS != nil {
					m.durationSamples = add(m.durationSamples, 1)
					m.durationTotal = add(m.durationTotal, *p.DurationMS)
				}
				if p.TTFT != nil {
					m.ttftSamples = add(m.ttftSamples, 1)
					m.ttftTotal = add(m.ttftTotal, *p.TTFT)
				}
			case event.Type == "event_msg" && p.Type == "turn_aborted":
				delete(turns, p.TurnID)
				if active == p.TurnID {
					active = ""
				}
			}
			if readErr != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				break
			}
		}
		return nil
	})
	if err != nil {
		return UsageStats{}, err
	}
	r := UsageStats{GeneratedAt: now.UTC().Format(time.RFC3339Nano), RangeDays: days, SessionsDir: dir, PricingSource: prices.Source, Models: []ModelUsageItem{}, Daily: []DailyUsageItem{}}
	var total tokens
	for name, m := range models {
		total.accumulate(m.usage)
		r.Summary.ModelCalls = add(r.Summary.ModelCalls, m.calls)
		r.Summary.EstimatedCostUSD += m.cost
		item := ModelUsageItem{Model: name, InputTokens: m.usage.Input, CachedInputTokens: m.usage.CachedInput, CacheWriteInputTokens: m.usage.CacheWrite, OutputTokens: m.usage.Output, ReasoningOutputTokens: m.usage.ReasoningOutput, TotalTokens: m.usage.total(), ModelCalls: m.calls, TurnCount: m.turns, AverageTokensPerTurn: average(m.turnTokens, m.turns), AverageDurationMS: average(m.durationTotal, m.durationSamples), AverageTimeToFirstTokenMS: average(m.ttftTotal, m.ttftSamples)}
		if m.priced {
			item.EstimatedCostUSD = fptr(m.cost)
		} else {
			r.Summary.UnpricedModelCount++
		}
		if p := findPrice(name, prices.Prices); p != nil {
			item.Price = &DisplayedPrice{Input: p.Input, CachedInput: p.CachedInput, CacheWrite: p.CacheWrite, Output: p.Output}
		}
		r.Models = append(r.Models, item)
	}
	r.Summary.InputTokens = total.Input
	r.Summary.CachedInputTokens = total.CachedInput
	r.Summary.CacheWriteInputTokens = total.CacheWrite
	r.Summary.OutputTokens = total.Output
	r.Summary.ReasoningOutputTokens = total.ReasoningOutput
	r.Summary.TotalTokens = total.total()
	r.Summary.Sessions = sessions
	sort.Slice(r.Models, func(i, j int) bool {
		if r.Models[i].TotalTokens == r.Models[j].TotalTokens {
			return r.Models[i].Model < r.Models[j].Model
		}
		return r.Models[i].TotalTokens > r.Models[j].TotalTokens
	})
	for date, d := range daily {
		r.Daily = append(r.Daily, DailyUsageItem{Date: date, TotalTokens: d.usage.total(), EstimatedCostUSD: d.cost})
	}
	sort.Slice(r.Daily, func(i, j int) bool { return r.Daily[i].Date < r.Daily[j].Date })
	return r, nil
}
