package plugin

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"cpa-access-guard/internal/policy"
)

const appTestNativeKey = "sk-app-test-native-0123456789"

// configureTestApp configures an app with one native binding for
// appTestNativeKey and a $1 / $2 per 1M price for gpt-5.
func configureTestApp(t *testing.T) *App {
	t.Helper()
	app := NewApp()
	yaml := []byte(`
enabled: true
state_file: "` + filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")) + `"
native_key_bindings:
  - id: app-test
    enabled: true
    caller_scope: "` + policy.NativeCallerScope(appTestNativeKey) + `"
    auth_ids: ["codex-a.json"]
    daily_usd: 10
`)
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure: %v", err)
	}
	t.Cleanup(app.Shutdown)
	if err := app.store.UpsertModelPrice(policy.ModelPrice{ID: "gpt-5", Provider: "openai", InputPricePerMillion: 1, OutputPricePerMillion: 2}); err != nil {
		t.Fatal(err)
	}
	return app
}

// TestRegistrationDeclaresOnlyBodylessHooks: the plugin must not register the
// hooks that receive full request or response bodies.
func TestRegistrationDeclaresOnlyBodylessHooks(t *testing.T) {
	raw, err := json.Marshal(NewApp().registration().Capabilities)
	if err != nil {
		t.Fatal(err)
	}
	for _, removed := range []string{"frontend_auth_provider", "model_router", "response_interceptor"} {
		if strings.Contains(string(raw), removed) {
			t.Fatalf("capabilities still declare %s: %s", removed, raw)
		}
	}
	var caps map[string]bool
	if err := json.Unmarshal(raw, &caps); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"scheduler", "scheduler_across_priorities", "usage_plugin", "management_api"} {
		if !caps[required] {
			t.Fatalf("capability %s missing: %s", required, raw)
		}
	}
}

func TestRemovedMethodsAndRoutesAreUnknown(t *testing.T) {
	app := configureTestApp(t)
	for _, method := range []string{"frontend_auth.authenticate", "model.route", "response.intercept_after"} {
		raw, err := app.HandleMethod(method, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var env Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatal(err)
		}
		if env.OK || env.Error == nil || env.Error.Code != "unknown_method" {
			t.Fatalf("%s envelope = %+v, want unknown_method", method, env)
		}
	}
	base := "/v0/management/plugins/access-guard"
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/keys"}, {http.MethodGet, "/aliases"}, {http.MethodGet, "/classify-rules"}, {http.MethodPost, "/catalog"},
	} {
		req, _ := json.Marshal(ManagementRequest{Method: route.method, Path: base + route.path})
		resp := managementResponseFromEnvelope(t, mustHandle(t, app, MethodManagementHandle, req))
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", route.method, route.path, resp.StatusCode)
		}
	}
	registered := app.managementRegistration()
	if len(registered.Routes) != 20 {
		t.Fatalf("registered %d management routes, want 20", len(registered.Routes))
	}
}

func TestUsageHandleBillsNativeKey(t *testing.T) {
	app := configureTestApp(t)
	record := func(apiKey string, failed bool) {
		t.Helper()
		req, _ := json.Marshal(UsageHandleRequest{
			Model: "gpt-5", Alias: "gpt-5", APIKey: apiKey, Failed: failed,
			Detail: UsageDetail{InputTokens: 1_000_000, OutputTokens: 1_000_000},
		})
		if !okEnvelope(t, mustHandle(t, app, MethodUsageHandle, req)) {
			t.Fatal("usage.handle must always succeed")
		}
	}
	record(appTestNativeKey, false)
	record(appTestNativeKey, true)
	record("sk-unbound-native-key-0123456789", false)
	binding := app.store.NativeKeyBindingsSnapshot()[0]
	usage := app.store.NativeBindingUsage(binding)
	if usage.DailyUSDUsed != 3 || usage.DailyCalls != 1 {
		t.Fatalf("usage = %+v, want one $3 call", usage)
	}
	// A malformed record bills nothing and never fails the request path.
	if !okEnvelope(t, mustHandle(t, app, MethodUsageHandle, []byte(`{"Detail":`))) {
		t.Fatal("malformed usage.handle must still succeed")
	}
}

func managementResponseFromEnvelope(t *testing.T, raw []byte) ManagementResponse {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp ManagementResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestManagementRegistrationDeclaresResource(t *testing.T) {
	app := NewApp()
	resp := app.managementRegistration()
	if len(resp.Resources) == 0 {
		t.Fatal("no resources declared")
	}
	found := false
	for _, r := range resp.Resources {
		if r.Path == "/index.html" {
			found = true
		}
	}
	if !found {
		t.Fatalf("resources do not include /index.html: %+v", resp.Resources)
	}
}

func TestNewAppDefersPersistentStateUntilLifecycleConfiguration(t *testing.T) {
	app := NewApp()
	if got := app.Store().StatePath(); got != "" {
		t.Fatalf("state path initialized before lifecycle configuration: %q", got)
	}
}

func TestHandleManagementServesResourceUI(t *testing.T) {
	app := NewApp()
	req, _ := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/access-guard/index.html",
	})
	raw, err := app.HandleMethod(MethodManagementHandle, req)
	if err != nil {
		t.Fatal(err)
	}
	resp := managementResponseFromEnvelope(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Headers.Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	if len(resp.Body) == 0 {
		t.Fatal("resource body empty")
	}
}

func TestHandleManagementResourceUnknownPath404(t *testing.T) {
	app := NewApp()
	req, _ := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/resource/plugins/access-guard/assets/app.js",
	})
	raw, err := app.HandleMethod(MethodManagementHandle, req)
	if err != nil {
		t.Fatal(err)
	}
	resp := managementResponseFromEnvelope(t, raw)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func okEnvelope(t *testing.T, raw []byte) bool {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return env.OK
}

func mustHandle(t *testing.T, app *App, method string, req []byte) []byte {
	t.Helper()
	raw, err := app.HandleMethod(method, req)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestSafePluginCallRecoversPanic(t *testing.T) {
	response, err := safePluginCall(func() ([]byte, error) {
		panic("boom")
	})
	if err == nil || response != nil {
		t.Fatalf("safePluginCall response=%q error=%v", response, err)
	}
}
