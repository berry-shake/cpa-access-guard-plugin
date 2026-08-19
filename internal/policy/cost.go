package policy

import (
	"encoding/json"
	"strings"
)

// TokenUsage is the prompt/completion token counts extracted from a response body.
// A zero value means no usage could be parsed (e.g. streaming response with no body).
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
	Found            bool
}

// isCacheAdditiveProvider reports whether the provider reports cache-hit input
// tokens SEPARATELY from InputTokens (so InputTokens excludes cache reads and
// the two must be summed). Anthropic/Claude is the additive case: CPA parses
// input_tokens, cache_read_input_tokens, cache_creation_input_tokens as
// independent fields and sets TotalTokens = Input + Output + CacheRead +
// CacheCreation (usage_helpers.parseClaudeUsageNode). All other providers CPA
// supports (OpenAI, Gemini, Codex, ...) report cache hits as a SUBSET already
// counted inside InputTokens (prompt_tokens_details.cached_tokens,
// cachedContentTokenCount) — the subset must be split out and repriced rather
// than added.
func isCacheAdditiveProvider(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "anthropic", "vertex-claude":
		return true
	default:
		return false
	}
}

// UsageDetail is the token breakdown delivered by the host's usage.handle call,
// already parsed from the upstream response (including the final usage frame of
// a stream). Only the fields we bill on are tracked here.
type UsageDetail struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
}

// ParseTokenUsage inspects a response body and extracts prompt/completion token
// counts. It supports the three usage shapes seen across CPA's providers:
//   - OpenAI / OpenAI-compatible: { "usage": { "prompt_tokens", "completion_tokens" } }
//   - Anthropic: { "usage": { "input_tokens", "output_tokens" } }
//   - Gemini:    { "usage_metadata": { "promptTokenCount", "candidatesTokenCount" } }
//
// For streaming responses the body is a Server-Sent-Events stream; the final
// frame usually carries cumulative usage. We split the SSE `data:` lines and
// take the max prompt/completion seen across frames (streaming usage is
// cumulative, so the max equals the final total — e.g. Anthropic emits
// input_tokens in message_start and output_tokens in a later message_delta).
//
// On any parse failure (empty body, garbage, SSE with no usage frame) it
// returns a zero-value TokenUsage (Found=false) so the caller skips billing
// rather than crashing a request path.
func ParseTokenUsage(body []byte) TokenUsage {
	if len(body) == 0 {
		return TokenUsage{}
	}
	// Fast path: a single JSON object (non-streaming response).
	if json.Valid(body) {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			if u := usageFromObject(payload); u.Found {
				return u
			}
		}
		return TokenUsage{}
	}
	// Streaming path: SSE text. Scan `data:` lines and accumulate usage.
	return parseTokenUsageFromSSE(body)
}

// usageFromObject extracts usage from a single decoded JSON object. It checks
// the top-level "usage" / "usage_metadata" keys, and also recurses one level
// into nested objects (Anthropic's streaming message_start frame nests usage
// under {"message":{"usage":{...}}}).
func usageFromObject(payload map[string]any) TokenUsage {
	if u := usageAtKeys(payload); u.Found {
		return u
	}
	for _, v := range payload {
		if nested, ok := v.(map[string]any); ok {
			if u := usageAtKeys(nested); u.Found {
				return u
			}
		}
	}
	return TokenUsage{}
}

// usageAtKeys checks the known usage-carrying keys on one object level.
func usageAtKeys(payload map[string]any) TokenUsage {
	if usage, ok := payload["usage"].(map[string]any); ok {
		if u := fromOpenAIUsage(usage); u.Found {
			return u
		}
		if u := fromAnthropicUsage(usage); u.Found {
			return u
		}
	}
	if usage, ok := payload["usage_metadata"].(map[string]any); ok {
		if u := fromGeminiUsage(usage); u.Found {
			return u
		}
	}
	return TokenUsage{}
}

// parseTokenUsageFromSSE walks each `data: <json>` line of an SSE stream and
// returns the cumulative token counts. Across frames we track the maximum
// prompt and completion tokens seen, because providers report usage
// cumulatively (the highest value is the final total). Returns Found=false if
// no frame carries usage.
func parseTokenUsageFromSSE(body []byte) TokenUsage {
	var maxPrompt, maxCompletion int
	found := false
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if !json.Valid([]byte(payload)) {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(payload), &obj); err != nil {
			continue
		}
		if u := usageFromObject(obj); u.Found {
			found = true
			if u.PromptTokens > maxPrompt {
				maxPrompt = u.PromptTokens
			}
			if u.CompletionTokens > maxCompletion {
				maxCompletion = u.CompletionTokens
			}
		}
	}
	if !found {
		return TokenUsage{}
	}
	return TokenUsage{PromptTokens: maxPrompt, CompletionTokens: maxCompletion, Found: true}
}

func fromOpenAIUsage(usage map[string]any) TokenUsage {
	p := toInt(usage["prompt_tokens"])
	c := toInt(usage["completion_tokens"])
	if p == 0 && c == 0 {
		return TokenUsage{}
	}
	return TokenUsage{PromptTokens: p, CompletionTokens: c, Found: true}
}

func fromAnthropicUsage(usage map[string]any) TokenUsage {
	p := toInt(usage["input_tokens"])
	c := toInt(usage["output_tokens"])
	if p == 0 && c == 0 {
		return TokenUsage{}
	}
	return TokenUsage{PromptTokens: p, CompletionTokens: c, Found: true}
}

func fromGeminiUsage(usage map[string]any) TokenUsage {
	p := toInt(usage["promptTokenCount"])
	c := toInt(usage["candidatesTokenCount"])
	if p == 0 && c == 0 {
		return TokenUsage{}
	}
	return TokenUsage{PromptTokens: p, CompletionTokens: c, Found: true}
}

func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case string:
		trimmed := strings.TrimSpace(n)
		if trimmed == "" {
			return 0
		}
		var i int
		for _, r := range trimmed {
			if r < '0' || r > '9' {
				return 0
			}
			i = i*10 + int(r-'0')
		}
		return i
	}
	return 0
}

// PriceSheet bundles the per-million prices used to bill one record.
type PriceSheet struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Priced     bool
}

// PriceForAlias looks up the configured per-million-token prices for an alias
// on this key. Returns ok=false when the alias has no rule (unknown alias) —
// callers treat unknown aliases as zero-cost (billed at 0, not blocked).
func (k *KeyConfig) PriceForAlias(alias string) (inputPerMillion, outputPerMillion, cacheReadPerMillion, cacheWritePerMillion float64, ok bool) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return 0, 0, 0, 0, false
	}
	for _, rule := range k.Models {
		if strings.EqualFold(rule.Alias, alias) {
			return rule.InputPricePerMillion, rule.OutputPricePerMillion, rule.CacheReadPricePerMillion, rule.CacheWritePricePerMillion, true
		}
	}
	return 0, 0, 0, 0, false
}

// ComputeCost converts token usage into a dollar amount using the alias's prices.
// Prices are USD per 1M tokens; cost = (tokens / 1_000_000) * price.
// Unknown alias (ok=false) → 0 cost (unpriced requests are not billed).
// This path does not see cache breakdown (it bills from a parsed response body
// with only prompt/completion counts); cache-aware billing lives in
// ComputeCacheCost, used by the usage.handle path that carries full token detail.
func ComputeCost(inputPerMillion, outputPerMillion float64, priced bool, usage TokenUsage) float64 {
	if !priced || !usage.Found {
		return 0
	}
	return float64(usage.PromptTokens)/1_000_000*inputPerMillion +
		float64(usage.CompletionTokens)/1_000_000*outputPerMillion
}

func ComputeCacheCost(provider string, sheet PriceSheet, detail UsageDetail) float64 {
	total, _, _, _, _ := ComputeCacheCostBreakdown(provider, sheet, detail)
	return total
}

// ComputeCacheCostBreakdown is the same biller as ComputeCacheCost but also
// returns the cache breakdown used for reporting:
//
//   - totalCost: the full dollar bill.
//   - cacheReadCost / cacheReadTokens: the dollar portion and token count of
//     cache-hit input tokens priced at the configured cache-read price
//     (reported as separably priced only when CacheWrite/Read price != 0).
//   - cacheWriteCost / cacheWriteTokens: the same for cache-creation (write)
//     tokens priced at the configured cache-write price.
//
// Pricing semantics:
//   - CacheRead == 0: cache hits fall back to the regular input price.
//   - CacheWrite == 0: cache writes fall back to the regular input price
//     (legacy behavior before write pricing existed).
//   - Additive providers (Anthropic): cache reads AND writes are outside
//     InputTokens; each is billed at its own price, Input at the input price.
//   - Subset providers (OpenAI/Gemini/Codex): cache hits are inside
//     InputTokens; peel off and reprice. Writes follow the additive rule.
//   - !Priced or no tokens: everything 0.
func ComputeCacheCostBreakdown(provider string, sheet PriceSheet, detail UsageDetail) (totalCost, cacheReadCost float64, cacheReadTokens int64, cacheWriteCost float64, cacheWriteTokens int64) {
	if !sheet.Priced {
		return 0, 0, 0, 0, 0
	}
	input := detail.InputTokens
	output := detail.OutputTokens
	if input == 0 && output == 0 {
		return 0, 0, 0, 0, 0
	}
	cacheRead := detail.CacheReadTokens
	if cacheRead == 0 {
		// OpenAI/Gemini/Codex report cache hits as CachedTokens (a subset of
		// input) without a separate CacheRead field; Claude sets CachedTokens =
		// CacheReadTokens. Either way CachedTokens is the cache-hit count.
		cacheRead = detail.CachedTokens
	}
	cacheReadPrice := sheet.CacheRead
	if cacheReadPrice == 0 {
		cacheReadPrice = sheet.Input
	}
	cacheWritePrice := sheet.CacheWrite
	if cacheWritePrice == 0 {
		cacheWritePrice = sheet.Input
	}

	cacheWrite := detail.CacheCreationTokens
	var inputTokensToBill int64
	if isCacheAdditiveProvider(provider) {
		// Cache hits and writes are NOT in input: bill each at its own price.
		inputTokensToBill = input
	} else {
		// Cache hits ARE a subset of input; peel them off and reprice.
		if cacheRead > input {
			cacheRead = input // defensive: clamp to what's reported
		}
		inputTokensToBill = input - cacheRead
	}

	cacheReadTokens = cacheRead
	cacheWriteTokens = cacheWrite
	billRead := float64(cacheRead) / 1_000_000 * cacheReadPrice
	billWrite := float64(cacheWrite) / 1_000_000 * cacheWritePrice
	// Separably-priced reporting only when an explicit cache price was set;
	// otherwise hits/writes were folded into the input-price bill and reporting
	// them would overstate savings in the dashboards. The bill itself always
	// includes the fallback-priced portion.
	if sheet.CacheRead != 0 {
		cacheReadCost = billRead
	}
	if sheet.CacheWrite != 0 {
		cacheWriteCost = billWrite
	}
	total := float64(inputTokensToBill)/1_000_000*sheet.Input +
		billRead + billWrite +
		float64(output)/1_000_000*sheet.Output
	return total, cacheReadCost, cacheReadTokens, cacheWriteCost, cacheWriteTokens
}
