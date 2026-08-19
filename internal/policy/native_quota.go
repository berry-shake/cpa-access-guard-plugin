package policy

import (
	"strings"
)

// Native usage accounting and quota enforcement for CPA-native downstream
// API keys. The binding's caller_scope (a SHA-256 of the plaintext key) is
// the account identity; the ledger entry uses a reserved prefix so it can
// never collide with a downstream key ID.
//
// Billing input: the host's usage.handle record carries the plaintext native
// key in APIKey (the config-api-key provider sets Principal = key plaintext,
// and server_middleware copies Principal into "userApiKey"). Pricing comes
// from the global alias table; requests for unpriced models are recorded
// with 0 USD but still count calls.
//
// Enforcement: pickScheduler consults CheckNativeKeyQuota before any group
// filtering. RPM is decremented at that gate (concurrency-safe), mirroring
// the downstream key whose RPM is decremented at Authenticate. USD limits are
// read-only comparisons against the ledger (writes happen in usage.handle).

// nativeUsageLedgerPrefix namespaces native-scope entries in the usage ledger.
const nativeUsageLedgerPrefix = "native-scope:"

// nativeUsageLedgerID returns the ledger account id for a caller scope.
func nativeUsageLedgerID(callerScope string) string {
	return nativeUsageLedgerPrefix + strings.ToLower(strings.TrimSpace(callerScope))
}

// NativeBindingUsageSummary reports a binding's limits and current usage for
// the management API and UI.
type NativeBindingUsageSummary struct {
	RPMLimit       int     `json:"rpm_limit"`
	DailyUSDLimit  float64 `json:"daily_usd_limit"`
	WeeklyUSDLimit float64 `json:"weekly_usd_limit"`
	RPMUsed        int     `json:"rpm_used"`
	DailyUSDUsed   float64 `json:"daily_usd_used"`
	WeeklyUSDUsed  float64 `json:"weekly_usd_used"`
	DailyCalls     int64   `json:"daily_calls"`
	WeeklyCalls    int64   `json:"weekly_calls"`
}

// findNativeBindingByScope returns the binding for a caller scope, or nil.
func (s *Store) findNativeBindingByScope(callerScope string) *NativeKeyBinding {
	callerScope = strings.ToLower(strings.TrimSpace(callerScope))
	if callerScope == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	binding := s.nativeKeyBindingsByScope[callerScope]
	if binding == nil {
		return nil
	}
	cp := *binding
	return &cp
}

// recordNativeUsage bills one finalized usage record against a native
// binding's ledger account. Pricing resolves through the global alias table
// (prefer the client-requested alias, fall back to the upstream model id);
// unpriced requests are recorded at 0 USD but still increment CallCount.
// Returns the billed amount (0 when unpriced or failed).
func (s *Store) recordNativeUsage(binding NativeKeyBinding, alias, model string, failed bool, detail UsageDetail) float64 {
	if failed {
		return 0
	}
	resolved := strings.TrimSpace(alias)
	if resolved == "" {
		resolved = strings.TrimSpace(model)
	}
	if resolved == "" {
		return 0
	}
	_, usageLedger := s.runtimeComponents()
	if usageLedger == nil {
		return 0
	}

	// Price from the global alias table. A native key has no per-key model
	// rules, so the alias's own price sheet is the single source of truth.
	var (
		inputPerMillion, outputPerMillion, cacheReadPerMillion float64
		priced, perCall                                        bool
		perCallUSD                                             float64
		provider                                               string
	)
	s.mu.RLock()
	for _, mapping := range s.aliasesSnapshotLocked() {
		if mapping.Alias != resolved {
			continue
		}
		provider = mapping.Targets[0].Provider
		if strings.EqualFold(mapping.BillingMode, "per_call") {
			perCall = true
			perCallUSD = mapping.PerCallUSD
		} else {
			inputPerMillion = mapping.InputPricePerMillion
			outputPerMillion = mapping.OutputPricePerMillion
			cacheReadPerMillion = mapping.CacheReadPricePerMillion
			priced = true
		}
		break
	}
	s.mu.RUnlock()

	account := nativeUsageLedgerID(binding.CallerScope)
	if perCall {
		cost := perCallUSD
		if cost < 0 {
			cost = 0
		}
		usageLedger.RecordCost(account, resolved, cost, 0, 0, 0, 0, 1)
		return cost
	}

	usage := TokenUsage{
		PromptTokens:     int(detail.InputTokens),
		CompletionTokens: int(detail.OutputTokens),
		Found:            detail.InputTokens > 0 || detail.OutputTokens > 0,
	}
	if !usage.Found {
		return 0
	}
	cost, cacheCost, cacheReadTokens := ComputeCacheCostBreakdown(provider, inputPerMillion, outputPerMillion, cacheReadPerMillion, priced, detail)
	var nonCacheInput int64
	if priced {
		if isCacheAdditiveProvider(provider) {
			nonCacheInput = detail.InputTokens + detail.CacheCreationTokens
		} else {
			cr := detail.CacheReadTokens
			if cr == 0 {
				cr = detail.CachedTokens
			}
			if cr > detail.InputTokens {
				cr = detail.InputTokens
			}
			nonCacheInput = detail.InputTokens - cr
		}
	}
	// Record even when unpriced (priced=false → cost 0) so CallCount and
	// token volume stay visible in the UI; USD stays 0 until the operator
	// prices the alias.
	usageLedger.RecordCost(account, resolved, cost, cacheCost, cacheReadTokens, nonCacheInput, int64(detail.OutputTokens), 1)
	return cost
}

// NativeQuotaDecision is the outcome of CheckNativeKeyQuota.
type NativeQuotaDecision struct {
	// Reason is "rpm_exceeded", "daily_exceeded", or "weekly_exceeded" when
	// limited; empty when allowed.
	Reason string
	// Usage carries the live counters at decision time (for error bodies
	// and logging).
	Usage NativeBindingUsageSummary
}

// CheckNativeKeyQuota is the pre-scheduling gate for a native key. It
// decrements the RPM allowance (when configured) and compares the ledger
// against the USD limits. Bindings without any limit configured always pass
// with zero side effects, so existing deployments behave identically.
func (s *Store) CheckNativeKeyQuota(callerScope string) (NativeQuotaDecision, bool) {
	binding := s.findNativeBindingByScope(callerScope)
	if binding == nil || !binding.Enabled {
		return NativeQuotaDecision{}, false
	}
	if binding.RPM <= 0 && binding.DailyUSD <= 0 && binding.WeeklyUSD <= 0 {
		return NativeQuotaDecision{}, false
	}
	decision := NativeQuotaDecision{Usage: s.NativeBindingUsage(*binding)}

	limiter, _ := s.runtimeComponents()
	if binding.RPM > 0 {
		if limiter == nil || !limiter.Allow(nativeUsageLedgerID(binding.CallerScope), binding.RPM) {
			decision.Reason = "rpm_exceeded"
			return decision, true
		}
		decision.Usage.RPMUsed = limiter.SnapshotID(nativeUsageLedgerID(binding.CallerScope))
	}
	if binding.DailyUSD > 0 && decision.Usage.DailyUSDUsed >= binding.DailyUSD {
		decision.Reason = "daily_exceeded"
		return decision, true
	}
	if binding.WeeklyUSD > 0 && decision.Usage.WeeklyUSDUsed >= binding.WeeklyUSD {
		decision.Reason = "weekly_exceeded"
		return decision, true
	}
	return decision, false
}

// NativeBindingUsage returns a binding's limits plus its live usage counters.
func (s *Store) NativeBindingUsage(binding NativeKeyBinding) NativeBindingUsageSummary {
	summary := NativeBindingUsageSummary{
		RPMLimit:       binding.RPM,
		DailyUSDLimit:  binding.DailyUSD,
		WeeklyUSDLimit: binding.WeeklyUSD,
	}
	_, usageLedger := s.runtimeComponents()
	if usageLedger == nil {
		return summary
	}
	// Reuse the downstream key summary by projecting the binding onto a
	// KeyConfig with the same ledger id and limits.
	projection := KeyConfig{
		ID:             nativeUsageLedgerID(binding.CallerScope),
		DailyLimitUSD:  binding.DailyUSD,
		WeeklyLimitUSD: binding.WeeklyUSD,
	}
	ledgerSummary := usageLedger.Summary(projection)
	summary.DailyUSDUsed = ledgerSummary.DailyUSD
	summary.WeeklyUSDUsed = ledgerSummary.WeeklyUSD
	summary.DailyCalls = ledgerSummary.DailyCallCount
	summary.WeeklyCalls = ledgerSummary.WeeklyCallCount
	if limiter, _ := s.runtimeComponents(); limiter != nil {
		summary.RPMUsed = limiter.SnapshotID(nativeUsageLedgerID(binding.CallerScope))
	}
	return summary
}
