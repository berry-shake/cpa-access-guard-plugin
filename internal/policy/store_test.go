package policy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const storeTestKey = "sk-store-test-0123456789"

// newBoundStore configures a store at path with one enabled binding for
// storeTestKey and a $1/$2 per 1M price for model "m".
func newBoundStore(t *testing.T, path string, now time.Time) *Store {
	t.Helper()
	store := NewStore()
	store.SetClock(func() time.Time { return now })
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	createTestBindingWithKey(t, store, "bound", storeTestKey, nil)
	if err := store.UpsertModelPrice(ModelPrice{ID: "m", InputPricePerMillion: 1, OutputPricePerMillion: 2}); err != nil {
		t.Fatal(err)
	}
	return store
}

func storeTestLedgerID() string {
	return nativeUsageLedgerID(NativeCallerScope(storeTestKey))
}

// TestConfigureFlushesBeforeReload: a reconfigure must flush un-persisted
// in-memory usage to the old state path before loading it again.
func TestConfigureFlushesBeforeReload(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	s := newBoundStore(t, path, now)
	s.StartUsageFlusher()
	if cost := s.RecordUsage(storeTestKey, "m", "m", false, UsageDetail{InputTokens: 1_000_000, OutputTokens: 500_000}); cost <= 0 {
		t.Fatalf("RecordUsage cost = %v, want > 0", cost)
	}
	if err := s.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	if sum := s.usage.Summary(storeTestLedgerID()); sum.DailyUSD <= 0 {
		t.Fatalf("after reconfigure, DailyUSD = %v, want > 0", sum.DailyUSD)
	}
}

func TestSaveUsageOnlyDoesNotOverwriteCorruptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	original := []byte(`{"keys":`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SaveUsageOnly(path, map[string]*UsageState{}); err == nil {
		t.Fatal("SaveUsageOnly accepted a corrupt state file")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(original) {
		t.Fatalf("corrupt state was overwritten: %q", current)
	}
}

func TestStopUsageFlusherWaitsForWorkerExit(t *testing.T) {
	store := newNativeQuotaStore(t, filepath.Join(t.TempDir(), "state.json"))
	store.StartUsageFlusher()
	store.mu.RLock()
	flusher := store.flusher
	store.mu.RUnlock()
	if flusher == nil {
		t.Fatal("usage flusher was not started")
	}
	store.StopUsageFlusher()
	select {
	case <-flusher.doneCh:
	default:
		t.Fatal("StopUsageFlusher returned before worker exit")
	}
}

func TestStopUsageFlusherFlushesWithoutWorker(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	store := newBoundStore(t, path, now)
	store.RecordUsage(storeTestKey, "m", "m", false, UsageDetail{InputTokens: 1_000_000})

	// A failed reconfigure can leave no worker running while the plugin keeps
	// serving. Shutdown must still persist usage recorded after that point.
	store.StopUsageFlusher()

	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	usage := state.Usage[storeTestLedgerID()]
	if usage == nil || !nearly(usage.Daily.TotalUSD, 1) || usage.Daily.CallCount != 1 {
		t.Fatalf("persisted usage = %#v, want one $1 call", usage)
	}
}

// TestFlushUsageSkipsUnchangedLedger: the periodic flush rewrites the state
// file only after the ledger changed.
func TestFlushUsageSkipsUnchangedLedger(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	store := newBoundStore(t, path, now)

	// The binding was just written synchronously, so nothing is pending.
	before := statFile(t, path)
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	if after := statFile(t, path); !sameFile(before, after) {
		t.Fatal("FlushUsage rewrote the state file without any usage change")
	}

	store.RecordUsage(storeTestKey, "m", "m", false, UsageDetail{InputTokens: 1_000_000})
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if usage := state.Usage[storeTestLedgerID()]; usage == nil || usage.Daily.CallCount != 1 {
		t.Fatalf("flushed usage = %#v, want one call", usage)
	}

	flushed := statFile(t, path)
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	if again := statFile(t, path); !sameFile(flushed, again) {
		t.Fatal("second FlushUsage rewrote an unchanged ledger")
	}
}

// TestBindingSaveCarriesPendingUsage: a binding mutation writes the full
// state including pending usage, so the next flush has nothing to add and a
// restart sees the usage.
func TestBindingSaveCarriesPendingUsage(t *testing.T) {
	now := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "state.json")
	store := newBoundStore(t, path, now)
	store.RecordUsage(storeTestKey, "m", "m", false, UsageDetail{InputTokens: 1_000_000})
	createTestBindingWithKey(t, store, "second", "sk-store-test-second-key", nil)

	saved := statFile(t, path)
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	if after := statFile(t, path); !sameFile(saved, after) {
		t.Fatal("FlushUsage rewrote usage already written by the binding save")
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if usage := state.Usage[storeTestLedgerID()]; usage == nil || usage.Daily.CallCount != 1 {
		t.Fatalf("persisted usage = %#v, want one call", usage)
	}
}

// TestConfigureDropsRemovedFeatureState: a state file written by a release
// with downstream keys, aliases and classification rules loads its native
// bindings and native usage; everything else disappears on the rewrite.
func TestConfigureDropsRemovedFeatureState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	scope := NativeCallerScope(storeTestKey)
	nativeID := nativeUsageLedgerID(scope)
	legacy := map[string]any{
		"version": 1,
		"keys": []map[string]any{{
			"id": "team-a", "enabled": true, "key_hash": "$2a$10$abcdefghijklmnopqrstuv",
			"models": []map[string]any{{"alias": "fast", "provider": "codex", "target_model": "gpt-5"}},
		}},
		"aliases":        []map[string]any{{"alias": "fast", "targets": []map[string]any{{"provider": "codex", "target_model": "gpt-5"}}}},
		"classify_rules": []map[string]any{{"name": "share", "group": "share", "enabled": true}},
		"usage": map[string]any{
			"team-a": map[string]any{"daily": map[string]any{"total_usd": 3}},
			nativeID: map[string]any{"daily": map[string]any{"total_usd": 5, "call_count": 2}},
		},
		"native_key_bindings": []map[string]any{{
			"id": "bound", "enabled": true, "caller_scope": scope,
			"auth_ids": []string{"a.json"}, "model_access": map[string]any{"mode": "all"},
		}},
		"updated_at": "2026-07-01T00:00:00Z",
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"keys"`, `"aliases"`, `"classify_rules"`, `"team-a"`} {
		if strings.Contains(string(rewritten), field) {
			t.Fatalf("rewritten state still contains %s: %s", field, rewritten)
		}
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if usage := state.Usage[nativeID]; usage == nil || usage.Daily.TotalUSD != 5 {
		t.Fatalf("native usage = %#v, want $5 kept", usage)
	}
	bindings := store.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || len(bindings[0].AuthIDs) != 1 {
		t.Fatalf("bindings = %#v, want the direct binding", bindings)
	}
}

// TestUsageStateLegacyByAliasMigrates: by_alias written as a single window per
// model loads into the dual-window form with the old value in Daily.
func TestUsageStateLegacyByAliasMigrates(t *testing.T) {
	raw := []byte(`{"daily":{"total_usd":0.8},"weekly":{"total_usd":0.8},"by_alias":{"fast":{"total_usd":0.8,"call_count":2,"input_tokens":800000},"new":{"daily":{"total_usd":1},"weekly":{"total_usd":2}}}}`)
	var state UsageState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	fast := state.ByAlias["fast"]
	if !nearly(fast.Daily.TotalUSD, 0.8) || fast.Daily.CallCount != 2 || fast.Daily.InputTokens != 800_000 {
		t.Fatalf("migrated daily = %+v", fast.Daily)
	}
	if fast.Weekly.TotalUSD != 0 {
		t.Fatalf("migrated weekly = %+v, want zero", fast.Weekly)
	}
	if current := state.ByAlias["new"]; current.Daily.TotalUSD != 1 || current.Weekly.TotalUSD != 2 {
		t.Fatalf("dual-window entry = %+v", current)
	}
}

func TestNativeKeyPreview(t *testing.T) {
	if got := NativeKeyPreview("sk-abcdefghijklmnopqrstuvwxyz"); got != "sk-abcd...vwxyz" {
		t.Fatalf("NativeKeyPreview() = %q", got)
	}
	if got := NativeKeyPreview("short-key"); got != "<redacted>" {
		t.Fatalf("NativeKeyPreview(short) = %q", got)
	}
}

func statFile(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// sameFile reports whether path was not replaced: every write is a temp
// file plus rename, which yields a new inode.
func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.ModTime().Equal(b.ModTime())
}
