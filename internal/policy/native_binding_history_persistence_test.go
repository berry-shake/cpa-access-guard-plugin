package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func persistenceHistoryFixture() []nativeBindingHistoryRecord {
	return []nativeBindingHistoryRecord{{
		ID: "history-persistence-operation", Kind: "batch", CreatedAt: time.Date(2026, 9, 26, 1, 2, 3, 0, time.UTC),
		Changes: []nativeBindingRecordedChange{{
			NativeBindingChange: NativeBindingChange{
				BindingID: "recorded-native", Name: "Synthetic history key", KeyPreview: "sk-fi...001",
				Before: &NativeBindingRestriction{Group: "team"},
				After:  &NativeBindingRestriction{AuthIDs: []string{"credential-a", "credential-b"}},
			},
			CallerScope: NativeCallerScope("sk-history-persistence-fixture"),
		}},
	}}
}

func seedPersistenceHistory(t *testing.T, path string) []nativeBindingHistoryRecord {
	t.Helper()
	history := persistenceHistoryFixture()
	if err := saveStateWithNativeHistory(path, nil, nil, nil, nil, nil, history); err != nil {
		t.Fatal(err)
	}
	return history
}

func assertPersistenceHistory(t *testing.T, path string, want []nativeBindingHistoryRecord) *State {
	t.Helper()
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.NativeBindingHistory, want) {
		t.Fatalf("native binding history changed: got %#v, want %#v", state.NativeBindingHistory, want)
	}
	return state
}

func TestNativeBindingHistorySurvivesOrdinaryStateMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := seedPersistenceHistory(t, path)
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	hash, err := HashKey("synthetic-history-plugin-key")
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, mutate func() error) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			if err := mutate(); err != nil {
				t.Fatal(err)
			}
			assertPersistenceHistory(t, path, want)
			store.mu.RLock()
			live := cloneNativeBindingHistory(store.nativeBindingHistory)
			store.mu.RUnlock()
			if !reflect.DeepEqual(live, want) {
				t.Fatal("ordinary mutation changed in-memory binding history")
			}
		})
	}
	run("upsert_alias", func() error {
		return store.UpsertAlias(AliasMapping{Alias: "history-alias", Targets: []AliasTarget{{Provider: "codex", TargetModel: "spark"}}})
	})
	run("upsert_key", func() error {
		return store.UpsertKey(KeyConfig{ID: "history-key", Enabled: true, KeyHash: hash, Aliases: []KeyAliasRef{{Alias: "history-alias"}}}, true)
	})
	run("rotate_key", func() error { _, _, err := store.RotateKey("history-key"); return err })
	run("delete_key", func() error { return store.DeleteKey("history-key") })
	run("delete_alias", func() error { return store.DeleteAlias("history-alias") })
	run("upsert_rule", func() error {
		return store.UpsertClassifyRule(ClassifyRule{Name: "history-rule", Field: "provider", Pattern: "^codex$", Group: "translation", Enabled: true})
	})
	run("reorder_rules", func() error { return store.ReorderClassifyRules([]string{"history-rule"}) })
	run("delete_rule", func() error { return store.DeleteClassifyRule("history-rule") })
	run("create_native_binding", func() error {
		_, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{ID: "ordinary-native", APIKey: "sk-ordinary-native-history", Enabled: true, Group: "team"})
		return err
	})
	run("update_native_binding", func() error {
		_, err := store.UpdateNativeKeyBinding("ordinary-native", UpdateNativeKeyBindingInput{Group: nativeStringPtr("plus")})
		return err
	})
	run("rotate_native_binding", func() error {
		_, err := store.UpdateNativeKeyBinding("ordinary-native", UpdateNativeKeyBindingInput{APIKey: "sk-ordinary-native-history-rotated"})
		return err
	})
	run("delete_native_binding", func() error { return store.DeleteNativeKeyBinding("ordinary-native") })
	run("flush_usage", store.FlushUsage)
	run("reconfigure", func() error { return store.Configure(Config{Enabled: true, StateFile: path}) })
}

func TestNativeBindingHistorySaveStatePreservesDiskHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := seedPersistenceHistory(t, path)
	usage := map[string]*UsageState{"active-key": {Daily: UsageWindow{TotalUSD: 17, CallCount: 29}}}
	if err := SaveState(path, []KeyConfig{{ID: "new-key"}}, usage, nil, nil); err != nil {
		t.Fatal(err)
	}
	state := assertPersistenceHistory(t, path, want)
	if len(state.Keys) != 1 || state.Keys[0].ID != "new-key" || state.Usage["active-key"].Daily.CallCount != 29 {
		t.Fatal("preserving history prevented the requested state update")
	}
}

func TestNativeBindingHistoryUsageFlushPreservesLatestDiskHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	oldHistory := seedPersistenceHistory(t, path)
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	createTestBinding(t, store, "usage-history", nil)
	store.RecordUsage(nativeTestKey, "", "unpriced", false, UsageDetail{InputTokens: 1})
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	latest := cloneNativeBindingHistory(oldHistory)
	latest[0].ID = "newer-history-on-disk"
	latest[0].Changes[0].After.AuthIDs = []string{"credential-c"}
	if err := saveStateWithNativeHistory(path, state.Keys, state.Usage, state.Aliases, state.ClassifyRules, state.NativeKeyBindings, latest); err != nil {
		t.Fatal(err)
	}
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	state = assertPersistenceHistory(t, path, latest)
	if usage := state.Usage[nativeUsageLedgerID(NativeCallerScope(nativeTestKey))]; usage == nil || usage.Daily.CallCount != 1 {
		t.Fatalf("usage flush did not persist current counters: %#v", state.Usage)
	}
	store.mu.RLock()
	liveHistory := cloneNativeBindingHistory(store.nativeBindingHistory)
	store.mu.RUnlock()
	if !reflect.DeepEqual(liveHistory, oldHistory) {
		t.Fatal("fixture no longer has a stale in-memory history snapshot")
	}
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	store.mu.RLock()
	liveHistory = cloneNativeBindingHistory(store.nativeBindingHistory)
	store.mu.RUnlock()
	if !reflect.DeepEqual(liveHistory, latest) {
		t.Fatal("reconfigure did not load the persisted history")
	}
}

func TestNativeBindingHistoryConfigureUsesOnlySelectedStatePath(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("new_path_exists_%t", existing), func(t *testing.T) {
			dir := t.TempDir()
			firstPath, secondPath := filepath.Join(dir, "first.json"), filepath.Join(dir, "second.json")
			want := seedPersistenceHistory(t, firstPath)
			store := NewStore()
			if err := store.Configure(Config{Enabled: true, StateFile: firstPath}); err != nil {
				t.Fatal(err)
			}
			if existing {
				if err := SaveState(secondPath, nil, nil, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.Configure(Config{Enabled: true, StateFile: secondPath}); err != nil {
				t.Fatal(err)
			}
			if history := store.NativeBindingHistorySnapshot(); len(history) != 0 {
				t.Fatal("history leaked from the previous state path")
			}
			assertPersistenceHistory(t, secondPath, nil)
			assertPersistenceHistory(t, firstPath, want)
		})
	}
}

func TestNativeBindingHistorySurvivesLegacyBindingMigrations(t *testing.T) {
	for _, mode := range []string{"missing_bindings", "missing_model_access", "encoded_auth_ids"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			want := persistenceHistoryFixture()
			binding := NativeKeyBinding{ID: "legacy-native", Enabled: true, CallerScope: NativeCallerScope("sk-legacy-history"), Group: "team"}
			state := State{Version: 1, NativeBindingHistory: want}
			cfg := Config{Enabled: true, StateFile: path}
			switch mode {
			case "missing_bindings":
				cfg.NativeKeyBindings = []NativeKeyBinding{binding}
			case "missing_model_access":
				state.NativeKeyBindings = []NativeKeyBinding{binding}
			case "encoded_auth_ids":
				binding.Group = encodeNativeAuthIDsGroup([]string{"credential-a", "credential-b"})
				binding.ModelAccess.Mode = NativeModelAccessAll
				state.NativeKeyBindings = []NativeKeyBinding{binding}
			}
			raw, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := SaveUsageOnly(path, nil); err != nil {
				t.Fatal(err)
			}
			store := NewStore()
			if err := store.Configure(cfg); err != nil {
				t.Fatal(err)
			}
			loaded := assertPersistenceHistory(t, path, want)
			if len(loaded.NativeKeyBindings) != 1 || loaded.NativeKeyBindings[0].ModelAccess.Mode != NativeModelAccessAll {
				t.Fatalf("binding migration did not complete: %#v", loaded.NativeKeyBindings)
			}
			if mode == "encoded_auth_ids" && len(loaded.NativeKeyBindings[0].AuthIDs) != 2 {
				t.Fatal("migration did not recover encoded credential IDs")
			}
		})
	}
}

func TestNativeBindingHistoryInvalidStateIsNotOverwritten(t *testing.T) {
	for _, kind := range []string{"missing_id", "invalid_scope", "empty_restriction", "duplicate_operation", "too_many_operations", "too_many_bytes"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			history := persistenceHistoryFixture()
			switch kind {
			case "missing_id":
				history[0].ID = ""
			case "invalid_scope":
				history[0].Changes[0].CallerScope = "not-an-irreversible-identity"
			case "empty_restriction":
				history[0].Changes[0].After = &NativeBindingRestriction{}
			case "duplicate_operation":
				history = append(history, history[0])
			case "too_many_operations":
				for len(history) <= nativeBindingHistoryLimit {
					entry := persistenceHistoryFixture()[0]
					entry.ID = fmt.Sprintf("overflow-%d", len(history))
					history = append(history, entry)
				}
			case "too_many_bytes":
				history[0].Changes[0].Name = strings.Repeat("x", nativeBindingHistoryBytes)
			}
			raw, err := json.Marshal(State{Version: 1, NativeBindingHistory: history})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadState(path); err == nil {
				t.Fatal("LoadState accepted invalid history")
			}
			if err := SaveUsageOnly(path, map[string]*UsageState{}); err == nil {
				t.Fatal("SaveUsageOnly accepted invalid history")
			}
			if err := SaveState(path, nil, nil, nil, nil); err == nil {
				t.Fatal("SaveState overwrote invalid history")
			}
			if err := NewStore().Configure(Config{Enabled: true, StateFile: path}); err == nil {
				t.Fatal("Configure accepted invalid history")
			}
			current, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, current) {
				t.Fatal("a failed operation overwrote the invalid state")
			}
		})
	}
}

func TestNativeBindingHistoryExplicitSaveValidationPreservesExistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := seedPersistenceHistory(t, path)
	invalid := cloneNativeBindingHistory(want)
	invalid[0].Changes[0].CallerScope = "invalid"
	if err := saveStateWithNativeHistory(path, nil, nil, nil, nil, nil, invalid); err == nil {
		t.Fatal("explicit history save accepted invalid history")
	}
	assertPersistenceHistory(t, path, want)
}

func TestNativeBindingHistoryLegacyFilesRemainCompatible(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveUsageOnly(path, nil); err != nil {
		t.Fatal(err)
	}
	state := assertPersistenceHistory(t, path, nil)
	if state.NativeKeyBindings == nil {
		t.Fatal("a new state must retain an explicit empty binding list")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"native_binding_history"`)) {
		t.Fatal("empty history should remain omitted for legacy compatibility")
	}
}
