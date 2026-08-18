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
    group: team
keys: []
`, filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")), enabled, testNativeCallerScope))
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatalf("configure native binding app: %v", err)
	}
	t.Cleanup(app.Shutdown)
	return app
}

func TestSchedulerPickNoGroupDefers(t *testing.T) {
	app, _ := configureTestApp(t)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options:  SchedulerPickOptions{Metadata: map[string]any{}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-a-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
			{ID: "codex-b-team", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
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
		t.Fatalf("expected Handled=false when no group, got %+v", resp)
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
			{ID: "codex-free", Provider: "codex", Priority: 100, Attributes: map[string]string{"plan_type": "free"}},
			{ID: "codex-team-low", Provider: "codex", Priority: 2, Attributes: map[string]string{"plan_type": "team"}},
			{ID: "codex-team-high", Provider: "codex", Priority: 9, Attributes: map[string]string{"plan_type": "team"}},
			// Unusable candidates remain ineligible even when they match the group.
			{ID: "codex-team-disabled", Provider: "codex", Priority: 200, Status: "disabled", Attributes: map[string]string{"plan_type": "team"}},
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

func TestSchedulerPickNativeBindingTakesPriorityOverExplicitGroup(t *testing.T) {
	app := configureNativeBindingSchedulerApp(t, true)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options: SchedulerPickOptions{Metadata: map[string]any{
			SchedulerGroupMetadataKey:       "free",
			SchedulerCallerScopeMetadataKey: testNativeCallerScope,
		}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
			{ID: "codex-team", Provider: "codex", Priority: 100, Attributes: map[string]string{"plan_type": "team"}},
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
	if !resp.Handled || resp.AuthID != "codex-team" {
		t.Fatalf("expected native binding to override unverified group metadata, got %+v", resp)
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
					SchedulerGroupMetadataKey:       "free",
					SchedulerCallerScopeMetadataKey: test.value,
				}},
				Candidates: []SchedulerAuthCandidate{
					{ID: "codex-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
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
			{ID: "codex-team", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
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
			{ID: "codex-team", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
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
    group: team
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
			{ID: "codex-team", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
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
			name: "only candidate outside bound group",
			candidates: []SchedulerAuthCandidate{
				{ID: "codex-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
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

func TestSchedulerPickFiltersByPlanType(t *testing.T) {
	app, _ := configureTestApp(t)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Model:    "gpt-5-codex",
		Options: SchedulerPickOptions{Metadata: map[string]any{
			"group": "team",
		}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-a-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
			{ID: "codex-b-team", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
			{ID: "codex-c-plus", Provider: "codex", Attributes: map[string]string{"plan_type": "plus"}},
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
	if !resp.Handled || resp.AuthID != "codex-b-team" {
		t.Fatalf("expected team-only pick, got %+v", resp)
	}
}

func TestSchedulerPickPriorityTiebreaksByID(t *testing.T) {
	app, _ := configureTestApp(t)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Options:  SchedulerPickOptions{Metadata: map[string]any{"group": "team"}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-z-team", Provider: "codex", Priority: 5, Attributes: map[string]string{"plan_type": "team"}},
			{ID: "codex-a-team", Provider: "codex", Priority: 5, Attributes: map[string]string{"plan_type": "team"}},
			{ID: "codex-m-team", Provider: "codex", Priority: 9, Attributes: map[string]string{"plan_type": "team"}},
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
		Options: SchedulerPickOptions{Metadata: map[string]any{"group": "team"}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-z-team", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
			{ID: "codex-a-team", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
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

// Isolation guarantee: when a tier group has no matching candidate, we must NOT
// fall back to a different tier. The plugin must return a structured scheduler
// error because an empty AuthID is invalid and would make the host fall back.
func TestSchedulerPickNoTierMatchRefusesFallback(t *testing.T) {
	app, _ := configureTestApp(t)
	req, _ := json.Marshal(SchedulerPickRequest{
		Options: SchedulerPickOptions{Metadata: map[string]any{"group": "team"}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-a-free", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
			{ID: "codex-b-plus", Provider: "codex", Attributes: map[string]string{"plan_type": "plus"}},
		},
	})
	raw, _ := app.HandleMethod(MethodSchedulerPick, req)
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "auth_not_found" || envelope.Error.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("expected auth_not_found scheduler error, got %+v", envelope)
	}
}

// "supported"/"unknown" group matches only untiered candidates: a key pinned to
// a real tier never lands on an untiered file, and an untiered key never stings
// onto a tiered file.
func TestSchedulerPickSupportedMatchesUntieredOnly(t *testing.T) {
	app, _ := configureTestApp(t)
	req, _ := json.Marshal(SchedulerPickRequest{
		Options: SchedulerPickOptions{Metadata: map[string]any{"group": "supported"}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "codex-no-claim", Provider: "codex", Attributes: map[string]string{}},
			{ID: "codex-team", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}},
		},
	})
	raw, _ := app.HandleMethod(MethodSchedulerPick, req)
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || resp.AuthID != "codex-no-claim" {
		t.Fatalf("expected untiered pick, got %+v", resp)
	}
}

func TestSchedulerPickSupportedDoesNotSplitNonTierProvider(t *testing.T) {
	app, _ := configureTestApp(t)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "claude",
		Options:  SchedulerPickOptions{Metadata: map[string]any{"group": "supported"}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "claude-oauth", Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}},
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
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "auth_not_found" {
		t.Fatalf("non-tier provider must stay flat rather than join supported: %+v", envelope)
	}
}

// Custom classify groups are matched with the classify: prefix so they never
// collide with built-in plan_type values like "free".
func TestSchedulerPickMatchesClassifyPrefix(t *testing.T) {
	app := NewApp()
	yaml := []byte(`
enabled: true
state_file: "` + filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")) + `"
classify_rules:
  - name: vip-files
    field: filename
    pattern: "vip"
    group: vip
    enabled: true
keys: []
`)
	reqCfg, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, reqCfg); err != nil {
		t.Fatal(err)
	}
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "codex",
		Options:  SchedulerPickOptions{Metadata: map[string]any{"group": "classify:vip"}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "free-user.json", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
			{ID: "vip-user.json", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
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
	if !resp.Handled || resp.AuthID != "vip-user.json" {
		t.Fatalf("expected vip-user via classify:vip, got %+v", resp)
	}

	// Bare "vip" (no prefix) must NOT match — isolation from unprefixed names.
	req2, _ := json.Marshal(SchedulerPickRequest{
		Options: SchedulerPickOptions{Metadata: map[string]any{"group": "vip"}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "vip-user.json", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}},
		},
	})
	raw2, _ := app.HandleMethod(MethodSchedulerPick, req2)
	var envelope Envelope
	if err := json.Unmarshal(raw2, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "auth_not_found" {
		t.Fatalf("bare vip must return auth_not_found, got %+v", envelope)
	}
}

func TestCandidateClassifyCacheTracksAttributeChanges(t *testing.T) {
	app := NewApp()
	free := app.candidateGroups(SchedulerAuthCandidate{ID: "same.json", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}})
	team := app.candidateGroups(SchedulerAuthCandidate{ID: "same.json", Provider: "codex", Attributes: map[string]string{"plan_type": "team"}})
	if len(free) != 1 || free[0] != "free" || len(team) != 1 || team[0] != "team" {
		t.Fatalf("cached groups did not track attributes: free=%v team=%v", free, team)
	}
}

func TestCandidateClassifyCacheIsBounded(t *testing.T) {
	app := NewApp()
	for index := 0; index < classifyCacheCapacity+25; index++ {
		app.candidateGroups(SchedulerAuthCandidate{ID: fmt.Sprintf("auth-%d", index), Provider: "codex"})
	}
	app.classifyMu.RLock()
	size := len(app.classifyCache)
	app.classifyMu.RUnlock()
	if size > classifyCacheCapacity {
		t.Fatalf("classify cache size = %d, capacity = %d", size, classifyCacheCapacity)
	}
}

// antigravity uses a "tier" attribute rather than plan_type; same filter logic.
func TestSchedulerPickMatchesAntigravityTier(t *testing.T) {
	app, _ := configureTestApp(t)
	req, _ := json.Marshal(SchedulerPickRequest{
		Provider: "antigravity",
		Options:  SchedulerPickOptions{Metadata: map[string]any{"group": "free-tier"}},
		Candidates: []SchedulerAuthCandidate{
			{ID: "ag-paid", Provider: "antigravity", Attributes: map[string]string{"tier": "paid-tier"}},
			{ID: "ag-free", Provider: "antigravity", Attributes: map[string]string{"tier": "free-tier"}},
		},
	})
	raw, _ := app.HandleMethod(MethodSchedulerPick, req)
	var resp SchedulerPickResponse
	if err := unmarshalOK(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Handled || resp.AuthID != "ag-free" {
		t.Fatalf("expected antigravity free-tier pick, got %+v", resp)
	}
}

func unmarshalOK(raw []byte, v any) error {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return err
	}
	return json.Unmarshal(env.Result, v)
}
