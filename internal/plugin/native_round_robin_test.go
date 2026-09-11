package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"cpa-access-guard/internal/policy"
)

func newNativeRoundRobinTestApp(t *testing.T, bindings ...policy.NativeKeyBinding) *App {
	t.Helper()
	app := NewApp()
	if err := app.store.Configure(policy.Config{
		Enabled: true, StateFile: filepath.Join(t.TempDir(), "state.json"), NativeKeyBindings: bindings,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Shutdown)
	return app
}

func nativeRoundRobinTestBinding() policy.NativeKeyBinding {
	return policy.NativeKeyBinding{
		ID: "translation", Enabled: true, RoundRobin: true, CallerScope: testNativeCallerScope,
		AuthIDs: []string{"a", "b", "c", "d"},
	}
}

func nativeRoundRobinTestRequest() SchedulerPickRequest {
	return SchedulerPickRequest{
		Provider: "codex", Model: "gpt-5.3-codex-spark",
		Options: SchedulerPickOptions{
			Headers: map[string][]string{"Session_id": {"same-session"}},
			Metadata: map[string]any{
				SchedulerCallerScopeMetadataKey: testNativeCallerScope,
				"session_id":                    "same-session", "prompt_cache_key": "same-cache", "session_affinity": true,
			},
		},
		Candidates: []SchedulerAuthCandidate{
			{ID: "d", Provider: "codex"}, {ID: "b", Provider: "codex"},
			{ID: "c", Provider: "codex"}, {ID: "a", Provider: "codex"},
		},
	}
}

func nativeRoundRobinPick(app *App, req SchedulerPickRequest) (SchedulerPickResponse, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return SchedulerPickResponse{}, err
	}
	response, err := app.HandleMethod(MethodSchedulerPick, raw)
	if err != nil {
		return SchedulerPickResponse{}, err
	}
	var pick SchedulerPickResponse
	err = unmarshalOK(response, &pick)
	return pick, err
}

func assertNativeRoundRobinSequence(t *testing.T, app *App, req SchedulerPickRequest, want ...string) {
	t.Helper()
	for i, id := range want {
		pick, err := nativeRoundRobinPick(app, req)
		if err != nil || !pick.Handled || pick.AuthID != id {
			t.Fatalf("pick %d = %+v, error=%v; want explicit %q", i, pick, err, id)
		}
	}
}

func TestNativeRoundRobinSequentialAndConcurrentDistribution(t *testing.T) {
	for _, count := range []int{1, 3, 8, 12, 20, 40} {
		for _, concurrent := range []bool{false, true} {
			t.Run(fmt.Sprintf("count=%d/concurrent=%t", count, concurrent), func(t *testing.T) {
				app := newNativeRoundRobinTestApp(t, nativeRoundRobinTestBinding())
				req := nativeRoundRobinTestRequest()
				results := make(chan SchedulerPickResponse, count)
				errors := make(chan error, count)
				start := make(chan struct{})
				var wg sync.WaitGroup
				pick := func() {
					defer wg.Done()
					<-start
					result, err := nativeRoundRobinPick(app, req)
					results <- result
					errors <- err
				}
				if !concurrent {
					close(start)
				}
				for i := 0; i < count; i++ {
					wg.Add(1)
					if concurrent {
						go pick()
					} else {
						pick()
					}
				}
				if concurrent {
					close(start)
				}
				wg.Wait()
				close(results)
				close(errors)
				for err := range errors {
					if err != nil {
						t.Fatal(err)
					}
				}
				counts := map[string]int{}
				for result := range results {
					if !result.Handled {
						t.Fatalf("bound key delegated to host: %+v", result)
					}
					counts[result.AuthID]++
				}
				for i, id := range []string{"a", "b", "c", "d"} {
					want := count / 4
					if i < count%4 {
						want++
					}
					if counts[id] != want {
						t.Fatalf("counts=%v, %s want %d", counts, id, want)
					}
				}
				// One/three requests leave the cursor ready for the next batch.
				assertNativeRoundRobinSequence(t, app, req, []string{"a", "b", "c", "d"}[count%4])
			})
		}
	}
}

func TestNativeRoundRobinDefaultOffAndUnaffectedHostScheduling(t *testing.T) {
	for _, mode := range []string{"default-off", "unbound", "binding-disabled", "plugin-disabled"} {
		t.Run(mode, func(t *testing.T) {
			binding := nativeRoundRobinTestBinding()
			binding.RoundRobin = false
			if mode == "binding-disabled" {
				binding.RoundRobin, binding.Enabled = true, false
			}
			app := newNativeRoundRobinTestApp(t, binding)
			req := nativeRoundRobinTestRequest()
			if mode == "unbound" {
				req.Options.Metadata[SchedulerCallerScopeMetadataKey] = testDisabledCallerScope
			}
			if mode == "plugin-disabled" {
				if err := app.store.Configure(policy.Config{Enabled: false, StateFile: filepath.Join(t.TempDir(), "off.json")}); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := json.Marshal(req)
			for i := 0; i < 20; i++ {
				result, err := nativeRoundRobinPick(app, req)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "default-off" {
					if !result.Handled || result.AuthID != "a" {
						t.Fatalf("default-off behavior changed: %+v", result)
					}
				} else if result.Handled || result.AuthID != "" {
					t.Fatalf("host scheduling was overridden: %+v", result)
				}
			}
			after, _ := json.Marshal(req)
			if string(before) != string(after) || len(app.nativeRoundRobin.cursors) != 0 {
				t.Fatal("request/session metadata changed or inactive key allocated a cursor")
			}
		})
	}
}

func TestNativeRoundRobinPoolChangesAndRetrySuccessors(t *testing.T) {
	app := newNativeRoundRobinTestApp(t, nativeRoundRobinTestBinding())
	req := nativeRoundRobinTestRequest()
	assertNativeRoundRobinSequence(t, app, req, "a", "b")
	// The host removes already-tried or cooling credentials before the callback.
	req.Candidates = []SchedulerAuthCandidate{{ID: "d", Provider: "codex"}, {ID: "a", Provider: "codex"}, {ID: "c", Provider: "codex"}}
	assertNativeRoundRobinSequence(t, app, req, "c")
	req.Candidates = []SchedulerAuthCandidate{{ID: "a", Provider: "codex"}}
	assertNativeRoundRobinSequence(t, app, req, "a", "a")
	req = nativeRoundRobinTestRequest()
	assertNativeRoundRobinSequence(t, app, req, "b", "c", "d")
	// A new credential joins immediately; auth-list edits do not reset the cursor.
	ids := []string{"e", "c", "a", "d", "b"}
	if _, err := app.store.UpdateNativeKeyBinding("translation", policy.UpdateNativeKeyBindingInput{AuthIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	req.Candidates = append(req.Candidates, SchedulerAuthCandidate{ID: "e", Provider: "codex"})
	assertNativeRoundRobinSequence(t, app, req, "e", "a", "b")
	ids = []string{"a", "c", "d", "e"}
	if _, err := app.store.UpdateNativeKeyBinding("translation", policy.UpdateNativeKeyBindingInput{AuthIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	// Even a stale host list still containing removed B cannot authorize it.
	assertNativeRoundRobinSequence(t, app, req, "c", "d", "e", "a", "c")
	req.Candidates = []SchedulerAuthCandidate{
		{ID: "d", Provider: "codex", Status: "cooldown"}, {ID: "e", Provider: "codex", Status: "error"},
	}
	assertNativeRoundRobinSequence(t, app, req, "e")
	// Expired cooldown status "error" must rejoin rather than stay blocked.
	req.Candidates = []SchedulerAuthCandidate{{ID: "d", Provider: "codex", Status: "error"}, {ID: "e", Provider: "codex"}}
	assertNativeRoundRobinSequence(t, app, req, "d", "e")
}

func TestNativeRoundRobinPriorityAndGroupIsolation(t *testing.T) {
	for _, direct := range []bool{true, false} {
		t.Run(fmt.Sprintf("direct=%t", direct), func(t *testing.T) {
			binding := nativeRoundRobinTestBinding()
			if !direct {
				binding.AuthIDs, binding.Group = nil, "team"
			}
			app := newNativeRoundRobinTestApp(t, binding)
			req := nativeRoundRobinTestRequest()
			req.Candidates = []SchedulerAuthCandidate{
				{ID: "a", Provider: "codex", Priority: 0, Attributes: map[string]string{"plan_type": "team"}},
				{ID: "b", Provider: "codex", Priority: 5, Attributes: map[string]string{"plan_type": "team"}},
				{ID: "c", Provider: "codex", Priority: 5, Attributes: map[string]string{"plan_type": "team"}},
				{ID: "d", Provider: "codex", Priority: 500, Status: "disabled", Attributes: map[string]string{"plan_type": "team"}},
				{ID: "outsider", Provider: "codex", Priority: 500, Attributes: map[string]string{"plan_type": "free"}},
			}
			assertNativeRoundRobinSequence(t, app, req, "b", "c", "b", "c")
		})
	}
}

func TestNativeRoundRobinModelAllowlistFailsClosed(t *testing.T) {
	binding := nativeRoundRobinTestBinding()
	binding.ModelAccess = policy.NativeModelAccessPolicy{
		Mode:   policy.NativeModelAccessAllowlist,
		Models: []policy.NativeAllowedModel{{Provider: "codex", Model: "gpt-5.3-codex-spark"}},
	}
	app := newNativeRoundRobinTestApp(t, binding)
	req := nativeRoundRobinTestRequest()
	for _, modify := range []func(*SchedulerPickRequest){
		func(r *SchedulerPickRequest) { r.Model = "unselected-model" },
		func(r *SchedulerPickRequest) { r.Provider = "claude" },
		func(r *SchedulerPickRequest) { r.Candidates = nil },
		func(r *SchedulerPickRequest) {
			r.Candidates = []SchedulerAuthCandidate{{ID: "outsider", Provider: "codex"}}
		},
		func(r *SchedulerPickRequest) { r.Candidates = []SchedulerAuthCandidate{{ID: "a", Provider: "claude"}} },
	} {
		bad := req
		modify(&bad)
		raw, _ := json.Marshal(bad)
		response, err := app.HandleMethod(MethodSchedulerPick, raw)
		if err != nil {
			t.Fatal(err)
		}
		var env Envelope
		if err := json.Unmarshal(response, &env); err != nil {
			t.Fatal(err)
		}
		if env.Error == nil || (env.Error.HTTPStatus != http.StatusForbidden && env.Error.HTTPStatus != http.StatusServiceUnavailable) {
			t.Fatalf("unauthorized request did not fail closed: %s", response)
		}
	}
	if len(app.nativeRoundRobin.cursors) != 0 {
		t.Fatal("denied requests advanced rotation")
	}
	req.Providers = []string{"claude", "codex"}
	req.Candidates = []SchedulerAuthCandidate{{ID: "a", Provider: "claude"}, {ID: "b", Provider: "codex"}, {ID: "c", Provider: "codex"}}
	assertNativeRoundRobinSequence(t, app, req, "b", "c", "b")
}

func TestNativeRoundRobinPerKeyAndCanonicalRouteIsolation(t *testing.T) {
	binding := nativeRoundRobinTestBinding()
	other := binding
	other.ID, other.CallerScope = "other", testDisabledCallerScope
	app := newNativeRoundRobinTestApp(t, binding, other)
	req := nativeRoundRobinTestRequest()
	assertNativeRoundRobinSequence(t, app, req, "a")
	req.Model = " GPT-5.3-CODEX-SPARK(high) "
	assertNativeRoundRobinSequence(t, app, req, "b")
	req.Options.Metadata[SchedulerCallerScopeMetadataKey] = testDisabledCallerScope
	assertNativeRoundRobinSequence(t, app, req, "a", "b")
	req.Model = "another-model"
	assertNativeRoundRobinSequence(t, app, req, "a")
	req.Provider = "another-provider"
	assertNativeRoundRobinSequence(t, app, req, "a")
	req.Provider = "openai-compatible-another-provider"
	assertNativeRoundRobinSequence(t, app, req, "b")
	req.Providers = []string{"codex", "another-provider"}
	assertNativeRoundRobinSequence(t, app, req, "a")
	req.Provider, req.Providers = "codex", []string{"another-provider", "codex"}
	assertNativeRoundRobinSequence(t, app, req, "b")
}

func TestNativeRoundRobinMixedKeysConcurrentKeepSeparateScheduling(t *testing.T) {
	binding := nativeRoundRobinTestBinding()
	defaultBinding := binding
	defaultBinding.ID, defaultBinding.CallerScope, defaultBinding.RoundRobin = "normal", testDisabledCallerScope, false
	app := newNativeRoundRobinTestApp(t, binding, defaultBinding)
	var wg sync.WaitGroup
	results := make(chan string, 60)
	errors := make(chan error, 60)
	start := make(chan struct{})
	for i := 0; i < 60; i++ {
		wg.Add(1)
		go func(kind int) {
			defer wg.Done()
			req := nativeRoundRobinTestRequest()
			if kind == 1 {
				req.Options.Metadata[SchedulerCallerScopeMetadataKey] = testDisabledCallerScope
			} else if kind == 2 {
				req.Options.Metadata[SchedulerCallerScopeMetadataKey] = policy.NativeCallerScope("unbound")
			}
			<-start
			result, err := nativeRoundRobinPick(app, req)
			if err != nil {
				errors <- err
			}
			results <- fmt.Sprintf("%d/%t/%s", kind, result.Handled, result.AuthID)
		}(i % 3)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for result := range results {
		counts[result]++
	}
	want := map[string]int{"0/true/a": 5, "0/true/b": 5, "0/true/c": 5, "0/true/d": 5, "1/true/a": 20, "2/false/": 20}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("mixed key distributions=%v, want %v", counts, want)
	}
}

func TestNativeRoundRobinCursorCleanupAndBound(t *testing.T) {
	app := newNativeRoundRobinTestApp(t, nativeRoundRobinTestBinding())
	req := nativeRoundRobinTestRequest()
	for i := 0; i < nativeRoundRobinCapacity+3; i++ {
		req.Model = fmt.Sprintf("model-%d", i)
		assertNativeRoundRobinSequence(t, app, req, "a")
	}
	if len(app.nativeRoundRobin.cursors) != nativeRoundRobinCapacity || app.nativeRoundRobin.recent.Len() != nativeRoundRobinCapacity {
		t.Fatal("cursor memory exceeded capacity")
	}
	access := policy.NativeModelAccessPolicy{Mode: policy.NativeModelAccessAllowlist, Models: []policy.NativeAllowedModel{{Provider: "codex", Model: req.Model}}}
	if _, err := app.store.UpdateNativeKeyBinding("translation", policy.UpdateNativeKeyBindingInput{ModelAccess: &access}); err != nil {
		t.Fatal(err)
	}
	if len(app.nativeRoundRobin.cursors) != 1 {
		t.Fatal("revoked model cursors were not pruned")
	}
	assertNativeRoundRobinSequence(t, app, req, "b")
	enabled := false
	if _, err := app.store.UpdateNativeKeyBinding("translation", policy.UpdateNativeKeyBindingInput{RoundRobin: &enabled}); err != nil {
		t.Fatal(err)
	}
	if len(app.nativeRoundRobin.cursors) != 0 || app.nativeRoundRobin.recent.Len() != 0 {
		t.Fatal("disabled round-robin retained cursors")
	}
	enabled = true
	if _, err := app.store.UpdateNativeKeyBinding("translation", policy.UpdateNativeKeyBindingInput{RoundRobin: &enabled}); err != nil {
		t.Fatal(err)
	}
	assertNativeRoundRobinSequence(t, app, req, "a")
	if _, err := app.store.UpdateNativeKeyBinding("translation", policy.UpdateNativeKeyBindingInput{APIKey: "rotated-key"}); err != nil {
		t.Fatal(err)
	}
	if len(app.nativeRoundRobin.cursors) != 0 {
		t.Fatal("rotated scope retained cursors")
	}
	req.Options.Metadata[SchedulerCallerScopeMetadataKey] = policy.NativeCallerScope("rotated-key")
	assertNativeRoundRobinSequence(t, app, req, "a")
	if err := app.store.DeleteNativeKeyBinding("translation"); err != nil {
		t.Fatal(err)
	}
	if len(app.nativeRoundRobin.cursors) != 0 || app.nativeRoundRobin.recent.Len() != 0 {
		t.Fatal("deleted binding retained cursors")
	}
}
