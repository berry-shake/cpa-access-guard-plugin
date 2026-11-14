package policy

import (
	"strings"
	"sync"
	"time"
)

const (
	dayWindow          = 24 * time.Hour
	weekWindow         = 7 * 24 * time.Hour
	usageFlushInterval = 15 * time.Second
)

// usageLedger tracks per-account dollar usage with a daily window (UTC
// midnight reset) and a weekly window.
//
// It is the in-memory source of truth; a background flusher persists it to
// the state JSON whenever it changed (see Store.FlushUsage).
type usageLedger struct {
	mu  sync.Mutex
	now func() time.Time
	// usage by ledger account id; nil entry allowed.
	entries map[string]*UsageState
	// generation counts in-memory changes; flushed is the generation last
	// written to disk. The flusher skips the write while they are equal.
	generation uint64
	flushed    uint64
}

func newUsageLedger(now func() time.Time) *usageLedger {
	if now == nil {
		now = time.Now
	}
	return &usageLedger{now: now, entries: make(map[string]*UsageState)}
}

// loadFromState seeds the ledger from a loaded state file (restart recovery).
func (l *usageLedger) loadFromState(usage map[string]*UsageState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = make(map[string]*UsageState, len(usage))
	for id, st := range usage {
		if st == nil {
			continue
		}
		l.entries[id] = cloneUsageState(st)
	}
}

// dirtySnapshot returns a deep copy plus its generation when the ledger
// changed since the last markFlushed.
func (l *usageLedger) dirtySnapshot() (map[string]*UsageState, uint64, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.generation == l.flushed {
		return nil, l.generation, false
	}
	return l.snapshotLocked(), l.generation, true
}

// snapshotWithGeneration returns a deep copy plus the generation it reflects.
func (l *usageLedger) snapshotWithGeneration() (map[string]*UsageState, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshotLocked(), l.generation
}

// markFlushed records that the snapshot of generation was persisted.
func (l *usageLedger) markFlushed(generation uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if generation > l.flushed {
		l.flushed = generation
	}
}

// snapshot returns a deep copy for persistence/reporting.
func (l *usageLedger) snapshot() map[string]*UsageState {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.snapshotLocked()
}

// snapshotLocked deep-copies every account so concurrent recording cannot
// mutate a snapshot while it is being serialized. Caller must hold l.mu.
func (l *usageLedger) snapshotLocked() map[string]*UsageState {
	out := make(map[string]*UsageState, len(l.entries))
	for id, st := range l.entries {
		if st == nil {
			continue
		}
		out[id] = cloneUsageState(st)
	}
	return out
}

func cloneUsageState(st *UsageState) *UsageState {
	cp := *st
	if st.WeeklyResetAnchor != nil {
		anchor := *st.WeeklyResetAnchor
		cp.WeeklyResetAnchor = &anchor
	}
	return &cp
}

// nativeWeeklyResetAnchor deliberately excludes plugin-owned and legacy
// accounts. Reading or billing those accounts must retain their old windows.
func nativeWeeklyResetAnchor(id string, st *UsageState) time.Time {
	if isNativeUsageLedgerID(id) && st.WeeklyResetAnchor != nil {
		return *st.WeeklyResetAnchor
	}
	return time.Time{}
}

func isNativeUsageLedgerID(id string) bool {
	scope, found := strings.CutPrefix(id, nativeUsageLedgerPrefix)
	return found && validateNativeCallerScope(scope) == nil
}

func fixedWeeklyWindowStart(anchor, now time.Time) time.Time {
	if now.Before(anchor) {
		return anchor.UTC()
	}
	return anchor.Add((now.Sub(anchor) / weekWindow) * weekWindow).UTC()
}

// advanceFixedWeeklyWindow never rewinds on a backwards clock adjustment.
func advanceFixedWeeklyWindow(w *UsageWindow, start time.Time) {
	if w.WindowStart.IsZero() || w.WindowStart.Before(start) {
		*w = UsageWindow{WindowStart: start}
	}
}

func (l *usageLedger) entryLocked(id string) *UsageState {
	st := l.entries[id]
	if st == nil {
		st = &UsageState{}
		l.entries[id] = st
	}
	return st
}

// ensureDailyWindow resets the daily window if we crossed UTC midnight since it
// last started. Caller must hold the mutex.
func (l *usageLedger) ensureDailyWindowLocked(st *UsageState, now time.Time) {
	startOfDay := now.UTC().Truncate(dayWindow)
	if st.Daily.WindowStart.IsZero() || !sameDay(st.Daily.WindowStart, startOfDay) {
		st.Daily = UsageWindow{WindowStart: startOfDay}
	}
}

func (l *usageLedger) ensureWeeklyWindowLocked(st *UsageState, now, anchor time.Time) {
	if !anchor.IsZero() {
		advanceFixedWeeklyWindow(&st.Weekly, fixedWeeklyWindowStart(anchor, now))
		return
	}
	// Rolling window: if the recorded start is older than 7 days, slide it
	// forward so only the trailing 7 days count. We drop the accumulated total
	// and reset the window to now (conservative — losing usage that aged out
	// rather than recomputing partial slices; acceptable for an over-quota guard).
	if st.Weekly.WindowStart.IsZero() || now.Sub(st.Weekly.WindowStart) >= weekWindow {
		st.Weekly = UsageWindow{WindowStart: now.UTC()}
	}
}

func sameDay(a, b time.Time) bool {
	a = a.UTC()
	b = b.UTC()
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day()
}

// RecordCost adds a dollar amount for an account to its daily and weekly
// buckets, advancing windows as needed. It also accumulates the
// cache-specific counters (cache-read tokens, cache spend, non-cache input
// tokens) used for the cache hit-rate / spend report — these do NOT feed limit
// enforcement, only the Summary the UI reads.
//
// callCount is the number of successful requests to add to CallCount for this
// record (1 for a normal request, 0 when billing a zero-cost/no-op record).
//
// amount is the total dollar bill for the record; cacheCost is the portion of
// that bill attributable to cache-hit input tokens priced at the cache price
// (0 when no cache price was configured); cacheReadTokens is the cache-hit
// count for the record; inputTokens is the non-cache input-token count charged
// at the regular input price (the denominator partner for hit-rate);
// outputTokens is the completion-token count charged at the output price.
func (l *usageLedger) RecordCost(id string, amount, cacheCost float64, cacheReadTokens int64, cacheWriteCost float64, cacheWriteTokens, inputTokens, outputTokens int64, callCount int64) {
	if id == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.generation++
	// Sample after locking so records queued behind a reset use the new period.
	now := l.now()
	st := l.entryLocked(id)
	anchor := nativeWeeklyResetAnchor(id, st)
	l.ensureDailyWindowLocked(st, now)
	l.ensureWeeklyWindowLocked(st, now, anchor)
	st.Daily.TotalUSD += amount
	st.Weekly.TotalUSD += amount
	st.Daily.CallCount += callCount
	st.Weekly.CallCount += callCount
	if cacheReadTokens > 0 {
		st.Daily.CacheReadTokens += cacheReadTokens
		st.Weekly.CacheReadTokens += cacheReadTokens
	}
	if cacheCost > 0 {
		st.Daily.CacheCostUSD += cacheCost
		st.Weekly.CacheCostUSD += cacheCost
	}
	if cacheWriteTokens > 0 {
		st.Daily.CacheWriteTokens += cacheWriteTokens
		st.Weekly.CacheWriteTokens += cacheWriteTokens
	}
	if cacheWriteCost > 0 {
		st.Daily.CacheWriteCostUSD += cacheWriteCost
		st.Weekly.CacheWriteCostUSD += cacheWriteCost
	}
	if inputTokens > 0 {
		st.Daily.InputTokens += inputTokens
		st.Weekly.InputTokens += inputTokens
	}
	if outputTokens > 0 {
		st.Daily.OutputTokens += outputTokens
		st.Weekly.OutputTokens += outputTokens
	}
}

// UsageSummary is the live usage of one ledger account. Windows are
// re-evaluated on read, so a report never shows totals from a window that
// already aged out.
type UsageSummary struct {
	DailyUSD        float64
	WeeklyUSD       float64
	DailyResetAt    time.Time
	WeeklyResetAt   time.Time
	DailyCallCount  int64
	WeeklyCallCount int64
}

// Summary returns the current usage of one ledger account. daily_reset_at =
// next UTC midnight; weekly_reset_at = window start + 7 days.
func (l *usageLedger) Summary(id string) UsageSummary {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	st := l.entries[id]
	summary := UsageSummary{DailyResetAt: now.UTC().Truncate(dayWindow).Add(dayWindow)}
	if st == nil {
		return summary
	}
	ensureSt := *st
	l.ensureDailyWindowLocked(&ensureSt, now)
	anchor := nativeWeeklyResetAnchor(id, st)
	l.ensureWeeklyWindowLocked(&ensureSt, now, anchor)
	summary.DailyUSD = ensureSt.Daily.TotalUSD
	summary.WeeklyUSD = ensureSt.Weekly.TotalUSD
	summary.DailyCallCount = ensureSt.Daily.CallCount
	summary.WeeklyCallCount = ensureSt.Weekly.CallCount
	// A legacy native account has no fixed schedule until a real record starts
	// its next period. Do not expose a read-time projection as a real deadline.
	legacyNativeWithoutPeriod := isNativeUsageLedgerID(id) && anchor.IsZero() &&
		(st.Weekly.WindowStart.IsZero() || now.Sub(st.Weekly.WindowStart) >= weekWindow)
	if !ensureSt.Weekly.WindowStart.IsZero() && !legacyNativeWithoutPeriod {
		summary.WeeklyResetAt = ensureSt.Weekly.WindowStart.Add(weekWindow)
	}
	return summary
}
