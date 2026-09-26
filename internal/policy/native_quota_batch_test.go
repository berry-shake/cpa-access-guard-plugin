package policy

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const quotaBatchOwnedID = "plugin-owned-quota-fixture"

func quotaBatchFixture(t *testing.T) (*Store, string, []NativeKeyBinding) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	store := newNativeQuotaStore(t, path)
	store.SetClock(func() time.Time { return time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC) })
	keys := []string{"sk-quota-batch-alpha-fixture", "sk-quota-batch-bravo-fixture", "sk-quota-batch-disabled-fixture"}
	for i, id := range []string{"alpha", "bravo", "disabled"} {
		createTestBindingWithKey(t, store, id, keys[i], func(in *CreateNativeKeyBindingInput) {
			in.Name = "Quota fixture " + id
			in.Enabled = i != 2
			in.RoundRobin = i != 1
			in.RPM, in.DailyUSD, in.WeeklyUSD = intPtr(20+i), floatPtr(100+float64(i)), floatPtr(700+float64(i))
			in.ModelAccess = &NativeModelAccessPolicy{Mode: NativeModelAccessAllowlist, Models: []NativeAllowedModel{{Provider: "codex", Model: "spark-xxx"}}}
		})
	}
	batchTestApply(t, store, NativeBindingBatchInput{
		APIKeys: keys, SelectedIndices: []int{0}, AuthIDs: []string{"quota-credential-a", "quota-credential-b"},
		AvailableAuthIDs: []string{"quota-credential-a", "quota-credential-b"}, CatalogComplete: true,
	})
	hash, err := HashKey("synthetic-plugin-owned-quota-key")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertKey(KeyConfig{ID: quotaBatchOwnedID, Enabled: true, KeyHash: hash, RPM: 10}, true); err != nil {
		t.Fatal(err)
	}
	bindings := store.NativeKeyBindingsSnapshot()
	limiter, usage := store.runtimeComponents()
	accounts := []string{quotaBatchOwnedID}
	for _, binding := range bindings {
		accounts = append(accounts, nativeUsageLedgerID(binding.CallerScope))
	}
	for i, account := range accounts {
		usage.RecordCost(account, "spark-xxx", float64(i+1), 0.2, 30, 0.3, 40, 50, 60, 2)
		usage.RecordCost(account, "spark-other", float64(i+2), 0.4, 70, 0.5, 80, 90, 100, 3)
		if !limiter.Allow(account, 20) || !limiter.Allow(account, 20) {
			t.Fatal("fixture RPM acquisition failed")
		}
	}
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	return store, path, bindings
}

func quotaBatchReadState(t *testing.T, path string) *State {
	t.Helper()
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func quotaBatchReadBytes(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func quotaBatchAssertPolicy(t *testing.T, store *Store, path string, bindings []NativeKeyBinding, history []NativeBindingOperation, diskBefore *State) {
	t.Helper()
	if !reflect.DeepEqual(store.NativeKeyBindingsSnapshot(), bindings) {
		t.Fatal("quota reset changed binding identity, policy, limits, enabled state, or round-robin setting")
	}
	if !reflect.DeepEqual(store.NativeBindingHistorySnapshot(), history) {
		t.Fatal("quota reset changed live binding history")
	}
	diskAfter := quotaBatchReadState(t, path)
	before, after := *diskBefore, *diskAfter
	before.Usage, after.Usage = nil, nil
	before.UpdatedAt, after.UpdatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("quota reset changed durable keys, native bindings, aliases, classification, or private binding history")
	}
}

func TestResetNativeKeyQuotasSelectionAndPersistence(t *testing.T) {
	cases := []struct {
		name string
		ids  []string
		want []string
	}{
		{name: "partial", ids: []string{"alpha"}, want: []string{"alpha"}},
		{name: "explicit_all", ids: []string{"alpha", "bravo", "disabled"}, want: []string{"alpha", "bravo", "disabled"}},
		{name: "disabled", ids: []string{"disabled"}, want: []string{"disabled"}},
		{name: "trim_case_dedupe_first_order", ids: []string{" BRAVO ", "Alpha", "bravo", "\tALPHA\n"}, want: []string{"bravo", "alpha"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, path, bindings := quotaBatchFixture(t)
			history := store.NativeBindingHistorySnapshot()
			diskBefore := quotaBatchReadState(t, path)
			limiter, usage := store.runtimeComponents()
			wantUsage, wantRPM := usage.snapshot(), limiter.Snapshot()
			selected := make(map[string]bool, len(tc.want))
			for _, id := range tc.want {
				selected[id] = true
			}
			for _, binding := range bindings {
				if selected[binding.ID] {
					account := nativeUsageLedgerID(binding.CallerScope)
					delete(wantUsage, account)
					delete(wantRPM, account)
				}
			}
			got, err := store.ResetNativeKeyQuotas(tc.ids)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ResetNativeKeyQuotas = %v, %v; want %v", got, err, tc.want)
			}
			if !reflect.DeepEqual(usage.snapshot(), wantUsage) || !reflect.DeepEqual(limiter.Snapshot(), wantRPM) {
				t.Fatal("reset did not clear exactly the selected usage and RPM accounts")
			}
			if !reflect.DeepEqual(quotaBatchReadState(t, path).Usage, wantUsage) {
				t.Fatal("durable usage differs from the selected reset, including plugin-owned usage")
			}
			quotaBatchAssertPolicy(t, store, path, bindings, history, diskBefore)
			restarted := newNativeQuotaStore(t, path)
			_, recovered := restarted.runtimeComponents()
			if !reflect.DeepEqual(recovered.snapshot(), wantUsage) {
				t.Fatal("restart resurrected selected daily/weekly amounts, calls, tokens, cache, or model breakdowns")
			}
			quotaBatchAssertPolicy(t, restarted, path, bindings, history, diskBefore)
			for _, binding := range bindings {
				if !selected[binding.ID] {
					continue
				}
				for _, current := range []*Store{store, restarted} {
					summary := current.NativeBindingUsage(binding)
					if summary.RPMUsed != 0 || summary.DailyUSDUsed != 0 || summary.WeeklyUSDUsed != 0 || summary.DailyCalls != 0 || summary.WeeklyCalls != 0 {
						t.Fatalf("selected quota remains nonzero: %+v", summary)
					}
					if summary.RPMLimit != binding.RPM || summary.DailyUSDLimit != binding.DailyUSD || summary.WeeklyUSDLimit != binding.WeeklyUSD {
						t.Fatalf("configured limits changed: %+v", summary)
					}
				}
			}
		})
	}
}

func TestResetNativeKeyQuotasRejectsWholeInvalidSelection(t *testing.T) {
	tooMany := make([]string, 4097)
	for i := range tooMany {
		tooMany[i] = "alpha"
	}
	tooLarge := make([]string, 513)
	for i := range tooLarge {
		tooLarge[i] = "alpha" + strings.Repeat(" ", 4091)
	}
	cases := []struct {
		name string
		ids  []string
		err  error
	}{
		{name: "nil", ids: nil, err: ErrInvalidNativeQuotaReset},
		{name: "empty", ids: []string{}, err: ErrInvalidNativeQuotaReset},
		{name: "blank", ids: []string{"\t\n "}, err: ErrInvalidNativeQuotaReset},
		{name: "valid_then_empty", ids: []string{"alpha", ""}, err: ErrInvalidNativeQuotaReset},
		{name: "null_byte", ids: []string{"alpha", "bravo\x00"}, err: ErrInvalidNativeQuotaReset},
		{name: "raw_count_before_dedupe", ids: tooMany, err: ErrInvalidNativeQuotaReset},
		{name: "raw_bytes_before_trim", ids: []string{"alpha" + strings.Repeat(" ", 4092)}, err: ErrInvalidNativeQuotaReset},
		{name: "utf8_bytes", ids: []string{strings.Repeat("界", 1366)}, err: ErrInvalidNativeQuotaReset},
		{name: "aggregate_bytes_before_dedupe", ids: tooLarge, err: ErrInvalidNativeQuotaReset},
		{name: "unknown_only", ids: []string{"unknown"}, err: ErrUnknownNativeKeyBinding},
		{name: "valid_then_unknown", ids: []string{"alpha", "bravo", "unknown"}, err: ErrUnknownNativeKeyBinding},
		{name: "unknown_then_valid", ids: []string{"unknown", "alpha"}, err: ErrUnknownNativeKeyBinding},
		{name: "plugin_owned_id", ids: []string{"alpha", quotaBatchOwnedID}, err: ErrUnknownNativeKeyBinding},
	}
	store, path, bindings := quotaBatchFixture(t)
	history := store.NativeBindingHistorySnapshot()
	limiter, usage := store.runtimeComponents()
	wantUsage, wantRPM := usage.snapshot(), limiter.Snapshot()
	wantDisk := quotaBatchReadBytes(t, path)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := store.ResetNativeKeyQuotas(tc.ids)
			if !errors.Is(err, tc.err) || got != nil {
				t.Fatalf("ResetNativeKeyQuotas = %v, %v; want nil, %v", got, err, tc.err)
			}
			if !reflect.DeepEqual(usage.snapshot(), wantUsage) || !reflect.DeepEqual(limiter.Snapshot(), wantRPM) {
				t.Fatal("invalid selection partially cleared live usage or RPM")
			}
			if !reflect.DeepEqual(store.NativeKeyBindingsSnapshot(), bindings) || !reflect.DeepEqual(store.NativeBindingHistorySnapshot(), history) {
				t.Fatal("invalid selection changed binding policy or history")
			}
			if !bytes.Equal(quotaBatchReadBytes(t, path), wantDisk) {
				t.Fatal("invalid selection wrote durable state")
			}
		})
	}
}

func TestResetNativeKeyQuotasAcceptsInputBoundaries(t *testing.T) {
	cases := []struct {
		name  string
		count int
		raw   string
	}{
		{name: "4096_occurrences", count: 4096, raw: "alpha"},
		{name: "4096_raw_bytes", count: 1, raw: "alpha" + strings.Repeat(" ", 4091)},
		{name: "two_megabytes", count: 512, raw: "alpha" + strings.Repeat(" ", 4091)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := newNativeQuotaStore(t, filepath.Join(t.TempDir(), "state.json"))
			binding := createTestBinding(t, store, "alpha", nil)
			_, usage := store.runtimeComponents()
			account := nativeUsageLedgerID(binding.CallerScope)
			usage.RecordCost(account, "spark", 1, 0, 0, 0, 0, 10, 20, 1)
			ids := make([]string, tc.count)
			for i := range ids {
				ids[i] = tc.raw
			}
			got, err := store.ResetNativeKeyQuotas(ids)
			if err != nil || !reflect.DeepEqual(got, []string{"alpha"}) {
				t.Fatalf("boundary rejected: ids=%v err=%v", got, err)
			}
			if len(usage.snapshot()) != 0 {
				t.Fatal("accepted boundary did not reset usage")
			}
		})
	}
}

func TestResetNativeKeyQuotasEmptyUsageAndRepeatedReset(t *testing.T) {
	store := newNativeQuotaStore(t, filepath.Join(t.TempDir(), "state.json"))
	createTestBinding(t, store, "fresh", nil)
	before := store.NativeKeyBindingsSnapshot()
	for i := 0; i < 2; i++ {
		got, err := store.ResetNativeKeyQuotas([]string{"fresh", " FRESH "})
		if err != nil || !reflect.DeepEqual(got, []string{"fresh"}) {
			t.Fatalf("empty-usage reset %d = %v, %v", i, got, err)
		}
		limiter, usage := store.runtimeComponents()
		if len(usage.snapshot()) != 0 || len(limiter.Snapshot()) != 0 {
			t.Fatal("reset created usage or RPM counters")
		}
		if !reflect.DeepEqual(store.NativeKeyBindingsSnapshot(), before) || len(store.NativeBindingHistorySnapshot()) != 0 {
			t.Fatal("empty-usage reset changed policy or created history")
		}
	}
}

func TestResetNativeKeyQuotasPersistenceFailureIsAtomic(t *testing.T) {
	for _, mode := range []string{"blocked_parent", "target_directory", "corrupt_state", "atomic_temp_creation"} {
		t.Run(mode, func(t *testing.T) {
			store, path, bindings := quotaBatchFixture(t)
			originalPath := path
			switch mode {
			case "blocked_parent":
				blocker := filepath.Join(filepath.Dir(path), "regular-file")
				if err := os.WriteFile(blocker, []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(blocker, "state.json")
			case "target_directory":
				path = filepath.Join(filepath.Dir(path), "directory-target")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "corrupt_state":
				if err := os.WriteFile(path, []byte("{broken-fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "atomic_temp_creation":
				// The existing basename fits NAME_MAX, but the atomic writer's
				// random temporary suffix makes its basename exceed that limit.
				path = filepath.Join(filepath.Dir(path), strings.Repeat("s", 240)+".json")
				if err := os.Rename(originalPath, path); err != nil {
					t.Skipf("filesystem cannot create the long-name fixture: %v", err)
				}
				originalPath = path
			}
			store.mu.Lock()
			store.statePath = path
			store.mu.Unlock()
			limiter, usage := store.runtimeComponents()
			wantUsage, wantRPM := usage.snapshot(), limiter.Snapshot()
			wantDisk := quotaBatchReadBytes(t, originalPath)
			history := store.NativeBindingHistorySnapshot()
			got, err := store.ResetNativeKeyQuotas([]string{"alpha", "bravo", "disabled"})
			if !errors.Is(err, ErrNativeKeyBindingPersistence) || got != nil {
				t.Fatalf("ResetNativeKeyQuotas = %v, %v; want persistence failure", got, err)
			}
			if !reflect.DeepEqual(usage.snapshot(), wantUsage) || !reflect.DeepEqual(limiter.Snapshot(), wantRPM) {
				t.Fatal("persistence failure partially published a usage or RPM reset")
			}
			if !reflect.DeepEqual(store.NativeKeyBindingsSnapshot(), bindings) || !reflect.DeepEqual(store.NativeBindingHistorySnapshot(), history) {
				t.Fatal("persistence failure changed binding policy or history")
			}
			if !bytes.Equal(quotaBatchReadBytes(t, originalPath), wantDisk) {
				t.Fatal("persistence failure changed durable state")
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(originalPath), ".*.tmp-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("atomic failure left temporary files: %v, %v", matches, err)
			}
		})
	}
}

func TestResetNativeKeyQuotas4096DistinctBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	bindings := make([]NativeKeyBinding, 4096)
	ids := make([]string, len(bindings))
	for i := range bindings {
		ids[i] = fmt.Sprintf("quota-large-%04d", i)
		bindings[i] = NativeKeyBinding{ID: ids[i], Enabled: i%2 == 0, RoundRobin: true, Group: "team", CallerScope: NativeCallerScope("synthetic-" + ids[i]), RPM: 5}
	}
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path, NativeKeyBindings: bindings}); err != nil {
		t.Fatal(err)
	}
	before := store.NativeKeyBindingsSnapshot()
	limiter, usage := store.runtimeComponents()
	for _, binding := range before {
		account := nativeUsageLedgerID(binding.CallerScope)
		usage.RecordCost(account, "spark-xxx", 1, 0.1, 20, 0.2, 30, 40, 50, 1)
		if !limiter.Allow(account, 5) {
			t.Fatal("large fixture RPM acquisition failed")
		}
	}
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	got, err := store.ResetNativeKeyQuotas(ids)
	if err != nil || !reflect.DeepEqual(got, ids) {
		t.Fatalf("4096 distinct binding reset returned %d ids, err=%v", len(got), err)
	}
	if len(usage.snapshot()) != 0 || len(limiter.Snapshot()) != 0 || len(quotaBatchReadState(t, path).Usage) != 0 {
		t.Fatal("large reset left live or durable counters")
	}
	if !reflect.DeepEqual(store.NativeKeyBindingsSnapshot(), before) {
		t.Fatal("large reset changed binding settings")
	}
	restarted := newNativeQuotaStore(t, path)
	_, recovered := restarted.runtimeComponents()
	if len(recovered.snapshot()) != 0 || !reflect.DeepEqual(restarted.NativeKeyBindingsSnapshot(), before) {
		t.Fatal("large reset did not survive restart with all policies intact")
	}
}

func TestResetNativeKeyQuotaSingleUsesBatchPersistence(t *testing.T) {
	store, path, bindings := quotaBatchFixture(t)
	if err := store.ResetNativeKeyQuota(" ALPHA "); err != nil {
		t.Fatal(err)
	}
	state := quotaBatchReadState(t, path)
	for _, binding := range bindings {
		_, exists := state.Usage[nativeUsageLedgerID(binding.CallerScope)]
		if exists == (binding.ID == "alpha") {
			t.Fatalf("single reset durable selection is incorrect for %s", binding.ID)
		}
	}
	if err := store.ResetNativeKeyQuota("  "); !errors.Is(err, ErrUnknownNativeKeyBinding) {
		t.Fatalf("single empty-ID compatibility changed: %v", err)
	}
}

func TestResetNativeKeyQuotasMemoryOnlyStore(t *testing.T) {
	for _, single := range []bool{false, true} {
		t.Run(fmt.Sprintf("single_%t", single), func(t *testing.T) {
			store := NewStore()
			binding := NativeKeyBinding{ID: "memory-only", Enabled: true, Group: "team", CallerScope: NativeCallerScope("sk-memory-only-fixture"), RPM: 5}
			store.nativeKeyBindings[binding.ID] = &binding
			store.nativeKeyBindingsByScope[binding.CallerScope] = &binding
			limiter, usage := store.runtimeComponents()
			account := nativeUsageLedgerID(binding.CallerScope)
			usage.RecordCost(account, "spark", 1, 0.1, 20, 0.2, 30, 40, 50, 1)
			if !limiter.Allow(account, binding.RPM) {
				t.Fatal("memory-only fixture RPM acquisition failed")
			}
			var err error
			if single {
				err = store.ResetNativeKeyQuota(binding.ID)
			} else {
				var got []string
				got, err = store.ResetNativeKeyQuotas([]string{binding.ID})
				if err == nil && !reflect.DeepEqual(got, []string{binding.ID}) {
					t.Fatalf("memory-only reset returned %v", got)
				}
			}
			if err != nil || len(usage.snapshot()) != 0 || len(limiter.Snapshot()) != 0 {
				t.Fatalf("memory-only reset failed: %v", err)
			}
			if store.StatePath() != "" || store.nativeKeyBindings[binding.ID] != &binding {
				t.Fatal("memory-only reset changed persistence path or binding")
			}
		})
	}
}
