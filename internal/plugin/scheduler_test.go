package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testNativeCallerScope   = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDisabledCallerScope = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func configureNativeBindingSchedulerApp(t *testing.T, enabled bool) *App {
	t.Helper()
	app := NewApp()
	yaml := []byte(fmt.Sprintf(`
enabled: true
state_file: %q
native_key_bindings:
  - id: native-client-a
    name: Native Client A
    enabled: %t
    caller_scope: %q
    key_preview: "sk-nat...nt-a"
    auth_ids:
      - codex-team-low
      - codex-team-high
      - codex-team-disabled
      - codex-team
      - codex-z-team
      - codex-a-team
      - codex-m-team
      - team-low
      - team-high
      - team-disabled
`, filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")), enabled, testNativeCallerScope))
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure native binding app: %v", err)
	}
	t.Cleanup(app.Shutdown)
	return app
}

func configureDirectNativeBindingSchedulerApp(t *testing.T, authIDs ...string) *App {
	t.Helper()
	app := NewApp()
	var authIDYAML strings.Builder
	for _, authID := range authIDs {
		authIDYAML.WriteString(fmt.Sprintf("      - %q\n", authID))
	}
	yaml := []byte(fmt.Sprintf(`
enabled: true
state_file: %q
native_key_bindings:
  - id: native-direct
    name: Native Direct
    enabled: true
    caller_scope: %q
    key_preview: "sk-nat...rect"
    auth_ids:
%s`, filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")), testNativeCallerScope, authIDYAML.String()))
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure direct native binding app: %v", err)
	}
	t.Cleanup(app.Shutdown)
	return app
}

func configureModelRestrictedNativeBindingSchedulerApp(t *testing.T, rpm int) *App {
	t.Helper()
	app := NewApp()
	yaml := []byte(fmt.Sprintf(`
enabled: true
state_file: %q
native_key_bindings:
  - id: native-models
    name: Native Models
    enabled: true
    caller_scope: %q
    key_preview: "sk-nat...dels"
    auth_ids:
      - "tenant/codex.json"
      - "tenant/claude.json"
    model_access:
      mode: allowlist
      models:
        - provider: codex
          model: gpt-5.6-luna
    rpm: %d
`, filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")), testNativeCallerScope, rpm))
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure model-restricted native binding app: %v", err)
	}
	t.Cleanup(app.Shutdown)
	return app
}

func TestSchedulerPickWithoutCallerScopeDefers(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, true)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options:  SchedulerPickOptions{Metadata: map[string]any{}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-a-free", Provider: "codex"},
			{ID: "codex-b-team", Provider: "codex"},
		},
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, req)
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Handled {
		t.Fatalf("expected Handled=false without caller_scope, got %+v", resp)
	}
}

func TestSchedulerPickNativeBindingFiltersAndPrioritizes(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, true)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options: SchedulerPickOptions{Metadata: map[string]any{
			SchedulerCallerScopeMetadataKey: testNativeCallerScope,
		}},
		Candidates: []SchedulerAuthCandidate{
			// A higher-priority candidate outside the binding must never win.
			{ID: "codex-free", Provider: "codex", Priority: 100},
			{ID: "codex-team-low", Provider: "codex", Priority: 2},
			{ID: "codex-team-high", Provider: "codex", Priority: 9},
			// Unusable candidates remain ineligible even when they match the group.
			{ID: "codex-team-disabled", Provider: "codex", Priority: 200, Status: "disabled"},
		},
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, req)
	if err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || resp.AuthID != "codex-team-high" {
		t.Fatalf("expected highest-priority usable candidate in native binding, got %+v", resp)
	}
}

func TestSchedulerPickDirectNativeBindingFiltersExactAuthIDs(t *testing.T) {
	app := configureDirectNativeBindingSchedulerApp(t, "tenant/Codex-A.json", "tenant/codex-b.json")
	request := func(candidates []SchedulerAuthCandidate) Envelope {
		t.Helper()
		req, _ := json.Marshal(SchedulerPickRequest{
			Provider: "codex",
			Model:    "gpt-5-codex",
			Options: SchedulerPickOptions{Metadata: map[string]any{
				"group":                         "free",
				SchedulerCallerScopeMetadataKey: testNativeCallerScope,
			}},
			Candidates: candidates,
		})
		raw, err := app.HandleMethod(MethodSchedulerPick, req)
		if err != nil {
			t.Fatal(err)
		}
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}

	first := request([]SchedulerAuthCandidate{
		{ID: "tenant/unselected.json", Provider: "codex", Priority: 100},
		{ID: "tenant/codex-b.json", Provider: "codex", Priority: 2},
		{ID: "tenant/Codex-A.json", Provider: "codex", Priority: 9},
		{ID: "tenant/codex-b.json", Provider: "codex", Priority: 200, Status: "disabled"},
		{ID: "tenant/codex-a.json", Provider: "codex", Priority: 300}, // wrong case
	})
	if !first.OK {
		t.Fatalf("direct selection failed: %+v", first)
	}
	var firstResponse SchedulerPickResponse
	if err := json.Unmarshal(first.Result, &firstResponse); err != nil {
		t.Fatal(err)
	}
	if !firstResponse.Handled || firstResponse.AuthID != "tenant/Codex-A.json" {
		t.Fatalf("direct selection = %+v", firstResponse)
	}

	// The host removes already-tried auths before retrying. The remaining allowed
	// ID must still win, and an unselected candidate must never become a fallback.
	retry := request([]SchedulerAuthCandidate{
		{ID: "tenant/unselected.json", Provider: "codex", Priority: 100},
		{ID: "tenant/codex-b.json", Provider: "codex", Priority: 2},
	})
	var retryResponse SchedulerPickResponse
	if err := json.Unmarshal(retry.Result, &retryResponse); err != nil {
		t.Fatal(err)
	}
	if !retry.OK || !retryResponse.Handled || retryResponse.AuthID != "tenant/codex-b.json" {
		t.Fatalf("direct retry = envelope %+v response %+v", retry, retryResponse)
	}

	missing := request([]SchedulerAuthCandidate{{ID: "tenant/unselected.json", Provider: "codex", Priority: 100}})
	if missing.OK || missing.Error == nil || missing.Error.Code != "auth_not_found" || missing.Error.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("missing direct credentials must fail closed: %+v", missing)
	}
}

func TestSchedulerPickDirectNativeBindingAllowsRecoveredErrorStatus(t *testing.T) {
	app := configureDirectNativeBindingSchedulerApp(t, "tenant/codex-a.json")
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5.5",
		Options: SchedulerPickOptions{Metadata: map[string]any{
			SchedulerCallerScopeMetadataKey: testNativeCallerScope,
		}},
		Candidates: []SchedulerAuthCandidate{
			{
				ID:       "tenant/codex-a.json",
				Provider: "codex",
				Status:   "error",
			},
		},
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, req)
	if err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || resp.AuthID != "tenant/codex-a.json" {
		t.Fatalf("recovered host-eligible credential was rejected: %+v", resp)
	}
}

func TestSchedulerPickNativeBindingEnforcesModelAllowlist(t *testing.T) {
	app := configureModelRestrictedNativeBindingSchedulerApp(t, 0)
	pick := func(req SchedulerPickRequest) Envelope {
		t.Helper()
		req.Options.Metadata = map[string]any{SchedulerCallerScopeMetadataKey: testNativeCallerScope}
		rawRequest, _ := json.Marshal(req)
		raw, err := app.HandleMethod(MethodSchedulerPick, rawRequest)
		if err != nil {
			t.Fatal(err)
		}
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}

	denied := pick(SchedulerPickRequest{
		Provider: "codex", Model: "gpt-5.5",
		Candidates: []SchedulerAuthCandidate{{ID: "tenant/codex.json", Provider: "codex"}},
	})
	if denied.OK || denied.Error == nil || denied.Error.Code != "model_not_allowed" || denied.Error.HTTPStatus != http.StatusForbidden {
		t.Fatalf("unselected model must return model_not_allowed/403: %+v", denied)
	}
	var deniedBody struct {
		Error struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(denied.Error.Message), &deniedBody); err != nil ||
		deniedBody.Error.Type != "permission_error" || deniedBody.Error.Code != "model_not_allowed" {
		t.Fatalf("downstream denial body = %+v err=%v", deniedBody, err)
	}

	allowed := pick(SchedulerPickRequest{
		Provider: "codex", Model: "GPT-5.6-LUNA(HIGH)",
		Candidates: []SchedulerAuthCandidate{{ID: "tenant/codex.json", Provider: "codex"}},
	})
	var response SchedulerPickResponse
	if !allowed.OK || json.Unmarshal(allowed.Result, &response) != nil || !response.Handled || response.AuthID != "tenant/codex.json" {
		t.Fatalf("selected suffixed model did not schedule: envelope=%+v response=%+v", allowed, response)
	}

	// The same model name under an unselected provider must not win, even when
	// the host supplies a multi-provider route and gives it higher priority.
	mixed := pick(SchedulerPickRequest{
		Providers: []string{"claude", "codex"}, Model: "gpt-5.6-luna",
		Candidates: []SchedulerAuthCandidate{
			{ID: "tenant/claude.json", Provider: "claude", Priority: 100},
			{ID: "tenant/codex.json", Provider: "codex", Priority: 1},
		},
	})
	response = SchedulerPickResponse{}
	if !mixed.OK || json.Unmarshal(mixed.Result, &response) != nil || response.AuthID != "tenant/codex.json" {
		t.Fatalf("unselected provider bypassed model policy: envelope=%+v response=%+v", mixed, response)
	}
}

func TestSchedulerPickDeniedModelDoesNotConsumeNativeRPM(t *testing.T) {
	app := configureModelRestrictedNativeBindingSchedulerApp(t, 1)
	request := func(model string) Envelope {
		t.Helper()
		req, _ := json.Marshal(SchedulerPickRequest{
			Provider: "codex",
			Model:    model,
			Options: SchedulerPickOptions{Metadata: map[string]any{
				SchedulerCallerScopeMetadataKey: testNativeCallerScope,
			}},
			Candidates: []SchedulerAuthCandidate{{ID: "tenant/codex.json", Provider: "codex"}},
		})
		raw, err := app.HandleMethod(MethodSchedulerPick, req)
		if err != nil {
			t.Fatal(err)
		}
		var envelope Envelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope
	}

	if denied := request("gpt-5.5"); denied.OK || denied.Error == nil || denied.Error.Code != "model_not_allowed" {
		t.Fatalf("denied request = %+v", denied)
	}
	if firstAllowed := request("gpt-5.6-luna"); !firstAllowed.OK {
		t.Fatalf("denied model consumed RPM allowance: %+v", firstAllowed)
	}
	if secondAllowed := request("gpt-5.6-luna"); secondAllowed.OK || secondAllowed.Error == nil || secondAllowed.Error.Code != "quota_exceeded" {
		t.Fatalf("second allowed request should exhaust RPM: %+v", secondAllowed)
	}
}

func TestSchedulerPickMalformedCallerScopeFailsClosed(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, true)
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "non-string", value: 123},
		{name: "empty", value: ""},
		{name: "wrong length", value: "abc"},
		{name: "non-hex", value: strings.Repeat("z", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, _ := json.Marshal(SchedulerPickRequest{
				Provider: "codex",
				Model:    "gpt-5-codex",
				Options: SchedulerPickOptions{Metadata: map[string]any{
					"group":                         "free",
					SchedulerCallerScopeMetadataKey: test.value,
				}},
				Candidates: []SchedulerAuthCandidate{
					{ID: "codex-free", Provider: "codex"},
				},
			})
			raw, err := app.HandleMethod(MethodSchedulerPick, req)
			if err != nil {
				t.Fatal(err)
			}
			var envelope Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != "invalid_scheduler_metadata" || envelope.Error.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("malformed caller_scope must fail closed, got %+v", envelope)
			}
		})
	}
}

func TestSchedulerPickUnboundNativeKeyDefers(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, true)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options: SchedulerPickOptions{Metadata: map[string]any{
			SchedulerCallerScopeMetadataKey: testDisabledCallerScope,
		}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-team", Provider: "codex"},
		},
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, req)
	if err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Handled || resp.AuthID != "" {
		t.Fatalf("expected unbound native key to defer, got %+v", resp)
	}
}

func TestSchedulerPickDisabledNativeBindingDefers(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, false)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options: SchedulerPickOptions{Metadata: map[string]any{
			SchedulerCallerScopeMetadataKey: testNativeCallerScope,
		}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-team", Provider: "codex"},
		},
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, req)
	if err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Handled || resp.AuthID != "" {
		t.Fatalf("expected disabled native binding to defer, got %+v", resp)
	}
}

func TestSchedulerPickDisabledPluginDefersWithEnabledNativeBinding(t *testing.T) {
	app := NewApp()
	statePath := filepath.ToSlash(filepath.Join(t.TempDir(), "state.json"))
	yaml := []byte(fmt.Sprintf(`
enabled: false
state_file: %q
native_key_bindings:
  - id: native-client-a
    enabled: true
    caller_scope: %q
    auth_ids: [codex-team]
`, statePath, testNativeCallerScope))
	reconfigure, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, reconfigure); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Shutdown)

	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options: SchedulerPickOptions{Metadata: map[string]any{
			SchedulerCallerScopeMetadataKey: testNativeCallerScope,
		}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-team", Provider: "codex"},
		},
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, req)
	if err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Handled || resp.AuthID != "" {
		t.Fatalf("disabled plugin must defer, got %+v", resp)
	}
}

func TestSchedulerPickNativeBindingWithoutEligibleCandidateFailsClosed(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, true)
	tests := []struct {
		name       string
		candidates []SchedulerAuthCandidate
	}{
		{name: "empty candidate list"},
		{
			name: "only candidate outside bound auth IDs",
			candidates: []SchedulerAuthCandidate{
				{ID: "codex-free", Provider: "codex"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, _ := json.Marshal(SchedulerPickRequest{
				Provider: "codex",
				Model:    "gpt-5-codex",
				Options: SchedulerPickOptions{Metadata: map[string]any{
					SchedulerCallerScopeMetadataKey: testNativeCallerScope,
				}},
				Candidates: test.candidates,
			})
			raw, err := app.HandleMethod(MethodSchedulerPick, req)
			if err != nil {
				t.Fatal(err)
			}
			var envelope Envelope
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.OK || envelope.Error == nil || envelope.Error.Code != "auth_not_found" || envelope.Error.HTTPStatus != http.StatusServiceUnavailable {
				t.Fatalf("expected bound native key to fail closed with 503, got %+v", envelope)
			}
		})
	}
}

func TestSchedulerPickPriorityTiebreaksByID(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, true)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Options:  SchedulerPickOptions{Metadata: map[string]any{SchedulerCallerScopeMetadataKey: testNativeCallerScope}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-z-team", Provider: "codex", Priority: 5},
			{ID: "codex-a-team", Provider: "codex", Priority: 5},
			{ID: "codex-m-team", Provider: "codex", Priority: 9},
		},
	})
	raw, _ := app.HandleMethod(MethodSchedulerPick, req)
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	// Higher priority wins.
	if resp.AuthID != "codex-m-team" {
		t.Fatalf("expected highest priority, got %q", resp.AuthID)
	}

	// Equal priority → lowest ID.
	req2, _ := json.Marshal(SchedulerPickRequest{
		Options: SchedulerPickOptions{Metadata: map[string]any{SchedulerCallerScopeMetadataKey: testNativeCallerScope}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-z-team", Provider: "codex"},
			{ID: "codex-a-team", Provider: "codex"},
		},
	})
	raw2, _ := app.HandleMethod(MethodSchedulerPick, req2)
	var resp2 SchedulerPickResponse
	if err := unmarshalOK(raw2, &resp2); err != nil {
		t.Fatal(err)
	}
	if resp2.AuthID != "codex-a-team" {
		t.Fatalf("expected lowest ID tiebreak, got %q", resp2.AuthID)
	}
}

func unmarshalOK(raw []byte, v any) error {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	return json.Unmarshal(env.Result, v)
}

// configureNativeBindingWithLimits builds an app whose native binding carries
// usage limits (yaml `rpm` / `daily_usd` / `weekly_usd`), for the quota gate.
func configureNativeBindingWithLimits(t *testing.T, rpm, dailyUSD, weeklyUSD int) *App {
	t.Helper()
	app := NewApp()
	yaml := []byte(fmt.Sprintf(`
enabled: true
state_file: %q
native_key_bindings:
  - id: native-quota
    enabled: true
    caller_scope: %q
    key_preview: "sk-nat...quota"
    auth_ids: [codex-team, codex-a, codex-b, team]
    rpm: %d
    daily_usd: %d
    weekly_usd: %d
`, filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")), testNativeCallerScope, rpm, dailyUSD, weeklyUSD))
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure native quota app: %v", err)
	}
	t.Cleanup(app.Shutdown)
	return app
}

func nativeQuotaPickRequest() []byte {
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options: SchedulerPickOptions{Metadata: map[string]any{
			SchedulerCallerScopeMetadataKey: testNativeCallerScope,
		}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-team", Provider: "codex"},
		},
	})
	return req
}

func TestSchedulerPickNativeQuotaUnlimitedPasses(t *testing.T) {
	// Zero limits must be bit-for-bit identical to the pre-quota behavior:
	// group filter still applies, normal scheduling outcome.
	app := configureNativeBindingWithLimits(t, 0, 0, 0)
	raw, err := app.HandleMethod(MethodSchedulerPick, nativeQuotaPickRequest())
	if err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || resp.AuthID != "codex-team" {
		t.Fatalf("unlimited binding must schedule normally, got %+v", resp)
	}
}

func TestSchedulerPickNativeQuotaRPMExceeded(t *testing.T) {
	app := configureNativeBindingWithLimits(t, 1, 0, 0)
	// First request consumes the single allowance and schedules normally.
	raw, err := app.HandleMethod(MethodSchedulerPick, nativeQuotaPickRequest())
	if err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil || !resp.Handled {
		t.Fatalf("first request must pass, resp=%+v err=%v", resp, err)
	}
	// Second request hits the quota gate: pre-JSON body passed through by the
	// host verbatim.
	raw, err = app.HandleMethod(MethodSchedulerPick, nativeQuotaPickRequest())
	if err != nil {
		t.Fatal(err)
	}
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.OK || env.Error == nil || env.Error.Code != "quota_exceeded" || env.Error.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("expected quota_exceeded 429, got %+v", env)
	}
	var passthrough struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(env.Error.Message), &passthrough); err != nil {
		t.Fatalf("message must be valid JSON for host passthrough, got %q", env.Error.Message)
	}
	if passthrough.Error.Type != "usage_limit_reached" || passthrough.Error.Message != "The usage limit has been reached" {
		t.Fatalf("unexpected passthrough body: %+v", passthrough)
	}
}

// TestSchedulerPickLegacyGroupBindingFailsClosed: a credential group saved by
// an older release no longer schedules; the bound key gets a 503 until the
// operator selects exact credentials.
func TestSchedulerPickLegacyGroupBindingFailsClosed(t *testing.T) {
	app := NewApp()
	yaml := []byte(fmt.Sprintf(`
enabled: true
state_file: %q
native_key_bindings:
  - id: legacy-group
    enabled: true
    caller_scope: %q
    group: team
`, filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")), testNativeCallerScope))
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure legacy group binding: %v", err)
	}
	t.Cleanup(app.Shutdown)
	pick, _ := json.Marshal(SchedulerPickRequest{
		Provider:   "codex",
		Model:      "gpt-5-codex",
		Options:    SchedulerPickOptions{Metadata: map[string]any{SchedulerCallerScopeMetadataKey: testNativeCallerScope}},
		Candidates: []SchedulerAuthCandidate{{ID: "team", Provider: "codex", Priority: 10}},
	})
	raw, err := app.HandleMethod(MethodSchedulerPick, pick)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "auth_not_found" || envelope.Error.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("legacy group binding must fail closed, got %+v", envelope)
	}
}

// TestSchedulerPickIgnoresCandidateDetails: CPA still sends each candidate's
// attributes and metadata; the plugin must decode and pick without them.
func TestSchedulerPickIgnoresCandidateDetails(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, true)
	raw := []byte(`{"Provider":"codex","Model":"gpt-5-codex","Stream":true,` +
		`"Options":{"Headers":{"User-Agent":["x"]},"Metadata":{"caller_scope":"` + testNativeCallerScope + `"}},` +
		`"Candidates":[` +
		`{"ID":"codex-free","Provider":"codex","Priority":50,"Attributes":{"plan_type":"free"},"Metadata":{"blob":{"nested":[1,2,3]}}},` +
		`{"ID":"codex-team","Provider":"codex","Priority":1,"Attributes":{"plan_type":"team"},"Metadata":{"email":"a@example.com"}}]}`)
	out, err := app.HandleMethod(MethodSchedulerPick, raw)
	if err != nil {
		t.Fatal(err)
	}
	var resp SchedulerPickResponse
	if err := unmarshalOK(out, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || resp.AuthID != "codex-team" {
		t.Fatalf("pick = %+v, want codex-team", resp)
	}
}
