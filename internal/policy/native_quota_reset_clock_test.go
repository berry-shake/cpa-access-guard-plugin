package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type quotaResetTestClock struct{ nanos atomic.Int64 }

func (c *quotaResetTestClock) now() time.Time  { return time.Unix(0, c.nanos.Load()).UTC() }
func (c *quotaResetTestClock) set(t time.Time) { c.nanos.Store(t.UnixNano()) }

func resetClockFixture(t *testing.T) (*Store, *quotaResetTestClock, []NativeKeyBinding, string) {
	t.Helper()
	clock := &quotaResetTestClock{}
	clock.set(time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore()
	store.SetClock(clock.now)
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two", "legacy"} {
		createTestBindingWithKey(t, store, id, "synthetic-reset-clock-"+id, func(in *CreateNativeKeyBindingInput) {
			in.RPM, in.DailyUSD, in.WeeklyUSD = intPtr(50), floatPtr(10), floatPtr(100)
		})
	}
	return store, clock, store.NativeKeyBindingsSnapshot(), path
}

func resetClockBinding(t *testing.T, bindings []NativeKeyBinding, id string) NativeKeyBinding {
	t.Helper()
	for _, binding := range bindings {
		if binding.ID == id {
			return binding
		}
	}
	t.Fatalf("missing fixture binding %s", id)
	return NativeKeyBinding{}
}

func assertResetClockDeadline(t *testing.T, store *Store, binding NativeKeyBinding, want time.Time) {
	t.Helper()
	got := store.NativeBindingUsage(binding).WeeklyResetAt
	if got != want.UTC().Format(time.RFC3339) {
		t.Fatalf("weekly deadline=%s want=%s", got, want)
	}
}

func TestNativeQuotaResetClockStartsImmediatelyAndSurvivesReload(t *testing.T) {
	store, clock, bindings, path := resetClockFixture(t)
	binding := resetClockBinding(t, bindings, "one")
	anchor := clock.now()
	if got := store.NativeBindingUsage(binding).WeeklyResetAt; got != "" {
		t.Fatalf("never-used legacy binding invented a deadline: %s", got)
	}
	if err := store.ResetNativeKeyQuota(binding.ID); err != nil {
		t.Fatal(err)
	}
	assertResetClockDeadline(t, store, binding, anchor.Add(weekWindow))
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	account := nativeUsageLedgerID(binding.CallerScope)
	saved := state.Usage[account]
	if saved == nil || saved.WeeklyResetAnchor == nil || !saved.WeeklyResetAnchor.Equal(anchor) || !saved.Weekly.WindowStart.Equal(anchor) || saved.Weekly.CallCount != 0 {
		t.Fatalf("zero-call reset anchor not durable: %+v", saved)
	}
	clock.set(anchor.Add(3 * dayWindow))
	for i := 0; i < 3; i++ {
		assertResetClockDeadline(t, store, binding, anchor.Add(weekWindow))
	}
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore()
	restarted.SetClock(clock.now)
	if err := restarted.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	assertResetClockDeadline(t, restarted, binding, anchor.Add(weekWindow))
	restarted.RecordUsage("synthetic-reset-clock-one", "", "late-first-model", false, UsageDetail{InputTokens: 100})
	assertResetClockDeadline(t, restarted, binding, anchor.Add(weekWindow))
	if got := restarted.NativeBindingUsage(binding); got.WeeklyCalls != 1 || got.DailyCalls != 1 {
		t.Fatalf("late first call was lost: %+v", got)
	}
	got := restarted.usage.snapshot()[account]
	if !got.ByAlias["late-first-model"].Weekly.WindowStart.Equal(anchor) {
		t.Fatal("late first model started a separate weekly period")
	}
}

func TestNativeQuotaResetClockBatchSamplesOnceAndRepeatRestartsPeriod(t *testing.T) {
	store, clock, bindings, path := resetClockFixture(t)
	anchor := clock.now()
	var samples atomic.Int64
	store.usage.now = func() time.Time {
		samples.Add(1)
		return clock.now()
	}
	if _, err := store.ResetNativeKeyQuotas([]string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	if got := samples.Load(); got != 1 {
		t.Fatalf("batch sampled reset time %d times", got)
	}
	for _, id := range []string{"one", "two"} {
		binding := resetClockBinding(t, bindings, id)
		assertResetClockDeadline(t, store, binding, anchor.Add(weekWindow))
	}
	repeat := anchor.Add(2*dayWindow + 17*time.Minute)
	clock.set(repeat)
	if err := store.ResetNativeKeyQuota("one"); err != nil {
		t.Fatal(err)
	}
	assertResetClockDeadline(t, store, resetClockBinding(t, bindings, "one"), repeat.Add(weekWindow))
	assertResetClockDeadline(t, store, resetClockBinding(t, bindings, "two"), anchor.Add(weekWindow))
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		want := anchor
		if id == "one" {
			want = repeat
		}
		st := state.Usage[nativeUsageLedgerID(resetClockBinding(t, bindings, id).CallerScope)]
		if st.WeeklyResetAnchor == nil || !st.WeeklyResetAnchor.Equal(want) || !st.Weekly.WindowStart.Equal(want) {
			t.Fatalf("%s persisted anchor not updated correctly: %+v", id, st)
		}
	}
}

func TestNativeQuotaResetClockFixedPeriodsAlignModelUsage(t *testing.T) {
	store, clock, bindings, path := resetClockFixture(t)
	binding := resetClockBinding(t, bindings, "one")
	account := nativeUsageLedgerID(binding.CallerScope)
	anchor := clock.now()
	if err := store.ResetNativeKeyQuota(binding.ID); err != nil {
		t.Fatal(err)
	}
	record := func(model string) { store.usage.RecordCost(account, model, 3, 1, 20, 0.5, 10, 80, 40, 1) }
	record("first-model")
	clock.set(anchor.Add(6 * dayWindow))
	record("day-six-model")
	for _, row := range store.usage.AliasUsage(KeyConfig{ID: account}) {
		if !row.Weekly.WindowStart.Equal(anchor) || row.Weekly.CallCount != 1 {
			t.Fatalf("model period is not aligned before boundary: %+v", row)
		}
	}
	for _, test := range []struct {
		name           string
		now, wantStart time.Time
		wantCalls      int64
	}{
		{"before_boundary", anchor.Add(weekWindow - time.Nanosecond), anchor, 2},
		{"exact_boundary", anchor.Add(weekWindow), anchor.Add(weekWindow), 0},
		{"after_boundary", anchor.Add(weekWindow + time.Minute), anchor.Add(weekWindow), 0},
		{"multiple_idle_weeks", anchor.Add(24*dayWindow + 5*time.Hour), anchor.Add(3 * weekWindow), 0},
		{"repeated_idle_read", anchor.Add(25 * dayWindow), anchor.Add(3 * weekWindow), 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock.set(test.now)
			before := store.usage.snapshot()
			assertResetClockDeadline(t, store, binding, test.wantStart.Add(weekWindow))
			summary := store.usage.Summary(KeyConfig{ID: account})
			if summary.WeeklyCallCount != test.wantCalls {
				t.Fatalf("weekly calls=%d want=%d", summary.WeeklyCallCount, test.wantCalls)
			}
			if test.wantCalls == 0 && (summary.WeeklyUSD != 0 || summary.WeeklyCacheCostUSD != 0 || summary.WeeklyCacheReadTokens != 0 || summary.WeeklyCacheWriteTokens != 0 || summary.WeeklyInputTokens != 0) {
				t.Fatalf("expired aggregate counters retained: %+v", summary)
			}
			for _, row := range store.usage.AliasUsage(KeyConfig{ID: account}) {
				if !row.Weekly.WindowStart.Equal(test.wantStart) {
					t.Fatalf("model window drift: %+v", row)
				}
				if test.wantCalls == 0 && row.Weekly != (UsageWindow{WindowStart: test.wantStart}) {
					t.Fatalf("expired model counters retained: %+v", row)
				}
			}
			if !reflect.DeepEqual(before, store.usage.snapshot()) {
				t.Fatal("read-only projection changed live ledger")
			}
		})
	}
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore()
	restarted.SetClock(clock.now)
	if err := restarted.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	assertResetClockDeadline(t, restarted, binding, anchor.Add(4*weekWindow))
	restarted.usage.RecordCost(account, "after-idle", 2, 0, 0, 0, 0, 30, 10, 1)
	st := restarted.usage.snapshot()[account]
	if !st.Weekly.WindowStart.Equal(anchor.Add(3*weekWindow)) || st.Weekly.TotalUSD != 2 || st.Weekly.CallCount != 1 {
		t.Fatalf("first record after idle shifted period or retained old charge: %+v", st.Weekly)
	}
	if !st.Daily.WindowStart.Equal(clock.now().Truncate(dayWindow)) || !st.ByAlias["after-idle"].Weekly.WindowStart.Equal(st.Weekly.WindowStart) {
		t.Fatal("daily midnight or model weekly alignment changed")
	}
	clock.set(anchor.Add(3 * weekWindow))
	restarted.usage.RecordCost(account, "after-idle", 1, 0, 0, 0, 0, 1, 1, 1)
	if got := restarted.usage.snapshot()[account].Weekly; got.TotalUSD != 3 || got.CallCount != 2 {
		t.Fatalf("backward clock within period cleared usage: %+v", got)
	}
	clock.set(anchor.Add(dayWindow))
	restarted.usage.RecordCost(account, "after-clock-rollback", 1, 0, 0, 0, 0, 1, 1, 1)
	assertResetClockDeadline(t, restarted, binding, anchor.Add(4*weekWindow))
	st = restarted.usage.snapshot()[account]
	if st.Weekly.TotalUSD != 4 || !st.ByAlias["after-clock-rollback"].Weekly.WindowStart.Equal(st.Weekly.WindowStart) {
		t.Fatal("clock rollback rewound period or lost consumed amount")
	}
}

func TestNativeQuotaResetClockFailurePreservesPreviousAnchorAndCounters(t *testing.T) {
	store, clock, bindings, path := resetClockFixture(t)
	if _, err := store.ResetNativeKeyQuotas([]string{"one", "two"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		account := nativeUsageLedgerID(resetClockBinding(t, bindings, id).CallerScope)
		store.usage.RecordCost(account, "model", 2, 0, 0, 0, 0, 20, 10, 1)
		store.limiter.Allow(account, 50)
	}
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	wantUsage, wantRPM := store.usage.snapshot(), store.limiter.Snapshot()
	wantDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	clock.set(clock.now().Add(3 * dayWindow))
	store.statePath = filepath.Dir(path)
	_, err = store.ResetNativeKeyQuotas([]string{"one", "two"})
	store.statePath = path
	if !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("expected persistence failure, got %v", err)
	}
	if !reflect.DeepEqual(wantUsage, store.usage.snapshot()) || !reflect.DeepEqual(wantRPM, store.limiter.Snapshot()) {
		t.Fatal("failed reset changed previous anchor or counters")
	}
	gotDisk, err := os.ReadFile(path)
	if err != nil || string(gotDisk) != string(wantDisk) {
		t.Fatal("failed reset changed durable previous anchor")
	}
}

func TestNativeQuotaResetClockLegacyAndDownstreamRemainUnchanged(t *testing.T) {
	anchor := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	now := anchor
	l := newUsageLedger(func() time.Time { return now })
	native := nativeUsageLedgerID(NativeCallerScope("legacy"))
	l.RecordCost(native, "model", 7, 0, 0, 0, 0, 50, 10, 1)
	l.RecordCost("downstream", "model", 9, 0, 0, 0, 0, 50, 10, 1)
	l.RecordCost("native-scope:downstream-label", "model", 9, 0, 0, 0, 0, 50, 10, 1)
	before := l.snapshot()
	if before[native].WeeklyResetAnchor != nil || before["downstream"].WeeklyResetAnchor != nil {
		t.Fatal("ordinary usage created manual reset anchors")
	}
	// Even an anchor loaded for a plugin-owned key must not opt it into the
	// native-only fixed-period behavior.
	l.entries["downstream"].WeeklyResetAnchor = &anchor
	l.entries["native-scope:downstream-label"].WeeklyResetAnchor = &anchor
	now = anchor.Add(3 * dayWindow)
	if l.Summary(KeyConfig{ID: native}).WeeklyUSD != 7 || l.Summary(KeyConfig{ID: "downstream"}).WeeklyUSD != 9 {
		t.Fatal("new anchor support modified existing consumed amounts")
	}
	now = anchor.Add(9 * dayWindow)
	for _, id := range []string{native, "downstream", "native-scope:downstream-label"} {
		wantDeadline := now.Add(weekWindow)
		if id == native {
			wantDeadline = time.Time{}
		}
		if got := l.Summary(KeyConfig{ID: id}); !got.WeeklyResetAt.Equal(wantDeadline) || got.WeeklyUSD != 0 {
			t.Fatalf("legacy first-use expiry behavior changed for %s: %+v", id, got)
		}
	}
	if got := l.Summary(KeyConfig{ID: nativeUsageLedgerID("no-ledger")}); !got.WeeklyResetAt.IsZero() {
		t.Fatal("fork19 missing reset state was assigned a guessed deadline")
	}
}

func TestNativeQuotaResetClockSerializationAndSnapshotOwnership(t *testing.T) {
	anchor := time.Date(2026, 10, 1, 9, 30, 0, 123, time.UTC)
	stateAnchor := anchor
	state := &UsageState{WeeklyResetAnchor: &stateAnchor, Weekly: UsageWindow{WindowStart: anchor}, ByAlias: map[string]AliasUsageWindows{}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var restored UsageState
	if err := json.Unmarshal(raw, &restored); err != nil || !reflect.DeepEqual(state, &restored) {
		t.Fatalf("anchor round trip failed: %v %+v", err, restored)
	}
	if err := json.Unmarshal([]byte(`{"daily":{},"weekly":{},"by_alias":{"legacy-model":{"total_usd":5}}}`), &restored); err != nil {
		t.Fatal(err)
	}
	if restored.WeeklyResetAnchor != nil || restored.ByAlias["legacy-model"].Daily.TotalUSD != 5 {
		t.Fatal("legacy decode retained a previous anchor or lost old amounts")
	}
	account := nativeUsageLedgerID("fixture")
	l := newUsageLedger(time.Now)
	l.loadFromState(map[string]*UsageState{account: state})
	*state.WeeklyResetAnchor = anchor.Add(time.Hour)
	if got := l.snapshot()[account].WeeklyResetAnchor; !got.Equal(anchor) {
		t.Fatal("loadFromState retained shared anchor pointer")
	}
	snapshot := l.snapshot()
	*snapshot[account].WeeklyResetAnchor = anchor.Add(2 * time.Hour)
	if got := l.snapshot()[account].WeeklyResetAnchor; !got.Equal(anchor) {
		t.Fatal("snapshot retained shared anchor pointer")
	}
}

func TestNativeQuotaResetClockQueuedRecordUsesCommittedPeriod(t *testing.T) {
	store, clock, bindings, path := resetClockFixture(t)
	binding := resetClockBinding(t, bindings, "one")
	account := nativeUsageLedgerID(binding.CallerScope)
	anchor := clock.now()
	entered, release := make(chan struct{}), make(chan struct{})
	result, recorded := make(chan error, 1), make(chan struct{})
	go func() {
		store.persistMu.Lock()
		defer store.persistMu.Unlock()
		result <- resetNativeQuotaAccounts([]string{account}, store.usage, store.limiter, func(snapshot map[string]*UsageState) error {
			close(entered)
			<-release
			return SaveUsageOnly(path, snapshot)
		})
	}()
	<-entered
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	go func() {
		store.usage.RecordCost(account, "queued-model", 2, 0, 0, 0, 0, 10, 2, 1)
		close(recorded)
	}()
	// Wait until the record actually waits on the reset's ledger lock before
	// advancing the controlled clock. Sampling time before locking would bill
	// this record into the previous period despite the delayed completion.
	buffer := make([]byte, 1<<20)
	waiting := false
	deadline := time.Now().Add(5 * time.Second)
	for !waiting && time.Now().Before(deadline) {
		for _, stack := range strings.Split(string(buffer[:runtime.Stack(buffer, true)]), "\n\n") {
			if strings.Contains(stack, "TestNativeQuotaResetClockQueuedRecordUsesCommittedPeriod.func") && strings.Contains(stack, ".RecordCost(") && strings.Contains(stack, "sync.(*Mutex).Lock") {
				waiting = true
				break
			}
		}
		runtime.Gosched()
	}
	if !waiting {
		t.Fatal("record did not wait on reset transaction")
	}
	clock.set(anchor.Add(8 * dayWindow))
	close(release)
	released = true
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	<-recorded
	st := store.usage.snapshot()[account]
	if st.WeeklyResetAnchor == nil || !st.WeeklyResetAnchor.Equal(anchor) || !st.Weekly.WindowStart.Equal(anchor.Add(weekWindow)) || st.Weekly.CallCount != 1 || st.Weekly.TotalUSD != 2 {
		t.Fatalf("queued record lost or assigned wrong period: %+v", st)
	}
	if !st.ByAlias["queued-model"].Weekly.WindowStart.Equal(st.Weekly.WindowStart) {
		t.Fatal("queued model counter did not follow committed aggregate period")
	}
	assertResetClockDeadline(t, store, binding, anchor.Add(2*weekWindow))
}
