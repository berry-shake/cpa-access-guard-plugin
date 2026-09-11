package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeRoundRobinConfigDefaultsAndNormalization(t *testing.T) {
	raw := []byte(`
enabled: true
native_key_bindings:
  - id: " Legacy "
    enabled: true
    caller_scope: "` + NativeCallerScope("legacy-round-robin-key") + `"
    group: " TEAM "
  - id: " Translation "
    enabled: true
    round_robin: true
    caller_scope: "` + NativeCallerScope("translation-round-robin-key") + `"
    auth_ids: [" codex-B.json ", "codex-A.json", "codex-B.json"]
    model_access:
      mode: allowlist
      models:
        - provider: codex
          model: gpt-5.3-codex-spark
`)
	cfg, err := DecodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.NativeKeyBindings) != 2 {
		t.Fatalf("bindings = %+v", cfg.NativeKeyBindings)
	}
	legacy, translation := cfg.NativeKeyBindings[0], cfg.NativeKeyBindings[1]
	if legacy.ID != "legacy" || legacy.Group != "team" || legacy.RoundRobin {
		t.Fatalf("legacy config must retain default scheduling: %+v", legacy)
	}
	if translation.ID != "translation" || !translation.RoundRobin || strings.Join(translation.AuthIDs, ",") != "codex-A.json,codex-B.json" {
		t.Fatalf("enabled normalized config = %+v", translation)
	}
	cloned := cloneNativeKeyBinding(translation)
	if !cloned.RoundRobin {
		t.Fatal("clone lost round_robin")
	}
	cloned.RoundRobin = false
	cloned.AuthIDs[0] = "another.json"
	if !translation.RoundRobin || translation.AuthIDs[0] != "codex-A.json" {
		t.Fatalf("clone mutation changed source: %+v", translation)
	}

	for _, want := range []bool{false, true} {
		translation.RoundRobin = want
		encoded, errMarshal := json.Marshal(translation)
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		var decoded NativeKeyBinding
		if errUnmarshal := json.Unmarshal(encoded, &decoded); errUnmarshal != nil {
			t.Fatal(errUnmarshal)
		}
		if decoded.RoundRobin != want {
			t.Fatalf("JSON round-trip round_robin = %v, want %v", decoded.RoundRobin, want)
		}
	}
	var absent NativeKeyBinding
	if errUnmarshal := json.Unmarshal([]byte(`{"id":"old-state"}`), &absent); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if absent.RoundRobin {
		t.Fatal("missing JSON round_robin must default to false")
	}
}

func TestNativeRoundRobinPersistenceAndPartialUpdates(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: statePath}); err != nil {
		t.Fatal(err)
	}
	const secret = "sk-native-round-robin-translation-0123456789"
	access := NativeModelAccessPolicy{Mode: NativeModelAccessAllowlist, Models: []NativeAllowedModel{{Provider: "codex", Model: "gpt-5.3-codex-spark"}}}
	binding, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "translation", Enabled: true, APIKey: secret, RoundRobin: true,
		AuthIDs: []string{"codex-A.json", "codex-B.json"}, ModelAccess: &access,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !binding.RoundRobin {
		t.Fatal("create lost enabled round_robin")
	}

	assertLive := func(t *testing.T, current *Store, want bool) {
		t.Helper()
		snapshot := current.NativeKeyBindingsSnapshot()
		if len(snapshot) != 1 || snapshot[0].RoundRobin != want {
			t.Fatalf("snapshot = %+v, want round_robin %v", snapshot, want)
		}
		constraint, ok := current.ResolveNativeKeyConstraint(binding.CallerScope, "codex", "gpt-5.3-codex-spark")
		if !ok || constraint.RoundRobin != want || len(constraint.AuthIDs) != 2 {
			t.Fatalf("constraint = %+v, resolved = %v, want round_robin %v", constraint, ok, want)
		}
		if !constraint.AllowsModel("codex", "gpt-5.3-codex-spark") || constraint.AllowsModel("codex", "gpt-5.6") || constraint.AllowsModel("claude", "gpt-5.3-codex-spark") {
			t.Fatal("round_robin changed model authorization")
		}
		// Callers can modify snapshots without changing the live constraint.
		snapshot[0].RoundRobin = !want
		snapshot[0].AuthIDs[0] = "unbound.json"
		fresh, _ := current.ResolveNativeKeyConstraint(binding.CallerScope, "codex", "gpt-5.3-codex-spark")
		if fresh.RoundRobin != want || fresh.AuthIDs[0] != "codex-A.json" {
			t.Fatalf("snapshot escaped into live constraint: %+v", fresh)
		}
	}
	assertLive(t, store, true)
	updated, err := store.UpdateNativeKeyBinding(binding.ID, UpdateNativeKeyBindingInput{Name: nativeStringPtr("Renamed")})
	if err != nil || !updated.RoundRobin {
		t.Fatalf("omitted round_robin must preserve true: %+v, %v", updated, err)
	}

	for _, want := range []bool{true, false} {
		if _, errUpdate := store.UpdateNativeKeyBinding(binding.ID, UpdateNativeKeyBindingInput{RoundRobin: nativeBoolPtr(want)}); errUpdate != nil {
			t.Fatal(errUpdate)
		}
		state, errLoad := LoadState(statePath)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		if len(state.NativeKeyBindings) != 1 || state.NativeKeyBindings[0].RoundRobin != want {
			t.Fatalf("durable state = %+v, want round_robin %v", state.NativeKeyBindings, want)
		}
		restarted := NewStore()
		if errConfigure := restarted.Configure(Config{Enabled: true, StateFile: statePath}); errConfigure != nil {
			t.Fatal(errConfigure)
		}
		assertLive(t, restarted, want)
		store = restarted
	}
	updated, err = store.UpdateNativeKeyBinding(binding.ID, UpdateNativeKeyBindingInput{Name: nativeStringPtr("Still disabled")})
	if err != nil || updated.RoundRobin {
		t.Fatalf("omitted round_robin must preserve false: %+v, %v", updated, err)
	}
	rawState, errRead := os.ReadFile(statePath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	if strings.Contains(string(rawState), secret) {
		t.Fatal("state persisted plaintext API key")
	}
}

func TestNativeRoundRobinFailedPersistencePreservesSelectionPolicy(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(map[bool]string{false: "enable", true: "disable"}[before], func(t *testing.T) {
			statePath := filepath.Join(t.TempDir(), "state.json")
			store := NewStore()
			if err := store.Configure(Config{Enabled: true, StateFile: statePath}); err != nil {
				t.Fatal(err)
			}
			binding, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
				ID: "durable", Enabled: true, APIKey: "durable-round-robin-key", Group: "team", RoundRobin: before,
			})
			if err != nil {
				t.Fatal(err)
			}
			// Replacing the destination with a directory makes atomic rename fail
			// deterministically, including when tests execute as root.
			if errRemove := os.Remove(statePath); errRemove != nil {
				t.Fatal(errRemove)
			}
			if errMkdir := os.Mkdir(statePath, 0o700); errMkdir != nil {
				t.Fatal(errMkdir)
			}
			if _, errUpdate := store.UpdateNativeKeyBinding(binding.ID, UpdateNativeKeyBindingInput{RoundRobin: nativeBoolPtr(!before)}); !errors.Is(errUpdate, ErrNativeKeyBindingPersistence) {
				t.Fatalf("update error = %v, want persistence failure", errUpdate)
			}
			constraint, ok := store.ResolveNativeKeyConstraint(binding.CallerScope, "codex", "gpt-5.3-codex-spark")
			if !ok || constraint.RoundRobin != before {
				t.Fatalf("failed update published selection policy: %+v, resolved %v", constraint, ok)
			}
			if got := store.NativeKeyBindingsSnapshot(); len(got) != 1 || got[0].RoundRobin != before {
				t.Fatalf("failed update changed snapshot: %+v", got)
			}
		})
	}
}
