package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"cpa-access-guard/internal/plugin/web"
	"cpa-access-guard/internal/policy"
)

type App struct {
	store             *policy.Store
	nativeRoundRobin  nativeRoundRobinState
	pricingRunMu      sync.Mutex
	pricingSyncMu     sync.Mutex
	pricingSyncer     *pricingSyncer
	pricingSyncStatus pricingSyncStatus
}

func NewApp() *App {
	// Defer persistent state initialization until the host sends the lifecycle
	// configuration. Configuring defaults here would create a stray
	// cpa-access-guard-state.json in the host process working directory before the
	// configured state_file path is known.
	app := &App{store: policy.NewStore()}
	app.store.SetOnNativeKeyBindingsChanged(app.pruneNativeRoundRobin)
	return app
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
	case MethodSchedulerPick:
		return a.pickScheduler(request)
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
	a.clearNativeRoundRobin()
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
				{Name: "native_key_bindings", Type: "array", Description: "Optional caller-scope bindings that constrain CPA client API keys by model access and exact auth IDs. State file wins after it exists. Requires a Scheduler path carrying caller_scope, Home disabled, no unsupported special route, and quota-exceeded.antigravity-credits=false."},
				{Name: "pricing_sync", Type: "object", Description: "Dual-source price refresh with LiteLLM primary and models.dev fallback. Always on. Optional {interval_hours, litellm_url, models_dev_url}; legacy url remains a models.dev override."},
			},
		},
		// No frontend auth, model router or response interceptor: those hooks
		// receive the full request or response body on every call, and the
		// native-key policy needs neither.
		Capabilities: Capabilities{
			SchedulerAcrossPriorities: true,
			Scheduler:                 true,
			UsagePlugin:               true,
			ManagementAPI:             true,
		},
	}
}

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

// nativeModelNotAllowedMessage is a complete OpenAI-compatible error body.
// CPA preserves valid JSON error text verbatim; a plain message would cause
// its generic 403 adapter to replace our machine-readable code with
// insufficient_quota.
func nativeModelNotAllowedMessage() string {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"type":    "permission_error",
			"code":    "model_not_allowed",
			"message": "Model is not allowed for this API key",
		},
	})
	if err != nil {
		return "Model is not allowed for this API key"
	}
	return string(body)
}

// noCandidateMessage renders the binding-isolation rejection exactly like the
// host's own "no auth available" error (see enrichAuthSelectionError), so the
// client cannot distinguish a binding-boundary rejection from a plain missing
// credential — the binding policy stays invisible.
func noCandidateMessage(req SchedulerPickRequest) string {
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

// pickScheduler implements the scheduler.pick host->plugin call. CPA's native
// config-api-key provider authenticates the downstream key and forwards its
// stable, hashed identity as Options.Metadata["caller_scope"]. A matching
// enabled binding restricts the request to the binding's exact auth IDs and
// model allow-list; every other request is left to the host scheduler.
//
// For a bound key, in order:
//  1. Reject a model outside the binding's model access (403, no RPM spent).
//  2. Enforce the RPM / daily / weekly quota (429).
//  3. Keep usable candidates of an allowed provider whose ID is bound.
//  4. Pick the highest priority, lowest ID; or round-robin among the highest
//     priority when the binding opts in.
//
// An empty result is an explicit 503 instead of Handled=false: deferring would
// let the host pick any credential and break the isolation guarantee. CPA
// calls scheduler.pick again on retry with tried auths removed, so exhaustion
// fails closed as well.
func (a *App) pickScheduler(raw []byte) ([]byte, error) {
	var req SchedulerPickRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if !a.store.Enabled() {
		// `enabled: false` disables every native key binding. The host remains
		// responsible for its default scheduling.
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	callerScope, callerScopePresent, callerScopeValid := schedulerCallerScopeFromMetadata(req.Options.Metadata)
	if callerScopePresent && !callerScopeValid {
		// CPA owns caller_scope and always emits a 64-character SHA-256 hex
		// string. Any other shape indicates an incompatible or corrupted
		// host/plugin path; never treat it as an unbound request.
		return ErrorEnvelope("invalid_scheduler_metadata", "invalid caller_scope metadata", http.StatusServiceUnavailable), nil
	}
	if callerScope == "" {
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	constraint, bound := a.store.ResolveNativeKeyConstraint(callerScope)
	if !bound {
		// Unbound native keys keep CPA's default scheduling.
		return OKEnvelope(SchedulerPickResponse{Handled: false})
	}
	if !schedulerRequestModelAllowed(constraint, req) {
		return ErrorEnvelope("model_not_allowed", nativeModelNotAllowedMessage(), http.StatusForbidden), nil
	}
	// RPM decrements here (concurrency safe); USD limits compare against the
	// usage.handle ledger. Model authorization runs first so a rejected
	// request never consumes RPM.
	if decision, limited := a.store.CheckNativeKeyQuota(callerScope); limited {
		code, message := nativeQuotaError(decision, req.Model)
		return ErrorEnvelope(code, message, http.StatusTooManyRequests), nil
	}
	if len(constraint.AuthIDs) == 0 {
		// A credential group saved by an older release: it stays fail-closed
		// until the operator selects exact credentials again.
		return ErrorEnvelope("auth_not_found", "native key binding has no credential restriction", http.StatusServiceUnavailable), nil
	}

	matched := make([]SchedulerAuthCandidate, 0, len(constraint.AuthIDs))
	for _, cand := range req.Candidates {
		if !schedulerCandidateUsable(cand.Status) || !slices.Contains(constraint.AuthIDs, cand.ID) {
			continue
		}
		if !constraint.AllowsModel(cand.Provider, req.Model) {
			// The request-level check permits a multi-provider route when at least
			// one provider is selected. Recheck each candidate so an unselected
			// provider carrying the same model name cannot bypass the policy.
			continue
		}
		matched = append(matched, cand)
	}
	if len(matched) == 0 {
		return ErrorEnvelope("auth_not_found", noCandidateMessage(req), http.StatusServiceUnavailable), nil
	}

	best := matched[0]
	for _, cand := range matched[1:] {
		if cand.Priority > best.Priority ||
			(cand.Priority == best.Priority && cand.ID < best.ID) {
			best = cand
		}
	}
	if constraint.RoundRobin {
		return OKEnvelope(SchedulerPickResponse{Handled: true, AuthID: a.pickNativeRoundRobin(callerScope, req, matched, best.Priority)})
	}
	return OKEnvelope(SchedulerPickResponse{Handled: true, AuthID: best.ID})
}

func schedulerRequestModelAllowed(constraint policy.NativeKeyConstraint, req SchedulerPickRequest) bool {
	providers := schedulerRequestProviders(req)
	if len(providers) == 0 {
		return constraint.AllowsModel("", req.Model)
	}
	for _, provider := range providers {
		if constraint.AllowsModel(provider, req.Model) {
			return true
		}
	}
	return false
}

func schedulerRequestProviders(req SchedulerPickRequest) []string {
	providers := make([]string, 0, len(req.Providers)+1)
	seen := make(map[string]struct{}, len(req.Providers)+1)
	for _, provider := range append([]string{req.Provider}, req.Providers...) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			continue
		}
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		providers = append(providers, provider)
	}
	return providers
}

func schedulerCandidateUsable(status string) bool {
	status = strings.ToLower(strings.TrimSpace(status))
	status = strings.NewReplacer("-", "_", " ", "_").Replace(status)
	// CPA filters scheduler candidates by effective model availability before
	// invoking the plugin. An expired cooldown is therefore offered again, but
	// the auth lifecycle status remains "error" until that credential completes
	// a successful request. Rejecting "error" here would prevent that recovery
	// request forever and turn an expired cooldown into a permanent 503.
	switch status {
	case "disabled", "expired", "revoked", "invalid", "unavailable", "cooldown", "cooling_down", "quota_exhausted", "exhausted", "blocked":
		return false
	default:
		return true
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

// handleUsage bills usage.handle records. CPA delivers a
// finalized, already-parsed token record here after every request completes —
// streaming and non-streaming alike. This is the billing path that covers
// streaming. Fire-and-forget: we always return an empty success envelope
// regardless of whether we actually billed (unbound keys cost nothing).
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
			{Method: http.MethodGet, Path: base + "/status", Description: "Show Access Guard runtime status."},
			{Method: http.MethodGet, Path: base + "/native-key-bindings", Description: "List CPA client API key credential bindings."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings", Description: "Bind one CPA client API key to exact auth IDs."},
			{Method: http.MethodPatch, Path: base + "/native-key-bindings", Description: "Update, rotate, enable, or disable one API key binding."},
			{Method: http.MethodDelete, Path: base + "/native-key-bindings", Description: "Delete one API key binding by id."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/catalog", Description: "Match CPA client API keys to current bindings without returning secrets."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/sync-names", Description: "Synchronize display names on matching API key bindings."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/reset-quota", Description: "Reset RPM and usage quota counters for one API key binding."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/reset-quota-batch", Description: "Reset RPM and usage quota counters for an explicit list of API key bindings."},
			{Method: http.MethodGet, Path: base + "/native-key-bindings/history", Description: "List redacted API key credential batch history."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/batch-preview", Description: "Preview an atomic API key credential replacement."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/batch", Description: "Apply a previewed API key credential replacement atomically."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/rollback-preview", Description: "Preview rollback of one API key credential operation."},
			{Method: http.MethodPost, Path: base + "/native-key-bindings/rollback", Description: "Apply a previewed API key credential rollback atomically."},
			{Method: http.MethodGet, Path: base + "/pricing", Description: "List the standalone model price catalog."},
			{Method: http.MethodPost, Path: base + "/pricing", Description: "Create or update one model price row."},
			{Method: http.MethodDelete, Path: base + "/pricing", Description: "Delete one model price row by modelId."},
			{Method: http.MethodPost, Path: base + "/pricing/restore-auto", Description: "Remove a manual override or deletion tombstone so source sync can restore the model."},
			{Method: http.MethodGet, Path: base + "/pricing-sync", Description: "Show LiteLLM-primary and models.dev-fallback pricing sync status."},
			{Method: http.MethodPost, Path: base + "/pricing-sync/run", Description: "Run one dual-source pricing sync immediately."},
		},
		Resources: []ResourceRoute{
			{Path: web.IndexPath, Menu: "Access Guard", Description: "Web UI for Access Guard (bind CPA-native keys to credentials and edit model prices)."},
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
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/sync-names":
		return OKEnvelope(a.syncNativeKeyBindingNames(req.Body))
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/reset-quota":
		return OKEnvelope(a.resetNativeKeyQuota(idFromRequest(req.Query, req.Body)))
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/reset-quota-batch":
		return OKEnvelope(a.resetNativeKeyQuotaBatch(req.Body))
	case req.Method == http.MethodGet && path == base+"/native-key-bindings/history":
		return OKEnvelope(a.listNativeBindingHistory())
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/batch-preview":
		return OKEnvelope(a.batchNativeBindings(req.Body, true))
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/batch":
		return OKEnvelope(a.batchNativeBindings(req.Body, false))
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/rollback-preview":
		return OKEnvelope(a.rollbackNativeBindings(req.Body, true))
	case req.Method == http.MethodPost && path == base+"/native-key-bindings/rollback":
		return OKEnvelope(a.rollbackNativeBindings(req.Body, false))
	case req.Method == http.MethodGet && path == base+"/pricing":
		return OKEnvelope(a.listModelPricing())
	case req.Method == http.MethodPost && path == base+"/pricing":
		return OKEnvelope(a.upsertModelPricing(req.Body))
	case req.Method == http.MethodDelete && path == base+"/pricing":
		return OKEnvelope(a.deleteModelPricing(req.Query, req.Body))
	case req.Method == http.MethodPost && path == base+"/pricing/restore-auto":
		return OKEnvelope(a.restoreAutomaticModelPricing(req.Body))
	case req.Method == http.MethodGet && path == base+"/pricing-sync":
		return OKEnvelope(jsonResponse(http.StatusOK, a.pricingSyncSnapshot()))
	case req.Method == http.MethodPost && path == base+"/pricing-sync/run":
		status := a.pricingSyncSnapshot()
		return OKEnvelope(jsonResponse(http.StatusOK, a.runPricingSync(status.liteLLMRawURL, status.modelsDevRawURL)))
	default:
		return OKEnvelope(jsonError(http.StatusNotFound, "not_found", "unknown management route"))
	}
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
