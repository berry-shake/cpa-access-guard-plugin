package plugin

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpa-access-guard/internal/policy"
)

func TestNativeQuotaResetClockManagementImmediateDeadline(t *testing.T) {
	app, _ := configureNativeBindingManagementApp(t)
	app.store.StopUsageFlusher()
	anchor := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	now := anchor
	app.store.SetClock(func() time.Time { return now })
	const path = "/v0/management/plugins/access-guard/native-key-bindings"
	for _, id := range []string{"one", "two", "never-used", "old-expired"} {
		resp := nativeBindingManagementCall(t, app, http.MethodPost, path, nil, map[string]any{
			"id": id, "key": "synthetic-reset-clock-" + id, "group": "team", "weekly_usd": 50,
		})
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("create %s: %s", id, resp.Body)
		}
	}
	app.store.RecordUsage("synthetic-reset-clock-old-expired", "", "old-model", false, policy.UsageDetail{InputTokens: 10})
	if resp := nativeBindingManagementCall(t, app, http.MethodPost, path+"/reset-quota", nil, map[string]any{"id": "one"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("single reset: %s", resp.Body)
	}
	if resp := nativeBindingManagementCall(t, app, http.MethodPost, path+"/reset-quota-batch", nil, map[string]any{"ids": []string{"two"}}); resp.StatusCode != http.StatusOK {
		t.Fatalf("batch reset: %s", resp.Body)
	}
	for _, days := range []int{0, 3, 8, 24} {
		now = anchor.Add(time.Duration(days) * 24 * time.Hour)
		resp := nativeBindingManagementCall(t, app, http.MethodGet, path, nil, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list: %s", resp.Body)
		}
		var payload struct {
			Bindings []publicNativeKeyBinding `json:"bindings"`
		}
		if err := json.Unmarshal(resp.Body, &payload); err != nil {
			t.Fatal(err)
		}
		for _, binding := range payload.Bindings {
			want := ""
			switch binding.ID {
			case "one", "two":
				want = anchor.Add(time.Duration(days/7+1) * 7 * 24 * time.Hour).Format(time.RFC3339)
				if binding.Usage == nil || binding.Usage.WeeklyCalls != 0 || binding.Usage.WeeklyUSDUsed != 0 {
					t.Fatalf("reset binding must remain unused: %+v", binding)
				}
			case "old-expired":
				if days < 7 {
					want = anchor.Add(7 * 24 * time.Hour).Format(time.RFC3339)
				}
			}
			if binding.Usage == nil || binding.Usage.WeeklyResetAt != want {
				t.Fatalf("day=%d id=%s usage=%+v want deadline=%s", days, binding.ID, binding.Usage, want)
			}
		}
		if strings.Contains(string(resp.Body), "weekly_reset_anchor") || strings.Contains(string(resp.Body), "caller_scope") {
			t.Fatalf("management DTO exposed private ledger fields: %s", resp.Body)
		}
	}
}
