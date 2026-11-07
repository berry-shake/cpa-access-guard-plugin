package plugin

import (
	"errors"
	"net/http"

	"cpa-access-guard/internal/policy"
)

type nativeBindingNameSyncRequest struct {
	Updates []struct {
		ID           string  `json:"id"`
		Key          string  `json:"key"`
		ExpectedName *string `json:"expected_name"`
		Name         *string `json:"name"`
	} `json:"updates"`
}

func (a *App) syncNativeKeyBindingNames(body []byte) ManagementResponse {
	var request nativeBindingNameSyncRequest
	if response := decodeNativeBindingMutation(body, &request); response != nil {
		return *response
	}
	if len(request.Updates) > nativeBatchMaxKeys {
		return nativeBindingInvalidRequest()
	}
	updates := make([]policy.NativeBindingNameUpdate, 0, len(request.Updates))
	for _, update := range request.Updates {
		if update.ExpectedName == nil || update.Name == nil ||
			len(update.ID) > nativeBatchMaxValueSize || len(update.Key) > nativeBatchMaxValueSize ||
			len(*update.ExpectedName) > nativeBatchMaxValueSize || len(*update.Name) > nativeBatchMaxValueSize {
			return nativeBindingInvalidRequest()
		}
		updates = append(updates, policy.NativeBindingNameUpdate{
			ID: update.ID, APIKey: update.Key, ExpectedName: *update.ExpectedName, Name: *update.Name,
		})
	}
	result, err := a.store.SyncNativeBindingNames(updates)
	if errors.Is(err, policy.ErrInvalidNativeBindingNames) {
		return nativeBindingInvalidRequest()
	}
	if err != nil {
		return nativeKeyBindingStoreError(err)
	}
	return jsonResponse(http.StatusOK, result)
}
