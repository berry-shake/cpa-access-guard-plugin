package plugin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"cpa-access-guard/internal/policy"
)

func TestNativeRoundRobinManagementCRUDAndRestart(t *testing.T) {
	app, statePath := configureNativeBindingManagementApp(t)
	const (
		basePath = "/v0/management/plugins/access-guard/native-key-bindings"
		secret   = "sk-management-round-robin-translation-0123456789"
	)
	assertBinding := func(resp ManagementResponse, status int, want bool) {
		t.Helper()
		if resp.StatusCode != status {
			t.Fatalf("status = %d, want %d, body = %s", resp.StatusCode, status, resp.Body)
		}
		assertNativeBindingResponseIsRedacted(t, resp.Body, secret)
		var payload struct {
			Binding struct {
				RoundRobin *bool `json:"round_robin"`
			} `json:"binding"`
		}
		if err := json.Unmarshal(resp.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Binding.RoundRobin == nil || *payload.Binding.RoundRobin != want {
			t.Fatalf("binding must expose explicit round_robin %v: %s", want, resp.Body)
		}
	}

	created := nativeBindingManagementCall(t, app, http.MethodPost, basePath, nil, map[string]any{
		"id": "translation", "key": secret, "auth_ids": []string{"codex-A.json", "codex-B.json"},
		"round_robin":  true,
		"model_access": map[string]any{"mode": "allowlist", "models": []map[string]string{{"provider": "codex", "model": "gpt-5.3-codex-spark"}}},
	})
	assertBinding(created, http.StatusCreated, true)
	assertBinding(nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
		"id": "translation", "name": "New translation name",
	}), http.StatusOK, true)

	for _, want := range []bool{true, false} {
		assertBinding(nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
			"id": "translation", "round_robin": want,
		}), http.StatusOK, want)
		state, errLoad := policy.LoadState(statePath)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		if len(state.NativeKeyBindings) != 1 || state.NativeKeyBindings[0].RoundRobin != want {
			t.Fatalf("persisted binding = %+v, want round_robin %v", state.NativeKeyBindings, want)
		}
		app.Shutdown()
		app = NewApp()
		t.Cleanup(app.Shutdown)
		config := []byte("enabled: true\nstate_file: \"" + filepath.ToSlash(statePath) + "\"\nkeys: []\n")
		req, errMarshal := json.Marshal(LifecycleRequest{ConfigYAML: config})
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if _, errConfigure := app.HandleMethod(MethodPluginReconfigure, req); errConfigure != nil {
			t.Fatal(errConfigure)
		}

		listed := nativeBindingManagementCall(t, app, http.MethodGet, basePath, nil, nil)
		if listed.StatusCode != http.StatusOK {
			t.Fatalf("list status = %d, body = %s", listed.StatusCode, listed.Body)
		}
		assertNativeBindingResponseIsRedacted(t, listed.Body, secret)
		var listPayload struct {
			Bindings []struct {
				RoundRobin *bool `json:"round_robin"`
			} `json:"bindings"`
		}
		if err := json.Unmarshal(listed.Body, &listPayload); err != nil {
			t.Fatal(err)
		}
		if len(listPayload.Bindings) != 1 || listPayload.Bindings[0].RoundRobin == nil || *listPayload.Bindings[0].RoundRobin != want {
			t.Fatalf("restarted list lost round_robin %v: %s", want, listed.Body)
		}
		catalog := nativeBindingManagementCall(t, app, http.MethodPost, basePath+"/catalog", nil, map[string]any{"api_keys": []string{secret}})
		if catalog.StatusCode != http.StatusOK {
			t.Fatalf("catalog status = %d, body = %s", catalog.StatusCode, catalog.Body)
		}
		assertNativeBindingResponseIsRedacted(t, catalog.Body, secret)
		var catalogPayload struct {
			Entries []struct {
				Binding struct {
					RoundRobin *bool `json:"round_robin"`
				} `json:"binding"`
			} `json:"entries"`
		}
		if err := json.Unmarshal(catalog.Body, &catalogPayload); err != nil {
			t.Fatal(err)
		}
		if len(catalogPayload.Entries) != 1 || catalogPayload.Entries[0].Binding.RoundRobin == nil || *catalogPayload.Entries[0].Binding.RoundRobin != want {
			t.Fatalf("catalog lost round_robin %v: %s", want, catalog.Body)
		}
	}
	assertBinding(nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
		"id": "translation", "name": "Still disabled",
	}), http.StatusOK, false)
}

func TestNativeRoundRobinManagementDefaultsAndInvalidValues(t *testing.T) {
	app, _ := configureNativeBindingManagementApp(t)
	const basePath = "/v0/management/plugins/access-guard/native-key-bindings"
	created := nativeBindingManagementCall(t, app, http.MethodPost, basePath, nil, map[string]any{
		"id": "legacy", "key": "legacy-management-round-robin-key", "group": "team",
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.StatusCode, created.Body)
	}
	var payload struct {
		Binding struct {
			RoundRobin *bool `json:"round_robin"`
		} `json:"binding"`
	}
	if err := json.Unmarshal(created.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Binding.RoundRobin == nil || *payload.Binding.RoundRobin {
		t.Fatalf("legacy create must return explicit round_robin false: %s", created.Body)
	}
	for _, value := range []any{"true", 1, []bool{true}, map[string]bool{"enabled": true}} {
		resp := nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
			"id": "legacy", "round_robin": value,
		})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid round_robin %#v accepted: status = %d, body = %s", value, resp.StatusCode, resp.Body)
		}
	}
	if snapshot := app.store.NativeKeyBindingsSnapshot(); len(snapshot) != 1 || snapshot[0].RoundRobin {
		t.Fatalf("invalid requests changed scheduling policy: %+v", snapshot)
	}
}
