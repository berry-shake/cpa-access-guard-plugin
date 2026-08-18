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
	raw := []byte(`
enabled: true
native_key_bindings:
  - id: " Client-A "
    enabled: true
    caller_scope: "` + scopeA + `"
    group: " CLASSIFY: VIP "
  - id: client-b
    name: Client B
    enabled: false
    caller_scope: "` + scopeB + `"
    group: " TEAM "
`)
	cfg, err := DecodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.NativeKeyBindings) != 2 {
		t.Fatalf("bindings = %+v", cfg.NativeKeyBindings)
	}
	a := cfg.NativeKeyBindings[0]
	if a.ID != "client-a" || a.Name != "client-a" || a.CallerScope != strings.ToLower(scopeA) || a.Group != "classify:vip" {
		t.Fatalf("first binding not normalized: %+v", a)
	}
	b := cfg.NativeKeyBindings[1]
	if b.ID != "client-b" || b.Name != "Client B" || b.Group != "team" {
		t.Fatalf("second binding not normalized: %+v", b)
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
				{ID: "Client", CallerScope: scopeA, Group: "team"},
				{ID: "client", CallerScope: scopeB, Group: "free"},
			},
			want: "duplicate native key binding id",
		},
		{
			name: "duplicate scope case insensitive",
			bindings: []NativeKeyBinding{
				{ID: "a", CallerScope: strings.ToUpper(scopeA), Group: "team"},
				{ID: "b", CallerScope: scopeA, Group: "free"},
			},
			want: "same caller_scope",
		},
		{
			name:     "invalid scope",
			bindings: []NativeKeyBinding{{ID: "a", CallerScope: "not-a-scope", Group: "team"}},
			want:     "caller_scope must be a 64-character",
		},
		{
			name:     "empty group",
			bindings: []NativeKeyBinding{{ID: "a", CallerScope: scopeA}},
			want:     "group is required",
		},
		{
			name:     "empty classify suffix",
			bindings: []NativeKeyBinding{{ID: "a", CallerScope: scopeA, Group: " CLASSIFY:  "}},
			want:     "classify group suffix is required",
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
		Group:   " TEAM ",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstScope := NativeCallerScope(firstSecret)
	if created.ID != "native-b" || created.Name != "Native client" || created.Group != "team" || created.CallerScope != firstScope {
		t.Fatalf("created = %+v", created)
	}
	if created.KeyPreview == "" || created.KeyPreview == firstSecret || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("created secret/timestamps are unsafe or missing: %+v", created)
	}
	if group, ok := store.ResolveNativeKeyGroup(strings.ToUpper(firstScope), "codex", "gpt-any"); !ok || group != "team" {
		t.Fatalf("ResolveNativeKeyGroup() = %q, %v", group, ok)
	}
	status := store.Status()
	if status["native_key_binding_count"] != 1 || status["native_key_binding_enabled_count"] != 1 {
		t.Fatalf("native binding status counts = %#v", status)
	}

	if _, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "NATIVE-B", Enabled: true, APIKey: "sk-another-long-native-secret", Group: "free",
	}); !errors.Is(err, ErrNativeKeyBindingExists) {
		t.Fatalf("duplicate ID error = %v", err)
	}
	if _, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "other", Enabled: true, APIKey: firstSecret, Group: "free",
	}); err == nil || !strings.Contains(err.Error(), "same caller_scope") {
		t.Fatalf("duplicate scope error = %v", err)
	}

	updated, err := store.UpdateNativeKeyBinding("NATIVE-B", UpdateNativeKeyBindingInput{
		Name: nativeStringPtr("Renamed"), Enabled: nativeBoolPtr(false), Group: nativeStringPtr(" CLASSIFY: VIP "),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CallerScope != firstScope || updated.KeyPreview != created.KeyPreview || updated.Group != "classify:vip" || updated.Enabled {
		t.Fatalf("update without rotation = %+v", updated)
	}
	if _, ok := store.ResolveNativeKeyGroup(firstScope, "", ""); ok {
		t.Fatal("disabled binding resolved")
	}

	const secondSecret = "sk-rotated-native-super-secret-fedcba9876543210"
	rotated, err := store.UpdateNativeKeyBinding("native-b", UpdateNativeKeyBindingInput{
		Name: nativeStringPtr("Renamed"), Enabled: nativeBoolPtr(true), APIKey: secondSecret, Group: nativeStringPtr("free"),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondScope := NativeCallerScope(secondSecret)
	if rotated.CallerScope != secondScope || rotated.CallerScope == firstScope || rotated.KeyPreview == secondSecret {
		t.Fatalf("rotated = %+v", rotated)
	}
	if _, ok := store.ResolveNativeKeyGroup(firstScope, "codex", "m"); ok {
		t.Fatal("old caller scope still resolved after rotation")
	}
	if group, ok := store.ResolveNativeKeyGroup(secondScope, "claude", "m"); !ok || group != "free" {
		t.Fatalf("new caller scope = %q, %v", group, ok)
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
	if group, ok := reloaded.ResolveNativeKeyGroup(secondScope, "codex", "m"); !ok || group != "free" {
		t.Fatalf("reloaded resolve = %q, %v", group, ok)
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

func TestLegacyStateBootstrapsNativeBindingsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"keys":[],"usage":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	binding := NativeKeyBinding{
		ID: "legacy", Enabled: true, CallerScope: strings.Repeat("c", 64), Group: "team",
	}
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path, NativeKeyBindings: []NativeKeyBinding{binding}}); err != nil {
		t.Fatal(err)
	}
	if group, ok := store.ResolveNativeKeyGroup(binding.CallerScope, "", ""); !ok || group != "team" {
		t.Fatalf("legacy bootstrap resolve = %q, %v", group, ok)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if state.NativeKeyBindings == nil || len(state.NativeKeyBindings) != 1 {
		t.Fatalf("legacy state was not migrated: %#v", state.NativeKeyBindings)
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
		ID: "durable", Enabled: true, APIKey: firstSecret, Group: "team",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Replace the state directory with a regular file so every subsequent
	// atomic save fails deterministically, even when tests run as root.
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
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
		ID: "new", Enabled: true, APIKey: secondSecret, Group: "free",
	}); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("create persistence error = %v", err)
	}
	if _, ok := store.ResolveNativeKeyGroup(NativeCallerScope(secondSecret), "", ""); ok {
		t.Fatal("failed create became active in memory")
	}

	if _, err := store.UpdateNativeKeyBinding(first.ID, UpdateNativeKeyBindingInput{
		Enabled: nativeBoolPtr(false), Group: nativeStringPtr("free"),
	}); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("update persistence error = %v", err)
	}
	if group, ok := store.ResolveNativeKeyGroup(first.CallerScope, "", ""); !ok || group != "team" {
		t.Fatalf("failed update changed live policy: group=%q ok=%v", group, ok)
	}

	if err := store.DeleteNativeKeyBinding(first.ID); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("delete persistence error = %v", err)
	}
	if group, ok := store.ResolveNativeKeyGroup(first.CallerScope, "", ""); !ok || group != "team" {
		t.Fatalf("failed delete removed live isolation: group=%q ok=%v", group, ok)
	}
	bindings := store.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || bindings[0].ID != first.ID || !bindings[0].Enabled || bindings[0].Group != "team" {
		t.Fatalf("live bindings changed after failed persistence: %+v", bindings)
	}
}

func TestConcurrentPartialNativeKeyBindingUpdatesDoNotLoseFields(t *testing.T) {
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: filepath.Join(t.TempDir(), "state.json")}); err != nil {
		t.Fatal(err)
	}
	created, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "client", Enabled: true, APIKey: "sk-concurrent-native-secret-0123456789", Group: "team",
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
		_, errUpdate := store.UpdateNativeKeyBinding(created.ID, UpdateNativeKeyBindingInput{Group: nativeStringPtr("classify:vip")})
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
	if len(bindings) != 1 || bindings[0].Enabled || bindings[0].Group != "classify:vip" {
		t.Fatalf("partial update was lost: %+v", bindings)
	}
}
