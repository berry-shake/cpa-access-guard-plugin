package policy

import (
	"testing"
)

func nearly(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// TestComputeCacheCostSubsetProvider: OpenAI-style — cache hits are a SUBSET of
// InputTokens. 1M input total, 200K of which are cached. Input $3/M, output
// $15/M, cache-read $0.30/M. Expected: (1M-200K)*3 + 200K*0.30 + 0.5M*15
// = 800K*3/1M + 200K*0.3/1M + 500K*15/1M = 2.4 + 0.06 + 7.5 = 9.96.
// Verify the cached subset is NOT double-billed at the input price.
func TestComputeCacheCostSubsetProvider(t *testing.T) {
	detail := UsageDetail{
		InputTokens: 1_000_000, OutputTokens: 500_000,
		CachedTokens: 200_000, // subset of input
	}
	got := ComputeCacheCost("openai", PriceSheet{Input: 3, Output: 15, CacheRead: 0.30, Priced: true}, detail)
	if !nearly(got, 9.96) {
		t.Fatalf("subset cache cost = %v, want 9.96", got)
	}
}

// TestComputeCacheCostAdditiveProvider: Anthropic — cache reads are OUTSIDE
// InputTokens. 800K regular input + 200K cache-read, 500K output. Cache-creation
// tokens (writes) billed at input price. Input $3/M, output $15/M, cache-read
// $0.30/M, plus 100K cache-creation. Expected: 800K*3 + 200K*0.30 + 100K*3 +
// 500K*15 = (900K*3 + 200K*0.3 + 500K*15)/1M = 2.7 + 0.06 + 7.5 = 10.26.
func TestComputeCacheCostAdditiveProvider(t *testing.T) {
	detail := UsageDetail{
		InputTokens:         800_000,
		OutputTokens:        500_000,
		CacheReadTokens:     200_000,
		CacheCreationTokens: 100_000,
	}
	got := ComputeCacheCost("claude", PriceSheet{Input: 3, Output: 15, CacheRead: 0.30, Priced: true}, detail)
	if !nearly(got, 10.26) {
		t.Fatalf("additive cache cost = %v, want 10.26", got)
	}
}

// TestComputeCacheCostNoCachePriceFallsBackToInput: with cacheReadPerMillion=0,
// cache hits fall back to the input price. Subset provider: input already
// includes cache, so total == plain input+output cost (no double count).
// 1M input (incl 200K cached) + 500K output @ $3/$15 → 3 + 7.5 = 10.5.
func TestComputeCacheCostNoCachePriceFallsBackToInput(t *testing.T) {
	detail := UsageDetail{InputTokens: 1_000_000, OutputTokens: 500_000, CachedTokens: 200_000}
	got := ComputeCacheCost("openai", PriceSheet{Input: 3, Output: 15, Priced: true}, detail)
	if !nearly(got, 10.5) {
		t.Fatalf("fallback cost = %v, want 10.5", got)
	}
	// Additive provider: cache reads are outside input, so the fallback total
	// must add them at the input price (matching pre-cache-pricing behavior).
	// 800K input + 200K cacheRead + 500K output @ $3/$15 → 1M*3 + 0.5M*15 = 10.5.
	detail2 := UsageDetail{InputTokens: 800_000, OutputTokens: 500_000, CacheReadTokens: 200_000}
	got2 := ComputeCacheCost("claude", PriceSheet{Input: 3, Output: 15, Priced: true}, detail2)
	if !nearly(got2, 10.5) {
		t.Fatalf("additive fallback cost = %v, want 10.5", got2)
	}
}

// TestComputeCacheCostUnpricedZero: unknown alias (priced=false) → 0 even with
// tokens and cache configured.
func TestComputeCacheCostUnpricedZero(t *testing.T) {
	detail := UsageDetail{InputTokens: 1_000_000, OutputTokens: 1_000_000, CachedTokens: 500_000}
	if c := ComputeCacheCost("openai", PriceSheet{Input: 3, Output: 15, CacheRead: 0.3}, detail); c != 0 {
		t.Fatalf("unpriced cost = %v, want 0", c)
	}
}

// TestComputeCacheCostBreakdown verifies the cache-spend / cache-hit breakdown
// returned alongside the total, used by the ledger for hit-rate + cache spend.
// Subset provider, explicit cache price: 1M input (incl 200K cached) + 500K
// output @ $3/$15/$0.30. Total 9.96 (as above). cacheCost = 200K×0.30/1M = 0.06.
// cacheReadTokens = 200K. nonCache input billed at input price = 800K.
func TestComputeCacheCostBreakdown(t *testing.T) {
	detail := UsageDetail{InputTokens: 1_000_000, OutputTokens: 500_000, CachedTokens: 200_000}
	total, cacheCost, cacheRead, _, _ := ComputeCacheCostBreakdown("openai", PriceSheet{Input: 3, Output: 15, CacheRead: 0.30, Priced: true}, detail)
	if !nearly(total, 9.96) {
		t.Fatalf("total = %v, want 9.96", total)
	}
	if !nearly(cacheCost, 0.06) {
		t.Fatalf("cacheCost = %v, want 0.06", cacheCost)
	}
	if cacheRead != 200_000 {
		t.Fatalf("cacheRead = %d, want 200000", cacheRead)
	}
}

// TestComputeCacheCostBreakdownNoCachePrice: when no cache price is configured,
// cache hits fold into the input-price line. cacheCost must be 0 (not separably
// priced) even though cacheRead is still reported for hit-rate accounting.
func TestComputeCacheCostBreakdownNoCachePrice(t *testing.T) {
	detail := UsageDetail{InputTokens: 1_000_000, OutputTokens: 500_000, CachedTokens: 200_000}
	total, cacheCost, cacheRead, _, _ := ComputeCacheCostBreakdown("openai", PriceSheet{Input: 3, Output: 15, Priced: true}, detail)
	if !nearly(total, 10.5) {
		t.Fatalf("total = %v, want 10.5", total)
	}
	if cacheCost != 0 {
		t.Fatalf("cacheCost = %v, want 0 (no separable cache spend without a cache price)", cacheCost)
	}
	if cacheRead != 200_000 {
		t.Fatalf("cacheRead = %d, want 200000 (still reported for hit-rate)", cacheRead)
	}
}

// TestComputeCacheCostBreakdownAdditive: Claude — cache reads outside input.
// 800K input + 200K cacheRead + 100K cacheCreation + 500K output @ $3/$15/$0.30.
// Total 10.26 (as above). cacheCost = 200K×0.30/1M = 0.06. cacheRead = 200K.
// cacheCreation tokens are NOT counted as cacheRead (they are writes).
func TestComputeCacheCostBreakdownAdditive(t *testing.T) {
	detail := UsageDetail{
		InputTokens:         800_000,
		OutputTokens:        500_000,
		CacheReadTokens:     200_000,
		CacheCreationTokens: 100_000,
	}
	total, cacheCost, cacheRead, cacheWriteCost, cacheWriteTokens := ComputeCacheCostBreakdown("claude", PriceSheet{Input: 3, Output: 15, CacheRead: 0.30, Priced: true}, detail)
	if cacheWriteCost != 0 {
		t.Fatalf("cacheWriteCost = %v, want 0 (no write price: folded into input)", cacheWriteCost)
	}
	if cacheWriteTokens != 100_000 {
		t.Fatalf("cacheWriteTokens = %d, want 100000 (reported regardless of price)", cacheWriteTokens)
	}
	if !nearly(total, 10.26) {
		t.Fatalf("total = %v, want 10.26", total)
	}
	if !nearly(cacheCost, 0.06) {
		t.Fatalf("cacheCost = %v, want 0.06", cacheCost)
	}
	if cacheRead != 200_000 {
		t.Fatalf("cacheRead = %d, want 200000 (creation excluded)", cacheRead)
	}
}

// TestComputeCacheCostWritePrice: cache-creation tokens billed at the
// dedicated write price (e.g. Anthropic 1.25x input) instead of the input
// price. Additive provider, no write price → legacy input-price fallback.
func TestComputeCacheCostWritePrice(t *testing.T) {
	detail := UsageDetail{
		InputTokens:         800_000,
		OutputTokens:        0,
		CacheReadTokens:     0,
		CacheCreationTokens: 1_000_000,
	}
	// Write price 3.75 (1.25x input of 3): 800K input @3 + 1M writes @3.75 = 6.15.
	got := ComputeCacheCost("claude", PriceSheet{Input: 3, Output: 15, CacheWrite: 3.75, Priced: true}, detail)
	if !nearly(got, 6.15) {
		t.Fatalf("write-priced cost = %v, want 6.15", got)
	}
	// Breakdown reports separable write spend + tokens.
	total, _, _, wCost, wTokens := ComputeCacheCostBreakdown("claude", PriceSheet{Input: 3, Output: 15, CacheWrite: 3.75, Priced: true}, detail)
	if !nearly(total, 6.15) || !nearly(wCost, 3.75) || wTokens != 1_000_000 {
		t.Fatalf("write breakdown = %v/%v/%d, want 6.15/3.75/1000000", total, wCost, wTokens)
	}
	// No write price configured: writes fall back to the input price: 2.4 + 3.
	got = ComputeCacheCost("claude", PriceSheet{Input: 3, Output: 15, Priced: true}, detail)
	if !nearly(got, 5.4) {
		t.Fatalf("fallback write cost = %v, want 5.4", got)
	}
	// Write price also works for subset providers (writes are always additive).
	got = ComputeCacheCost("openai", PriceSheet{Input: 3, Output: 15, CacheWrite: 3.75, Priced: true}, detail)
	if !nearly(got, 6.15) {
		t.Fatalf("subset write cost = %v, want 6.15", got)
	}
}
