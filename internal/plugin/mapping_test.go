package plugin

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"cpa-access-guard/internal/policy"
)

// TestClassifyPreview verifies the classify-preview endpoint evaluates rules
// against credential descriptors and returns correct group mappings.
func TestClassifyPreview(t *testing.T) {
	app := NewApp()
	cfg := policy.Config{
		Enabled:   true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		ClassifyRules: []policy.ClassifyRule{
			{Name: "team-rule", Field: "plan_type", Pattern: "^team$", Group: "team", Enabled: true},
			{Name: "free-rule", Field: "tier", Pattern: "^free$", Group: "free", Enabled: true},
		},
	}
	if err := app.store.Configure(cfg); err != nil {
		t.Fatal(err)
	}

	// Build a classify-preview request with 3 descriptors.
	reqBody, _ := json.Marshal(map[string]any{
		"descriptors": []map[string]any{
			{"id": "codex-team-001", "provider": "codex", "attributes": map[string]string{"plan_type": "team"}},
			{"id": "codex-free-001", "provider": "codex", "attributes": map[string]string{"plan_type": "free"}},
			{"id": "unknown-001", "provider": "codex", "attributes": map[string]string{}},
		},
	})

	resp := app.classifyPreview(reqBody)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(resp.Body))
	}

	var result struct {
		Groups      map[string][]string `json:"groups"`
		GroupCounts map[string]int      `json:"group_counts"`
		RuleMatches map[string][]string `json:"rule_matches"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}

	// "team" group should have 1 file (codex-team-001).
	if len(result.Groups["team"]) != 1 || result.Groups["team"][0] != "codex-team-001" {
		t.Fatalf("team group mismatch: %+v", result.Groups["team"])
	}
	// "supported" group should have 1 file (unknown-001 with no attributes).
	if len(result.Groups["supported"]) != 1 {
		t.Fatalf("supported group should have 1 file, got %+v", result.Groups["supported"])
	}
	// "free" group: plan_type=free doesn't match ^team$ (team-rule), but
	// free-rule checks tier, not plan_type. So codex-free-001 should fall to
	// built-in: plan_type=free → "free" group.
	if len(result.Groups["free"]) != 1 || result.Groups["free"][0] != "codex-free-001" {
		t.Fatalf("free group mismatch: %+v", result.Groups["free"])
	}
	if len(result.RuleMatches["team-rule"]) != 1 || result.RuleMatches["team-rule"][0] != "codex-team-001" {
		t.Fatalf("team-rule matches mismatch: %+v", result.RuleMatches)
	}
}

// TestClassifyPreviewCustomRules verifies that custom rules with a custom
// field name work correctly.
func TestClassifyPreviewCustomField(t *testing.T) {
	app := NewApp()
	cfg := policy.Config{
		Enabled:   true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		ClassifyRules: []policy.ClassifyRule{
			{Name: "email-rule", Field: "email", Pattern: "@company\\.com$", Group: "company", Enabled: true},
		},
	}
	if err := app.store.Configure(cfg); err != nil {
		t.Fatal(err)
	}

	reqBody, _ := json.Marshal(map[string]any{
		"descriptors": []map[string]any{
			{"id": "user1", "provider": "codex", "attributes": map[string]string{"email": "user@company.com"}},
			{"id": "user2", "provider": "codex", "attributes": map[string]string{"email": "user@other.com"}},
		},
	})

	resp := app.classifyPreview(reqBody)
	var result struct {
		Groups map[string][]string `json:"groups"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}

	// "company" group should have user1.
	if len(result.Groups["company"]) != 1 || result.Groups["company"][0] != "user1" {
		t.Fatalf("company group mismatch: %+v", result.Groups["company"])
	}
	// user2 should fall to "supported" (no matching custom rule, no plan_type/tier).
	if len(result.Groups["supported"]) != 1 || result.Groups["supported"][0] != "user2" {
		t.Fatalf("supported group mismatch: %+v", result.Groups["supported"])
	}
}

// TestClassifyPreviewMultiGroup verifies that a credential matching multiple
// rules appears in multiple groups (multi-group semantics).
func TestClassifyPreviewMultiGroup(t *testing.T) {
	app := NewApp()
	cfg := policy.Config{
		Enabled:   true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		ClassifyRules: []policy.ClassifyRule{
			{Name: "by-plan", Field: "plan_type", Pattern: "^team$", Group: "team", Enabled: true},
			{Name: "by-filename", Field: "filename", Pattern: "^codex-", Group: "codex-files", Enabled: true},
		},
	}
	if err := app.store.Configure(cfg); err != nil {
		t.Fatal(err)
	}

	reqBody, _ := json.Marshal(map[string]any{
		"descriptors": []map[string]any{
			{"id": "codex-team-001", "provider": "codex", "attributes": map[string]string{"plan_type": "team"}},
		},
	})

	resp := app.classifyPreview(reqBody)
	var result struct {
		Groups map[string][]string `json:"groups"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}

	// The credential should appear in BOTH "team" and "codex-files" groups.
	if len(result.Groups["team"]) != 1 || result.Groups["team"][0] != "codex-team-001" {
		t.Fatalf("team group mismatch: %+v", result.Groups["team"])
	}
	if len(result.Groups["codex-files"]) != 1 || result.Groups["codex-files"][0] != "codex-team-001" {
		t.Fatalf("codex-files group mismatch: %+v", result.Groups["codex-files"])
	}
}

func TestClassifyPreviewUsesRE2ValidationForExplicitRules(t *testing.T) {
	app := NewApp()
	validBody, _ := json.Marshal(map[string]any{
		"rules": []map[string]any{
			{"name": "case-insensitive", "field": "note", "pattern": "(?i)^tenant-a$", "group": "tenant-a", "enabled": true},
		},
		"descriptors": []map[string]any{
			{"id": "a.json", "provider": "codex", "attributes": map[string]string{"note": "TENANT-A"}},
		},
	})
	valid := app.classifyPreview(validBody)
	if valid.StatusCode != 200 {
		t.Fatalf("valid Go/RE2 inline flag status=%d body=%s", valid.StatusCode, valid.Body)
	}
	var validResult struct {
		RuleMatches map[string][]string `json:"rule_matches"`
	}
	if err := json.Unmarshal(valid.Body, &validResult); err != nil {
		t.Fatal(err)
	}
	if got := validResult.RuleMatches["case-insensitive"]; len(got) != 1 || got[0] != "a.json" {
		t.Fatalf("valid RE2 rule matches=%v", got)
	}

	invalidBody, _ := json.Marshal(map[string]any{
		"rules": []map[string]any{
			{"name": "lookahead", "field": "filename", "pattern": "^(?=a)a\\.json$", "group": "bad", "enabled": true},
		},
		"descriptors": []map[string]any{},
	})
	invalid := app.classifyPreview(invalidBody)
	if invalid.StatusCode != 400 || !strings.Contains(string(invalid.Body), "validation_error") || !strings.Contains(string(invalid.Body), "Go/RE2") {
		t.Fatalf("invalid lookahead status=%d body=%s", invalid.StatusCode, invalid.Body)
	}

	// Disabled rules are still persisted configuration and may later be enabled.
	// Validate their regex now instead of allowing an invalid draft to appear
	// valid merely because it is currently switched off.
	disabledInvalidBody, _ := json.Marshal(map[string]any{
		"rules": []map[string]any{
			{"name": "disabled-lookahead", "field": "filename", "pattern": "^(?=a)a\\.json$", "group": "bad", "enabled": false},
		},
		"descriptors": []map[string]any{},
	})
	disabledInvalid := app.classifyPreview(disabledInvalidBody)
	if disabledInvalid.StatusCode != 400 || !strings.Contains(string(disabledInvalid.Body), "validation_error") || !strings.Contains(string(disabledInvalid.Body), "Go/RE2") {
		t.Fatalf("disabled invalid lookahead status=%d body=%s", disabledInvalid.StatusCode, disabledInvalid.Body)
	}
}

func TestClassifyPreviewReportsPerRuleMatchesAndDeduplicatesGroupUnion(t *testing.T) {
	app := NewApp()
	reqBody, _ := json.Marshal(map[string]any{
		"rules": []map[string]any{
			{"name": "by-file", "field": "filename", "pattern": "^a\\.json$", "group": "tenant-a", "enabled": true},
			{"name": "by-note", "field": "note", "pattern": "^tenant-a$", "group": "tenant-a", "enabled": true},
			{"name": "disabled", "field": "provider", "pattern": "^codex$", "group": "tenant-a", "enabled": false},
		},
		"descriptors": []map[string]any{
			{"id": "a.json", "provider": "codex", "attributes": map[string]string{"note": "tenant-a"}},
		},
	})
	resp := app.classifyPreview(reqBody)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var result struct {
		Groups      map[string][]string `json:"groups"`
		GroupCounts map[string]int      `json:"group_counts"`
		RuleMatches map[string][]string `json:"rule_matches"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if got := result.Groups["tenant-a"]; len(got) != 1 || got[0] != "a.json" || result.GroupCounts["tenant-a"] != 1 {
		t.Fatalf("group union was not de-duplicated: groups=%v counts=%v", result.Groups, result.GroupCounts)
	}
	for _, name := range []string{"by-file", "by-note"} {
		if got := result.RuleMatches[name]; len(got) != 1 || got[0] != "a.json" {
			t.Fatalf("rule %s matches=%v", name, got)
		}
	}
	if got := result.RuleMatches["disabled"]; got == nil || len(got) != 0 {
		t.Fatalf("disabled rule must have an explicit empty match list, got %#v", got)
	}
}

func TestClassifyPreviewExplicitEmptyRulesAndNonTierProviderStayFlat(t *testing.T) {
	app := NewApp()
	reqBody := []byte(`{"rules":[],"descriptors":[{"id":"claude.json","provider":"claude","attributes":{}},{"id":"ag.json","provider":"antigravity","attributes":{}},{"id":"  ","provider":"codex","attributes":{}}]}`)
	resp := app.classifyPreview(reqBody)
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var result struct {
		Groups      map[string][]string `json:"groups"`
		RuleMatches map[string][]string `json:"rule_matches"`
	}
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if got := result.Groups[""]; len(got) != 1 || got[0] != "claude.json" {
		t.Fatalf("non-tier provider should remain in flat empty group, got %+v", result.Groups)
	}
	if got := result.Groups["supported"]; len(got) != 1 || got[0] != "ag.json" {
		t.Fatalf("untiered antigravity should use supported, got %+v", result.Groups)
	}
	for group, ids := range result.Groups {
		for _, id := range ids {
			if strings.TrimSpace(id) == "" {
				t.Fatalf("blank descriptor ID leaked into group %q: %+v", group, result.Groups)
			}
		}
	}
	if len(result.RuleMatches) != 0 {
		t.Fatalf("explicit empty rules unexpectedly used stored rules: %+v", result.RuleMatches)
	}
}

// TestSchedulerCustomClassifyRule verifies that the scheduler's candidateGroups
// respects custom classify rules (multi-group, override built-in).
func TestSchedulerCustomClassifyRule(t *testing.T) {
	app := NewApp()
	cfg := policy.Config{
		Enabled:   true,
		StateFile: filepath.Join(t.TempDir(), "state.json"),
		ClassifyRules: []policy.ClassifyRule{
			{Name: "override-team", Field: "plan_type", Pattern: "^team$", Group: "custom-team", Enabled: true},
		},
	}
	if err := app.store.Configure(cfg); err != nil {
		t.Fatal(err)
	}

	// A candidate with plan_type=team should be in "classify:custom-team"
	// (custom rule group names are prefixed so they never collide with built-in
	// plan_type values) AND NOT in bare "team" (custom match skips built-in).
	cand := SchedulerAuthCandidate{
		ID:         "codex-team-001",
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "team"},
	}
	groups := app.candidateGroups(cand)

	found := map[string]bool{}
	for _, g := range groups {
		found[g] = true
	}
	if !found["classify:custom-team"] {
		t.Fatalf("expected 'classify:custom-team' group, got %v", groups)
	}
	if found["custom-team"] {
		t.Fatalf("bare custom-team must not appear (needs classify: prefix), got %v", groups)
	}
	if found["team"] {
		t.Fatalf("built-in 'team' should not appear when custom rule matched, got %v", groups)
	}
}
