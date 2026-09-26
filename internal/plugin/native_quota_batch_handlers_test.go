package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"cpa-access-guard/internal/policy"
)

const nativeQuotaBatchTestPath = "/v0/management/plugins/access-guard/native-key-bindings/reset-quota-batch"

func nativeQuotaBatchFixture(t *testing.T) (*App, string, []policy.NativeKeyBinding) {
	t.Helper()
	app, statePath := configureNativeBindingManagementApp(t)
	app.store.StopUsageFlusher()
	if err := app.store.UpsertModelPrice(policy.ModelPrice{
		ID: "quota-batch-model", InputPricePerMillion: 1, OutputPricePerMillion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"one", "two", "three"} {
		secret := "sk-native-quota-batch-" + id + "-0123456789"
		response := nativeBindingManagementCall(t, app, http.MethodPost, nativeBatchTestPath, nil, map[string]any{
			"id": id, "key": secret, "group": "team", "rpm": 10, "daily_usd": 10, "weekly_usd": 40,
		})
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: status=%d body=%s", id, response.StatusCode, response.Body)
		}
		for request := 0; request <= index; request++ {
			if _, limited := app.store.CheckNativeKeyQuota(policy.NativeCallerScope(secret)); limited {
				t.Fatalf("seed %s request %d unexpectedly limited", id, request)
			}
			app.store.RecordUsage(secret, "", "quota-batch-model", false, policy.UsageDetail{
				InputTokens: 100_000, OutputTokens: 50_000,
			})
		}
	}
	if err := app.store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	bindings := app.store.NativeKeyBindingsSnapshot()
	for _, binding := range bindings {
		usage := app.store.NativeBindingUsage(binding)
		if usage.RPMUsed == 0 || usage.DailyUSDUsed == 0 || usage.WeeklyUSDUsed == 0 || usage.DailyCalls == 0 || usage.WeeklyCalls == 0 {
			t.Fatalf("fixture did not seed all quota counters: %+v", usage)
		}
	}
	return app, statePath, bindings
}

func nativeQuotaBatchUsage(app *App, bindings []policy.NativeKeyBinding) map[string]policy.NativeBindingUsageSummary {
	usage := make(map[string]policy.NativeBindingUsageSummary, len(bindings))
	for _, binding := range bindings {
		usage[binding.ID] = app.store.NativeBindingUsage(binding)
	}
	return usage
}

func nativeQuotaBatchRawCall(t *testing.T, app *App, body []byte) ManagementResponse {
	t.Helper()
	rawRequest, err := json.Marshal(ManagementRequest{Method: http.MethodPost, Path: nativeQuotaBatchTestPath, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := app.HandleMethod(MethodManagementHandle, rawRequest)
	if err != nil {
		t.Fatal(err)
	}
	return managementResponseFromEnvelope(t, raw)
}

func assertNativeQuotaBatchResponse(t *testing.T, response ManagementResponse, expectedIDs []string) {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("reset: status=%d body=%s", response.StatusCode, response.Body)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(response.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) != 3 || string(payload["reset"]) != "true" || string(payload["count"]) != fmt.Sprint(len(expectedIDs)) {
		t.Fatalf("unexpected response schema: %s", response.Body)
	}
	var ids []string
	if err := json.Unmarshal(payload["ids"], &ids); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, expectedIDs) {
		t.Fatalf("reset ids=%v, want %v", ids, expectedIDs)
	}
	for _, id := range []string{"one", "two", "three"} {
		assertNativeBindingResponseIsRedacted(t, response.Body, "sk-native-quota-batch-"+id+"-0123456789")
	}
}

func TestNativeQuotaBatchManagementResetsOnlyExplicitSelection(t *testing.T) {
	for _, test := range []struct {
		name string
		ids  []string
		want []string
	}{
		{name: "normalized_subset", ids: []string{" THREE ", "One", "three", " ONE "}, want: []string{"three", "one"}},
		{name: "explicit_all", ids: []string{"two", "three", "one"}, want: []string{"two", "three", "one"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			app, statePath, bindings := nativeQuotaBatchFixture(t)
			before := nativeQuotaBatchUsage(app, bindings)
			response := nativeBindingManagementCall(t, app, http.MethodPost, nativeQuotaBatchTestPath, nil, map[string]any{"ids": test.ids})
			assertNativeQuotaBatchResponse(t, response, test.want)
			if !reflect.DeepEqual(app.store.NativeKeyBindingsSnapshot(), bindings) {
				t.Fatal("quota reset changed the binding policies")
			}
			selected := make(map[string]bool, len(test.want))
			for _, id := range test.want {
				selected[id] = true
			}
			for _, binding := range bindings {
				after := app.store.NativeBindingUsage(binding)
				if selected[binding.ID] {
					if after.RPMUsed != 0 || after.DailyUSDUsed != 0 || after.WeeklyUSDUsed != 0 || after.DailyCalls != 0 || after.WeeklyCalls != 0 {
						t.Fatalf("selected %s was not reset: %+v", binding.ID, after)
					}
					if after.RPMLimit != before[binding.ID].RPMLimit || after.DailyUSDLimit != before[binding.ID].DailyUSDLimit || after.WeeklyUSDLimit != before[binding.ID].WeeklyUSDLimit {
						t.Fatalf("selected %s limits changed: %+v", binding.ID, after)
					}
				} else if after != before[binding.ID] {
					t.Fatalf("unselected %s changed: before=%+v after=%+v", binding.ID, before[binding.ID], after)
				}
			}

			// Load a separate Store directly from disk to verify response success
			// means the reset is durable, without flushing the original Store.
			restarted := policy.NewStore()
			if err := restarted.Configure(policy.Config{Enabled: true, StateFile: statePath}); err != nil {
				t.Fatal(err)
			}
			for _, binding := range restarted.NativeKeyBindingsSnapshot() {
				after := restarted.NativeBindingUsage(binding)
				if selected[binding.ID] {
					if after.DailyUSDUsed != 0 || after.WeeklyUSDUsed != 0 || after.DailyCalls != 0 || after.WeeklyCalls != 0 {
						t.Fatalf("restart resurrected %s quota: %+v", binding.ID, after)
					}
				} else if after.DailyUSDUsed != before[binding.ID].DailyUSDUsed || after.WeeklyUSDUsed != before[binding.ID].WeeklyUSDUsed || after.DailyCalls != before[binding.ID].DailyCalls || after.WeeklyCalls != before[binding.ID].WeeklyCalls {
					t.Fatalf("restart changed unselected %s quota: %+v", binding.ID, after)
				}
			}
		})
	}
}

func TestNativeQuotaBatchManagementRejectsInvalidRequestsWithoutResetting(t *testing.T) {
	app, statePath, bindings := nativeQuotaBatchFixture(t)
	beforeUsage := nativeQuotaBatchUsage(app, bindings)
	beforeDisk, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, nativeBatchMaxKeys+1)
	for index := range tooMany {
		tooMany[index] = "one"
	}
	tooManyJSON, err := json.Marshal(map[string]any{"ids": tooMany})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "sk-invalid-quota-sensitive-0123456789"
	for _, test := range []struct {
		name, body, code string
		status           int
	}{
		{"empty_body", "", "invalid_json", http.StatusBadRequest},
		{"truncated", `{"ids":["one"]`, "invalid_json", http.StatusBadRequest},
		{"unknown_field", `{"ids":["one"],"` + secret + `":true}`, "invalid_json", http.StatusBadRequest},
		{"wrong_type", `{"ids":"` + secret + `"}`, "invalid_json", http.StatusBadRequest},
		{"wrong_item_type", `{"ids":["one",2]}`, "invalid_json", http.StatusBadRequest},
		{"trailing_json", `{"ids":["one"]}{}`, "invalid_json", http.StatusBadRequest},
		{"trailing_garbage", `{"ids":["one"]}x`, "invalid_json", http.StatusBadRequest},
		{"missing_ids", `{}`, "invalid_request", http.StatusBadRequest},
		{"null_body", `null`, "invalid_request", http.StatusBadRequest},
		{"null_ids", `{"ids":null}`, "invalid_request", http.StatusBadRequest},
		{"empty_ids", `{"ids":[]}`, "invalid_request", http.StatusBadRequest},
		{"blank_id", `{"ids":["one"," \t "]}`, "invalid_request", http.StatusBadRequest},
		{"null_item", `{"ids":["one",null]}`, "invalid_request", http.StatusBadRequest},
		{"oversized_id", `{"ids":["one","` + strings.Repeat("x", nativeBatchMaxValueSize+1) + `"]}`, "invalid_request", http.StatusBadRequest},
		{"too_many_ids", string(tooManyJSON), "invalid_request", http.StatusBadRequest},
		{"oversized_body", `{"ids":["one"]}` + strings.Repeat(" ", nativeBatchMaxBodyBytes), "request_too_large", http.StatusRequestEntityTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := nativeQuotaBatchRawCall(t, app, []byte(test.body))
			if response.StatusCode != test.status || !bytes.Contains(response.Body, []byte(`"code":"`+test.code+`"`)) {
				t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
			}
			assertNativeBindingResponseIsRedacted(t, response.Body, secret)
			if !reflect.DeepEqual(nativeQuotaBatchUsage(app, bindings), beforeUsage) {
				t.Fatal("invalid request changed runtime quota")
			}
			afterDisk, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(beforeDisk, afterDisk) {
				t.Fatal("invalid request changed persisted state")
			}
		})
	}
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeQuotaBatchTestPath, url.Values{"ids": {"one"}}, map[string]any{})
	if response.StatusCode != http.StatusBadRequest || !reflect.DeepEqual(nativeQuotaBatchUsage(app, bindings), beforeUsage) {
		t.Fatalf("query ids must not replace explicit body ids: status=%d body=%s", response.StatusCode, response.Body)
	}
}

func TestNativeQuotaBatchManagementUnknownIDRejectsWholeBatch(t *testing.T) {
	app, statePath, bindings := nativeQuotaBatchFixture(t)
	beforeUsage := nativeQuotaBatchUsage(app, bindings)
	beforeDisk, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	const unknown = "sk-unknown-quota-sensitive-0123456789"
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeQuotaBatchTestPath, nil, map[string]any{
		"ids": []string{"one", unknown, "three"},
	})
	if response.StatusCode != http.StatusNotFound || !bytes.Contains(response.Body, []byte(`"code":"not_found"`)) {
		t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
	}
	assertNativeBindingResponseIsRedacted(t, response.Body, unknown)
	if !reflect.DeepEqual(nativeQuotaBatchUsage(app, bindings), beforeUsage) {
		t.Fatal("unknown ID caused a partial runtime reset")
	}
	afterDisk, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeDisk, afterDisk) {
		t.Fatal("unknown ID changed persisted state")
	}
}

func TestNativeQuotaBatchManagementPersistenceFailureIsAtomicAndRedacted(t *testing.T) {
	app, statePath, bindings := nativeQuotaBatchFixture(t)
	beforeUsage := nativeQuotaBatchUsage(app, bindings)
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
	response := nativeBindingManagementCall(t, app, http.MethodPost, nativeQuotaBatchTestPath, nil, map[string]any{
		"ids": []string{"one", "three"},
	})
	if response.StatusCode != http.StatusInternalServerError || string(response.Body) != `{"error":{"code":"persistence_failed","message":"failed to persist native quota reset"}}` {
		t.Fatalf("unsafe persistence error: status=%d body=%s", response.StatusCode, response.Body)
	}
	if !reflect.DeepEqual(nativeQuotaBatchUsage(app, bindings), beforeUsage) {
		t.Fatal("failed persistence changed runtime quota")
	}
	if !reflect.DeepEqual(app.store.NativeKeyBindingsSnapshot(), bindings) {
		t.Fatal("failed persistence changed binding policies")
	}
}

func TestNativeQuotaBatchManagementBodyLimitAndRegistration(t *testing.T) {
	app, _, _ := nativeQuotaBatchFixture(t)
	registered := make(map[string]int)
	for _, route := range app.managementRegistration().Routes {
		if strings.HasPrefix(route.Path, "/plugins/access-guard/native-key-bindings/reset-quota") {
			registered[route.Method+" "+route.Path]++
		}
	}
	for _, suffix := range []string{"reset-quota", "reset-quota-batch"} {
		if registered[http.MethodPost+" /plugins/access-guard/native-key-bindings/"+suffix] != 1 {
			t.Fatalf("missing or duplicate POST route: %s", suffix)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPatch, http.MethodDelete} {
		response := nativeBindingManagementCall(t, app, method, nativeQuotaBatchTestPath, nil, map[string]any{"ids": []string{"one"}})
		if response.StatusCode != http.StatusNotFound {
			t.Fatalf("unexpected %s route: status=%d body=%s", method, response.StatusCode, response.Body)
		}
	}
	body := []byte(`{"ids":["one"]}`)
	body = append(body, bytes.Repeat([]byte(" "), nativeBatchMaxBodyBytes-len(body))...)
	response := nativeQuotaBatchRawCall(t, app, body)
	assertNativeQuotaBatchResponse(t, response, []string{"one"})
}

func TestNativeQuotaBatchManagementStoreErrorsAreFixedAndRedacted(t *testing.T) {
	for _, test := range []struct {
		name, code string
		err        error
		status     int
	}{
		{"invalid", "invalid_request", policy.ErrInvalidNativeQuotaReset, http.StatusBadRequest},
		{"unknown", "not_found", policy.ErrUnknownNativeKeyBinding, http.StatusNotFound},
		{"persistence", "persistence_failed", policy.ErrNativeKeyBindingPersistence, http.StatusInternalServerError},
		{"unexpected", "operation_failed", errors.New("unexpected failure"), http.StatusInternalServerError},
	} {
		t.Run(test.name, func(t *testing.T) {
			const secret = "sk-quota-error-sensitive-0123456789"
			response := nativeQuotaBatchStoreError(fmt.Errorf("%s /private/state.json disk full: %w", secret, test.err))
			if response.StatusCode != test.status || !bytes.Contains(response.Body, []byte(`"code":"`+test.code+`"`)) {
				t.Fatalf("status=%d body=%s", response.StatusCode, response.Body)
			}
			assertNativeBindingResponseIsRedacted(t, response.Body, secret)
			for _, detail := range []string{"/private", "disk full", "unexpected failure"} {
				if bytes.Contains(response.Body, []byte(detail)) {
					t.Fatalf("error disclosed private details: %s", response.Body)
				}
			}
		})
	}
}
