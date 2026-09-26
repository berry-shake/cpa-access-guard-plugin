package plugin

import (
	"errors"
	"net/http"

	"cpa-access-guard/internal/policy"
)

type nativeQuotaBatchRequest struct {
	IDs []string `json:"ids"`
}

type nativeQuotaBatchResponse struct {
	Reset bool     `json:"reset"`
	IDs   []string `json:"ids"`
	Count int      `json:"count"`
}

func (a *App) resetNativeKeyQuotaBatch(body []byte) ManagementResponse {
	var request nativeQuotaBatchRequest
	if response := decodeNativeBindingMutation(body, &request); response != nil {
		return *response
	}
	ids, err := a.store.ResetNativeKeyQuotas(request.IDs)
	if err != nil {
		return nativeQuotaBatchStoreError(err)
	}
	return jsonResponse(http.StatusOK, nativeQuotaBatchResponse{Reset: true, IDs: ids, Count: len(ids)})
}

func nativeQuotaBatchStoreError(err error) ManagementResponse {
	switch {
	case errors.Is(err, policy.ErrInvalidNativeQuotaReset):
		return jsonError(http.StatusBadRequest, "invalid_request", "A non-empty list of valid native key binding ids is required")
	case errors.Is(err, policy.ErrUnknownNativeKeyBinding):
		return jsonError(http.StatusNotFound, "not_found", "native key binding not found")
	case errors.Is(err, policy.ErrNativeKeyBindingPersistence):
		return jsonError(http.StatusInternalServerError, "persistence_failed", "failed to persist native quota reset")
	default:
		return jsonError(http.StatusInternalServerError, "operation_failed", "native quota reset failed")
	}
}
