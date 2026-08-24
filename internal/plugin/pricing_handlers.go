package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"cpa-access-guard/internal/policy"
)

func (a *App) listModelPricing() ManagementResponse {
	return jsonResponse(http.StatusOK, map[string]any{
		"models":            a.store.PricingSnapshot(),
		"deleted_model_ids": a.store.PricingDeletedSnapshot(),
	})
}

func (a *App) upsertModelPricing(raw []byte) ManagementResponse {
	var price policy.ModelPrice
	if err := json.Unmarshal(raw, &price); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	if err := a.store.UpsertModelPrice(price); err != nil {
		return jsonError(http.StatusBadRequest, "validation_error", err.Error())
	}
	id := policy.NormalizeModelIDForPricing(price.ID)
	for _, row := range a.store.PricingSnapshot() {
		if row.ID == id {
			return jsonResponse(http.StatusOK, map[string]any{"model": row})
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{"model": price})
}

func (a *App) deleteModelPricing(query url.Values, raw []byte) ManagementResponse {
	id := strings.TrimSpace(query.Get("modelId"))
	if id == "" {
		id = strings.TrimSpace(query.Get("model_id"))
	}
	if id == "" && len(raw) > 0 {
		var body struct {
			ModelID string `json:"modelId"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
		}
		id = strings.TrimSpace(body.ModelID)
	}
	if err := a.store.DeleteModelPrice(id); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return jsonError(http.StatusNotFound, "not_found", err.Error())
		}
		return jsonError(http.StatusBadRequest, "delete_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"deleted": true, "modelId": policy.NormalizeModelIDForPricing(id)})
}

func (a *App) restoreAutomaticModelPricing(raw []byte) ManagementResponse {
	var body struct {
		ModelID string `json:"modelId"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return jsonError(http.StatusBadRequest, "invalid_json", err.Error())
	}
	id := policy.NormalizeModelIDForPricing(body.ModelID)
	if id == "" {
		return jsonError(http.StatusBadRequest, "validation_error", "modelId is required")
	}
	if err := a.store.RestoreModelPriceAutomatic(id); err != nil {
		return jsonError(http.StatusBadRequest, "restore_failed", err.Error())
	}
	return jsonResponse(http.StatusOK, map[string]any{"restored": true, "modelId": id})
}
