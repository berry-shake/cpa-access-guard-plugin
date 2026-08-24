package plugin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpa-access-guard/internal/policy"
)

func TestResolvedPricingURLsKeepsLegacyModelsDevOverride(t *testing.T) {
	liteURL, modelsDevURL := resolvedPricingURLs(policy.PricingSyncConfig{URL: "https://legacy.example.test/models.json"})
	if liteURL != policy.DefaultLiteLLMURL || modelsDevURL != "https://legacy.example.test/models.json" {
		t.Fatalf("legacy URLs = %q / %q", liteURL, modelsDevURL)
	}

	liteURL, modelsDevURL = resolvedPricingURLs(policy.PricingSyncConfig{
		LiteLLMURL:   "https://primary.example.test/catalog.json",
		ModelsDevURL: "https://fallback.example.test/catalog.json",
		URL:          "https://ignored.example.test/catalog.json",
	})
	if liteURL != "https://primary.example.test/catalog.json" || modelsDevURL != "https://fallback.example.test/catalog.json" {
		t.Fatalf("explicit URLs = %q / %q", liteURL, modelsDevURL)
	}
}

func TestPricingSyncSerializesConcurrentRuns(t *testing.T) {
	var active atomic.Int32
	var peak atomic.Int32
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		current := active.Add(1)
		for {
			prior := peak.Load()
			if current <= prior || peak.CompareAndSwap(prior, current) {
				break
			}
		}
		defer active.Add(-1)
		time.Sleep(40 * time.Millisecond)
		if r.URL.Path == "/litellm" {
			_, _ = w.Write([]byte(`{"gpt-5.6-sol":{"litellm_provider":"openai","mode":"chat","input_cost_per_token":0.000005,"output_cost_per_token":0.00003}}`))
			return
		}
		_, _ = w.Write([]byte(`{"openai":{"models":{"gpt-5.6-luna":{"name":"GPT-5.6 Luna","cost":{"input":0.2,"output":1.2},"modalities":{"output":["text"]}}}}}`))
	}))
	t.Cleanup(srv.Close)

	app := NewApp()
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result := app.runPricingSync(srv.URL+"/litellm", srv.URL+"/models-dev")
			if result.Error != "" {
				t.Errorf("sync error = %q", result.Error)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := requests.Load(); got != 4 {
		t.Fatalf("requests = %d, want 4", got)
	}
	if got := peak.Load(); got != 2 {
		t.Fatalf("peak concurrent source requests = %d, want 2", got)
	}
}

func TestPricingSyncWritesCatalogFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/litellm" {
			_, _ = w.Write([]byte(`{
				"gpt-5.6-sol": {
					"litellm_provider": "openai",
					"mode": "chat",
					"input_cost_per_token": 0.000005,
					"output_cost_per_token": 0.00003,
					"cache_read_input_token_cost": 0.0000005,
					"cache_creation_input_token_cost": 0.00000625
				}
			}`))
			return
		}
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
  litellm_url: "` + srv.URL + `/litellm"
  models_dev_url: "` + srv.URL + `/models-dev"
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
		Path:   "/v0/management/plugins/access-guard/pricing-sync/run",
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
		Path:   "/v0/management/plugins/access-guard/status",
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
		Path:   "/v0/management/plugins/access-guard/pricing",
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
		Path:   "/v0/management/plugins/access-guard/pricing",
	})
	raw, err = app.HandleMethod(MethodManagementHandle, listReq)
	if err != nil {
		t.Fatal(err)
	}
	listed := managementResponseFromEnvelope(t, raw)
	var payload struct {
		Models []policy.ModelPrice `json:"models"`
	}
	if err := json.Unmarshal(listed.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Models) != 1 || payload.Models[0].ID != "gpt-5.5" || payload.Models[0].InputPricePerMillion != 5 || payload.Models[0].Source != policy.PricingSourceManual {
		t.Fatalf("list=%s", listed.Body)
	}

	delReq, _ := json.Marshal(ManagementRequest{
		Method: http.MethodDelete,
		Path:   "/v0/management/plugins/access-guard/pricing",
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
