package policy

import (
	"reflect"
	"regexp"
	"testing"
)

func TestFormatClassifyGroup(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  VIP ", "classify:vip"},
		{"classify:vip", "classify:vip"},
		{"classify:Already", "classify:already"},
		{"free", "classify:free"},
	}
	for _, c := range cases {
		if got := FormatClassifyGroup(c.in); got != c.want {
			t.Fatalf("FormatClassifyGroup(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func mustRule(t *testing.T, name, field, pattern, group string) ClassifyRule {
	t.Helper()
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatal(err)
	}
	r := ClassifyRule{Name: name, Field: field, Pattern: pattern, Group: group, Enabled: true}
	// set unexported compiled via Normalize path: use a tiny Config
	cfg := Config{Enabled: true, ClassifyRules: []ClassifyRule{r}}
	if err := normalizeConfig(&cfg); err != nil {
		t.Fatal(err)
	}
	_ = re
	return cfg.ClassifyRules[0]
}

func TestGroupsForCredentialCustomPrefix(t *testing.T) {
	rule := mustRule(t, "vip", "filename", `vip`, "vip")
	groups := GroupsForCredential("codex", map[string]string{"plan_type": "free"}, "user-vip.json", []ClassifyRule{rule})
	if !reflect.DeepEqual(groups, []string{"classify:vip"}) {
		t.Fatalf("got %v", groups)
	}
}

func TestGroupsForCredentialBuiltinWhenNoCustom(t *testing.T) {
	groups := GroupsForCredential("codex", map[string]string{"plan_type": "team"}, "a.json", nil)
	if !reflect.DeepEqual(groups, []string{"team"}) {
		t.Fatalf("got %v", groups)
	}
}

func TestGroupsForCredentialAntigravityWithoutTierUsesSupported(t *testing.T) {
	groups := GroupsForCredential("antigravity", nil, "ag.json", nil)
	if !reflect.DeepEqual(groups, []string{"supported"}) {
		t.Fatalf("untiered antigravity should use supported, got %v", groups)
	}
}

func TestGroupsForCredentialNonTieredFlat(t *testing.T) {
	groups := GroupsForCredential("claude", nil, "c.json", nil)
	if !reflect.DeepEqual(groups, []string{""}) {
		t.Fatalf("non-tiered should be flat empty group, got %v", groups)
	}
}

func TestGroupsForCredentialMultiCustom(t *testing.T) {
	r1 := mustRule(t, "a", "filename", `x`, "alpha")
	r2 := mustRule(t, "b", "filename", `x`, "beta")
	groups := GroupsForCredential("codex", map[string]string{"plan_type": "free"}, "x.json", []ClassifyRule{r1, r2})
	if !reflect.DeepEqual(groups, []string{"classify:alpha", "classify:beta"}) {
		t.Fatalf("got %v", groups)
	}
}

func TestBuildCatalogEntriesMergesModels(t *testing.T) {
	rule := mustRule(t, "vip", "filename", `vip`, "vip")
	entries := BuildCatalogEntries([]CatalogCredential{
		{ID: "a-vip.json", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}, Models: []string{"gpt-5.4-mini", "gpt-5.5"}},
		{ID: "b-vip.json", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}, Models: []string{"gpt-5.5", "gpt-5.4"}},
		{ID: "c-free.json", Provider: "codex", Attributes: map[string]string{"plan_type": "free"}, Models: []string{"gpt-5.4-mini"}},
	}, []ClassifyRule{rule})

	byGroup := map[string][]string{}
	for _, e := range entries {
		byGroup[e.Group] = e.Models
	}
	// vip files → classify:vip with union models
	if got := byGroup["classify:vip"]; len(got) != 3 {
		t.Fatalf("classify:vip models = %v, want 3 unique", got)
	}
	// non-vip free file → built-in free
	if got := byGroup["free"]; !reflect.DeepEqual(got, []string{"gpt-5.4-mini"}) {
		t.Fatalf("free models = %v", got)
	}
}

func TestBuildCatalogEntriesCompatNotAffected(t *testing.T) {
	// Catalog only sees auth-file credentials; empty provider skipped.
	entries := BuildCatalogEntries([]CatalogCredential{
		{ID: "x", Provider: "", Models: []string{"m"}},
		{ID: "y", Provider: "claude", Models: []string{"sonnet"}},
	}, nil)
	if len(entries) != 1 || entries[0].Provider != "claude" || entries[0].Group != "" {
		t.Fatalf("expected flat claude entry, got %+v", entries)
	}
}
