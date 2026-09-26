package plugin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"cpa-access-guard/internal/policy"
)

const (
	nativeBatchMaxBodyBytes = 2 << 20
	nativeBatchMaxKeys      = 4096
	nativeBatchMaxAuthIDs   = 16384
	nativeBatchMaxValueSize = 4096
)

// These snapshots come from the authenticated management client, as in the
// native-key catalog API. The plugin cannot independently query CPA's catalog.
type nativeBindingBatchRequest struct {
	APIKeys          []string `json:"api_keys"`
	SelectedIndices  []int    `json:"selected_indices"`
	AuthIDs          []string `json:"auth_ids"`
	AvailableAuthIDs []string `json:"available_auth_ids"`
	CatalogComplete  bool     `json:"catalog_complete"`
	ExpectedRevision string   `json:"expected_revision,omitempty"`
}

type nativeBindingRollbackRequest struct {
	OperationID      string   `json:"operation_id"`
	APIKeys          []string `json:"api_keys"`
	AvailableAuthIDs []string `json:"available_auth_ids"`
	CatalogComplete  bool     `json:"catalog_complete"`
	ExpectedRevision string   `json:"expected_revision,omitempty"`
}

// Explicit response projections prevent persisted identity or secret fields
// from becoming public if the policy history structs grow in the future.
type publicNativeBindingRestriction struct {
	Group   string   `json:"group,omitempty"`
	AuthIDs []string `json:"auth_ids,omitempty"`
}

type publicNativeBindingChange struct {
	BindingID  string                          `json:"binding_id"`
	Name       string                          `json:"name"`
	KeyPreview string                          `json:"key_preview"`
	Before     *publicNativeBindingRestriction `json:"before"`
	After      *publicNativeBindingRestriction `json:"after"`
}

type publicNativeBindingConflict struct {
	BindingID string `json:"binding_id"`
	Code      string `json:"code"`
}

type publicNativeBindingPreview struct {
	Revision  string                        `json:"revision"`
	Changes   []publicNativeBindingChange   `json:"changes"`
	Conflicts []publicNativeBindingConflict `json:"conflicts"`
	CanApply  bool                          `json:"can_apply"`
	Noop      bool                          `json:"noop"`
	Warnings  []string                      `json:"warnings"`
}

type publicNativeBindingOperation struct {
	ID                string                      `json:"id"`
	Kind              string                      `json:"kind"`
	SourceOperationID string                      `json:"source_operation_id,omitempty"`
	RevertedBy        string                      `json:"reverted_by,omitempty"`
	CreatedAt         string                      `json:"created_at"`
	Changes           []publicNativeBindingChange `json:"changes"`
}

type publicNativeBindingMutationResult struct {
	Operation *publicNativeBindingOperation `json:"operation,omitempty"`
	Changed   int                           `json:"changed"`
	Noop      bool                          `json:"noop"`
}

func (a *App) listNativeBindingHistory() ManagementResponse {
	operations := a.store.NativeBindingHistorySnapshot()
	public := make([]publicNativeBindingOperation, 0, len(operations))
	for _, operation := range operations {
		public = append(public, publicNativeBindingOperationFromPolicy(operation))
	}
	return jsonResponse(http.StatusOK, map[string]any{"operations": public})
}

func (a *App) batchNativeBindings(body []byte, preview bool) ManagementResponse {
	var req nativeBindingBatchRequest
	if response := decodeNativeBindingMutation(body, &req); response != nil {
		return *response
	}
	if !validNativeBindingSnapshot(req.APIKeys, req.AvailableAuthIDs, req.ExpectedRevision, !preview) ||
		len(req.SelectedIndices) == 0 || len(req.SelectedIndices) > nativeBatchMaxKeys ||
		len(req.AuthIDs) == 0 || !validNativeBindingValues(req.AuthIDs, nativeBatchMaxAuthIDs) {
		return nativeBindingInvalidRequest()
	}
	input := policy.NativeBindingBatchInput{
		APIKeys: req.APIKeys, SelectedIndices: req.SelectedIndices, AuthIDs: req.AuthIDs,
		AvailableAuthIDs: req.AvailableAuthIDs, CatalogComplete: req.CatalogComplete,
		ExpectedRevision: req.ExpectedRevision,
	}
	if preview {
		result, err := a.store.PreviewNativeBindingBatch(input)
		if err != nil {
			return nativeBindingMutationError(err)
		}
		return jsonResponse(http.StatusOK, map[string]any{"preview": publicNativeBindingPreviewFromPolicy(result)})
	}
	result, err := a.store.ApplyNativeBindingBatch(input)
	if err != nil {
		return nativeBindingMutationError(err)
	}
	return jsonResponse(http.StatusOK, publicNativeBindingMutationFromPolicy(result))
}

func (a *App) rollbackNativeBindings(body []byte, preview bool) ManagementResponse {
	var req nativeBindingRollbackRequest
	if response := decodeNativeBindingMutation(body, &req); response != nil {
		return *response
	}
	if strings.TrimSpace(req.OperationID) == "" || len(req.OperationID) > 256 ||
		!validNativeBindingSnapshot(req.APIKeys, req.AvailableAuthIDs, req.ExpectedRevision, !preview) {
		return nativeBindingInvalidRequest()
	}
	input := policy.NativeBindingRollbackInput{
		OperationID: req.OperationID, APIKeys: req.APIKeys, AvailableAuthIDs: req.AvailableAuthIDs,
		CatalogComplete: req.CatalogComplete, ExpectedRevision: req.ExpectedRevision,
	}
	if preview {
		result, err := a.store.PreviewNativeBindingRollback(input)
		if err != nil {
			return nativeBindingMutationError(err)
		}
		return jsonResponse(http.StatusOK, map[string]any{"preview": publicNativeBindingPreviewFromPolicy(result)})
	}
	result, err := a.store.ApplyNativeBindingRollback(input)
	if err != nil {
		return nativeBindingMutationError(err)
	}
	return jsonResponse(http.StatusOK, publicNativeBindingMutationFromPolicy(result))
}

func decodeNativeBindingMutation(body []byte, target any) *ManagementResponse {
	if len(body) > nativeBatchMaxBodyBytes {
		response := jsonError(http.StatusRequestEntityTooLarge, "request_too_large", "Native binding request exceeds the size limit")
		return &response
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		response := jsonError(http.StatusBadRequest, "invalid_json", "Invalid native binding request JSON")
		return &response
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		response := jsonError(http.StatusBadRequest, "invalid_json", "Invalid native binding request JSON")
		return &response
	}
	return nil
}

func validNativeBindingSnapshot(keys, available []string, revision string, commit bool) bool {
	return validNativeBindingValues(keys, nativeBatchMaxKeys) &&
		validNativeBindingValues(available, nativeBatchMaxAuthIDs) && len(revision) <= 256 &&
		(!commit || strings.TrimSpace(revision) != "")
}

func validNativeBindingValues(values []string, maximum int) bool {
	if len(values) > maximum {
		return false
	}
	for _, value := range values {
		if len(value) > nativeBatchMaxValueSize || strings.TrimSpace(value) == "" {
			return false
		}
	}
	return true
}

func nativeBindingInvalidRequest() ManagementResponse {
	return jsonError(http.StatusBadRequest, "invalid_request", "Invalid native binding request or catalog snapshot")
}

func nativeBindingMutationError(err error) ManagementResponse {
	switch {
	case errors.Is(err, policy.ErrNativeBindingRevisionMismatch):
		return jsonError(http.StatusConflict, "revision_mismatch", "Native bindings changed; refresh the preview before applying")
	case errors.Is(err, policy.ErrNativeBindingBatchConflict):
		return jsonError(http.StatusConflict, "batch_conflict", "Native binding operation conflicts with the current state; refresh the preview")
	case errors.Is(err, policy.ErrUnknownNativeBindingOperation):
		return jsonError(http.StatusNotFound, "operation_not_found", "Native binding history operation was not found")
	case errors.Is(err, policy.ErrNativeKeyBindingPersistence):
		return jsonError(http.StatusInternalServerError, "persistence_failed", "Failed to persist native binding operation")
	case errors.Is(err, policy.ErrInvalidNativeBindingBatch):
		return nativeBindingInvalidRequest()
	default:
		return jsonError(http.StatusInternalServerError, "operation_failed", "Native binding operation failed")
	}
}

func publicNativeBindingRestrictionFromPolicy(restriction *policy.NativeBindingRestriction) *publicNativeBindingRestriction {
	if restriction == nil {
		return nil
	}
	return &publicNativeBindingRestriction{Group: restriction.Group, AuthIDs: append([]string(nil), restriction.AuthIDs...)}
}

func publicNativeBindingChangesFromPolicy(changes []policy.NativeBindingChange) []publicNativeBindingChange {
	public := make([]publicNativeBindingChange, 0, len(changes))
	for _, change := range changes {
		public = append(public, publicNativeBindingChange{
			BindingID: change.BindingID, Name: change.Name, KeyPreview: change.KeyPreview,
			Before: publicNativeBindingRestrictionFromPolicy(change.Before),
			After:  publicNativeBindingRestrictionFromPolicy(change.After),
		})
	}
	return public
}

func publicNativeBindingPreviewFromPolicy(preview policy.NativeBindingPreview) publicNativeBindingPreview {
	conflicts := make([]publicNativeBindingConflict, 0, len(preview.Conflicts))
	for _, conflict := range preview.Conflicts {
		conflicts = append(conflicts, publicNativeBindingConflict{BindingID: conflict.BindingID, Code: conflict.Code})
	}
	return publicNativeBindingPreview{
		Revision: preview.Revision, Changes: publicNativeBindingChangesFromPolicy(preview.Changes),
		Conflicts: conflicts, CanApply: preview.CanApply, Noop: preview.Noop,
		Warnings: append([]string{}, preview.Warnings...),
	}
}

func publicNativeBindingOperationFromPolicy(operation policy.NativeBindingOperation) publicNativeBindingOperation {
	return publicNativeBindingOperation{
		ID: operation.ID, Kind: operation.Kind, SourceOperationID: operation.SourceOperationID,
		RevertedBy: operation.RevertedBy, CreatedAt: operation.CreatedAt.UTC().Format(time.RFC3339Nano),
		Changes: publicNativeBindingChangesFromPolicy(operation.Changes),
	}
}

func publicNativeBindingMutationFromPolicy(result policy.NativeBindingMutationResult) publicNativeBindingMutationResult {
	public := publicNativeBindingMutationResult{Changed: result.Changed, Noop: result.Noop}
	if result.Operation != nil {
		operation := publicNativeBindingOperationFromPolicy(*result.Operation)
		public.Operation = &operation
	}
	return public
}
