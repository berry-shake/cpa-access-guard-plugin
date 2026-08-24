package plugin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"cpa-access-guard/internal/policy"
)

// nativeKeyBindingWriteRequest is used by both create and patch operations.
// Key is accepted once to derive CPA's caller_scope, but is never returned or
// persisted. Pointer fields let PATCH distinguish an omitted field from a
// deliberate false/empty value.
type nativeKeyBindingWriteRequest struct {
	ID        string    `json:"id"`
	Name      *string   `json:"name,omitempty"`
	Enabled   *bool     `json:"enabled,omitempty"`
	Key       string    `json:"key,omitempty"`
	Group     *string   `json:"group,omitempty"`
	AuthIDs   *[]string `json:"auth_ids,omitempty"`
	RPM       *int      `json:"rpm,omitempty"`
	DailyUSD  *float64  `json:"daily_usd,omitempty"`
	WeeklyUSD *float64  `json:"weekly_usd,omitempty"`
}

// publicNativeKeyBinding deliberately omits CallerScope. Although the scope is
// irreversible, it is a stable authorization identity and management clients
// do not need it. The API exposes only a short key preview for identification.
type publicNativeKeyBinding struct {
	ID         string                            `json:"id"`
	Name       string                            `json:"name"`
	Enabled    bool                              `json:"enabled"`
	KeyPreview string                            `json:"key_preview"`
	Group      string                            `json:"group,omitempty"`
	AuthIDs    []string                          `json:"auth_ids,omitempty"`
	RPM        int                               `json:"rpm,omitempty"`
	DailyUSD   float64                           `json:"daily_usd,omitempty"`
	WeeklyUSD  float64                           `json:"weekly_usd,omitempty"`
	Usage      *policy.NativeBindingUsageSummary `json:"usage,omitempty"`
	CreatedAt  string                            `json:"created_at,omitempty"`
	UpdatedAt  string                            `json:"updated_at,omitempty"`
}

// nativeKeyBindingCatalogRequest carries the current CPA top-level API keys.
// The keys are used only for this in-memory join and are never returned or
// persisted by the plugin.
type nativeKeyBindingCatalogRequest struct {
	APIKeys []string `json:"api_keys"`
}

// nativeKeyBindingCatalogEntry preserves the source array index so callers
// can associate the redacted response with their in-memory key list without
// exposing the plaintext key or its caller_scope.
type nativeKeyBindingCatalogEntry struct {
	KeyIndex   int                     `json:"key_index"`
	KeyPreview string                  `json:"key_preview"`
	Binding    *publicNativeKeyBinding `json:"binding,omitempty"`
}

func (a *App) listNativeKeyBindings() ManagementResponse {
	bindings := a.store.NativeKeyBindingsSnapshot()
	public := make([]publicNativeKeyBinding, 0, len(bindings))
	for _, binding := range bindings {
		public = append(public, a.publicNativeKeyBindingWithUsage(binding))
	}
	return jsonResponse(http.StatusOK, map[string]any{"bindings": public})
}

func (a *App) catalogNativeKeyBindings(body []byte) ManagementResponse {
	var req nativeKeyBindingCatalogRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}

	bindings := a.store.NativeKeyBindingsSnapshot()
	bindingsByScope := make(map[string]policy.NativeKeyBinding, len(bindings))
	for _, binding := range bindings {
		bindingsByScope[binding.CallerScope] = binding
	}

	entries := make([]nativeKeyBindingCatalogEntry, 0, len(req.APIKeys))
	seenScopes := make(map[string]struct{}, len(req.APIKeys))
	matchedScopes := make(map[string]struct{}, len(bindings))
	for index, rawKey := range req.APIKeys {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		callerScope := policy.NativeCallerScope(key)
		if _, seen := seenScopes[callerScope]; seen {
			continue
		}
		seenScopes[callerScope] = struct{}{}

		entry := nativeKeyBindingCatalogEntry{
			KeyIndex:   index,
			KeyPreview: policy.NativeKeyPreview(key),
		}
		if binding, ok := bindingsByScope[callerScope]; ok {
			publicBinding := a.publicNativeKeyBindingWithUsage(binding)
			entry.Binding = &publicBinding
			matchedScopes[callerScope] = struct{}{}
		}
		entries = append(entries, entry)
	}

	orphanBindings := make([]publicNativeKeyBinding, 0)
	for _, binding := range bindings {
		if _, matched := matchedScopes[binding.CallerScope]; matched {
			continue
		}
		orphanBindings = append(orphanBindings, a.publicNativeKeyBindingWithUsage(binding))
	}

	return jsonResponse(http.StatusOK, map[string]any{
		"entries":         entries,
		"orphan_bindings": orphanBindings,
	})
}

func (a *App) createNativeKeyBinding(body []byte) ManagementResponse {
	var req nativeKeyBindingWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		return jsonError(http.StatusBadRequest, "missing_key", "key is required")
	}
	group := ""
	if req.Group != nil {
		group = strings.TrimSpace(*req.Group)
	}
	authIDs := []string(nil)
	if req.AuthIDs != nil {
		authIDs = append(authIDs, (*req.AuthIDs)...)
	}
	hasAuthIDs := nativeAuthIDsPresent(authIDs)
	if group == "" && !hasAuthIDs {
		return jsonError(http.StatusBadRequest, "missing_restriction", "group or auth_ids is required")
	}
	if group != "" && hasAuthIDs {
		return jsonError(http.StatusBadRequest, "conflicting_restriction", "group and auth_ids are mutually exclusive")
	}
	name := req.ID
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		name = strings.TrimSpace(*req.Name)
	}
	binding, err := a.store.CreateNativeKeyBinding(policy.CreateNativeKeyBindingInput{
		ID:        req.ID,
		Name:      name,
		Enabled:   applyBool(req.Enabled, true),
		APIKey:    key,
		Group:     group,
		AuthIDs:   authIDs,
		RPM:       req.RPM,
		DailyUSD:  req.DailyUSD,
		WeeklyUSD: req.WeeklyUSD,
	})
	if err != nil {
		return nativeKeyBindingStoreError(err)
	}
	return jsonResponse(http.StatusCreated, map[string]any{
		"binding": publicNativeKeyBindingFromPolicy(binding),
	})
}

func (a *App) patchNativeKeyBinding(body []byte) ManagementResponse {
	var req nativeKeyBindingWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	id := strings.ToLower(strings.TrimSpace(req.ID))
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}

	binding, err := a.store.UpdateNativeKeyBinding(id, policy.UpdateNativeKeyBindingInput{
		Name:      trimmedOptionalString(req.Name),
		Enabled:   req.Enabled,
		APIKey:    strings.TrimSpace(req.Key),
		Group:     trimmedOptionalString(req.Group),
		AuthIDs:   req.AuthIDs,
		RPM:       req.RPM,
		DailyUSD:  req.DailyUSD,
		WeeklyUSD: req.WeeklyUSD,
	})
	if err != nil {
		return nativeKeyBindingStoreError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"binding": publicNativeKeyBindingFromPolicy(binding),
	})
}

func (a *App) deleteNativeKeyBinding(id string) ManagementResponse {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	if err := a.store.DeleteNativeKeyBinding(id); err != nil {
		return nativeKeyBindingStoreError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true, "id": id})
}

func (a *App) resetNativeKeyQuota(id string) ManagementResponse {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return jsonError(http.StatusBadRequest, "missing_id", "id is required")
	}
	if err := a.store.ResetNativeKeyQuota(id); err != nil {
		return nativeKeyBindingStoreError(err)
	}
	return jsonResponse(http.StatusOK, map[string]any{"reset": true, "id": id})
}

func nativeKeyBindingStoreError(err error) ManagementResponse {
	switch {
	case errors.Is(err, policy.ErrUnknownNativeKeyBinding):
		return jsonError(http.StatusNotFound, "not_found", "native key binding not found")
	case errors.Is(err, policy.ErrNativeKeyBindingExists):
		return jsonError(http.StatusConflict, "already_exists", "native key binding already exists")
	case errors.Is(err, policy.ErrNativeKeyBindingPersistence):
		// Do not expose state paths or lower-level I/O details. The old durable
		// binding remains live because Store persists before publishing changes.
		return jsonError(http.StatusInternalServerError, "persistence_failed", "failed to persist native key binding")
	default:
		return jsonError(http.StatusBadRequest, "invalid_binding", err.Error())
	}
}

func trimmedOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	return &trimmed
}

func nativeAuthIDsPresent(authIDs []string) bool {
	for _, authID := range authIDs {
		if strings.TrimSpace(authID) != "" {
			return true
		}
	}
	return false
}

func publicNativeKeyBindingFromPolicy(binding policy.NativeKeyBinding) publicNativeKeyBinding {
	return publicNativeKeyBindingFromPolicyWithUsage(binding, nil)
}

// publicNativeKeyBindingFromPolicyWithUsage attaches live usage counters when
// the caller has a store at hand (list endpoints); write endpoints skip the
// extra lookup and return the persisted policy only.
func (a *App) publicNativeKeyBindingWithUsage(binding policy.NativeKeyBinding) publicNativeKeyBinding {
	usage := a.store.NativeBindingUsage(binding)
	return publicNativeKeyBindingFromPolicyWithUsage(binding, &usage)
}

func publicNativeKeyBindingFromPolicyWithUsage(binding policy.NativeKeyBinding, usage *policy.NativeBindingUsageSummary) publicNativeKeyBinding {
	out := publicNativeKeyBinding{
		ID:         binding.ID,
		Name:       binding.Name,
		Enabled:    binding.Enabled,
		KeyPreview: binding.KeyPreview,
		RPM:        binding.RPM,
		DailyUSD:   binding.DailyUSD,
		WeeklyUSD:  binding.WeeklyUSD,
		Usage:      usage,
	}
	if len(binding.AuthIDs) > 0 {
		out.AuthIDs = append([]string(nil), binding.AuthIDs...)
	} else {
		out.Group = binding.Group
	}
	if !binding.CreatedAt.IsZero() {
		out.CreatedAt = binding.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !binding.UpdatedAt.IsZero() {
		out.UpdatedAt = binding.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}
