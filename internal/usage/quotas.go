package usage

import (
	"bytes"
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

type accountAuth struct {
	AccessToken string
	AccountID   string
}

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

func readAccountAuth(state store.AccountsState, a store.AccountItem) (accountAuth, error) {
	// 当前账号可能已由 Codex 刷新凭据，查询时优先读取正在生效的 auth.json。
	path := a.AuthPath
	if a.IsActive {
		path = state.TargetAuthPath
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return accountAuth{}, errors.New("无法读取该账号的 auth.json")
	}
	var auth struct {
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil {
		return accountAuth{}, errors.New("该账号的 auth.json 不是合法 JSON")
	}
	if strings.TrimSpace(auth.Tokens.AccessToken) == "" {
		// API Key 不属于 ChatGPT 订阅账号，不能查询或重置 Codex 订阅额度。
		if strings.TrimSpace(auth.APIKey) != "" {
			return accountAuth{}, errors.New("API Key 账号不提供 ChatGPT Codex 订阅配额")
		}
		return accountAuth{}, errors.New("auth.json 中没有可用的 Codex access token")
	}
	return accountAuth{AccessToken: auth.Tokens.AccessToken, AccountID: auth.Tokens.AccountID}, nil
}

func newAccountRequest(ctx context.Context, method, endpoint string, auth accountAuth, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+auth.AccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.0.0 Safari/537.36")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(auth.AccountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", auth.AccountID)
	}
	return req, nil
}

func (c *Client) accountQuota(ctx context.Context, state store.AccountsState, a store.AccountItem) AccountQuota {
	// 读取账号自身凭据，确保多账号查询互不串号。
	auth, err := readAccountAuth(state, a)
	if err != nil {
		return quotaError(a, err.Error())
	}

	// 查询该账号的订阅限额和可用重置卡数量。
	req, err := newAccountRequest(ctx, http.MethodGet, c.QuotaEndpoint, auth, nil)
	if err != nil {
		return quotaError(a, "无法创建配额请求")
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
		PlanType     *string                    `json:"plan_type"`
		RateLimit    map[string]json.RawMessage `json:"rate_limit"`
		ResetCredits *struct {
			AvailableCount int64 `json:"available_count"`
		} `json:"rate_limit_reset_credits"`
		Credits *struct {
			HasCredits bool            `json:"has_credits"`
			Unlimited  bool            `json:"unlimited"`
			Balance    json.RawMessage `json:"balance"`
		} `json:"credits"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&payload) != nil {
		return quotaError(a, "OpenAI 用量接口返回了无法识别的数据")
	}

	// 将后端动态窗口归类为界面稳定展示的 5 小时、周和月限额。
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
	if p := payload.ResetCredits; p != nil {
		r.ResetCredits = &RateLimitResetCredits{AvailableCount: p.AvailableCount, Credits: []RateLimitResetCredit{}}
		if p.AvailableCount > 0 {
			// 只有存在可用卡时才补查详情，避免为没有卡的账号增加请求。
			details, err := c.listResetCredits(ctx, auth)
			if err != nil {
				r.ResetCredits.Error = strptr(err.Error())
			} else {
				r.ResetCredits.AvailableCount = details.AvailableCount
				r.ResetCredits.Credits = details.Credits
			}
		}
	}
	return r
}

func (c *Client) listResetCredits(ctx context.Context, auth accountAuth) (RateLimitResetCredits, error) {
	// 使用同一账号凭据查询卡片明细，以补齐每张卡的到期时间。
	req, err := newAccountRequest(ctx, http.MethodGet, c.ResetCreditsEndpoint, auth, nil)
	if err != nil {
		return RateLimitResetCredits{}, errors.New("无法创建重置卡查询请求")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return RateLimitResetCredits{}, errors.New("重置卡详情查询失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RateLimitResetCredits{}, fmt.Errorf("重置卡详情接口返回 HTTP %d", resp.StatusCode)
	}
	// 后端时间为 RFC3339 字符串，原样传给前端按本地时区展示。
	var result struct {
		Credits []struct {
			ID          string  `json:"id"`
			ResetType   string  `json:"reset_type"`
			Status      string  `json:"status"`
			GrantedAt   string  `json:"granted_at"`
			ExpiresAt   *string `json:"expires_at"`
			Title       *string `json:"title"`
			Description *string `json:"description"`
		} `json:"credits"`
		AvailableCount int64 `json:"available_count"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result) != nil {
		return RateLimitResetCredits{}, errors.New("重置卡详情接口返回了无法识别的数据")
	}
	credits := make([]RateLimitResetCredit, 0, len(result.Credits))
	for _, credit := range result.Credits {
		credits = append(credits, RateLimitResetCredit{ID: credit.ID, ResetType: credit.ResetType, Status: credit.Status, GrantedAt: credit.GrantedAt, ExpiresAt: credit.ExpiresAt, Title: credit.Title, Description: credit.Description})
	}
	return RateLimitResetCredits{AvailableCount: result.AvailableCount, Credits: credits}, nil
}

func (c *Client) ConsumeResetCredit(ctx context.Context, state store.AccountsState, accountID, creditID, redeemRequestID string) (ConsumeRateLimitResetCreditResult, error) {
	// 先锁定用户选择的账号，避免使用当前激活账号替代目标账号。
	var account *store.AccountItem
	for i := range state.Accounts {
		if state.Accounts[i].ID == accountID {
			account = &state.Accounts[i]
			break
		}
	}
	if account == nil {
		return ConsumeRateLimitResetCreditResult{}, errors.New("账号不存在")
	}
	if strings.TrimSpace(redeemRequestID) == "" {
		return ConsumeRateLimitResetCreditResult{}, errors.New("重置请求标识不能为空")
	}
	auth, err := readAccountAuth(state, *account)
	if err != nil {
		return ConsumeRateLimitResetCreditResult{}, err
	}

	// 使用固定幂等标识提交消费请求，网络失败后重试不会重复扣卡。
	payload := struct {
		RedeemRequestID string  `json:"redeem_request_id"`
		CreditID        *string `json:"credit_id,omitempty"`
	}{RedeemRequestID: redeemRequestID}
	if strings.TrimSpace(creditID) != "" {
		payload.CreditID = &creditID
	}
	body, _ := json.Marshal(payload)
	req, err := newAccountRequest(ctx, http.MethodPost, c.ResetCreditConsumeEndpoint, auth, bytes.NewReader(body))
	if err != nil {
		return ConsumeRateLimitResetCreditResult{}, errors.New("无法创建重置卡使用请求")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ConsumeRateLimitResetCreditResult{}, errors.New("使用重置卡失败，请重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ConsumeRateLimitResetCreditResult{}, fmt.Errorf("重置卡接口返回 HTTP %d", resp.StatusCode)
	}
	// 解析服务端最终结果，不在客户端猜测是否已扣卡或完成重置。
	var result struct {
		Code         string `json:"code"`
		WindowsReset int64  `json:"windows_reset"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result) != nil || result.Code == "" {
		return ConsumeRateLimitResetCreditResult{}, errors.New("重置卡接口返回了无法识别的数据")
	}
	switch result.Code {
	case "reset", "nothing_to_reset", "no_credit", "already_redeemed":
	default:
		return ConsumeRateLimitResetCreditResult{}, errors.New("重置卡接口返回了未知结果")
	}
	return ConsumeRateLimitResetCreditResult{Code: result.Code, WindowsReset: result.WindowsReset}, nil
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
