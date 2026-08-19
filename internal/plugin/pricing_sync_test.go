package plugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cpa-access-guard/internal/policy"
)

func TestPricingSyncWritesCatalogFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"openai": {
				"name": "OpenAI",
				"models": {
					"gpt-5.6-sol": {
						"name": "GPT 5.6 Sol",
						"cost": {"input": 5, "output": 30, "cache_read": 0.5, "cache_write": 6.25},
						"modalities": {"output": ["text"]}
					}
				}
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	app := NewApp()
	t.Cleanup(app.Shutdown)
	yaml := []byte(`
enabled: true
state_file: "` + filepath.ToSlash(statePath) + `"
pricing_sync:
  url: "` + srv.URL + `"
aliases:
  - alias: gpt-5.6-sol
    targets: [{provider: codex, target_model: gpt-5.6-sol}]
keys: []
`)
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatal(err)
	}

	runReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-access-guard/pricing-sync/run",
	})
	raw, err := app.HandleMethod(MethodManagementHandle, runReq)
	if err != nil {
		t.Fatal(err)
	}
	resp := managementResponseFromEnvelope(t, raw)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("run status=%d body=%s", resp.StatusCode, resp.Body)
	}
	var result policy.PricingSyncResult
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Error != "" || result.Updated != 1 || result.Catalog < 1 {
		t.Fatalf("result=%+v", result)
	}

	statusReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-access-guard/status",
	})
	raw, err = app.HandleMethod(MethodManagementHandle, statusReq)
	if err != nil {
		t.Fatal(err)
	}
	statusResp := managementResponseFromEnvelope(t, raw)
	var status map[string]any
	if err := json.Unmarshal(statusResp.Body, &status); err != nil {
		t.Fatal(err)
	}
	pricingFile, _ := status["pricing_file"].(string)
	if pricingFile == "" {
		t.Fatalf("status missing pricing_file: %s", statusResp.Body)
	}
	if _, err := os.Stat(pricingFile); err != nil {
		t.Fatalf("pricing file not written: %v", err)
	}
	file, err := policy.LoadModelPricingFile(pricingFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Models) == 0 || file.Models[0].InputPricePerMillion != 5 {
		t.Fatalf("catalog=%+v", file)
	}

	aliases := app.store.AliasesSnapshot()
	if len(aliases) != 1 || aliases[0].InputPricePerMillion != 5 {
		t.Fatalf("alias prices not stamped: %+v", aliases)
	}
}

func TestModelPricingManagementCRUD(t *testing.T) {
	app := NewApp()
	t.Cleanup(app.Shutdown)
	yaml := []byte(`
enabled: true
state_file: "` + filepath.ToSlash(filepath.Join(t.TempDir(), "state.json")) + `"
keys: []
`)
	req, _ := json.Marshal(LifecycleRequest{ConfigYAML: yaml})
	if _, err := app.HandleMethod(MethodPluginReconfigure, req); err != nil {
		t.Fatal(err)
	}

	createReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodPost,
		Path:   "/v0/management/plugins/cpa-access-guard/pricing",
		Body:   []byte(`{"modelId":"gpt-5.5","displayName":"GPT-5.5","inputCostPerMillion":"5","outputCostPerMillion":"30","cacheReadCostPerMillion":"0.5","cacheCreationCostPerMillion":"0"}`),
	})
	raw, err := app.HandleMethod(MethodManagementHandle, createReq)
	if err != nil {
		t.Fatal(err)
	}
	created := managementResponseFromEnvelope(t, raw)
	if created.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.StatusCode, created.Body)
	}

	listReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodGet,
		Path:   "/v0/management/plugins/cpa-access-guard/pricing",
	})
	raw, err = app.HandleMethod(MethodManagementHandle, listReq)
	if err != nil {
		t.Fatal(err)
	}
	listed := managementResponseFromEnvelope(t, raw)
	var payload struct {
		Models []map[string]string `json:"models"`
	}
	if err := json.Unmarshal(listed.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 1 || payload.Models[0]["modelId"] != "gpt-5.5" || payload.Models[0]["inputCostPerMillion"] != "5" {
		t.Fatalf("list=%s", listed.Body)
	}

	delReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodDelete,
		Path:   "/v0/management/plugins/cpa-access-guard/pricing",
		Body:   []byte(`{"modelId":"gpt-5.5"}`),
	})
	raw, err = app.HandleMethod(MethodManagementHandle, delReq)
	if err != nil {
		t.Fatal(err)
	}
	deleted := managementResponseFromEnvelope(t, raw)
	if deleted.StatusCode != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.StatusCode, deleted.Body)
	}
}
