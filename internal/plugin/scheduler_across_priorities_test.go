package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"

	"cpa-access-guard/internal/policy"
)

func TestSchedulerAcrossPrioritiesRegistrationContract(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.Shutdown)
	config, err := json.Marshal(LifecycleRequest{ConfigYAML: []byte(fmt.Sprintf("enabled: true\nstate_file: %q\n", filepath.ToSlash(filepath.Join(t.TempDir(), "state.json"))))})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := app.HandleMethod(MethodPluginRegister, config)
	if err != nil {
		t.Fatal(err)
	}
	var registration struct {
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := unmarshalOK(raw, &registration); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"scheduler", "scheduler_across_priorities"} {
		if string(registration.Capabilities[name]) != "true" {
			t.Fatalf("registration capability %s = %s, want true", name, registration.Capabilities[name])
		}
	}
}

func acrossPriorityRequest(candidates ...SchedulerAuthCandidate) SchedulerPickRequest {
	return SchedulerPickRequest{
		Provider: "codex", Providers: []string{"codex"}, Model: "gpt-6.1-sol",
		Options: SchedulerPickOptions{
			Headers: map[string][]string{"Session_id": {"preserve-this-session"}},
			Metadata: map[string]any{
				SchedulerCallerScopeMetadataKey: testNativeCallerScope,
				"session_id":                    "preserve-this-session", "prompt_cache_key": "keep-cache-key",
			},
		},
		Candidates: candidates,
	}
}

func assertAcrossPriorityRejection(t *testing.T, app *App, request SchedulerPickRequest) {
	t.Helper()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	response, err := app.HandleMethod(MethodSchedulerPick, raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	if err := json.Unmarshal(response, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.OK || envelope.Error == nil || envelope.Error.Code != "auth_not_found" || envelope.Error.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("exhausted authorization must reject, not delegate: %+v", envelope)
	}
}

func TestSchedulerAcrossPrioritiesSingleBoundAPISupply(t *testing.T) {
	const supply = "codex:apikey:synthetic-selected"
	for _, roundRobin := range []bool{false, true} {
		binding := policy.NativeKeyBinding{ID: "api-only", Enabled: true, RoundRobin: roundRobin, CallerScope: testNativeCallerScope, AuthIDs: []string{supply}}
		app := newNativeRoundRobinTestApp(t, binding)
		request := acrossPriorityRequest(
			SchedulerAuthCandidate{ID: "unbound-oauth.json", Provider: "codex", Priority: 100, Status: "active"},
			SchedulerAuthCandidate{ID: supply, Provider: "codex", Priority: -5, Status: "active"},
			SchedulerAuthCandidate{ID: "codex:apikey:unbound", Provider: "codex", Priority: 200, Status: "active"},
		)
		before, _ := json.Marshal(request)
		assertNativeRoundRobinSequence(t, app, request, supply, supply, supply)
		after, _ := json.Marshal(request)
		if string(before) != string(after) {
			t.Fatal("selection changed caller/session metadata")
		}
		// Older hosts only offer their global top tier. Do not try to invent
		// unavailable candidates or delegate outside the binding on those hosts.
		request.Candidates = request.Candidates[:1]
		assertAcrossPriorityRejection(t, app, request)
	}
}

func TestSchedulerAcrossPrioritiesAuthorizedTierAndRetry(t *testing.T) {
	for _, roundRobin := range []bool{false, true} {
		binding := policy.NativeKeyBinding{ID: "authorized-pool", Enabled: true, RoundRobin: roundRobin, CallerScope: testNativeCallerScope, AuthIDs: []string{"high-a", "high-b", "low"}}
		app := newNativeRoundRobinTestApp(t, binding)
		request := acrossPriorityRequest(
			SchedulerAuthCandidate{ID: "outside", Provider: "codex", Priority: 100},
			SchedulerAuthCandidate{ID: "low", Provider: "codex", Priority: -2},
			SchedulerAuthCandidate{ID: "high-b", Provider: "codex", Priority: 10},
			SchedulerAuthCandidate{ID: "high-a", Provider: "codex", Priority: 10},
		)
		if roundRobin {
			assertNativeRoundRobinSequence(t, app, request, "high-a", "high-b", "high-a", "high-b")
		} else {
			assertNativeRoundRobinSequence(t, app, request, "high-a", "high-a", "high-a")
		}
		// CPA removes tried/cooled high-tier credentials. The next eligible
		// lower-tier member is still inside the explicit authorization pool.
		request.Candidates = request.Candidates[:2]
		assertNativeRoundRobinSequence(t, app, request, "low")
		request.Candidates = request.Candidates[:1]
		assertAcrossPriorityRejection(t, app, request)
		request.Candidates = nil
		assertAcrossPriorityRejection(t, app, request)
	}
}

func TestSchedulerAcrossPrioritiesGroupAndDownstreamBoundaries(t *testing.T) {
	for _, native := range []bool{true, false} {
		app := configureNativeBindingSchedulerApp(t, true)
		request := acrossPriorityRequest(
			SchedulerAuthCandidate{ID: "outside-free", Provider: "codex", Priority: 500, Attributes: map[string]string{"plan_type": "free"}},
			SchedulerAuthCandidate{ID: "team-low", Provider: "codex", Priority: -10, Attributes: map[string]string{"plan_type": "team"}},
			SchedulerAuthCandidate{ID: "team-high", Provider: "codex", Priority: -1, Attributes: map[string]string{"plan_type": "team"}},
			SchedulerAuthCandidate{ID: "team-disabled", Provider: "codex", Priority: 1000, Status: "disabled", Attributes: map[string]string{"plan_type": "team"}},
		)
		if !native {
			request.Options.Metadata = map[string]any{SchedulerGroupMetadataKey: "team"}
		}
		assertNativeRoundRobinSequence(t, app, request, "team-high")
		request.Candidates = request.Candidates[:2]
		assertNativeRoundRobinSequence(t, app, request, "team-low")
		request.Candidates = request.Candidates[:1]
		assertAcrossPriorityRejection(t, app, request)
	}
}

func TestSchedulerAcrossPrioritiesUnboundAndDisabledPreserveHostSelection(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		app := configureNativeBindingSchedulerApp(t, !disabled)
		request := acrossPriorityRequest(
			SchedulerAuthCandidate{ID: "high", Provider: "codex", Priority: 100},
			SchedulerAuthCandidate{ID: "low", Provider: "codex", Priority: -10},
		)
		if !disabled {
			request.Options.Metadata[SchedulerCallerScopeMetadataKey] = testDisabledCallerScope
		}
		before, _ := json.Marshal(request)
		result, err := nativeRoundRobinPick(app, request)
		if err != nil || !reflect.DeepEqual(result, SchedulerPickResponse{}) {
			t.Fatalf("unbound/disabled selection must defer: %+v %v", result, err)
		}
		after, _ := json.Marshal(request)
		if string(before) != string(after) || len(app.nativeRoundRobin.cursors) != 0 {
			t.Fatal("unbound/disabled request altered session metadata or round-robin state")
		}
	}
}
