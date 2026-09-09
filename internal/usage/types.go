package usage

type ModelPrice struct {
	Model           string   `json:"model"`
	Input           float64  `json:"input"`
	CachedInput     *float64 `json:"cachedInput"`
	CacheWrite      *float64 `json:"cacheWrite"`
	Output          float64  `json:"output"`
	LongInput       *float64 `json:"longInput"`
	LongCachedInput *float64 `json:"longCachedInput"`
	LongCacheWrite  *float64 `json:"longCacheWrite"`
	LongOutput      *float64 `json:"longOutput"`
}

type CachedPriceCatalog struct {
	SourceURL string       `json:"sourceUrl"`
	FetchedAt string       `json:"fetchedAt"`
	Prices    []ModelPrice `json:"prices"`
}

type PricingSource struct {
	URL        string  `json:"url"`
	FetchedAt  string  `json:"fetchedAt"`
	Kind       string  `json:"kind"`
	ModelCount int     `json:"modelCount"`
	Warning    *string `json:"warning"`
}

type UsageSummary struct {
	InputTokens           uint64  `json:"inputTokens"`
	CachedInputTokens     uint64  `json:"cachedInputTokens"`
	CacheWriteInputTokens uint64  `json:"cacheWriteInputTokens"`
	OutputTokens          uint64  `json:"outputTokens"`
	ReasoningOutputTokens uint64  `json:"reasoningOutputTokens"`
	TotalTokens           uint64  `json:"totalTokens"`
	Sessions              uint64  `json:"sessions"`
	ModelCalls            uint64  `json:"modelCalls"`
	EstimatedCostUSD      float64 `json:"estimatedCostUsd"`
	UnpricedModelCount    int     `json:"unpricedModelCount"`
}

type ModelUsageItem struct {
	Model                     string          `json:"model"`
	InputTokens               uint64          `json:"inputTokens"`
	CachedInputTokens         uint64          `json:"cachedInputTokens"`
	CacheWriteInputTokens     uint64          `json:"cacheWriteInputTokens"`
	OutputTokens              uint64          `json:"outputTokens"`
	ReasoningOutputTokens     uint64          `json:"reasoningOutputTokens"`
	TotalTokens               uint64          `json:"totalTokens"`
	ModelCalls                uint64          `json:"modelCalls"`
	TurnCount                 uint64          `json:"turnCount"`
	AverageTokensPerTurn      *float64        `json:"averageTokensPerTurn"`
	AverageDurationMS         *float64        `json:"averageDurationMs"`
	AverageTimeToFirstTokenMS *float64        `json:"averageTimeToFirstTokenMs"`
	EstimatedCostUSD          *float64        `json:"estimatedCostUsd"`
	Price                     *DisplayedPrice `json:"price"`
}

type DisplayedPrice struct {
	Input       float64  `json:"input"`
	CachedInput *float64 `json:"cachedInput"`
	CacheWrite  *float64 `json:"cacheWrite"`
	Output      float64  `json:"output"`
}

type DailyUsageItem struct {
	Date             string  `json:"date"`
	TotalTokens      uint64  `json:"totalTokens"`
	EstimatedCostUSD float64 `json:"estimatedCostUsd"`
}

type UsageStats struct {
	GeneratedAt   string           `json:"generatedAt"`
	RangeDays     uint32           `json:"rangeDays"`
	SessionsDir   string           `json:"sessionsDir"`
	PricingSource PricingSource    `json:"pricingSource"`
	Summary       UsageSummary     `json:"summary"`
	Models        []ModelUsageItem `json:"models"`
	Daily         []DailyUsageItem `json:"daily"`
}

type RateLimitWindow struct {
	UsedPercent       float64 `json:"usedPercent"`
	WindowMinutes     *int64  `json:"windowMinutes"`
	ResetsAt          *int64  `json:"resetsAt"`
	ResetAfterSeconds *int64  `json:"resetAfterSeconds"`
}

type CreditsInfo struct {
	HasCredits bool    `json:"hasCredits"`
	Unlimited  bool    `json:"unlimited"`
	Balance    *string `json:"balance"`
}

type AccountQuota struct {
	AccountID   string           `json:"accountId"`
	AccountName string           `json:"accountName"`
	Ok          bool             `json:"ok"`
	PlanType    *string          `json:"planType"`
	Primary     *RateLimitWindow `json:"primary"`
	Secondary   *RateLimitWindow `json:"secondary"`
	Tertiary    *RateLimitWindow `json:"tertiary"`
	FiveHour    *RateLimitWindow `json:"fiveHour"`
	Weekly      *RateLimitWindow `json:"weekly"`
	Monthly     *RateLimitWindow `json:"monthly"`
	Credits     *CreditsInfo     `json:"credits"`
	FetchedAt   *string          `json:"fetchedAt"`
	Error       *string          `json:"error"`
}

type AccountQuotas struct {
	SourceURL string         `json:"sourceUrl"`
	Accounts  []AccountQuota `json:"accounts"`
}
