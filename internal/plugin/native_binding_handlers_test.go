package plugin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpa-access-guard/internal/policy"
)

func configureNativeBindingManagementApp(t *testing.T) (*App, string) {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "state.json")
	app := NewApp()
	config := []byte("enabled: true\nstate_file: \"" + filepath.ToSlash(statePath) + "\"\nkeys: []\n")
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: config})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure: %v", err)
	}
	t.Cleanup(app.Shutdown)
	return app, statePath
}

func nativeBindingManagementCall(t *testing.T, app *App, method, path string, query url.Values, body any) ManagementResponse {
	t.Helper()
	var rawBody []byte
	if body != nil {
		var err error
		rawBody, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	rawReq, _ := json.Marshal(ManagementRequest{Method: method, Path: path, Query: query, Body: rawBody})
	raw, err := app.HandleMethod(MethodManagementHandle, rawReq)
	if err != nil {
		t.Fatal(err)
	}
	return managementResponseFromEnvelope(t, raw)
}

func TestNativeKeyBindingManagementCRUDDoesNotExposeSecretOrScope(t *testing.T) {
	app, statePath := configureNativeBindingManagementApp(t)
	const (
		basePath  = "/v0/management/plugins/access-guard/native-key-bindings"
		secret    = "sk-native-management-secret-0123456789"
		newSecret = "sk-native-management-rotated-9876543210"
	)

	created := nativeBindingManagementCall(t, app, http.MethodPost, basePath, nil, map[string]any{
		"id": " Client-A ", "name": "Client A", "key": secret, "group": " CLASSIFY: TENANT-A ",
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.StatusCode, created.Body)
	}
	assertNativeBindingResponseIsRedacted(t, created.Body, secret)
	var createPayload struct {
		Binding publicNativeKeyBinding `json:"binding"`
	}
	if err := json.Unmarshal(created.Body, &createPayload); err != nil {
		t.Fatal(err)
	}
	if got := createPayload.Binding; got.ID != "client-a" || got.Name != "Client A" || !got.Enabled || got.Group != "classify:tenant-a" || got.KeyPreview == "" {
		t.Fatalf("created binding=%+v", got)
	}
	originalPreview := createPayload.Binding.KeyPreview

	listed := nativeBindingManagementCall(t, app, http.MethodGet, basePath, nil, nil)
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.StatusCode, listed.Body)
	}
	assertNativeBindingResponseIsRedacted(t, listed.Body, secret)
	var listPayload struct {
		Bindings []publicNativeKeyBinding `json:"bindings"`
	}
	if err := json.Unmarshal(listed.Body, &listPayload); err != nil {
		t.Fatal(err)
	}
	if len(listPayload.Bindings) != 1 || listPayload.Bindings[0].ID != "client-a" {
		t.Fatalf("listed bindings=%+v", listPayload.Bindings)
	}

	// A partial PATCH preserves the derived identity and key preview while
	// changing only policy/display fields.
	patched := nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
		"id": "CLIENT-A", "name": "Renamed", "enabled": false, "group": "team",
	})
	if patched.StatusCode != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", patched.StatusCode, patched.Body)
	}
	var patchPayload struct {
		Binding publicNativeKeyBinding `json:"binding"`
	}
	if err := json.Unmarshal(patched.Body, &patchPayload); err != nil {
		t.Fatal(err)
	}
	if patchPayload.Binding.Enabled || patchPayload.Binding.Name != "Renamed" || patchPayload.Binding.Group != "team" || patchPayload.Binding.KeyPreview != originalPreview {
		t.Fatalf("patched binding=%+v", patchPayload.Binding)
	}

	rotated := nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
		"id": "client-a", "key": newSecret, "enabled": true,
	})
	if rotated.StatusCode != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", rotated.StatusCode, rotated.Body)
	}
	assertNativeBindingResponseIsRedacted(t, rotated.Body, newSecret)
	var rotatePayload struct {
		Binding publicNativeKeyBinding `json:"binding"`
	}
	if err := json.Unmarshal(rotated.Body, &rotatePayload); err != nil {
		t.Fatal(err)
	}
	if !rotatePayload.Binding.Enabled || rotatePayload.Binding.KeyPreview == originalPreview {
		t.Fatalf("rotated binding=%+v", rotatePayload.Binding)
	}

	stateRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateRaw, []byte(secret)) || bytes.Contains(stateRaw, []byte(newSecret)) {
		t.Fatalf("state contains a plaintext API key: %s", stateRaw)
	}
	state, err := policy.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.NativeKeyBindings) != 1 || state.NativeKeyBindings[0].CallerScope != policy.NativeCallerScope(newSecret) {
		t.Fatalf("persisted binding=%+v", state.NativeKeyBindings)
	}

	status := nativeBindingManagementCall(t, app, http.MethodGet, "/v0/management/plugins/access-guard/status", nil, nil)
	var statusPayload map[string]any
	if err := json.Unmarshal(status.Body, &statusPayload); err != nil {
		t.Fatal(err)
	}
	if statusPayload["native_key_binding_count"] != float64(1) || statusPayload["native_key_binding_enabled_count"] != float64(1) {
		t.Fatalf("status payload=%+v", statusPayload)
	}
	if _, exists := statusPayload["caller_scope"]; exists {
		t.Fatalf("status exposed caller_scope: %+v", statusPayload)
	}

	deleted := nativeBindingManagementCall(t, app, http.MethodDelete, basePath, url.Values{"id": {"CLIENT-A"}}, nil)
	if deleted.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.StatusCode, deleted.Body)
	}
	empty := nativeBindingManagementCall(t, app, http.MethodGet, basePath, nil, nil)
	if string(empty.Body) != `{"bindings":[]}` {
		t.Fatalf("empty list body=%s", empty.Body)
	}
}

func TestNativeKeyBindingManagementDirectAuthIDsAndModeSwitch(t *testing.T) {
	app, statePath := configureNativeBindingManagementApp(t)
	const (
		basePath = "/v0/management/plugins/access-guard/native-key-bindings"
		secret   = "sk-native-direct-management-secret-0123456789"
	)

	created := nativeBindingManagementCall(t, app, http.MethodPost, basePath, nil, map[string]any{
		"id": "direct", "key": secret,
		"auth_ids": []string{" tenant/codex-B.json ", "tenant/codex-A.json", "tenant/codex-B.json"},
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.StatusCode, created.Body)
	}
	assertNativeBindingResponseIsRedacted(t, created.Body, secret)
	var createPayload struct {
		Binding publicNativeKeyBinding `json:"binding"`
	}
	if err := json.Unmarshal(created.Body, &createPayload); err != nil {
		t.Fatal(err)
	}
	if createPayload.Binding.Group != "" || len(createPayload.Binding.AuthIDs) != 2 ||
		createPayload.Binding.AuthIDs[0] != "tenant/codex-A.json" ||
		bytes.Contains(created.Body, []byte("@access-guard/direct-auth-ids")) {
		t.Fatalf("public direct binding=%+v body=%s", createPayload.Binding, created.Body)
	}

	state, err := policy.LoadState(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.NativeKeyBindings) != 1 || len(state.NativeKeyBindings[0].AuthIDs) != 2 || state.NativeKeyBindings[0].Group == "" {
		t.Fatalf("persisted direct binding=%+v", state.NativeKeyBindings)
	}

	groupMode := nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
		"id": "direct", "group": "team",
	})
	if groupMode.StatusCode != http.StatusOK {
		t.Fatalf("group switch status=%d body=%s", groupMode.StatusCode, groupMode.Body)
	}
	var groupPayload struct {
		Binding publicNativeKeyBinding `json:"binding"`
	}
	if err := json.Unmarshal(groupMode.Body, &groupPayload); err != nil {
		t.Fatal(err)
	}
	if groupPayload.Binding.Group != "team" || len(groupPayload.Binding.AuthIDs) != 0 {
		t.Fatalf("group switch binding=%+v", groupPayload.Binding)
	}

	directMode := nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
		"id": "direct", "auth_ids": []string{"codex-only.json"},
	})
	if directMode.StatusCode != http.StatusOK {
		t.Fatalf("direct switch status=%d body=%s", directMode.StatusCode, directMode.Body)
	}
	var directPayload struct {
		Binding publicNativeKeyBinding `json:"binding"`
	}
	if err := json.Unmarshal(directMode.Body, &directPayload); err != nil {
		t.Fatal(err)
	}
	if directPayload.Binding.Group != "" || len(directPayload.Binding.AuthIDs) != 1 || directPayload.Binding.AuthIDs[0] != "codex-only.json" {
		t.Fatalf("direct switch binding=%+v", directPayload.Binding)
	}

	conflict := nativeBindingManagementCall(t, app, http.MethodPatch, basePath, nil, map[string]any{
		"id": "direct", "group": "free", "auth_ids": []string{"codex-conflict.json"},
	})
	if conflict.StatusCode != http.StatusBadRequest || !bytes.Contains(conflict.Body, []byte("mutually exclusive")) {
		t.Fatalf("conflicting switch status=%d body=%s", conflict.StatusCode, conflict.Body)
	}
	listed := nativeBindingManagementCall(t, app, http.MethodGet, basePath, nil, nil)
	if !bytes.Contains(listed.Body, []byte("codex-only.json")) || bytes.Contains(listed.Body, []byte("codex-conflict.json")) {
		t.Fatalf("conflicting patch changed live binding: %s", listed.Body)
	}
}

func TestNativeKeyBindingCatalogMatchesScopesAndRedactsSecrets(t *testing.T) {
	app, statePath := configureNativeBindingManagementApp(t)
	const (
		basePath      = "/v0/management/plugins/access-guard/native-key-bindings"
		catalogPath   = basePath + "/catalog"
		matchedSecret = "sk-native-catalog-matched-0123456789"
		orphanSecret  = "sk-native-catalog-orphan-9876543210"
		unboundSecret = "sk-native-catalog-unbound-1122334455"
		shortSecret   = "short"
	)

	for _, input := range []map[string]any{
		{"id": "matched", "name": "Matched", "key": matchedSecret, "group": "team"},
		{"id": "orphan", "name": "Orphan", "key": orphanSecret, "group": "free"},
	} {
		created := nativeBindingManagementCall(t, app, http.MethodPost, basePath, nil, input)
		if created.StatusCode != http.StatusCreated {
			t.Fatalf("create status=%d body=%s", created.StatusCode, created.Body)
		}
	}

	stateBefore, errReadBefore := os.ReadFile(statePath)
	if errReadBefore != nil {
		t.Fatal(errReadBefore)
	}
	catalog := nativeBindingManagementCall(t, app, http.MethodPost, catalogPath, nil, map[string]any{
		"api_keys": []string{
			" ",
			"  " + matchedSecret + "  ",
			matchedSecret,
			unboundSecret,
			"\t",
			shortSecret,
			unboundSecret,
		},
	})
	if catalog.StatusCode != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", catalog.StatusCode, catalog.Body)
	}
	assertNativeBindingResponseIsRedacted(t, catalog.Body, matchedSecret, orphanSecret, unboundSecret, shortSecret)
	if bytes.Contains(catalog.Body, []byte(`"binding":null`)) {
		t.Fatalf("unmatched catalog entry must omit binding: %s", catalog.Body)
	}

	var payload struct {
		Entries        []nativeKeyBindingCatalogEntry `json:"entries"`
		OrphanBindings []publicNativeKeyBinding       `json:"orphan_bindings"`
	}
	if errUnmarshal := json.Unmarshal(catalog.Body, &payload); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(payload.Entries) != 3 {
		t.Fatalf("entries=%+v, want three non-blank unique keys", payload.Entries)
	}
	if payload.Entries[0].KeyIndex != 1 || payload.Entries[0].KeyPreview != policy.NativeKeyPreview(matchedSecret) ||
		payload.Entries[0].Binding == nil || payload.Entries[0].Binding.ID != "matched" {
		t.Fatalf("matched entry=%+v", payload.Entries[0])
	}
	if payload.Entries[1].KeyIndex != 3 || payload.Entries[1].KeyPreview != policy.NativeKeyPreview(unboundSecret) || payload.Entries[1].Binding != nil {
		t.Fatalf("unbound entry=%+v", payload.Entries[1])
	}
	if payload.Entries[2].KeyIndex != 5 || payload.Entries[2].KeyPreview != "<redacted>" || payload.Entries[2].Binding != nil {
		t.Fatalf("short-key entry=%+v", payload.Entries[2])
	}
	if len(payload.OrphanBindings) != 1 || payload.OrphanBindings[0].ID != "orphan" {
		t.Fatalf("orphan bindings=%+v", payload.OrphanBindings)
	}

	stateAfter, errReadAfter := os.ReadFile(statePath)
	if errReadAfter != nil {
		t.Fatal(errReadAfter)
	}
	if !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("catalog request modified persistent state")
	}

	emptyCatalog := nativeBindingManagementCall(t, app, http.MethodPost, catalogPath, nil, map[string]any{"api_keys": []string{}})
	if emptyCatalog.StatusCode != http.StatusOK {
		t.Fatalf("empty catalog status=%d body=%s", emptyCatalog.StatusCode, emptyCatalog.Body)
	}
	if errUnmarshal := json.Unmarshal(emptyCatalog.Body, &payload); errUnmarshal != nil {
		t.Fatal(errUnmarshal)
	}
	if len(payload.Entries) != 0 || len(payload.OrphanBindings) != 2 {
		t.Fatalf("empty catalog payload=%+v", payload)
	}

	invalid := app.catalogNativeKeyBindings([]byte(`{"api_keys":[`))
	if invalid.StatusCode != http.StatusBadRequest || !bytes.Contains(invalid.Body, []byte(`"code":"invalid_json"`)) {
		t.Fatalf("invalid catalog status=%d body=%s", invalid.StatusCode, invalid.Body)
	}
}

func TestNativeKeyBindingManagementValidationAndRegistration(t *testing.T) {
	app, _ := configureNativeBindingManagementApp(t)
	const path = "/v0/management/plugins/access-guard/native-key-bindings"

	for _, tc := range []struct {
		name string
		body map[string]any
	}{
		{name: "missing id", body: map[string]any{"key": "sk-long-enough-secret", "group": "team"}},
		{name: "missing key", body: map[string]any{"id": "a", "group": "team"}},
		{name: "missing group", body: map[string]any{"id": "a", "key": "sk-long-enough-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := nativeBindingManagementCall(t, app, http.MethodPost, path, nil, tc.body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
			}
		})
	}

	secret := "sk-duplicate-native-secret-0123456789"
	first := nativeBindingManagementCall(t, app, http.MethodPost, path, nil, map[string]any{
		"id": "a", "key": secret, "group": "team",
	})
	if first.StatusCode != http.StatusCreated {
		t.Fatalf("first create status=%d body=%s", first.StatusCode, first.Body)
	}
	duplicateID := nativeBindingManagementCall(t, app, http.MethodPost, path, nil, map[string]any{
		"id": "A", "key": "sk-different-native-secret-9876543210", "group": "free",
	})
	if duplicateID.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate id status=%d body=%s", duplicateID.StatusCode, duplicateID.Body)
	}
	duplicateScope := nativeBindingManagementCall(t, app, http.MethodPost, path, nil, map[string]any{
		"id": "b", "key": secret, "group": "free",
	})
	if duplicateScope.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate scope status=%d body=%s", duplicateScope.StatusCode, duplicateScope.Body)
	}
	assertNativeBindingResponseIsRedacted(t, duplicateScope.Body, secret)

	unknownPatch := nativeBindingManagementCall(t, app, http.MethodPatch, path, nil, map[string]any{"id": "missing", "enabled": false})
	if unknownPatch.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown patch status=%d body=%s", unknownPatch.StatusCode, unknownPatch.Body)
	}
	unknownDelete := nativeBindingManagementCall(t, app, http.MethodDelete, path, url.Values{"id": {"missing"}}, nil)
	if unknownDelete.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown delete status=%d body=%s", unknownDelete.StatusCode, unknownDelete.Body)
	}

	methods := map[string]bool{}
	for _, route := range app.managementRegistration().Routes {
		if route.Path == "/plugins/access-guard/native-key-bindings" {
			methods[route.Method] = true
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		if !methods[method] {
			t.Fatalf("management registration missing %s %s", method, path)
		}
	}

	catalogRegistered := false
	for _, route := range app.managementRegistration().Routes {
		if route.Path == "/plugins/access-guard/native-key-bindings/catalog" && route.Method == http.MethodPost {
			catalogRegistered = true
			break
		}
	}
	if !catalogRegistered {
		t.Fatal("management registration missing POST native-key-bindings/catalog")
	}
	resetRegistered := false
	for _, route := range app.managementRegistration().Routes {
		if route.Path == "/plugins/access-guard/native-key-bindings/reset-quota" && route.Method == http.MethodPost {
			resetRegistered = true
			break
		}
	}
	if !resetRegistered {
		t.Fatal("management registration missing POST native-key-bindings/reset-quota")
	}
}

func TestResetNativeKeyBindingQuotaManagement(t *testing.T) {
	app, _ := configureNativeBindingManagementApp(t)
	const (
		basePath = "/v0/management/plugins/access-guard/native-key-bindings"
		secret   = "sk-native-reset-secret-0123456789"
	)
	created := nativeBindingManagementCall(t, app, http.MethodPost, basePath, nil, map[string]any{
		"id": "reset-me", "key": secret, "group": "team", "rpm": 10, "daily_usd": 5, "weekly_usd": 20,
	})
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.StatusCode, created.Body)
	}
	binding := app.store.NativeKeyBindingsSnapshot()[0]
	if _, limited := app.store.CheckNativeKeyQuota(binding.CallerScope); limited {
		t.Fatal("first RPM request unexpectedly limited")
	}
	app.store.RecordUsage(secret, "", "unpriced-model", false, policy.UsageDetail{InputTokens: 10})
	before := app.store.NativeBindingUsage(binding)
	if before.RPMUsed == 0 || before.DailyCalls == 0 || before.WeeklyCalls == 0 {
		t.Fatalf("usage before reset=%+v", before)
	}

	reset := nativeBindingManagementCall(t, app, http.MethodPost, basePath+"/reset-quota", nil, map[string]any{"id": "reset-me"})
	if reset.StatusCode != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.StatusCode, reset.Body)
	}
	after := app.store.NativeBindingUsage(binding)
	if after.RPMUsed != 0 || after.DailyUSDUsed != 0 || after.WeeklyUSDUsed != 0 || after.DailyCalls != 0 || after.WeeklyCalls != 0 {
		t.Fatalf("usage after reset=%+v", after)
	}
}

func TestNativeKeyBindingPersistenceErrorIsInternalAndRedacted(t *testing.T) {
	resp := nativeKeyBindingStoreError(fmt.Errorf("%w: /sensitive/state/path: disk full", policy.ErrNativeKeyBindingPersistence))
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	if bytes.Contains(resp.Body, []byte("/sensitive/state/path")) || bytes.Contains(resp.Body, []byte("disk full")) {
		t.Fatalf("persistence response exposed I/O details: %s", resp.Body)
	}
	if !bytes.Contains(resp.Body, []byte(`"code":"persistence_failed"`)) {
		t.Fatalf("persistence response body=%s", resp.Body)
	}
}

func assertNativeBindingResponseIsRedacted(t *testing.T, body []byte, secrets ...string) {
	t.Helper()
	text := string(body)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("response exposed plaintext API key: %s", text)
		}
		if callerScope := policy.NativeCallerScope(secret); callerScope != "" && strings.Contains(text, callerScope) {
			t.Fatalf("response exposed caller_scope value: %s", text)
		}
	}
	if strings.Contains(text, `"caller_scope"`) {
		t.Fatalf("response exposed caller_scope: %s", text)
	}
}
