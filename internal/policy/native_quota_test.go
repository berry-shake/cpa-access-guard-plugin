package policy

import (
	"testing"
	"time"
)

func newNativeQuotaStore(t *testing.T, path string) *Store {
	t.Helper()
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	return store
}

const nativeTestKey = "sk-native-test-1234567890"

func intPtr(i int) *int           { return &i }
func floatPtr(f float64) *float64 { return &f }

func createTestBinding(t *testing.T, store *Store, id string, mutate func(*CreateNativeKeyBindingInput)) NativeKeyBinding {
	return createTestBindingWithKey(t, store, id, nativeTestKey, mutate)
}

func createTestBindingWithKey(t *testing.T, store *Store, id, key string, mutate func(*CreateNativeKeyBindingInput)) NativeKeyBinding {
	t.Helper()
	input := CreateNativeKeyBindingInput{ID: id, Enabled: true, APIKey: key, Group: "team"}
	if mutate != nil {
		mutate(&input)
	}
	binding, err := store.CreateNativeKeyBinding(input)
	if err != nil {
		t.Fatalf("CreateNativeKeyBinding: %v", err)
	}
	return binding
}

func seedFastAlias(t *testing.T, store *Store) {
	t.Helper()
	if err := store.UpsertAlias(AliasMapping{
		Alias:                "fast",
		InputPricePerMillion: 1,
		OutputPricePerMillion: 2,
		Targets:              []AliasTarget{{Provider: "codex", TargetModel: "gpt-5"}},
	}); err != nil {
		t.Fatalf("UpsertAlias: %v", err)
	}
}

func TestNativeBindingLimitValidation(t *testing.T) {
	store := newNativeQuotaStore(t, t.TempDir()+"/state.json")
	_, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "neg", Enabled: true, APIKey: nativeTestKey, Group: "team", RPM: intPtr(-1),
	})
	if err == nil {
		t.Fatal("negative rpm must be rejected")
	}
	_, err = store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "negd", Enabled: true, APIKey: nativeTestKey, Group: "team", DailyUSD: floatPtr(-0.5),
	})
	if err == nil {
		t.Fatal("negative daily_usd must be rejected")
	}
}

func TestRecordUsageNativeFallback(t *testing.T) {
	store := newNativeQuotaStore(t, t.TempDir()+"/state.json")
	binding := createTestBinding(t, store, "native-1", nil)
	if scope := NativeCallerScope(nativeTestKey); scope != binding.CallerScope {
		t.Fatal("scope mismatch")
	}

	// Native key plaintext billed via the binding, priced from the global
	// alias table: 1M in @1 + 1M out @2 = $3.
	seedFastAlias(t, store)
	cost := store.RecordUsage(nativeTestKey, "fast", "gpt-5", false, UsageDetail{InputTokens: 1_000_000, OutputTokens: 1_000_000})
	if cost != 3 {
		t.Fatalf("expected $3, got %v", cost)
	}
	usage := store.NativeBindingUsage(binding)
	if usage.DailyUSDUsed != 3 || usage.DailyCalls != 1 {
		t.Fatalf("usage not recorded: %+v", usage)
	}

	// Unpriced model: 0 USD but CallCount increments.
	if cost = store.RecordUsage(nativeTestKey, "", "unknown-model", false, UsageDetail{InputTokens: 100, OutputTokens: 100}); cost != 0 {
		t.Fatalf("unpriced model must bill 0, got %v", cost)
	}
	if usage = store.NativeBindingUsage(binding); usage.DailyCalls != 2 {
		t.Fatalf("call count must advance on unpriced records: %+v", usage)
	}

	// Failed requests never bill nor count.
	store.RecordUsage(nativeTestKey, "fast", "gpt-5", true, UsageDetail{InputTokens: 100, OutputTokens: 100})
	if usage = store.NativeBindingUsage(binding); usage.DailyCalls != 2 || usage.DailyUSDUsed != 3 {
		t.Fatalf("failed request must not bill: %+v", usage)
	}
}

func TestCheckNativeKeyQuotaRPM(t *testing.T) {
	store := newNativeQuotaStore(t, t.TempDir()+"/state.json")
	rpm := 2
	binding := createTestBinding(t, store, "limited", func(in *CreateNativeKeyBindingInput) {
		in.RPM = intPtr(rpm)
	})

	scope := binding.CallerScope
	for i := 0; i < rpm; i++ {
		if _, limited := store.CheckNativeKeyQuota(scope); limited {
			t.Fatalf("request %d within RPM must pass", i+1)
		}
	}
	decision, limited := store.CheckNativeKeyQuota(scope)
	if !limited || decision.Reason != "rpm_exceeded" {
		t.Fatalf("expected rpm_exceeded, got limited=%v reason=%q", limited, decision.Reason)
	}

	// No limits configured → always pass. (Different plaintext key.)
	unlimited := createTestBindingWithKey(t, store, "unlimited", nativeTestKey+"-x", nil)
	for i := 0; i < 10; i++ {
		if _, limited := store.CheckNativeKeyQuota(unlimited.CallerScope); limited {
			t.Fatal("unlimited binding must never be limited")
		}
	}
}

func TestCheckNativeKeyQuotaDollarLimits(t *testing.T) {
	store := newNativeQuotaStore(t, t.TempDir()+"/state.json")
	binding := createTestBinding(t, store, "budget", func(in *CreateNativeKeyBindingInput) {
		in.DailyUSD = floatPtr(5)
	})
	seedFastAlias(t, store)
	if _, limited := store.CheckNativeKeyQuota(binding.CallerScope); limited {
		t.Fatal("fresh binding must pass")
	}
	store.RecordUsage(nativeTestKey, "fast", "gpt-5", false, UsageDetail{InputTokens: 6_000_000})
	decision, limited := store.CheckNativeKeyQuota(binding.CallerScope)
	if !limited || decision.Reason != "daily_exceeded" {
		t.Fatalf("expected daily_exceeded, got limited=%v reason=%q", limited, decision.Reason)
	}
}

func TestCheckNativeKeyQuotaDisabledBinding(t *testing.T) {
	store := newNativeQuotaStore(t, t.TempDir()+"/state.json")
	binding := createTestBinding(t, store, "off", func(in *CreateNativeKeyBindingInput) {
		in.RPM = intPtr(1)
	})
	if _, err := store.UpdateNativeKeyBinding(binding.ID, UpdateNativeKeyBindingInput{Enabled: nativeBoolPtr(false)}); err != nil {
		t.Fatalf("disable: %v", err)
	}
	if _, limited := store.CheckNativeKeyQuota(binding.CallerScope); limited {
		t.Fatal("disabled binding must not be quota-gated")
	}
}

func TestNativeUsagePersistsAcrossReload(t *testing.T) {
	path := t.TempDir() + "/state.json"
	store := newNativeQuotaStore(t, path)
	binding := createTestBinding(t, store, "persist", func(in *CreateNativeKeyBindingInput) {
		in.DailyUSD = floatPtr(10)
	})
	seedFastAlias(t, store)
	store.RecordUsage(nativeTestKey, "fast", "gpt-5", false, UsageDetail{InputTokens: 1_000_000})

	// Simulate the flusher's write-back before reload.
	store.StopUsageFlusher()

	store2 := newNativeQuotaStore(t, path)
	usage := store2.NativeBindingUsage(binding)
	if usage.DailyUSDUsed != 1 {
		t.Fatalf("native usage lost across reload: %+v", usage)
	}
}

func TestNativeWeeklyWindowAgesOut(t *testing.T) {
	now := time.Now()
	l := newUsageLedger(func() time.Time { return now })
	account := nativeUsageLedgerID("abc")
	l.RecordCost(account, "fast", 8, 0, 0, 0, 0, 0, 0, 1)
	if l.entries[account].Weekly.TotalUSD != 8 {
		t.Fatal("seed failed")
	}
	l.now = func() time.Time { return now.Add(weekWindow + time.Minute) }
	l.RecordCost(account, "fast", 1, 0, 0, 0, 0, 0, 0, 1)
	if got := l.entries[account].Weekly.TotalUSD; got != 1 {
		t.Fatalf("weekly window must reset after 7 days, got %v", got)
	}
}
