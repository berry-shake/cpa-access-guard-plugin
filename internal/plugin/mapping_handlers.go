package plugin

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"cpa-access-guard/internal/policy"
)

// --- Alias mapping management handlers ---

// aliasUpsertRequest is the body for POST /aliases.
type aliasUpsertRequest struct {
	Alias                    string               `json:"alias"`
	Targets                  []policy.AliasTarget `json:"targets"`
	Dispatch                 string               `json:"dispatch"`
	BillingMode              string               `json:"billing_mode"`
	InputPricePerMillion     float64              `json:"input_price_per_million"`
	OutputPricePerMillion    float64              `json:"output_price_per_million"`
	CacheReadPricePerMillion  float64             `json:"cache_read_price_per_million"`
	CacheWritePricePerMillion float64             `json:"cache_write_price_per_million"`
	PerCallUSD                float64             `json:"per_call_usd"`
}

func (a *App) upsertAlias(raw []byte) ManagementResponse {
	var req aliasUpsertRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonError(http.StatusBadRequest, "bad_request", err.Error())
	}
	// Build the new alias entry.
	alias := policy.AliasMapping{
		Alias:                    req.Alias,
		Targets:                  req.Targets,
		Dispatch:                 req.Dispatch,
		BillingMode:              req.BillingMode,
		InputPricePerMillion:     req.InputPricePerMillion,
		OutputPricePerMillion:    req.OutputPricePerMillion,
		CacheReadPricePerMillion:  req.CacheReadPricePerMillion,
		CacheWritePricePerMillion: req.CacheWritePricePerMillion,
		PerCallUSD:                req.PerCallUSD,
	}
	if err := a.store.UpsertAlias(alias); err != nil {
		return jsonError(http.StatusBadRequest, "validation_error", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"alias": alias})
}

// aliasDeleteRequest is the body for DELETE /aliases.
type aliasDeleteRequest struct {
	Alias string `json:"alias"`
}

func (a *App) deleteAlias(raw []byte) ManagementResponse {
	var req aliasDeleteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonError(http.StatusBadRequest, "bad_request", err.Error())
	}
	if err := a.store.DeleteAlias(req.Alias); err != nil {
		return jsonError(http.StatusBadRequest, "delete_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true})
}

// --- Classification rule management handlers ---

// classifyRuleUpsertRequest is the body for POST /classify-rules.
type classifyRuleUpsertRequest struct {
	Name    string `json:"name"`
	Field   string `json:"field"`
	Pattern string `json:"pattern"`
	Group   string `json:"group"`
	Enabled bool   `json:"enabled"`
}

func (a *App) upsertClassifyRule(raw []byte) ManagementResponse {
	var req classifyRuleUpsertRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonError(http.StatusBadRequest, "bad_request", err.Error())
	}
	rule := policy.ClassifyRule{
		Name:    req.Name,
		Field:   req.Field,
		Pattern: req.Pattern,
		Group:   req.Group,
		Enabled: req.Enabled,
	}
	if err := a.store.UpsertClassifyRule(rule); err != nil {
		return jsonError(http.StatusBadRequest, "validation_error", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"rule": rule})
}

// classifyRuleDeleteRequest is the body for DELETE /classify-rules.
type classifyRuleDeleteRequest struct {
	Name string `json:"name"`
}

func (a *App) deleteClassifyRule(raw []byte) ManagementResponse {
	var req classifyRuleDeleteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonError(http.StatusBadRequest, "bad_request", err.Error())
	}
	if err := a.store.DeleteClassifyRule(req.Name); err != nil {
		return jsonError(http.StatusBadRequest, "delete_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true})
}

// classifyReorderRequest is the body for POST /classify-rules/reorder.
type classifyReorderRequest struct {
	Names []string `json:"names"` // ordered list of rule names
}

func (a *App) reorderClassifyRules(raw []byte) ManagementResponse {
	var req classifyReorderRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonError(http.StatusBadRequest, "bad_request", err.Error())
	}
	if err := a.store.ReorderClassifyRules(req.Names); err != nil {
		return jsonError(http.StatusBadRequest, "reorder_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"reordered": true})
}

// --- Classify preview handler ---

// classifyPreviewRequest is the body for POST /classify-preview. The frontend
// sends a list of credential descriptors (from the auth-files endpoint) and
// the rules to evaluate (or the store's current rules if Rules is empty).
type classifyPreviewRequest struct {
	// Descriptors are the credential descriptors to classify. Each has a
	// filename (ID), provider, and optional attributes (plan_type, tier, etc.).
	Descriptors []credentialDescriptor `json:"descriptors"`
	// Rules optionally supplies an explicit rule set. A pointer distinguishes an
	// omitted field (use the store's current rules) from `rules: []` (preview no
	// custom rules). Explicit rules are compiled strictly with Go's RE2 engine;
	// invalid patterns return a validation error instead of a misleading empty
	// preview.
	Rules *[]policy.ClassifyRule `json:"rules,omitempty"`
}

type credentialDescriptor struct {
	ID         string            `json:"id"`
	Provider   string            `json:"provider"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// classifyPreviewResponse is the result of POST /classify-preview.
type classifyPreviewResponse struct {
	// Groups maps group name → list of matching credential IDs.
	Groups map[string][]string `json:"groups"`
	// GroupCounts maps group name → count of matching credentials.
	GroupCounts map[string]int `json:"group_counts"`
	// RuleMatches reports each rule's own matches. Groups is the de-duplicated
	// union and cannot be used for a per-rule count when several rules target the
	// same group.
	RuleMatches map[string][]string `json:"rule_matches"`
}

func (a *App) classifyPreview(raw []byte) ManagementResponse {
	var req classifyPreviewRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonError(http.StatusBadRequest, "bad_request", err.Error())
	}

	// Use an explicitly supplied rule set (including an empty one), or fall back
	// to the store's current, already-validated rules.
	rules := []policy.ClassifyRule(nil)
	explicitRules := req.Rules != nil
	if explicitRules {
		rules = append(rules, (*req.Rules)...)
	} else {
		rules = a.store.ClassifyRulesSnapshot()
	}

	// Pre-compile the rules.
	type compiledRule struct {
		rule    policy.ClassifyRule
		pattern *regexp.Regexp
	}
	compiled := make([]compiledRule, 0, len(rules))
	ruleMatches := make(map[string][]string, len(rules))
	ruleSeen := make(map[string]struct{}, len(rules))
	for _, r := range rules {
		r.Name = strings.TrimSpace(r.Name)
		r.Field = strings.ToLower(strings.TrimSpace(r.Field))
		r.Pattern = strings.TrimSpace(r.Pattern)
		r.Group = strings.ToLower(strings.TrimSpace(r.Group))
		if explicitRules {
			if r.Name == "" {
				return jsonError(http.StatusBadRequest, "validation_error", "classify rule name is required")
			}
			if r.Field == "" {
				return jsonError(http.StatusBadRequest, "validation_error", "classify rule field is required")
			}
			if r.Pattern == "" {
				return jsonError(http.StatusBadRequest, "validation_error", "classify rule pattern is required")
			}
			if r.Group == "" {
				return jsonError(http.StatusBadRequest, "validation_error", "classify rule group is required")
			}
		}
		nameKey := strings.ToLower(r.Name)
		if _, exists := ruleSeen[nameKey]; exists {
			return jsonError(http.StatusBadRequest, "validation_error", "duplicate classify rule name: "+r.Name)
		}
		ruleSeen[nameKey] = struct{}{}
		ruleMatches[r.Name] = []string{}
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return jsonError(http.StatusBadRequest, "validation_error", "classify rule "+r.Name+" has invalid Go/RE2 regex: "+err.Error())
		}
		if !r.Enabled {
			continue
		}
		compiled = append(compiled, compiledRule{rule: r, pattern: re})
	}

	groups := make(map[string][]string)
	groupSeen := make(map[string]map[string]struct{})
	ruleMatchSeen := make(map[string]map[string]struct{}, len(rules))
	for name := range ruleMatches {
		ruleMatchSeen[name] = make(map[string]struct{})
	}
	appendGroup := func(group, id string) {
		seen := groupSeen[group]
		if seen == nil {
			seen = make(map[string]struct{})
			groupSeen[group] = seen
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		groups[group] = append(groups[group], id)
	}
	appendRuleMatch := func(name, id string) {
		seen := ruleMatchSeen[name]
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		ruleMatches[name] = append(ruleMatches[name], id)
	}

	for _, desc := range req.Descriptors {
		desc.ID = strings.TrimSpace(desc.ID)
		desc.Provider = strings.ToLower(strings.TrimSpace(desc.Provider))
		if desc.ID == "" {
			continue
		}
		matched := false
		// 1. Evaluate custom rules (multi-group: collect all matches).
		for _, cr := range compiled {
			val := descriptorFieldValue(desc, cr.rule.Field)
			if val != "" && cr.pattern.MatchString(val) {
				appendGroup(cr.rule.Group, desc.ID)
				appendRuleMatch(cr.rule.Name, desc.ID)
				matched = true
			}
		}
		// 2. If no custom rule matched, fall back to built-in plan_type/tier.
		if !matched {
			appendGroup(descriptorBuiltInGroup(desc), desc.ID)
		}
	}
	groupCounts := make(map[string]int, len(groups))
	for group, ids := range groups {
		groupCounts[group] = len(ids)
	}

	return jsonResponse(http.StatusOK, classifyPreviewResponse{
		Groups:      groups,
		GroupCounts: groupCounts,
		RuleMatches: ruleMatches,
	})
}

// descriptorFieldValue extracts a field value from a credential descriptor.
func descriptorFieldValue(desc credentialDescriptor, field string) string {
	field = strings.ToLower(strings.TrimSpace(field))
	switch field {
	case "filename", "id":
		return desc.ID
	case "provider":
		return desc.Provider
	default:
		if desc.Attributes != nil {
			return desc.Attributes[field]
		}
	}
	return ""
}

// descriptorBuiltInGroup mirrors policy.GroupsForCredential: explicit
// plan_type/tier values are honored for any provider, but the synthetic
// "supported" bucket exists only for built-in tier providers. Other providers
// remain flat (empty group) when no custom rule matches.
func descriptorBuiltInGroup(desc credentialDescriptor) string {
	provider := strings.ToLower(strings.TrimSpace(desc.Provider))
	plan, tier := "", ""
	if desc.Attributes != nil {
		plan = strings.ToLower(strings.TrimSpace(desc.Attributes["plan_type"]))
		tier = strings.ToLower(strings.TrimSpace(desc.Attributes["tier"]))
	}
	if plan != "" {
		return plan
	}
	if tier != "" {
		return tier
	}
	if policy.BuiltinTierProviders[provider] {
		return "supported"
	}
	return ""
}

// --- Catalog builder (POST /catalog) ---

// catalogRequest is the body for POST /catalog. The frontend gathers auth-file
// descriptors + per-file models (the plugin cannot list host auth-files itself)
// and the plugin applies classify rules + built-in tiering to produce picker
// entries with classify:-prefixed custom groups.
type catalogRequest struct {
	Credentials []policy.CatalogCredential `json:"credentials"`
	// Rules optionally override the store's classify rules (for dry-run
	// previews). Empty → use currently configured rules.
	Rules []policy.ClassifyRule `json:"rules,omitempty"`
}

type catalogResponse struct {
	Entries []policy.CatalogEntry `json:"entries"`
}

func (a *App) buildCatalog(raw []byte) ManagementResponse {
	var req catalogRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return jsonError(http.StatusBadRequest, "bad_request", err.Error())
	}
	rules := req.Rules
	if len(rules) == 0 {
		rules = a.store.ClassifyRulesSnapshot()
	}
	entries := policy.BuildCatalogEntries(req.Credentials, rules)
	if entries == nil {
		entries = []policy.CatalogEntry{}
	}
	return jsonResponse(http.StatusOK, catalogResponse{Entries: entries})
}
