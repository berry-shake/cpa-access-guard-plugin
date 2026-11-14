package plugin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"cpa-access-guard/internal/policy"
)

const nativeNameSyncPath = "/v0/management/plugins/access-guard/native-key-bindings/sync-names"

func TestNativeBindingNameSyncManagementRoute(t *testing.T) {
	app, statePath := configureNativeBindingManagementApp(t)
	app.store.StopUsageFlusher()
	const secret = "sk-synthetic-native-name-sync-0123456789"
	created := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath, nil, map[string]any{
		"id": "client", "key": secret, "auth_ids": []string{"team.json"},
	})
	if created.StatusCode != http.StatusCreated || !bytes.Contains(created.Body, []byte(`"name":""`)) {
		t.Fatalf("create without name: status=%d body=%s", created.StatusCode, created.Body)
	}
	input := map[string]any{"updates": []map[string]string{
		{"id": "client", "key": secret, "expected_name": "", "name": "From CPA"},
		{"id": "missing", "key": secret, "expected_name": "", "name": "Ignored"},
	}}
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeNameSyncPath, nil, input)
	if response.StatusCode != http.StatusOK || string(response.Body) != `{"updated_ids":["client"],"skipped_ids":["missing"]}` {
		t.Fatalf("sync: status=%d body=%s", response.StatusCode, response.Body)
	}
	assertNativeBindingResponseIsRedacted(t, response.Body, secret)
	if binding := app.store.NativeKeyBindingsSnapshot()[0]; binding.Name != "From CPA" || len(binding.AuthIDs) != 1 || binding.AuthIDs[0] != "team.json" {
		t.Fatalf("unexpected synchronized binding: %+v", binding)
	}
	response = nativeBindingManagementCall(t, app, http.MethodPatch, nativeBatchTestPath, nil, map[string]string{"id": "client", "name": " "})
	if response.StatusCode != http.StatusOK || !bytes.Contains(response.Body, []byte(`"name":""`)) {
		t.Fatalf("clear name: status=%d body=%s", response.StatusCode, response.Body)
	}
	response = nativeBindingManagementCall(t, app, http.MethodPatch, nativeBatchTestPath, nil, map[string]any{"id": "client", "enabled": false})
	if response.StatusCode != http.StatusOK || !bytes.Contains(response.Body, []byte(`"name":""`)) {
		t.Fatalf("patch without name: status=%d body=%s", response.StatusCode, response.Body)
	}
	restarted := policy.NewStore()
	if err := restarted.Configure(policy.Config{Enabled: true, StateFile: statePath}); err != nil {
		t.Fatal(err)
	}
	if binding := restarted.NativeKeyBindingsSnapshot()[0]; binding.Name != "" {
		t.Fatalf("reload normalized blank name to %q", binding.Name)
	}
	registered := 0
	for _, route := range app.managementRegistration().Routes {
		if route.Path == "/plugins/access-guard/native-key-bindings/sync-names" && route.Method == http.MethodPost {
			registered++
		}
	}
	if registered != 1 {
		t.Fatalf("sync-names registered %d times", registered)
	}
}

func TestNativeBindingNameSyncRejectsInvalidRequestsWithoutLeakingKey(t *testing.T) {
	app, statePath := configureNativeBindingManagementApp(t)
	app.store.StopUsageFlusher()
	const secret = "sk-synthetic-native-name-private-0123456789"
	created := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath, nil, map[string]any{
		"id": "client", "key": secret, "auth_ids": []string{"team.json"}, "name": "Before",
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create: status=%d", created.StatusCode)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	const validUpdate = `{"id":"client","key":"` + secret + `","expected_name":"Before","name":"After"}`
	for _, body := range []string{
		`{"updates":[` + validUpdate + `,null]}`,
		`{"updates":[` + validUpdate + `,` + validUpdate + `]}`,
		`{"updates":[{"id":"client","key":"` + secret + `","name":"After"}]}`,
		`{"updates":[` + validUpdate + `],"` + secret + `":true}`,
		`{"updates":[` + validUpdate + `]}{}`,
	} {
		request, err := json.Marshal(ManagementRequest{Method: http.MethodPost, Path: nativeNameSyncPath, Body: []byte(body)})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := app.HandleMethod(MethodManagementHandle, request)
		if err != nil {
			t.Fatal(err)
		}
		response := managementResponseFromEnvelope(t, raw)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid request: status=%d body=%s", response.StatusCode, response.Body)
		}
		assertNativeBindingResponseIsRedacted(t, response.Body, secret)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || app.store.NativeKeyBindingsSnapshot()[0].Name != "Before" {
		t.Fatal("invalid request changed a live or persisted binding")
	}
}
