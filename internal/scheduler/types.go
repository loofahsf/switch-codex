// Package scheduler executes one isolated, serial Codex batch per local day.
package scheduler

import (
	"errors"
	"strings"
	"time"
)

const Model = "gpt-5.6-luna"
const Prompt = "What model are you?"
const PollInterval = time.Minute
const Grace = 5 * time.Minute
const AccountTimeout = 120 * time.Second
const MaxOutputBytes = 1024 * 1024

type Settings struct {
	Enabled bool    `json:"enabled"`
	Time    *string `json:"time"`
	CLIPath *string `json:"cliPath"`
}

func ptr(s string) *string { return &s }
func (s *Settings) Validate() error {
	if s.Time != nil {
		v := *s.Time
		if len(v) == 5 {
			v += ":00"
		}
		parsed, err := time.Parse("15:04:05", v)
		if err != nil || parsed.Format("15:04:05") != v {
			return errors.New("执行时间必须为 HH:mm:ss，秒数为 00–59")
		}
		s.Time = &v
	}
	if s.Enabled && s.Time == nil {
		return errors.New("启用前请选择执行时间")
	}
	if s.CLIPath != nil {
		v := strings.TrimSpace(*s.CLIPath)
		s.CLIPath = nil
		if v != "" {
			s.CLIPath = &v
		}
	}
	return nil
}

type AccountStatus string

const (
	Waiting     AccountStatus = "waiting"
	Running     AccountStatus = "running"
	Success     AccountStatus = "success"
	Failed      AccountStatus = "failed"
	Interrupted AccountStatus = "interrupted"
)

type AccountResult struct {
	AccountID   string        `json:"accountId"`
	AccountName string        `json:"accountName"`
	Status      AccountStatus `json:"status"`
	StartedAt   *string       `json:"startedAt"`
	FinishedAt  *string       `json:"finishedAt"`
	Message     *string       `json:"message"`
	Prompt      *string       `json:"prompt"`
	// nil means a legacy record; an empty string means no completed response yet.
	Response *string `json:"response"`
}
type BatchResult struct {
	StartedAt  string          `json:"startedAt"`
	FinishedAt *string         `json:"finishedAt"`
	Accounts   []AccountResult `json:"accounts"`
}
type savedState struct {
	Settings    Settings     `json:"settings"`
	LastRunDate *string      `json:"lastRunDate"`
	LastRun     *BatchResult `json:"lastRun"`
}
type RunStatus struct {
	NextRunAt *string      `json:"nextRunAt"`
	Timezone  string       `json:"timezone"`
	Running   bool         `json:"running"`
	Error     *string      `json:"error"`
	LastRun   *BatchResult `json:"lastRun"`
}

func nextRun(s Settings, now time.Time, last *string) *time.Time {
	if !s.Enabled || s.Time == nil {
		return nil
	}
	v := *s.Time
	if len(v) == 5 {
		v += ":00"
	}
	clock, err := time.Parse("15:04:05", v)
	if err != nil {
		return nil
	}
	// Start with today: a newly saved future time must not be delayed until
	// tomorrow. Only a passed (or already claimed) time rolls forward.
	for daysAhead := 0; daysAhead < 370; daysAhead++ {
		date := now.AddDate(0, 0, daysAhead)
		if last != nil && date.Format("2006-01-02") <= *last {
			continue
		}
		wall := time.Date(date.Year(), date.Month(), date.Day(), clock.Hour(), clock.Minute(), clock.Second(), 0, time.UTC)
		// Try offsets on both sides of a DST transition, validating the wall time.
		// Missing times are skipped; repeated times always choose the first instant.
		var earliest *time.Time
		for _, hours := range []int{-48, -24, 0, 24, 48} {
			_, offset := wall.Add(time.Duration(hours) * time.Hour).In(now.Location()).Zone()
			candidate := wall.Add(-time.Duration(offset) * time.Second).In(now.Location())
			if candidate.Format("2006-01-02 15:04:05") != date.Format("2006-01-02")+" "+v {
				continue
			}
			if earliest == nil || candidate.Before(*earliest) {
				copy := candidate
				earliest = &copy
			}
		}
		if earliest != nil && earliest.After(now) {
			return earliest
		}
	}
	return nil
}
func interrupt(saved *savedState, now time.Time) {
	if b := saved.LastRun; b != nil && b.FinishedAt == nil {
		stamp := now.UTC().Format(time.RFC3339Nano)
		b.FinishedAt = &stamp
		for i := range b.Accounts {
			a := &b.Accounts[i]
			if a.Status == Waiting || a.Status == Running {
				a.Status = Interrupted
				a.FinishedAt = &stamp
				a.Message = ptr("应用退出，任务已中断，不自动补跑")
			}
		}
	}
}
