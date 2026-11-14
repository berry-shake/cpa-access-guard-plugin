package policy

import (
	"strings"
)

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

// PriceSheet bundles the per-million prices used to bill one record.
type PriceSheet struct {
	Input      float64
	Output     float64
	CacheRead  float64
	CacheWrite float64
	Priced     bool
}

// ComputeCacheCost returns only the total of ComputeCacheCostBreakdown.
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
