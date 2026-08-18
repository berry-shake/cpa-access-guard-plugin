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
	ID      string  `json:"id"`
	Name    *string `json:"name,omitempty"`
	Enabled *bool   `json:"enabled,omitempty"`
	Key     string  `json:"key,omitempty"`
	Group   *string `json:"group,omitempty"`
}

// publicNativeKeyBinding deliberately omits CallerScope. Although the scope is
// irreversible, it is a stable authorization identity and management clients
// do not need it. The API exposes only a short key preview for identification.
type publicNativeKeyBinding struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Enabled    bool   `json:"enabled"`
	KeyPreview string `json:"key_preview"`
	Group      string `json:"group"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

func (a *App) listNativeKeyBindings() ManagementResponse {
	bindings := a.store.NativeKeyBindingsSnapshot()
	public := make([]publicNativeKeyBinding, 0, len(bindings))
	for _, binding := range bindings {
		public = append(public, publicNativeKeyBindingFromPolicy(binding))
	}
	return jsonResponse(http.StatusOK, map[string]any{"bindings": public})
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
	if group == "" {
		return jsonError(http.StatusBadRequest, "missing_group", "group is required")
	}
	name := req.ID
	if req.Name != nil && strings.TrimSpace(*req.Name) != "" {
		name = strings.TrimSpace(*req.Name)
	}
	binding, err := a.store.CreateNativeKeyBinding(policy.CreateNativeKeyBindingInput{
		ID:      req.ID,
		Name:    name,
		Enabled: applyBool(req.Enabled, true),
		APIKey:  key,
		Group:   group,
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
		Name:    trimmedOptionalString(req.Name),
		Enabled: req.Enabled,
		APIKey:  strings.TrimSpace(req.Key),
		Group:   trimmedOptionalString(req.Group),
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

func publicNativeKeyBindingFromPolicy(binding policy.NativeKeyBinding) publicNativeKeyBinding {
	out := publicNativeKeyBinding{
		ID:         binding.ID,
		Name:       binding.Name,
		Enabled:    binding.Enabled,
		KeyPreview: binding.KeyPreview,
		Group:      binding.Group,
	}
	if !binding.CreatedAt.IsZero() {
		out.CreatedAt = binding.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !binding.UpdatedAt.IsZero() {
		out.UpdatedAt = binding.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}
