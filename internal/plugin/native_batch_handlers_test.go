package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"cpa-access-guard/internal/policy"
)

const nativeBatchTestPath = "/v0/management/plugins/access-guard/native-key-bindings"

func nativeBatchTestRequest() nativeBindingBatchRequest {
	return nativeBindingBatchRequest{
		APIKeys:         []string{"sk-native-batch-one-0123456789", "sk-native-batch-two-0123456789"},
		SelectedIndices: []int{0, 1}, AuthIDs: []string{"B.json", "C.json"},
		AvailableAuthIDs: []string{"A.json", "B.json", "C.json"}, CatalogComplete: true,
	}
}

func nativeBatchTestPreview(t *testing.T, app *App, path string, request any) publicNativeBindingPreview {
	t.Helper()
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+path, nil, request)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("preview status=%d body=%s", response.StatusCode, response.Body)
	}
	var payload struct {
		Preview publicNativeBindingPreview `json:"preview"`
	}
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Preview
}

func assertNativeBatchRedacted(t *testing.T, body []byte, keys []string) {
	t.Helper()
	for _, key := range keys {
		assertNativeBindingResponseIsRedacted(t, body, key)
	}
	for _, field := range []string{"caller_scope", "api_keys", "expected_revision", "fingerprint"} {
		if bytes.Contains(body, []byte(`"`+field+`"`)) {
			t.Fatalf("response exposed %s: %s", field, body)
		}
	}
}

func TestNativeBindingBatchManagementPreviewCommitRollbackAndHistory(t *testing.T) {
	app, statePath := configureNativeBindingManagementApp(t)
	request := nativeBatchTestRequest()
	created := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath, nil, map[string]any{
		"id": "existing", "key": request.APIKeys[0], "auth_ids": []string{"A.json"},
		"enabled": false, "round_robin": true, "rpm": 20,
		"model_access": map[string]any{"mode": "allowlist", "models": []map[string]string{{"provider": "codex", "model": "spark"}}},
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create: %s", created.Body)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	preview := nativeBatchTestPreview(t, app, "/batch-preview", request)
	if !preview.CanApply || preview.Noop || len(preview.Changes) != 2 || preview.Revision == "" {
		t.Fatalf("preview=%+v", preview)
	}
	afterPreview, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterPreview) || len(app.store.NativeKeyBindingsSnapshot()) != 1 {
		t.Fatal("preview mutated bindings or persisted state")
	}
	previewRaw, _ := json.Marshal(preview)
	assertNativeBatchRedacted(t, previewRaw, request.APIKeys)
	request.ExpectedRevision = preview.Revision
	applied := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/batch", nil, request)
	if applied.StatusCode != http.StatusOK {
		t.Fatalf("apply: status=%d body=%s", applied.StatusCode, applied.Body)
	}
	assertNativeBatchRedacted(t, applied.Body, request.APIKeys)
	var appliedPayload publicNativeBindingMutationResult
	if err := json.Unmarshal(applied.Body, &appliedPayload); err != nil {
		t.Fatal(err)
	}
	if appliedPayload.Operation == nil || appliedPayload.Changed != 2 || appliedPayload.Noop {
		t.Fatalf("apply=%+v", appliedPayload)
	}
	bindings := app.store.NativeKeyBindingsSnapshot()
	if len(bindings) != 2 {
		t.Fatalf("bindings=%+v", bindings)
	}
	for _, binding := range bindings {
		if binding.ID == "existing" && (binding.Enabled || !binding.RoundRobin || binding.RPM != 20 || binding.ModelAccess.Mode != "allowlist") {
			t.Fatalf("batch changed unrelated policy: %+v", binding)
		}
	}
	history := nativeBindingManagementCall(t, app, http.MethodGet, nativeBatchTestPath+"/history", nil, nil)
	if history.StatusCode != http.StatusOK {
		t.Fatalf("history=%s", history.Body)
	}
	assertNativeBatchRedacted(t, history.Body, request.APIKeys)
	var historyPayload struct {
		Operations []publicNativeBindingOperation `json:"operations"`
	}
	if err := json.Unmarshal(history.Body, &historyPayload); err != nil {
		t.Fatal(err)
	}
	if len(historyPayload.Operations) != 1 || historyPayload.Operations[0].ID != appliedPayload.Operation.ID {
		t.Fatalf("history=%s", history.Body)
	}

	rollbackRequest := nativeBindingRollbackRequest{OperationID: appliedPayload.Operation.ID, APIKeys: request.APIKeys,
		AvailableAuthIDs: request.AvailableAuthIDs, CatalogComplete: true}
	missingHostKeys := rollbackRequest
	missingHostKeys.APIKeys = []string{}
	missingHostPreview := nativeBatchTestPreview(t, app, "/rollback-preview", missingHostKeys)
	if missingHostPreview.CanApply || len(missingHostPreview.Conflicts) == 0 {
		t.Fatalf("removed host keys did not block rollback: %+v", missingHostPreview)
	}
	rollbackPreview := nativeBatchTestPreview(t, app, "/rollback-preview", rollbackRequest)
	if !rollbackPreview.CanApply || len(rollbackPreview.Changes) != 2 {
		t.Fatalf("rollback preview=%+v", rollbackPreview)
	}
	rollbackRequest.ExpectedRevision = rollbackPreview.Revision
	rolledBack := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/rollback", nil, rollbackRequest)
	if rolledBack.StatusCode != http.StatusOK {
		t.Fatalf("rollback=%s", rolledBack.Body)
	}
	assertNativeBatchRedacted(t, rolledBack.Body, request.APIKeys)
	bindings = app.store.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || bindings[0].ID != "existing" || len(bindings[0].AuthIDs) != 1 || bindings[0].AuthIDs[0] != "A.json" {
		t.Fatalf("rollback bindings=%+v", bindings)
	}
	history = nativeBindingManagementCall(t, app, http.MethodGet, nativeBatchTestPath+"/history", nil, nil)
	if err := json.Unmarshal(history.Body, &historyPayload); err != nil {
		t.Fatal(err)
	}
	if len(historyPayload.Operations) != 2 || historyPayload.Operations[0].Kind != "rollback" || historyPayload.Operations[1].RevertedBy == "" {
		t.Fatalf("rollback history=%s", history.Body)
	}
	stateRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range request.APIKeys {
		if bytes.Contains(stateRaw, []byte(key)) {
			t.Fatal("persisted history contains a plaintext key")
		}
	}
}

func TestNativeBindingBatchManagementStalePreviewAndConflict(t *testing.T) {
	app, _ := configureNativeBindingManagementApp(t)
	request := nativeBatchTestRequest()
	preview := nativeBatchTestPreview(t, app, "/batch-preview", request)
	request.ExpectedRevision = preview.Revision
	changed := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath, nil, map[string]any{
		"id": "changed", "key": request.APIKeys[0], "auth_ids": []string{"A.json"},
	})
	if changed.StatusCode != http.StatusCreated {
		t.Fatalf("create=%s", changed.Body)
	}
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/batch", nil, request)
	if response.StatusCode != http.StatusConflict || !bytes.Contains(response.Body, []byte("revision_mismatch")) {
		t.Fatalf("stale=%s", response.Body)
	}
	assertNativeBatchRedacted(t, response.Body, request.APIKeys)
	request.ExpectedRevision = ""
	request.CatalogComplete = false
	preview = nativeBatchTestPreview(t, app, "/batch-preview", request)
	if preview.CanApply || len(preview.Conflicts) == 0 {
		t.Fatalf("incomplete catalog=%+v", preview)
	}
	request.ExpectedRevision = preview.Revision
	response = nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/batch", nil, request)
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", response.StatusCode, response.Body)
	}
	if len(app.store.NativeKeyBindingsSnapshot()) != 1 {
		t.Fatal("rejected mutation changed bindings")
	}
}

func TestNativeBindingBatchManagementNoopDoesNotCreateHistory(t *testing.T) {
	app, _ := configureNativeBindingManagementApp(t)
	request := nativeBatchTestRequest()
	request.ExpectedRevision = nativeBatchTestPreview(t, app, "/batch-preview", request).Revision
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/batch", nil, request)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("initial commit=%s", response.Body)
	}
	preview := nativeBatchTestPreview(t, app, "/batch-preview", request)
	if !preview.Noop || preview.CanApply || len(preview.Changes) != 0 {
		t.Fatalf("unchanged preview=%+v", preview)
	}
	request.ExpectedRevision = preview.Revision
	response = nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/batch", nil, request)
	if response.StatusCode != http.StatusOK || string(response.Body) != `{"changed":0,"noop":true}` {
		t.Fatalf("noop response=%s", response.Body)
	}
	if len(app.store.NativeBindingHistorySnapshot()) != 1 {
		t.Fatal("no-op created an additional history operation")
	}
}

func TestNativeBindingBatchManagementPersistenceFailureIsAtomicAndRedacted(t *testing.T) {
	app, statePath := configureNativeBindingManagementApp(t)
	request := nativeBatchTestRequest()
	request.ExpectedRevision = nativeBatchTestPreview(t, app, "/batch-preview", request).Revision
	backupPath := statePath + ".backup"
	if err := os.Rename(statePath, backupPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(statePath); err != nil {
			t.Errorf("remove persistence blocker: %v", err)
		}
		if err := os.Rename(backupPath, statePath); err != nil {
			t.Errorf("restore state fixture: %v", err)
		}
	})
	if err := os.Mkdir(statePath, 0700); err != nil {
		t.Fatal(err)
	}
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/batch", nil, request)
	if response.StatusCode != http.StatusInternalServerError || !bytes.Contains(response.Body, []byte("persistence_failed")) {
		t.Fatalf("persistence failure status=%d body=%s", response.StatusCode, response.Body)
	}
	assertNativeBatchRedacted(t, response.Body, request.APIKeys)
	if bytes.Contains(response.Body, []byte(statePath)) {
		t.Fatal("persistence error leaked filesystem path")
	}
	if len(app.store.NativeKeyBindingsSnapshot()) != 0 || len(app.store.NativeBindingHistorySnapshot()) != 0 {
		t.Fatal("failed persistence published changes")
	}
}

func TestNativeBindingBatchManagementRejectsMalformedAndOversizedRequests(t *testing.T) {
	app, _ := configureNativeBindingManagementApp(t)
	const secret = "sk-invalid-sensitive-0123456789"
	tests := []struct {
		name, raw string
		status    int
	}{
		{"truncated", `{"api_keys":["` + secret + `"]`, http.StatusBadRequest},
		{"unknown_field", `{"` + secret + `":true}`, http.StatusBadRequest},
		{"wrong_type", `{"api_keys":"` + secret + `"}`, http.StatusBadRequest},
		{"two_values", `{}` + `{}`, http.StatusBadRequest},
		{"null", `null`, http.StatusBadRequest},
		{"missing_commit_revision", `{"api_keys":["` + secret + `"],"selected_indices":[0],"auth_ids":["A"],"available_auth_ids":["A"],"catalog_complete":true}`, http.StatusBadRequest},
		{"too_large", strings.Repeat(" ", nativeBatchMaxBodyBytes+1), http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := app.batchNativeBindings([]byte(test.raw), false)
			if response.StatusCode != test.status {
				t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
			}
			assertNativeBatchRedacted(t, response.Body, []string{secret})
		})
	}
	oversized := nativeBatchTestRequest()
	oversized.APIKeys = make([]string, nativeBatchMaxKeys+1)
	for i := range oversized.APIKeys {
		oversized.APIKeys[i] = secret
	}
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/batch-preview", nil, oversized)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized keys=%s", response.Body)
	}
	if len(app.store.NativeKeyBindingsSnapshot()) != 0 || len(app.store.NativeBindingHistorySnapshot()) != 0 {
		t.Fatal("invalid requests mutated state")
	}
}

func TestNativeBindingMutationErrorsAndRouteRegistration(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
	}{
		{policy.ErrNativeBindingRevisionMismatch, 409}, {policy.ErrNativeBindingBatchConflict, 409},
		{policy.ErrUnknownNativeBindingOperation, 404}, {policy.ErrInvalidNativeBindingBatch, 400},
		{policy.ErrNativeKeyBindingPersistence, 500}, {errors.New("unknown"), 500},
	} {
		response := nativeBindingMutationError(fmt.Errorf("sk-error-secret-0123456789 /private/state.json: %w", test.err))
		if response.StatusCode != test.status || bytes.Contains(response.Body, []byte("sk-error-secret")) || bytes.Contains(response.Body, []byte("/private")) {
			t.Fatalf("unsafe error response=%s", response.Body)
		}
	}
	app, _ := configureNativeBindingManagementApp(t)
	want := map[string]string{"/history": "GET", "/batch-preview": "POST", "/batch": "POST", "/rollback-preview": "POST", "/rollback": "POST"}
	for _, route := range app.managementRegistration().Routes {
		for suffix, method := range want {
			if route.Path == "/plugins/access-guard/native-key-bindings"+suffix && route.Method == method {
				delete(want, suffix)
			}
		}
	}
	if len(want) != 0 {
		t.Fatalf("unregistered routes=%v", want)
	}
	history := nativeBindingManagementCall(t, app, http.MethodGet, nativeBatchTestPath+"/history", nil, nil)
	if history.StatusCode != http.StatusOK || string(history.Body) != `{"operations":[]}` {
		t.Fatalf("empty history=%s", history.Body)
	}
	missing := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath+"/rollback-preview", nil, nativeBindingRollbackRequest{
		OperationID: "missing", APIKeys: []string{"sk-test-native-key-0123456789"}, AvailableAuthIDs: []string{"A"}, CatalogComplete: true,
	})
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing operation=%s", missing.Body)
	}
}
