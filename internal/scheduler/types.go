// Package scheduler executes one isolated Codex batch per local day.
package scheduler

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const Model = "gpt-5.6-luna"
const PollInterval = time.Minute
const Grace = 5 * time.Minute
const AccountTimeout = 120 * time.Second
const MinAccountDelay = 60 * time.Second
const MaxAccountDelay = 300 * time.Second
const MaxOutputBytes = 1024 * 1024

func promptPool(date string) []string {
	return []string{
		"What model are you? Answer in one short sentence.",
		"计算 17 × 23，只回复结果。",
		"用一句话解释什么是缓存。",
		"写一句不超过 20 个字的鼓励语。",
		"给出一个两分钟内能完成的伸展建议。",
		"给出一个提高专注力的简单技巧。",
		"用三个短要点说明如何整理桌面。",
		"出一道简单脑筋急转弯，并立即给出答案。",
		"写一首关于清晨的三行小诗。",
		"推荐一个十分钟内能完成的休息活动。",
		"今天是 " + date + "。给出一份包含工作、休息和运动的三项通用今日安排。",
		"用一句话说明如何判断一条新闻是否可信。",
		"用一句话解释天气预报为什么会变化。",
	}
}

type Settings struct {
	Enabled      bool    `json:"enabled"`
	Time         *string `json:"time"`
	CLIPath      *string `json:"cliPath"`
	AutoSyncAuth bool    `json:"autoSyncAuth"`
}

func (s *Settings) UnmarshalJSON(raw []byte) error {
	var value struct {
		Enabled      bool    `json:"enabled"`
		Time         *string `json:"time"`
		CLIPath      *string `json:"cliPath"`
		AutoSyncAuth *bool   `json:"autoSyncAuth"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	s.Enabled, s.Time, s.CLIPath = value.Enabled, value.Time, value.CLIPath
	s.AutoSyncAuth = true
	if value.AutoSyncAuth != nil {
		s.AutoSyncAuth = *value.AutoSyncAuth
	}
	return nil
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
	ScheduledAt *string       `json:"scheduledAt"`
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
