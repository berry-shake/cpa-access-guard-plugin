package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestNativeCallerScopeMatchesCPA(t *testing.T) {
	const want = "3221316eb769c26e286abf5ea9fffd93fdda082858b47ada32e4a12ea606e2e1"
	if got := NativeCallerScope("downstream-secret"); got != want {
		t.Fatalf("NativeCallerScope() = %q, want CPA scope %q", got, want)
	}
	if got := NativeCallerScope("  downstream-secret\n"); got != want {
		t.Fatalf("NativeCallerScope() did not trim like CPA: %q", got)
	}
	if got := NativeCallerScope(" \t "); got != "" {
		t.Fatalf("NativeCallerScope(blank) = %q, want empty", got)
	}
}

func TestDecodeConfigNormalizesNativeKeyBindings(t *testing.T) {
	scopeA := strings.Repeat("A", 64)
	scopeB := strings.Repeat("b", 64)
	scopeC := strings.Repeat("c", 64)
	raw := []byte(`
enabled: true
native_key_bindings:
  - id: " Client-A "
    enabled: true
    caller_scope: "` + scopeA + `"
    group: " CLASSIFY:VIP "
  - id: client-b
    name: Client B
    enabled: false
    caller_scope: "` + scopeB + `"
    group: " TEAM "
  - id: client-c
    enabled: true
    caller_scope: "` + scopeC + `"
    auth_ids:
      - " tenant/codex-B.json "
      - "tenant/codex-A.json"
      - "tenant/codex-B.json"
`)
	cfg, err := DecodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.NativeKeyBindings) != 3 {
		t.Fatalf("bindings = %+v", cfg.NativeKeyBindings)
	}
	a := cfg.NativeKeyBindings[0]
	if a.ID != "client-a" || a.Name != "" || a.CallerScope != strings.ToLower(scopeA) || a.Group != "classify:vip" || !NativeKeyBindingNeedsReselection(a) {
		t.Fatalf("first binding not normalized: %+v", a)
	}
	b := cfg.NativeKeyBindings[1]
	if b.ID != "client-b" || b.Name != "Client B" || NativeKeyBindingLegacyGroup(b) != "team" {
		t.Fatalf("second binding not normalized: %+v", b)
	}
	c := cfg.NativeKeyBindings[2]
	if c.Group != encodeNativeAuthIDsGroup(c.AuthIDs) || len(c.AuthIDs) != 2 ||
		c.AuthIDs[0] != "tenant/codex-A.json" || c.AuthIDs[1] != "tenant/codex-B.json" {
		t.Fatalf("direct binding not normalized: %+v", c)
	}
}

func TestNativeKeyBindingValidation(t *testing.T) {
	scopeA := strings.Repeat("a", 64)
	scopeB := strings.Repeat("b", 64)
	tests := []struct {
		name     string
		bindings []NativeKeyBinding
		want     string
	}{
		{
			name: "duplicate id case insensitive",
			bindings: []NativeKeyBinding{
				{ID: "Client", CallerScope: scopeA, AuthIDs: []string{"a.json"}},
				{ID: "client", CallerScope: scopeB, AuthIDs: []string{"b.json"}},
			},
			want: "duplicate native key binding id",
		},
		{
			name: "duplicate scope case insensitive",
			bindings: []NativeKeyBinding{
				{ID: "a", CallerScope: strings.ToUpper(scopeA), AuthIDs: []string{"a.json"}},
				{ID: "b", CallerScope: scopeA, AuthIDs: []string{"b.json"}},
			},
			want: "same caller_scope",
		},
		{
			name:     "invalid scope",
			bindings: []NativeKeyBinding{{ID: "a", CallerScope: "not-a-scope", AuthIDs: []string{"a.json"}}},
			want:     "caller_scope must be a 64-character",
		},
		{
			name:     "no auth ids",
			bindings: []NativeKeyBinding{{ID: "a", CallerScope: scopeA}},
			want:     "auth_ids is required",
		},
		{
			name: "group and auth ids",
			bindings: []NativeKeyBinding{{
				ID: "a", CallerScope: scopeA, Group: "team", AuthIDs: []string{"codex-a.json"},
			}},
			want: "group and auth_ids are mutually exclusive",
		},
		{
			name: "blank auth ids",
			bindings: []NativeKeyBinding{{
				ID: "a", CallerScope: scopeA, AuthIDs: []string{" ", "\t"},
			}},
			want: "auth_ids is required",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{NativeKeyBindings: tc.bindings}
			err := normalizeConfig(&cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("normalizeConfig() error = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestNativeKeyBindingCRUDPersistenceAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}

	const firstSecret = "sk-native-super-secret-0123456789abcdef"
	created, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID:      " Native-B ",
		Name:    " Native client ",
		Enabled: true,
		APIKey:  firstSecret,
		AuthIDs: []string{" team.json "},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstScope := NativeCallerScope(firstSecret)
	if created.ID != "native-b" || created.Name != "Native client" || !sameAuthIDs(created.AuthIDs, "team.json") || created.CallerScope != firstScope {
		t.Fatalf("created = %+v", created)
	}
	if created.KeyPreview == "" || created.KeyPreview == firstSecret || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created secret/timestamps are unsafe or missing: %+v", created)
	}
	if authIDs, ok := nativeAuthIDsOf(store, strings.ToUpper(firstScope)); !ok || !sameAuthIDs(authIDs, "team.json") {
		t.Fatalf("ResolveNativeKeyConstraint() = %q, %v", authIDs, ok)
	}
	status := store.Status()
	if status["native_key_binding_count"] != 1 || status["native_key_binding_enabled_count"] != 1 {
		t.Fatalf("native binding status counts = %#v", status)
	}

	if _, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "NATIVE-B", Enabled: true, APIKey: "sk-another-long-native-secret", AuthIDs: []string{"free.json"},
	}); !errors.Is(err, ErrNativeKeyBindingExists) {
		t.Fatalf("duplicate ID error = %v", err)
	}
	if _, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "other", Enabled: true, APIKey: firstSecret, AuthIDs: []string{"free.json"},
	}); err == nil || !strings.Contains(err.Error(), "same caller_scope") {
		t.Fatalf("duplicate scope error = %v", err)
	}

	updated, err := store.UpdateNativeKeyBinding("NATIVE-B", UpdateNativeKeyBindingInput{
		Name: nativeStringPtr("Renamed"), Enabled: nativeBoolPtr(false), AuthIDs: nativeStringsPtr("vip.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CallerScope != firstScope || updated.KeyPreview != created.KeyPreview || !sameAuthIDs(updated.AuthIDs, "vip.json") || updated.Enabled {
		t.Fatalf("update without rotation = %+v", updated)
	}
	if _, ok := nativeAuthIDsOf(store, firstScope); ok {
		t.Fatal("disabled binding resolved")
	}

	const secondSecret = "sk-rotated-native-super-secret-fedcba9876543210"
	rotated, err := store.UpdateNativeKeyBinding("native-b", UpdateNativeKeyBindingInput{
		Name: nativeStringPtr("Renamed"), Enabled: nativeBoolPtr(true), APIKey: secondSecret, AuthIDs: nativeStringsPtr("free.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondScope := NativeCallerScope(secondSecret)
	if rotated.CallerScope != secondScope || rotated.CallerScope == firstScope || rotated.KeyPreview == secondSecret {
		t.Fatalf("rotated = %+v", rotated)
	}
	if _, ok := nativeAuthIDsOf(store, firstScope); ok {
		t.Fatal("old caller scope still resolved after rotation")
	}
	if authIDs, ok := nativeAuthIDsOf(store, secondScope); !ok || !sameAuthIDs(authIDs, "free.json") {
		t.Fatalf("new caller scope = %q, %v", authIDs, ok)
	}

	rawState, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawState), firstSecret) || strings.Contains(string(rawState), secondSecret) {
		t.Fatalf("plaintext native API key persisted: %s", rawState)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.NativeKeyBindings) != 1 || loaded.NativeKeyBindings[0].CallerScope != secondScope {
		t.Fatalf("persisted bindings = %+v", loaded.NativeKeyBindings)
	}

	reloaded := NewStore()
	if err := reloaded.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	if authIDs, ok := nativeAuthIDsOf(reloaded, secondScope); !ok || !sameAuthIDs(authIDs, "free.json") {
		t.Fatalf("reloaded resolve = %q, %v", authIDs, ok)
	}
	bindings := reloaded.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || bindings[0].ID != "native-b" {
		t.Fatalf("reloaded snapshot = %+v", bindings)
	}

	if err := reloaded.DeleteNativeKeyBinding("NATIVE-B"); err != nil {
		t.Fatal(err)
	}
	if err := reloaded.DeleteNativeKeyBinding("native-b"); !errors.Is(err, ErrUnknownNativeKeyBinding) {
		t.Fatalf("delete unknown error = %v", err)
	}
	afterDelete, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if afterDelete.NativeKeyBindings == nil || len(afterDelete.NativeKeyBindings) != 0 {
		t.Fatalf("delete must persist explicit empty bindings, got %#v", afterDelete.NativeKeyBindings)
	}

	// An explicit empty state is authoritative: the config copy must not
	// resurrect a binding deliberately deleted through management.
	noResurrection := NewStore()
	if err := noResurrection.Configure(Config{Enabled: true, StateFile: path, NativeKeyBindings: []NativeKeyBinding{rotated}}); err != nil {
		t.Fatal(err)
	}
	if got := noResurrection.NativeKeyBindingsSnapshot(); len(got) != 0 {
		t.Fatalf("deleted binding resurrected from config: %+v", got)
	}
}

func nativeStringPtr(value string) *string { return &value }

func nativeBoolPtr(value bool) *bool { return &value }

func nativeStringsPtr(values ...string) *[]string { return &values }

// nativeAuthIDsOf resolves the auth IDs an enabled binding admits.
func nativeAuthIDsOf(store *Store, scope string) ([]string, bool) {
	constraint, ok := store.ResolveNativeKeyConstraint(scope)
	return constraint.AuthIDs, ok
}

func sameAuthIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestNativeKeyBindingDirectAuthIDsPersistenceAndModeSwitch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}

	const secret = "sk-native-direct-secret-0123456789abcdef"
	created, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID:      "direct",
		Enabled: true,
		APIKey:  secret,
		AuthIDs: []string{" tenant/codex-B.json ", "tenant/codex-A.json", "tenant/codex-B.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Group != encodeNativeAuthIDsGroup(created.AuthIDs) || len(created.AuthIDs) != 2 ||
		created.AuthIDs[0] != "tenant/codex-A.json" || created.AuthIDs[1] != "tenant/codex-B.json" {
		t.Fatalf("created direct binding = %+v", created)
	}
	constraint, ok := store.ResolveNativeKeyConstraint(created.CallerScope)
	if !ok || len(constraint.AuthIDs) != 2 {
		t.Fatalf("direct constraint = %+v, %v", constraint, ok)
	}

	constraint.AuthIDs[0] = "mutated"
	snapshot := store.NativeKeyBindingsSnapshot()
	snapshot[0].AuthIDs[0] = "also-mutated"
	fresh, ok := store.ResolveNativeKeyConstraint(created.CallerScope)
	if !ok || fresh.AuthIDs[0] != "tenant/codex-A.json" {
		t.Fatalf("store leaked mutable auth_ids slice: %+v", fresh)
	}

	stateRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stateRaw), `"auth_ids"`) || !strings.Contains(string(stateRaw), nativeAuthIDsFailClosedGroup) {
		t.Fatalf("direct binding state lacks auth_ids or fail-closed group: %s", stateRaw)
	}

	reloaded := NewStore()
	if err := reloaded.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	if got, okReload := reloaded.ResolveNativeKeyConstraint(created.CallerScope); !okReload || len(got.AuthIDs) != 2 {
		t.Fatalf("reloaded direct constraint = %+v, %v", got, okReload)
	}

	directMode, err := reloaded.UpdateNativeKeyBinding("direct", UpdateNativeKeyBindingInput{
		AuthIDs: nativeStringsPtr("codex-Z.json", "codex-Y.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if directMode.Group != encodeNativeAuthIDsGroup(directMode.AuthIDs) || len(directMode.AuthIDs) != 2 || directMode.AuthIDs[0] != "codex-Y.json" {
		t.Fatalf("direct mode = %+v", directMode)
	}

	if _, errEmpty := reloaded.UpdateNativeKeyBinding("direct", UpdateNativeKeyBindingInput{
		AuthIDs: nativeStringsPtr(),
	}); errEmpty == nil || !strings.Contains(errEmpty.Error(), "auth_ids is required") {
		t.Fatalf("empty direct restriction error = %v", errEmpty)
	}
	if got, okFinal := reloaded.ResolveNativeKeyConstraint(created.CallerScope); !okFinal || len(got.AuthIDs) != 2 {
		t.Fatalf("failed updates changed live constraint: %+v, %v", got, okFinal)
	}
}

func TestDirectNativeBindingRecoversAuthIDsFromRedundantGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	scope := strings.Repeat("d", 64)
	wantAuthIDs := []string{"Tenant/Codex-A.JSON", "tenant/codex-b.json"}
	encodedGroup := encodeNativeAuthIDsGroup(wantAuthIDs)
	if encodedGroup != strings.ToLower(encodedGroup) {
		t.Fatalf("redundant group is not lowercase-safe: %q", encodedGroup)
	}
	if err := SaveState(path, map[string]*UsageState{}, []NativeKeyBinding{{
		ID: "recover-direct", Enabled: true, CallerScope: scope, Group: encodedGroup,
	}}); err != nil {
		t.Fatal(err)
	}

	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatalf("Configure() did not recover redundant auth IDs: %v", err)
	}
	bindings := store.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || len(bindings[0].AuthIDs) != 2 ||
		bindings[0].AuthIDs[0] != wantAuthIDs[0] || bindings[0].AuthIDs[1] != wantAuthIDs[1] {
		t.Fatalf("recovered bindings = %+v", bindings)
	}
	if NativeKeyBindingNeedsReselection(bindings[0]) {
		t.Fatalf("recovered binding still requires reselection: %+v", bindings[0])
	}
	constraint, ok := store.ResolveNativeKeyConstraint(scope)
	if !ok || len(constraint.AuthIDs) != 2 || constraint.AuthIDs[0] != wantAuthIDs[0] {
		t.Fatalf("recovered constraint = %+v, %v", constraint, ok)
	}

	persisted, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.NativeKeyBindings) != 1 || len(persisted.NativeKeyBindings[0].AuthIDs) != 2 {
		t.Fatalf("recovered auth IDs were not repaired on disk: %+v", persisted.NativeKeyBindings)
	}
}

func TestDegradedDirectNativeBindingLoadsFailClosedAndCanBeRepaired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	scope := strings.Repeat("e", 64)
	if err := SaveState(path, map[string]*UsageState{}, []NativeKeyBinding{{
		ID: "degraded-direct", Enabled: true, CallerScope: scope, Group: nativeAuthIDsFailClosedGroup,
	}}); err != nil {
		t.Fatal(err)
	}

	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatalf("Configure() rejected a degraded direct binding: %v", err)
	}
	bindings := store.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || !NativeKeyBindingNeedsReselection(bindings[0]) {
		t.Fatalf("degraded binding was not identified: %+v", bindings)
	}
	constraint, ok := store.ResolveNativeKeyConstraint(scope)
	if !ok || len(constraint.AuthIDs) != 0 || NativeKeyBindingLegacyGroup(bindings[0]) != "" {
		t.Fatalf("degraded constraint = %+v, %v; want fail-closed without a visible group", constraint, ok)
	}

	repaired, err := store.UpdateNativeKeyBinding("degraded-direct", UpdateNativeKeyBindingInput{
		AuthIDs: nativeStringsPtr("restart-selected.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if NativeKeyBindingNeedsReselection(repaired) || len(repaired.AuthIDs) != 1 ||
		repaired.Group != encodeNativeAuthIDsGroup(repaired.AuthIDs) {
		t.Fatalf("repaired binding = %+v", repaired)
	}
}

func TestLegacyStateBootstrapsNativeBindingsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"keys":[],"usage":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binding := NativeKeyBinding{
		ID: "legacy", Enabled: true, CallerScope: strings.Repeat("c", 64), AuthIDs: []string{"team.json"},
	}
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path, NativeKeyBindings: []NativeKeyBinding{binding}}); err != nil {
		t.Fatal(err)
	}
	if authIDs, ok := nativeAuthIDsOf(store, binding.CallerScope); !ok || !sameAuthIDs(authIDs, "team.json") {
		t.Fatalf("legacy bootstrap resolve = %q, %v", authIDs, ok)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.NativeKeyBindings == nil || len(state.NativeKeyBindings) != 1 {
		t.Fatalf("legacy state was not migrated: %#v", state.NativeKeyBindings)
	}
}

func TestLegacyNativeBindingStateMigratesModelAccessToAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	raw := `{"version":1,"keys":[],"native_key_bindings":[{"id":"legacy-models","enabled":true,"caller_scope":"` +
		strings.Repeat("e", 64) + `","group":"team"}],"aliases":[],"classify_rules":[],"usage":{}}`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	binding := store.NativeKeyBindingsSnapshot()[0]
	if binding.ModelAccess.Mode != NativeModelAccessAll || len(binding.ModelAccess.Models) != 0 {
		t.Fatalf("live migrated model access = %+v", binding.ModelAccess)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.NativeKeyBindings[0].ModelAccess.Mode != NativeModelAccessAll || len(state.NativeKeyBindings[0].ModelAccess.Models) != 0 {
		t.Fatalf("persisted migrated model access = %+v", state.NativeKeyBindings[0].ModelAccess)
	}
}

func TestSaveUsageOnlyCreatesExplicitEmptyNativeBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := SaveUsageOnly(path, map[string]*UsageState{}); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.NativeKeyBindings == nil || len(state.NativeKeyBindings) != 0 {
		t.Fatalf("new usage-only state bindings = %#v, want explicit []", state.NativeKeyBindings)
	}
}

func TestNativeKeyBindingPersistenceFailureDoesNotPublish(t *testing.T) {
	root := t.TempDir()
	statePath := filepath.Join(root, "state", "state.json")
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: statePath}); err != nil {
		t.Fatal(err)
	}
	const firstSecret = "sk-durable-native-secret-0123456789"
	first, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "durable", Enabled: true, APIKey: firstSecret, AuthIDs: []string{"team.json"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Replace the state directory with a regular file so every subsequent
	// atomic save fails deterministically, even when tests run as root.
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if pricingPath := store.PricingPath(); pricingPath != "" {
		_ = os.Remove(pricingPath)
	}
	stateDir := filepath.Dir(statePath)
	if err := os.Remove(stateDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	const secondSecret = "sk-undurable-native-secret-9876543210"
	if _, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "new", Enabled: true, APIKey: secondSecret, AuthIDs: []string{"free.json"},
	}); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("create persistence error = %v", err)
	}
	if _, ok := nativeAuthIDsOf(store, NativeCallerScope(secondSecret)); ok {
		t.Fatal("failed create became active in memory")
	}

	if _, err := store.UpdateNativeKeyBinding(first.ID, UpdateNativeKeyBindingInput{
		Enabled: nativeBoolPtr(false), AuthIDs: nativeStringsPtr("free.json"),
	}); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("update persistence error = %v", err)
	}
	if authIDs, ok := nativeAuthIDsOf(store, first.CallerScope); !ok || !sameAuthIDs(authIDs, "team.json") {
		t.Fatalf("failed update changed live policy: auth_ids=%q ok=%v", authIDs, ok)
	}

	if err := store.DeleteNativeKeyBinding(first.ID); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("delete persistence error = %v", err)
	}
	if authIDs, ok := nativeAuthIDsOf(store, first.CallerScope); !ok || !sameAuthIDs(authIDs, "team.json") {
		t.Fatalf("failed delete removed live isolation: auth_ids=%q ok=%v", authIDs, ok)
	}
	bindings := store.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || bindings[0].ID != first.ID || !bindings[0].Enabled || !sameAuthIDs(bindings[0].AuthIDs, "team.json") {
		t.Fatalf("live bindings changed after failed persistence: %+v", bindings)
	}
}

func TestConcurrentPartialNativeKeyBindingUpdatesDoNotLoseFields(t *testing.T) {
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(t.TempDir(), "state.json")}); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "client", Enabled: true, APIKey: "sk-concurrent-native-secret-0123456789", AuthIDs: []string{"team.json"},
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		_, errUpdate := store.UpdateNativeKeyBinding(created.ID, UpdateNativeKeyBindingInput{Enabled: nativeBoolPtr(false)})
		errs <- errUpdate
	}()
	go func() {
		defer wg.Done()
		<-start
		_, errUpdate := store.UpdateNativeKeyBinding(created.ID, UpdateNativeKeyBindingInput{AuthIDs: nativeStringsPtr("vip.json")})
		errs <- errUpdate
	}()
	close(start)
	wg.Wait()
	close(errs)
	for errUpdate := range errs {
		if errUpdate != nil {
			t.Fatal(errUpdate)
		}
	}

	bindings := store.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || bindings[0].Enabled || !sameAuthIDs(bindings[0].AuthIDs, "vip.json") {
		t.Fatalf("partial update was lost: %+v", bindings)
	}
}

func TestNativeKeyBindingModelAccessPersistenceAndMatching(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	access := NativeModelAccessPolicy{
		Mode: " ALLOWLIST ",
		Models: []NativeAllowedModel{
			{Provider: " Gemini ", Model: "GPT-5.6-LUNA"},
			{Provider: " CODEX ", Model: " GPT-5.6-LUNA(HIGH) "},
			{Provider: "codex", Model: "gpt-5.6-luna"},
		},
	}
	created, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "models", Enabled: true, APIKey: "sk-native-model-secret-0123456789", AuthIDs: []string{"team.json"}, ModelAccess: &access,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ModelAccess.Mode != NativeModelAccessAllowlist || len(created.ModelAccess.Models) != 2 ||
		created.ModelAccess.Models[0] != (NativeAllowedModel{Provider: "codex", Model: "gpt-5.6-luna"}) ||
		created.ModelAccess.Models[1] != (NativeAllowedModel{Provider: "gemini", Model: "gpt-5.6-luna"}) {
		t.Fatalf("normalized model access = %+v", created.ModelAccess)
	}
	constraint, ok := store.ResolveNativeKeyConstraint(created.CallerScope)
	if !ok || !constraint.AllowsModel("codex", "gpt-5.6-luna(high)") ||
		!constraint.AllowsModel("", "GPT-5.6-LUNA") || constraint.AllowsModel("claude", "gpt-5.6-luna") {
		t.Fatalf("unexpected model matching: ok=%v constraint=%+v", ok, constraint)
	}

	// Returned snapshots must not share mutable model slices or runtime indexes
	// with the live authorization policy.
	snapshot := store.NativeKeyBindingsSnapshot()
	snapshot[0].ModelAccess.Models[0].Model = "tampered"
	if fresh, _ := store.ResolveNativeKeyConstraint(created.CallerScope); !fresh.AllowsModel("codex", "gpt-5.6-luna") {
		t.Fatal("mutating a snapshot changed live model authorization")
	}

	reloaded := NewStore()
	if err := reloaded.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	if fresh, okReload := reloaded.ResolveNativeKeyConstraint(created.CallerScope); !okReload || !fresh.AllowsModel("gemini", "gpt-5.6-luna") || fresh.AllowsModel("claude", "gpt-5.6-luna") {
		t.Fatalf("reloaded model access mismatch: ok=%v constraint=%+v", okReload, fresh)
	}

	empty := NativeModelAccessPolicy{Mode: NativeModelAccessAllowlist}
	updated, err := reloaded.UpdateNativeKeyBinding(created.ID, UpdateNativeKeyBindingInput{ModelAccess: &empty})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ModelAccess.Mode != NativeModelAccessAllowlist || len(updated.ModelAccess.Models) != 0 {
		t.Fatalf("empty allow-list was not preserved: %+v", updated.ModelAccess)
	}
	if denied, _ := reloaded.ResolveNativeKeyConstraint(created.CallerScope); denied.AllowsModel("codex", "gpt-5.6-luna") {
		t.Fatal("empty allow-list must deny every model")
	}

	all := NativeModelAccessPolicy{
		Mode:   NativeModelAccessAll,
		Models: []NativeAllowedModel{{Provider: "codex", Model: "stale-model"}},
	}
	updated, err = reloaded.UpdateNativeKeyBinding(created.ID, UpdateNativeKeyBindingInput{ModelAccess: &all})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ModelAccess.Mode != NativeModelAccessAll || len(updated.ModelAccess.Models) != 0 {
		t.Fatalf("all mode retained stale entries: %+v", updated.ModelAccess)
	}
	if unrestricted, _ := reloaded.ResolveNativeKeyConstraint(created.CallerScope); !unrestricted.AllowsModel("future-provider", "future-model") {
		t.Fatal("all mode must allow future provider/model pairs")
	}
}

func TestNativeKeyBindingMissingModelAccessMigratesToAll(t *testing.T) {
	scope := strings.Repeat("f", 64)
	cfg := Config{NativeKeyBindings: []NativeKeyBinding{{
		ID: "legacy", Enabled: true, CallerScope: scope, Group: "team",
	}}}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.NativeKeyBindings[0].ModelAccess.Mode != NativeModelAccessAll || len(cfg.NativeKeyBindings[0].ModelAccess.Models) != 0 {
		t.Fatalf("legacy policy = %+v", cfg.NativeKeyBindings[0].ModelAccess)
	}

	invalid := cfg.NativeKeyBindings[0]
	invalid.ModelAccess = NativeModelAccessPolicy{
		Mode:   NativeModelAccessAllowlist,
		Models: []NativeAllowedModel{{Provider: "", Model: "gpt-5.6-luna"}},
	}
	if err := normalizeNativeKeyBindings([]NativeKeyBinding{invalid}); err == nil || !strings.Contains(err.Error(), "provider is required") {
		t.Fatalf("invalid provider error = %v", err)
	}
	invalid.ModelAccess = NativeModelAccessPolicy{Mode: "surprise"}
	if err := normalizeNativeKeyBindings([]NativeKeyBinding{invalid}); err == nil || !strings.Contains(err.Error(), "model_access.mode") {
		t.Fatalf("invalid mode error = %v", err)
	}
}

// TestLegacyGroupBindingFailsClosedUntilReselected: a credential group saved
// by an older release loads, admits nothing, and is repaired by selecting
// exact auth IDs.
func TestLegacyGroupBindingFailsClosedUntilReselected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	scope := NativeCallerScope("sk-legacy-group-binding-0123")
	if err := SaveState(path, nil, []NativeKeyBinding{{
		ID: "legacy-group", Enabled: true, CallerScope: scope, Group: "Team",
		ModelAccess: NativeModelAccessPolicy{Mode: NativeModelAccessAll},
	}}); err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	binding := store.NativeKeyBindingsSnapshot()[0]
	if !NativeKeyBindingNeedsReselection(binding) || NativeKeyBindingLegacyGroup(binding) != "team" {
		t.Fatalf("legacy binding = %+v", binding)
	}
	if authIDs, ok := nativeAuthIDsOf(store, scope); !ok || len(authIDs) != 0 {
		t.Fatalf("legacy group constraint = %q, %v; want bound without auth IDs", authIDs, ok)
	}
	// Renaming keeps the legacy group visible for the operator.
	renamed, err := store.UpdateNativeKeyBinding("legacy-group", UpdateNativeKeyBindingInput{Name: nativeStringPtr("Old")})
	if err != nil || NativeKeyBindingLegacyGroup(renamed) != "team" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	repaired, err := store.UpdateNativeKeyBinding("legacy-group", UpdateNativeKeyBindingInput{AuthIDs: nativeStringsPtr("a.json")})
	if err != nil {
		t.Fatal(err)
	}
	if NativeKeyBindingNeedsReselection(repaired) || repaired.Group != encodeNativeAuthIDsGroup(repaired.AuthIDs) {
		t.Fatalf("repaired = %+v", repaired)
	}
	if _, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{ID: "no-auth", Enabled: true, APIKey: "sk-no-auth-ids-0123456789"}); err == nil || !strings.Contains(err.Error(), "auth_ids is required") {
		t.Fatalf("create without auth_ids error = %v", err)
	}
}
