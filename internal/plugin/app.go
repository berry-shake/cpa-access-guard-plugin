package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"cpa-access-guard/internal/plugin/web"
	"cpa-access-guard/internal/policy"
)

type App struct {
	store             *policy.Store
	classifyMu        sync.RWMutex
	classifyCache     map[string][]string
	pricingSyncMu     sync.Mutex
	pricingSyncer     *pricingSyncer
	pricingSyncStatus pricingSyncStatus
}

const classifyCacheCapacity = 4096

func NewApp() *App {
	// Defer persistent state initialization until the host sends the lifecycle
	// configuration. Configuring defaults here would create a stray
	// cpa-access-guard-state.json in the host process working directory before the
	// configured state_file path is known.
	return &App{store: policy.NewStore(), classifyCache: make(map[string][]string)}
}

func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	return safePluginCall(func() ([]byte, error) {
		return a.handleMethod(method, request)
	})
}

func (a *App) handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if err := a.configure(request); err != nil {
			return nil, err
		}
		return OKEnvelope(a.registration())
	case MethodFrontendAuthIdentifier:
		return OKEnvelope(IdentifierResponse{Identifier: PluginID})
	case MethodFrontendAuthAuthenticate:
		return a.authenticate(request)
	case MethodModelRoute:
		return a.routeModel(request)
	case MethodSchedulerPick:
		return a.pickScheduler(request)
	case MethodResponseInterceptAfter:
		return a.interceptResponse(request)
	case MethodUsageHandle:
		return a.handleUsage(request)
	case MethodManagementRegister:
		return OKEnvelope(a.managementRegistration())
	case MethodManagementHandle:
		return a.handleManagement(request)
	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method, http.StatusNotFound), nil
	}
}

func safePluginCall(call func() ([]byte, error)) (response []byte, err error) {
	defer func() {
		if recover() != nil {
			response = nil
			err = errors.New("plugin panic recovered")
		}
	}()
	return call()
}

func (a *App) configure(raw []byte) error {
	var req LifecycleRequest
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
	}
	cfg, err := policy.DecodeConfig(req.ConfigYAML)
	if err != nil {
		return err
	}
	if err := a.store.Configure(cfg); err != nil {
		return err
	}
	// Register the classify cache clear callback, then clear once for safety.
	a.store.SetOnClassifyRulesChanged(func() {
		a.clearClassifyCache()
	})
	a.clearClassifyCache()
	a.store.StartUsageFlusher()
	a.startPricingSyncer(cfg.PricingSync)
	return nil
}

// Shutdown flushes usage and stops the pricing sync loop. Host calls this on
// plugin unload.
func (a *App) Shutdown() {
	a.store.StopUsageFlusher()
	a.pricingSyncMu.Lock()
	if a.pricingSyncer != nil {
		a.pricingSyncer.stop()
		s := a.pricingSyncer
		a.pricingSyncer = nil
		a.pricingSyncMu.Unlock()
		<-s.doneCh
	} else {
		a.pricingSyncMu.Unlock()
	}
}

func (a *App) registration() Registration {
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           "origin652 / berry-shake",
			GitHubRepository: "https://github.com/berry-shake/cpa-access-guard-plugin",
			Logo:             "/v0/resource/plugins/" + PluginID + web.LogoPath,
			ConfigFields: []ConfigField{
				{Name: "enabled", Type: "boolean", Description: "Enable or disable this plugin without unloading it."},
				{Name: "state_file", Type: "string", Description: "JSON state file used for access-policy changes made through the Management API."},
				{Name: "pricing_file", Type: "string", Description: "Standalone model price catalog (USD per 1M tokens). Default: cpa-access-guard-model-pricing.json next to state_file. Relative paths resolve against the CPA process working directory."},
				{Name: "keys", Type: "array", Description: "Initial downstream API-key access-policy list. State file wins after it exists."},
				{Name: "native_key_bindings", Type: "array", Description: "Optional caller-scope bindings that constrain CPA-native downstream API keys to a credential group or exact auth IDs. Requires a Scheduler path carrying caller_scope, Home disabled, no unsupported special route, and quota-exceeded.antigravity-credits=false."},
				{Name: "pricing_sync", Type: "object", Description: "models.dev catalog refresh. Always on. Optional {interval_hours: int, url: string}."},
			},
		},
		Capabilities: Capabilities{
			FrontendAuthProvider:          true,
			FrontendAuthProviderExclusive: false,
			ModelRouter:                   true,
			Scheduler:                     true,
			ResponseInterceptor:           true,
			UsagePlugin:                   true,
			ManagementAPI:                 true,
		},
	}
}

func (a *App) authenticate(raw []byte) ([]byte, error) {
	var req FrontendAuthRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	decision := a.store.Authenticate(req.Method, req.Path, req.Headers, req.Query, req.Body)
	if !decision.Known || !decision.Allowed {
		return OKEnvelope(FrontendAuthResponse{Authenticated: false})
	}
	meta := map[string]string{
		"provider":        PluginID,
		"key_id":          decision.KeyID,
		"requested_model": decision.Requested,
	}
	if decision.Rule.Alias != "" {
		meta["alias"] = decision.Rule.Alias
		meta["target_provider"] = decision.Rule.Provider
		meta["target_model"] = decision.Rule.TargetModel
		if decision.Rule.Group != "" {
			// Group lets our Scheduler (scheduler.pick) restrict auth-file
			// selection to a tier/plan (codex plan_type, antigravity tier).
			// Empty = legacy "any file for the provider" behavior.
			meta["group"] = decision.Rule.Group
		}
	}
	return OKEnvelope(FrontendAuthResponse{
		Authenticated: true,
		Principal:     decision.Principal,
		Metadata:      meta,
	})
}

func (a *App) routeModel(raw []byte) ([]byte, error) {
	var req ModelRouteRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	rule, keyID, ok := a.store.Route(req.Headers, req.Query, req.RequestedModel)
	if !ok {
		return OKEnvelope(ModelRouteResponse{Handled: false})
	}
	return OKEnvelope(ModelRouteResponse{
		Handled:     true,
		TargetKind:  "provider",
		Target:      resolveProviderKey(rule.Provider, req.AvailableProviders),
		TargetModel: rule.TargetModel,
		Reason:      "access-guard:" + keyID,
	})
}

// resolveProviderKey maps a ModelRule's provider to the provider key CPA's
// auth manager uses, so HasBuiltinProvider(target) succeeds.
//
// OpenAI-compatibility providers are registered with auth.Provider
// prefixed as "openai-compatible-<name>" (see synthesizer.config:
// auth.Provider = OpenAICompatibleProviderKey(name)). The plugin's
// ModelRule.Provider field carries the bare name (e.g. "nvidia",
// "opencode"). Returning the bare name makes CPA skip the router
// ("model router returned unavailable provider") and fall back to the
// native path, which fails for non-native alias names like "test1".
//
// We pick the key present in AvailableProviders, trying the bare name
// first (for built-in providers like codex/claude/gemini) then the
// openai-compatible- prefixed form. If neither matches we return the
// bare name and let CPA's availability check skip us (the native path
// still resolves true model names).
// nativeQuotaError renders a quota rejection. The message is a complete JSON
// error body: CPA's BuildErrorResponseBody returns pre-JSON error text
// verbatim, so the client receives this object as-is.
func nativeQuotaError(decision policy.NativeQuotaDecision, model string) (code, message string) {
	_ = decision
	_ = model
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "usage_limit_reached",
			"message": "The usage limit has been reached",
		},
	})
	if err != nil {
		return "quota_exceeded", "The usage limit has been reached"
	}
	return "quota_exceeded", string(body)
}

// noCandidateMessage renders the group-isolation rejection exactly like the
// host's own "no auth available" error (see enrichAuthSelectionError), so the
// client cannot distinguish a group-boundary rejection from a plain missing
// credential — the grouping policy stays invisible.
func noCandidateMessage(req SchedulerPickRequest, group string) string {
	_ = group
	providers := req.Providers
	if len(providers) == 0 && req.Provider != "" {
		providers = []string{req.Provider}
	}
	providerText := strings.Join(providers, ",")
	if providerText == "" {
		providerText = "unknown"
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = "unknown"
	}
	return fmt.Sprintf("no auth available (providers=%s, model=%s)", providerText, model)
}

func resolveProviderKey(provider string, availableProviders []string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "" {
		return p
	}
	if len(availableProviders) == 0 {
		return p
	}
	for _, candidate := range []string{p, "openai-compatible-" + p} {
		for _, avail := range availableProviders {
			if strings.EqualFold(candidate, avail) {
				return candidate
			}
		}
	}
	return p
}

func (a *App) interceptResponse(raw []byte) ([]byte, error) {
	var req ResponseInterceptRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	// NOTE: billing is NOT done here. The host only invokes
	// response.intercept_after for non-streaming responses (the streaming path
	// goes through response.intercept_stream_chunk, which we don't handle).
	// Billing for both paths is centralized in usage.handle (handleUsage),
	// which the host fires after every request completes with already-parsed
	// token counts. Doing it here too would double-bill non-streaming requests.
	if req.Stream {
		// Streaming responses are not safe to rewrite (SSE framing) — return as-is.
		return OKEnvelope(ResponseInterceptResponse{})
	}
	alias, ok := a.store.ResponseAlias(req.RequestHeaders, nil, req.RequestedModel)
	if !ok {
		return OKEnvelope(ResponseInterceptResponse{})
	}
	body, changed := policy.RewriteTopLevelModel(req.Body, alias)
	if !changed {
		return OKEnvelope(ResponseInterceptResponse{})
	}
	return OKEnvelope(ResponseInterceptResponse{Body: body})
}

// pickScheduler implements the scheduler.pick host->plugin call. When the
// routed ModelRule had a Group (codex plan_type / antigravity tier), restrict
// candidate auths to those whose Attributes carry a matching identity. For a
// request authenticated by CPA's native config-api-key provider, a persisted
// caller_scope binding can supply either a group or an exact auth-ID allow-list.
// A matching native-key binding always wins over generic group metadata:
// caller_scope is the stable identity CPA derives after authentication, while
// group has no provider provenance in the Scheduler ABI and must not be able to
// weaken a binding.
//
// The plugin never sees the downstream ModelRule directly here; the group was
// stamped into request metadata by authenticate(), and the host forwards it as
// Options.Metadata["group"]. We read it defensively as either string or any.
//
// Candidate filtering, in order:
//  1. Keep candidates whose Attributes["plan_type"] (codex) equals the group.
//     Also accept Attributes["tier"] (antigravity) to match the same group.
//  2. A group of "supported" means "codex without an id_token plan" — match
//     candidates whose plan_type we cannot read (treat unknown plan as that
//     bucket), so a supported-but-untiered auth file serves them rather than
//     any tiered one.
//
// Among filtered candidates, pick the host's highest-priority one (ties broken
// by lowest ID for determinism). CPA filters the scheduler candidate list for
// the routed model before invoking this plugin. On an execution retry it calls
// scheduler.pick again with already-tried auths removed, so the group filter is
// reapplied and exhaustion fails closed instead of falling back across groups.
func (a *App) pickScheduler(raw []byte) ([]byte, error) {
	var req SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !a.store.Enabled() {
		// `enabled: false` disables every policy capability, including native
		// key bindings. The host remains responsible for its default scheduling.
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	group := ""
	var authIDs []string
	nativeBinding := false
	// Native CPA keys are authenticated before scheduler.pick. CPA forwards
	// their stable (hashed) identity as caller_scope, allowing the policy store
	// to resolve an auth-file group without exposing or persisting the plaintext
	// downstream key here. Resolve it before reading group: Scheduler metadata
	// does not identify which frontend-auth provider supplied an arbitrary group.
	callerScope, callerScopePresent, callerScopeValid := schedulerCallerScopeFromMetadata(req.Options.Metadata)
	if callerScopePresent && !callerScopeValid {
		// CPA owns caller_scope and always emits a 64-character SHA-256 hex
		// string. If the field exists with any other shape, do not silently treat
		// the request as unbound or let generic group metadata bypass the identity
		// check. This indicates an incompatible or corrupted host/plugin path.
		return ErrorEnvelope("invalid_scheduler_metadata", "invalid caller_scope metadata", http.StatusServiceUnavailable), nil
	}
	if callerScope != "" {
		// Usage-limit gate for native keys: RPM decrements here (concurrency
		// safe), USD limits compare against the usage.handle ledger. A binding
		// with no limits configured passes through with zero side effects.
		if decision, limited := a.store.CheckNativeKeyQuota(callerScope); limited {
			code, message := nativeQuotaError(decision, req.Model)
			return ErrorEnvelope(code, message, http.StatusTooManyRequests), nil
		}
		constraint, resolved := a.store.ResolveNativeKeyConstraint(callerScope, req.Provider, req.Model)
		nativeBinding = resolved
		if nativeBinding {
			group = strings.ToLower(strings.TrimSpace(constraint.Group))
			authIDs = constraint.AuthIDs
		}
	}
	if nativeBinding {
		if group == "" && len(authIDs) == 0 {
			// The store normally rejects empty restrictions during configuration.
			// Keep this guard fail-closed in case a future migration or Store
			// implementation violates that invariant.
			return ErrorEnvelope("auth_not_found", "native key binding has no credential restriction", http.StatusServiceUnavailable), nil
		}
	} else {
		group = schedulerGroupFromMetadata(req.Options.Metadata)
		if group == "" {
			// No group narrowed by this downstream key → let the host pick
			// freely, preserving legacy behavior for unbound native keys.
			return OKEnvelope(SchedulerPickResponse{Handled: false})
		}
	}
	if len(req.Candidates) == 0 {
		if nativeBinding {
			// A resolved native binding is an isolation boundary. Deferring on an
			// empty candidate list would let a host fallback escape that boundary.
			return ErrorEnvelope("auth_not_found", noCandidateMessage(req, group), http.StatusServiceUnavailable), nil
		}
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}

	allowedAuthIDs := make(map[string]struct{}, len(authIDs))
	for _, authID := range authIDs {
		allowedAuthIDs[authID] = struct{}{}
	}
	matched := make([]SchedulerAuthCandidate, 0, len(req.Candidates))
	for _, cand := range req.Candidates {
		if !schedulerCandidateUsable(cand.Status) {
			continue
		}
		if len(allowedAuthIDs) > 0 {
			if _, allowed := allowedAuthIDs[cand.ID]; allowed {
				matched = append(matched, cand)
			}
		} else if a.candidateMatchesGroup(cand, group) {
			matched = append(matched, cand)
		}
	}
	if len(matched) == 0 {
		// No candidate of this tier is available: do not silently degrade to a
		// different tier (that would break the isolation guarantee). Returning
		// Handled=false would let the host pick ANY auth including other tiers.
		// Instead we report an explicit "auth_not_found" so the caller sees the
		// intent honored (no available tier-matching auth) rather than a leak.
		return ErrorEnvelope("auth_not_found", noCandidateMessage(req, group), http.StatusServiceUnavailable), nil
	}

	best := matched[0]
	for _, cand := range matched[1:] {
		if cand.Priority > best.Priority ||
			(cand.Priority == best.Priority && cand.ID < best.ID) {
			best = cand
		}
	}
	return OKEnvelope(SchedulerPickResponse{Handled: true, AuthID: best.ID})
}

func schedulerCandidateUsable(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	status = strings.NewReplacer("-", "_", " ", "_").Replace(status)
	switch status {
	case "disabled", "error", "expired", "revoked", "invalid", "unavailable", "cooldown", "cooling_down", "quota_exhausted", "exhausted", "blocked":
		return false
	default:
		return true
	}
}

// candidateMatchesGroup reports whether a candidate auth belongs to the
// requested group. It first evaluates user-defined ClassifyRules (which can
// override built-in detection — a candidate may belong to multiple groups).
// If no custom rule matches, it falls back to the built-in plan_type/tier
// detection. Uses an ID-level cache for performance with large auth-file sets.
func (a *App) candidateMatchesGroup(cand SchedulerAuthCandidate, group string) bool {
	groups := a.candidateGroups(cand)
	for _, g := range groups {
		if g == group {
			return true
		}
	}
	return false
}

// candidateGroups returns all groups a candidate belongs to. Custom rules are
// evaluated first (multi-group: a candidate can match multiple rules). If no
// custom rule matches, the built-in plan_type/tier detection runs. Results are
// cached by candidate ID; the cache is cleared on reconfigure.
func (a *App) candidateGroups(cand SchedulerAuthCandidate) []string {
	cacheKey := candidateClassifyCacheKey(cand)
	// Check cache.
	a.classifyMu.RLock()
	if cached, ok := a.classifyCache[cacheKey]; ok {
		a.classifyMu.RUnlock()
		return cached
	}
	a.classifyMu.RUnlock()

	var groups []string
	// 1. Evaluate custom classify rules (multi-group: collect all matches).
	// Group names are stored bare on the rule but stamped/matched with the
	// classify: prefix so they never collide with built-in plan_type values.
	for _, rule := range a.store.ClassifyRulesSnapshot() {
		if !rule.Enabled || rule.Compiled() == nil {
			continue
		}
		val := candidateFieldValue(cand, rule.Field)
		if val != "" && rule.Compiled().MatchString(val) {
			if g := policy.FormatClassifyGroup(rule.Group); g != "" {
				groups = append(groups, g)
			}
		}
	}
	// 2. If no custom rule matched, fall back to built-in plan_type/tier.
	if len(groups) == 0 {
		if g := builtInGroup(cand); g != "" {
			groups = append(groups, g)
		}
	}

	// Cache the result.
	a.classifyMu.Lock()
	if a.classifyCache == nil || len(a.classifyCache) >= classifyCacheCapacity {
		a.classifyCache = make(map[string][]string)
	}
	a.classifyCache[cacheKey] = groups
	a.classifyMu.Unlock()
	return groups
}

func (a *App) clearClassifyCache() {
	a.classifyMu.Lock()
	a.classifyCache = make(map[string][]string)
	a.classifyMu.Unlock()
}

func candidateClassifyCacheKey(cand SchedulerAuthCandidate) string {
	keys := make([]string, 0, len(cand.Attributes))
	for key := range cand.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var builder strings.Builder
	builder.WriteString(cand.ID)
	builder.WriteByte(0)
	builder.WriteString(cand.Provider)
	for _, key := range keys {
		builder.WriteByte(0)
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(cand.Attributes[key])
	}
	return builder.String()
}

// candidateFieldValue extracts the value of a named field from the candidate.
// Supported fields: "filename" (cand.ID), "provider" (cand.Provider),
// "plan_type" (cand.Attributes["plan_type"]), "tier" (cand.Attributes["tier"]),
// or any custom attribute key.
func candidateFieldValue(cand SchedulerAuthCandidate, field string) string {
	field = strings.ToLower(strings.TrimSpace(field))
	switch field {
	case "filename", "id":
		return cand.ID
	case "provider":
		return cand.Provider
	default:
		if cand.Attributes != nil {
			return cand.Attributes[field]
		}
	}
	return ""
}

// builtInGroup mirrors policy.GroupsForCredential. An explicit plan_type/tier
// is honored for any provider, while the synthetic "supported" bucket exists
// only for providers that participate in built-in tiering. Other providers are
// flat when no custom rule matches.
func builtInGroup(cand SchedulerAuthCandidate) string {
	plan, tier := "", ""
	if cand.Attributes != nil {
		plan = strings.ToLower(strings.TrimSpace(cand.Attributes["plan_type"]))
		tier = strings.ToLower(strings.TrimSpace(cand.Attributes["tier"]))
	}
	if plan != "" {
		return plan
	}
	if tier != "" {
		return tier
	}
	if policy.BuiltinTierProviders[strings.ToLower(strings.TrimSpace(cand.Provider))] {
		return "supported"
	}
	return ""
}

// schedulerGroupFromMetadata reads the group stamped at authenticate time out
// of request-provided scheduler options. Tolerates string or any-typed values.
func schedulerGroupFromMetadata(meta map[string]any) string {
	if meta == nil {
		return ""
	}
	raw, ok := meta[SchedulerGroupMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch v := raw.(type) {
	case string:
		return strings.ToLower(strings.TrimSpace(v))
	default:
		return strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", v)))
	}
}

// schedulerCallerScopeFromMetadata reads and validates the stable native-key
// identity CPA stamps into request metadata after authentication. The returned
// booleans report whether the field was present and, when present, whether it
// had the host's required 64-character SHA-256 hex representation. Coercing or
// ignoring arbitrary values could let unrelated metadata bypass a binding.
func schedulerCallerScopeFromMetadata(meta map[string]any) (value string, present, valid bool) {
	if meta == nil {
		return "", false, true
	}
	raw, ok := meta[SchedulerCallerScopeMetadataKey]
	if !ok {
		return "", false, true
	}
	value, ok = raw.(string)
	if !ok {
		return "", true, false
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != 64 {
		return "", true, false
	}
	for i := 0; i < len(value); i++ {
		if (value[i] < '0' || value[i] > '9') && (value[i] < 'a' || value[i] > 'f') {
			return "", true, false
		}
	}
	return value, true, true
}

// finalized, already-parsed token record here after every request completes —
// streaming and non-streaming alike. This is the billing path that covers
// streaming (the host never invokes response.intercept_after on streams).
// Fire-and-forget: we always return an empty success envelope regardless of
// whether we actually billed (best-effort; unknown keys/aliases cost nothing).
func (a *App) handleUsage(raw []byte) ([]byte, error) {
	var req UsageHandleRequest
	// A malformed record must never break the request path: bill nothing.
	if err := json.Unmarshal(raw, &req); err != nil {
		return OKEnvelope(UsageHandleResponse{})
	}
	_ = a.store.RecordUsage(req.APIKey, req.Alias, req.Model, req.Failed, policy.UsageDetail{
		InputTokens:         req.Detail.InputTokens,
		OutputTokens:        req.Detail.OutputTokens,
		ReasoningTokens:     req.Detail.ReasoningTokens,
		CachedTokens:        req.Detail.CachedTokens,
		CacheReadTokens:     req.Detail.CacheReadTokens,
		CacheCreationTokens: req.Detail.CacheCreationTokens,
		TotalTokens:         req.Detail.TotalTokens,
	})
	return OKEnvelope(UsageHandleResponse{})
}

func (a *App) managementRegistration() ManagementRegistrationResponse {
	base := "/plugins/" + PluginID
	return ManagementRegistrationResponse{
		Routes: []ManagementRoute{
			{Method: http.MethodGet, Path: base + "/keys", Description: "List downstream CPA key policies."},
			{Method: http.MethodPost, Path: base + "/keys", Description: "Create a downstream API-key access policy."},
			{Method: http.MethodPatch, Path: base + "/keys", Description: "Update a downstream API-key access policy by id."},
			{Method: http.MethodDelete, Path: base + "/keys", Description: "Delete a downstream API-key access policy by id."},
			{Method: http.MethodPost, Path: base + "/keys/rotate", Description: "Rotate one downstream CPA key by id."},
			{Method: http.MethodPost, Path: base + "/keys/reset-rpm", Description: "Reset one downstream CPA key RPM counter by id."},
			{Method: http.MethodGet, Path: base + "/keys/usage", Description: "Per-alias usage breakdown for one downstream CPA key by id."},
			{Method: http.MethodGet, Path: base + "/status", Description: "Show Access Guard runtime status."},
			{Method: http.MethodGet, Path: base + "/native-key-bindings", Description: "List CPA-native downstream API-key auth-file restrictions."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings", Description: "Bind one CPA-native downstream API key to an auth-file group or exact auth IDs."},
			{Method: http.MethodPatch, Path: base + "/native-key-bindings", Description: "Update, rotate, enable, or disable one CPA-native key binding."},
			{Method: http.MethodDelete, Path: base + "/native-key-bindings", Description: "Delete one CPA-native key binding by id."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/catalog", Description: "Match CPA-native downstream API keys to current bindings without returning secrets."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/reset-quota", Description: "Reset RPM and usage quota counters for one CPA-native key binding."},
			{Method: http.MethodGet, Path: base + "/aliases", Description: "List the global alias mapping table."},
			{Method: http.MethodPost, Path: base + "/aliases", Description: "Create or update a global alias mapping."},
			{Method: http.MethodDelete, Path: base + "/aliases", Description: "Delete a global alias mapping by name."},
			{Method: http.MethodGet, Path: base + "/classify-rules", Description: "List credential classification rules."},
			{Method: http.MethodPost, Path: base + "/classify-rules", Description: "Create or update a classification rule."},
			{Method: http.MethodDelete, Path: base + "/classify-rules", Description: "Delete a classification rule by name."},
			{Method: http.MethodPost, Path: base + "/classify-rules/reorder", Description: "Reorder classification rules."},
			{Method: http.MethodPost, Path: base + "/classify-preview", Description: "Preview credential classification results for given descriptors."},
			{Method: http.MethodPost, Path: base + "/catalog", Description: "Build auth-file model picker catalog with classify + built-in groups."},
			{Method: http.MethodGet, Path: base + "/pricing", Description: "List the standalone model price catalog."},
			{Method: http.MethodPost, Path: base + "/pricing", Description: "Create or update one model price row."},
			{Method: http.MethodDelete, Path: base + "/pricing", Description: "Delete one model price row by modelId."},
			{Method: http.MethodGet, Path: base + "/pricing-sync", Description: "Show models.dev pricing sync status."},
			{Method: http.MethodPost, Path: base + "/pricing-sync/run", Description: "Run one models.dev pricing sync immediately."},
		},
		Resources: []ResourceRoute{
			{Path: web.IndexPath, Menu: "Access Guard", Description: "Web UI for Access Guard (create keys, bind credentials, and pick models)."},
			{Path: web.LogoPath, Description: "Plugin logo shown in the panel plugin list."},
		},
	}
}

func (a *App) handleManagement(raw []byte) ([]byte, error) {
	var req ManagementRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	path := strings.TrimRight(req.Path, "/")

	// Plugin resource GETs (unauthenticated browser UI) are dispatched through
	// the same management.handle method by CPA's ServeResourceHTTP.
	resourcePrefix := "/v0/resource/plugins/" + PluginID
	if req.Method == http.MethodGet && strings.HasPrefix(path, resourcePrefix) {
		status, headers, body := web.Serve(strings.TrimPrefix(path, resourcePrefix))
		return OKEnvelope(ManagementResponse{StatusCode: status, Headers: headers, Body: body})
	}

	base := "/v0/management/plugins/" + PluginID
	switch {
	case req.Method == http.MethodGet && path == base+"/keys":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"keys": a.publicKeys(a.store.Keys())}))
	case req.Method == http.MethodPost && path == base+"/keys":
		return OKEnvelope(a.createKey(req.Body))
	case req.Method == http.MethodPatch && path == base+"/keys":
		return OKEnvelope(a.patchKey(req.Body))
	case req.Method == http.MethodDelete && path == base+"/keys":
		return OKEnvelope(a.deleteKey(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodPost && path == base+"/keys/rotate":
		return OKEnvelope(a.rotateKey(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodPost && path == base+"/keys/reset-rpm":
		return OKEnvelope(a.resetRPM(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodGet && path == base+"/keys/usage":
		return OKEnvelope(a.keyUsage(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodGet && path == base+"/status":
		return OKEnvelope(jsonResponse(http.StatusOK, a.store.Status()))
	case req.Method == http.MethodGet && path == base+"/native-key-bindings":
		return OKEnvelope(a.listNativeKeyBindings())
	case req.Method == http.MethodPost && path == base+"/native-key-bindings":
		return OKEnvelope(a.createNativeKeyBinding(req.Body))
	case req.Method == http.MethodPatch && path == base+"/native-key-bindings":
		return OKEnvelope(a.patchNativeKeyBinding(req.Body))
	case req.Method == http.MethodDelete && path == base+"/native-key-bindings":
		return OKEnvelope(a.deleteNativeKeyBinding(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/catalog":
		return OKEnvelope(a.catalogNativeKeyBindings(req.Body))
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/reset-quota":
		return OKEnvelope(a.resetNativeKeyQuota(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodGet && path == base+"/aliases":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"aliases": a.store.AliasesSnapshot()}))
	case req.Method == http.MethodPost && path == base+"/aliases":
		return OKEnvelope(a.upsertAlias(req.Body))
	case req.Method == http.MethodDelete && path == base+"/aliases":
		return OKEnvelope(a.deleteAlias(req.Body))
	case req.Method == http.MethodGet && path == base+"/classify-rules":
		return OKEnvelope(jsonResponse(http.StatusOK, map[string]any{"rules": a.store.ClassifyRulesSnapshot()}))
	case req.Method == http.MethodPost && path == base+"/classify-rules":
		return OKEnvelope(a.upsertClassifyRule(req.Body))
	case req.Method == http.MethodDelete && path == base+"/classify-rules":
		return OKEnvelope(a.deleteClassifyRule(req.Body))
	case req.Method == http.MethodPost && path == base+"/classify-rules/reorder":
		return OKEnvelope(a.reorderClassifyRules(req.Body))
	case req.Method == http.MethodPost && path == base+"/classify-preview":
		return OKEnvelope(a.classifyPreview(req.Body))
	case req.Method == http.MethodPost && path == base+"/catalog":
		return OKEnvelope(a.buildCatalog(req.Body))
	case req.Method == http.MethodGet && path == base+"/pricing":
		return OKEnvelope(a.listModelPricing())
	case req.Method == http.MethodPost && path == base+"/pricing":
		return OKEnvelope(a.upsertModelPricing(req.Body))
	case req.Method == http.MethodDelete && path == base+"/pricing":
		return OKEnvelope(a.deleteModelPricing(req.Query, req.Body))
	case req.Method == http.MethodGet && path == base+"/pricing-sync":
		return OKEnvelope(jsonResponse(http.StatusOK, a.pricingSyncSnapshot()))
	case req.Method == http.MethodPost && path == base+"/pricing-sync/run":
		status := a.pricingSyncSnapshot()
		return OKEnvelope(jsonResponse(http.StatusOK, a.runPricingSync(status.URL)))
	default:
		return OKEnvelope(jsonError(http.StatusNotFound, "not_found", "unknown management route"))
	}
}

type keyWriteRequest struct {
	ID                  string               `json:"id"`
	Name                *string              `json:"name,omitempty"`
	Enabled             *bool                `json:"enabled,omitempty"`
	Key                 string               `json:"key,omitempty"`
	RPM                 *int                 `json:"rpm,omitempty"`
	Models              []policy.ModelRule   `json:"models,omitempty"`
	Aliases             []policy.KeyAliasRef `json:"aliases,omitempty"`
	DailyLimitUSD       *float64             `json:"daily_limit_usd,omitempty"`
	WeeklyLimitUSD      *float64             `json:"weekly_limit_usd,omitempty"`
	AllowModelsEndpoint *bool                `json:"allow_models_endpoint,omitempty"`
}

type publicKey struct {
	ID                  string               `json:"id"`
	Name                string               `json:"name"`
	Enabled             bool                 `json:"enabled"`
	KeyPreview          string               `json:"key_preview"`
	RPM                 int                  `json:"rpm"`
	Models              []policy.ModelRule   `json:"models"`
	Aliases             []policy.KeyAliasRef `json:"aliases"`
	DailyLimitUSD       float64              `json:"daily_limit_usd"`
	WeeklyLimitUSD      float64              `json:"weekly_limit_usd"`
	AllowModelsEndpoint bool                 `json:"allow_models_endpoint,omitempty"`
	Usage               policy.UsageSummary  `json:"usage"`
	CreatedAt           string               `json:"created_at,omitempty"`
	UpdatedAt           string               `json:"updated_at,omitempty"`
}

func (a *App) createKey(body []byte) ManagementResponse {
	var req keyWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	plain := strings.TrimSpace(req.Key)
	generated := false
	var err error
	if plain == "" {
		plain, err = policy.GenerateKey()
		if err != nil {
			return jsonError(http.StatusInternalServerError, "key_generation_failed", err.Error())
		}
		generated = true
	}
	hash, err := policy.HashKey(plain)
	if err != nil {
		return jsonError(http.StatusBadRequest, "invalid_key", err.Error())
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	rpm := 0
	if req.RPM != nil {
		rpm = *req.RPM
	}
	name := req.ID
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		name = strings.TrimSpace(*req.Name)
	}
	item := policy.KeyConfig{
		ID:                  req.ID,
		Name:                name,
		Enabled:             enabled,
		KeyHash:             hash,
		KeyPreview:          policy.PreviewKey(plain),
		RPM:                 rpm,
		Models:              req.Models,
		Aliases:             req.Aliases,
		DailyLimitUSD:       applyFloat64(req.DailyLimitUSD, 0),
		WeeklyLimitUSD:      applyFloat64(req.WeeklyLimitUSD, 0),
		AllowModelsEndpoint: applyBool(req.AllowModelsEndpoint, false),
	}
	if err := a.store.UpsertKey(item, true); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_policy", err.Error())
	}
	bodyMap := map[string]any{
		"key":       a.publicKeyFromConfig(item),
		"plain_key": plain,
		"generated": generated,
	}
	return jsonResponse(http.StatusCreated, bodyMap)
}

func (a *App) patchKey(body []byte) ManagementResponse {
	var req keyWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	keys := a.store.Keys()
	var current *policy.KeyConfig
	for i := range keys {
		if keys[i].ID == id {
			copy := keys[i]
			current = &copy
			break
		}
	}
	if current == nil {
		return jsonError(http.StatusNotFound, "not_found", "key not found")
	}
	if req.Name != nil {
		current.Name = strings.TrimSpace(*req.Name)
	}
	if req.Enabled != nil {
		current.Enabled = *req.Enabled
	}
	if req.RPM != nil {
		current.RPM = *req.RPM
	}
	if req.DailyLimitUSD != nil {
		current.DailyLimitUSD = *req.DailyLimitUSD
	}
	if req.WeeklyLimitUSD != nil {
		current.WeeklyLimitUSD = *req.WeeklyLimitUSD
	}
	if req.AllowModelsEndpoint != nil {
		current.AllowModelsEndpoint = *req.AllowModelsEndpoint
	}
	if req.Models != nil {
		current.Models = req.Models
	}
	if req.Aliases != nil {
		current.Aliases = req.Aliases
	}
	if strings.TrimSpace(req.Key) != "" {
		hash, err := policy.HashKey(req.Key)
		if err != nil {
			return jsonError(http.StatusBadRequest, "invalid_key", err.Error())
		}
		current.KeyHash = hash
		current.KeyPreview = policy.PreviewKey(req.Key)
	}
	if err := a.store.UpsertKey(*current, true); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_policy", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"key": a.publicKeyFromConfig(*current)})
}

func (a *App) deleteKey(id string) ManagementResponse {
	if err := a.store.DeleteKey(id); err != nil {
		return storeError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true, "id": strings.TrimSpace(id)})
}

func (a *App) rotateKey(id string) ManagementResponse {
	plain, item, err := a.store.RotateKey(id)
	if err != nil {
		return storeError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"key":       a.publicKeyFromConfig(item),
		"plain_key": plain,
		"generated": true,
	})
}

func (a *App) resetRPM(id string) ManagementResponse {
	if err := a.store.ResetRPM(id); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_request", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"reset": true, "id": strings.TrimSpace(id)})
}

// keyUsage returns the per-alias usage breakdown for one downstream key (the
// key detail subpage data source). id is taken from the query string (or body),
// matching the rotate/reset-rpm/delete convention.
func (a *App) keyUsage(id string) ManagementResponse {
	id = strings.TrimSpace(id)
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	key, aliases, ok := a.store.AliasUsageFor(id)
	if !ok {
		return jsonError(http.StatusNotFound, "not_found", "key not found")
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"key_id":           key.ID,
		"key_name":         key.Name,
		"daily_limit_usd":  key.DailyLimitUSD,
		"weekly_limit_usd": key.WeeklyLimitUSD,
		"aliases":          aliases,
	})
}

func storeError(err error) ManagementResponse {
	if errors.Is(err, policy.ErrUnknownKey) {
		return jsonError(http.StatusNotFound, "not_found", "key not found")
	}
	return jsonError(http.StatusBadRequest, "invalid_request", err.Error())
}

func idFromRequest(query map[string][]string, body []byte) string {
	if query != nil {
		for _, name := range []string{"id", "key_id"} {
			if values := query[name]; len(values) > 0 && strings.TrimSpace(values[0]) != "" {
				return strings.TrimSpace(values[0])
			}
		}
	}
	var payload struct {
		ID    string `json:"id"`
		KeyID string `json:"key_id"`
	}
	if len(body) > 0 && json.Unmarshal(body, &payload) == nil {
		if strings.TrimSpace(payload.ID) != "" {
			return strings.TrimSpace(payload.ID)
		}
		return strings.TrimSpace(payload.KeyID)
	}
	return ""
}

func (a *App) publicKeys(keys []policy.KeyConfig) []publicKey {
	out := make([]publicKey, 0, len(keys))
	for _, key := range keys {
		out = append(out, a.publicKeyFromConfig(key))
	}
	return out
}

func (a *App) publicKeyFromConfig(key policy.KeyConfig) publicKey {
	out := publicKey{
		ID:         key.ID,
		Name:       key.Name,
		Enabled:    key.Enabled,
		KeyPreview: key.KeyPreview,
		RPM:        key.RPM,
		// Ensure models/aliases always serialize as [] (never null). A nil slice
		// would marshal to JSON null, which the UI accesses as .length and
		// crashes on. Models is derived (resolved from Aliases × global table);
		// Aliases is the canonical source.
		Models:              append([]policy.ModelRule{}, key.Models...),
		Aliases:             append([]policy.KeyAliasRef{}, key.Aliases...),
		DailyLimitUSD:       key.DailyLimitUSD,
		WeeklyLimitUSD:      key.WeeklyLimitUSD,
		AllowModelsEndpoint: key.AllowModelsEndpoint,
		Usage:               a.store.UsageSummaryFor(key),
	}
	if !key.CreatedAt.IsZero() {
		out.CreatedAt = key.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if !key.UpdatedAt.IsZero() {
		out.UpdatedAt = key.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	return out
}

func applyFloat64(v *float64, def float64) float64 {
	if v == nil {
		return def
	}
	return *v
}

func applyBool(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

func jsonResponse(status int, payload any) ManagementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		return jsonError(http.StatusInternalServerError, "json_error", err.Error())
	}
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func jsonError(status int, code, message string) ManagementResponse {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	body, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"code":    strings.TrimSpace(code),
			"message": strings.TrimSpace(message),
		},
	})
	return ManagementResponse{
		StatusCode: status,
		Headers:    http.Header{"Content-Type": []string{"application/json; charset=utf-8"}},
		Body:       body,
	}
}

func (a *App) Store() *policy.Store {
	if a == nil {
		return nil
	}
	return a.store
}

func DebugEnvelope(raw []byte) string {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Sprintf("invalid envelope: %v", err)
	}
	if env.Error != nil {
		return env.Error.Message
	}
	return string(env.Result)
}
