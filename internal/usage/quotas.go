package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"switch-codex/internal/store"
	"time"
)

func (c *Client) AccountQuotas(ctx context.Context, state store.AccountsState) AccountQuotas {
	result := AccountQuotas{SourceURL: QuotaURL, Accounts: make([]AccountQuota, 0, len(state.Accounts))}
	for i, a := range state.Accounts {
		if i > 0 {
			timer := time.NewTimer(c.QuotaInterval)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
		result.Accounts = append(result.Accounts, c.accountQuota(ctx, state, a))
	}
	return result
}
func (c *Client) AccountQuota(ctx context.Context, state store.AccountsState, id string) (AccountQuotas, error) {
	for _, a := range state.Accounts {
		if a.ID == id {
			return AccountQuotas{SourceURL: QuotaURL, Accounts: []AccountQuota{c.accountQuota(ctx, state, a)}}, nil
		}
	}
	return AccountQuotas{}, errors.New("账号不存在")
}
func quotaError(a store.AccountItem, message string) AccountQuota {
	return AccountQuota{AccountID: a.ID, AccountName: a.Name, Error: strptr(message)}
}
func (c *Client) accountQuota(ctx context.Context, state store.AccountsState, a store.AccountItem) AccountQuota {
	path := a.AuthPath
	if a.IsActive {
		path = state.TargetAuthPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return quotaError(a, "无法读取该账号的 auth.json")
	}
	var auth struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return quotaError(a, "该账号的 auth.json 不是合法 JSON")
	}
	if strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		if strings.TrimSpace(auth.APIKey) != "" {
			return quotaError(a, "API Key 账号不提供 ChatGPT Codex 订阅配额")
		}
		return quotaError(a, "auth.json 中没有可用的 Codex access token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.QuotaEndpoint, nil)
	if err != nil {
		return quotaError(a, "无法创建配额请求")
	}
	req.Header.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36")
	if strings.TrimSpace(auth.Tokens.AccountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.Tokens.AccountID)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return quotaError(a, "网络请求超时，请检查代理连接是否正常")
		}
		return quotaError(a, "网络连接失败，请检查网络代理（建议开启全局代理或 TUN 模式）")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("OpenAI 用量接口返回 HTTP %d", resp.StatusCode)
		if resp.StatusCode == 401 {
			message = "登录凭据已过期，请先切换到该账号并让 Codex 完成登录刷新"
		}
		if resp.StatusCode == 403 {
			message = "当前账号无权读取 Codex 配额"
		}
		return quotaError(a, message)
	}
	var payload struct {
		PlanType  *string                    `json:"plan_type"`
		RateLimit map[string]json.RawMessage `json:"rate_limit"`
		Credits   *struct {
			HasCredits bool            `json:"has_credits"`
			Unlimited  bool            `json:"unlimited"`
			Balance    json.RawMessage `json:"balance"`
		} `json:"credits"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload) != nil {
		return quotaError(a, "OpenAI 用量接口返回了无法识别的数据")
	}
	r := AccountQuota{AccountID: a.ID, AccountName: a.Name, Ok: true, PlanType: payload.PlanType, FetchedAt: strptr(c.Now().UTC().Format(time.RFC3339Nano))}
	r.Primary = parseWindow(payload.RateLimit["primary_window"])
	r.Secondary = parseWindow(payload.RateLimit["secondary_window"])
	r.Tertiary = parseWindow(payload.RateLimit["tertiary_window"])
	classify := func(w *RateLimitWindow, seconds int64) {
		if w == nil {
			return
		}
		if w.WindowMinutes != nil {
			seconds = *w.WindowMinutes * 60
		}
		if seconds <= 86400 {
			if r.FiveHour == nil {
				r.FiveHour = w
			}
		} else if seconds <= 1209600 {
			if r.Weekly == nil {
				r.Weekly = w
			}
		} else if r.Monthly == nil {
			r.Monthly = w
		}
	}
	classify(r.Primary, 18000)
	classify(r.Secondary, 604800)
	classify(r.Tertiary, 2592000)
	keys := make([]string, 0, len(payload.RateLimit))
	for k := range payload.RateLimit {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k != "primary_window" && k != "secondary_window" && k != "tertiary_window" {
			classify(parseWindow(payload.RateLimit[k]), 0)
		}
	}
	if p := payload.Credits; p != nil {
		r.Credits = &CreditsInfo{HasCredits: p.HasCredits, Unlimited: p.Unlimited}
		var s string
		if json.Unmarshal(p.Balance, &s) == nil {
			r.Credits.Balance = &s
		} else if len(p.Balance) > 0 && string(p.Balance) != "null" {
			var n json.Number
			if json.Unmarshal(p.Balance, &n) == nil {
				r.Credits.Balance = strptr(n.String())
			}
		}
	}
	return r
}
func parseWindow(raw []byte) *RateLimitWindow {
	var w struct {
		UsedPercent        *float64 `json:"used_percent"`
		LimitWindowSeconds *int64   `json:"limit_window_seconds"`
		ResetAt            *int64   `json:"reset_at"`
		ResetAfterSeconds  *int64   `json:"reset_after_seconds"`
	}
	if json.Unmarshal(raw, &w) != nil || w.UsedPercent == nil {
		return nil
	}
	r := &RateLimitWindow{UsedPercent: *w.UsedPercent, ResetsAt: w.ResetAt, ResetAfterSeconds: w.ResetAfterSeconds}
	if w.LimitWindowSeconds != nil && *w.LimitWindowSeconds > 0 {
		v := (*w.LimitWindowSeconds + 59) / 60
		r.WindowMinutes = &v
	}
	return r
}
