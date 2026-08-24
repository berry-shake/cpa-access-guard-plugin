package policy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFetchLiteLLMPricingSelectsCanonicalRows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{
			"sample_spec": "not a model",
			"chatgpt/gpt-5.4": {
				"model_name": "GPT-5.4 ChatGPT",
				"litellm_provider": "chatgpt",
				"mode": "responses"
			},
			"gpt-5.4": {
				"model_name": "GPT-5.4",
				"litellm_provider": "openai",
				"mode": "chat",
				"input_cost_per_token": 0.0000025,
				"output_cost_per_token": 0.000015,
				"cache_read_input_token_cost": 0.00000025
			},
			"chatgpt/gpt-5.3-codex-spark": {
				"model_name": "GPT-5.3 Codex Spark",
				"litellm_provider": "chatgpt",
				"mode": "responses"
			},
			"gpt-image-2": {
				"litellm_provider": "openai",
				"mode": "image_generation",
				"input_cost_per_token": 0.000005,
				"output_cost_per_token": 0.00001,
				"input_cost_per_image_token": 0.000008,
				"output_cost_per_image_token": 0.00003
			},
			"azure/gpt-5.4": {
				"litellm_provider": "azure",
				"mode": "chat",
				"input_cost_per_token": 0.000009
			},
			"gpt-invalid": {
				"litellm_provider": "openai",
				"mode": "chat",
				"input_cost_per_token": -1
			}
		}`))
	}))
	t.Cleanup(srv.Close)

	result, err := FetchLiteLLMPricing(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if result.Fetched != 7 || result.Accepted != 3 || len(result.Entries) != 3 {
		t.Fatalf("result=%+v", result)
	}
	byID := make(map[string]PricingCatalogEntry, len(result.Entries))
	for _, entry := range result.Entries {
		byID[entry.ModelID] = entry
	}
	if got := byID["gpt-5.4"]; got.Input != 2.5 || got.Output != 15 || got.CacheRead != 0.25 || got.Status != PricingStatusPriced || got.SourceModelID != "gpt-5.4" {
		t.Fatalf("gpt-5.4=%+v", got)
	}
	if got := byID["gpt-5.3-codex-spark"]; got.Status != PricingStatusKnownUnpriced || got.hasAnyPriceForTest() {
		t.Fatalf("spark=%+v", got)
	}
	if got := byID["gpt-image-2"]; got.Mode != PricingModeImageGeneration || got.ImageInput != 8 || got.ImageOutput != 30 || got.Status != PricingStatusPriced {
		t.Fatalf("image=%+v", got)
	}
}

func (e PricingCatalogEntry) hasAnyPriceForTest() bool {
	return e.Input != 0 || e.Output != 0 || e.CacheRead != 0 || e.CacheWrite != 0 || e.ImageInput != 0 || e.ImageOutput != 0
}

func TestPricingFetchErrorDoesNotExposeURLQuery(t *testing.T) {
	_, err := FetchLiteLLMPricing(context.Background(), "http://127.0.0.1:1/catalog.json?token=super-secret")
	if err == nil {
		t.Fatal("expected request failure")
	}
	if got := err.Error(); got != "litellm: request failed for http://127.0.0.1:1/catalog.json" {
		t.Fatalf("error leaked or changed unexpectedly: %q", got)
	}

	_, err = FetchLiteLLMPricing(context.Background(), "http://example.test/%zz?token=super-secret")
	if err == nil {
		t.Fatal("expected malformed URL failure")
	}
	if got := err.Error(); got != "litellm: invalid URL" {
		t.Fatalf("malformed URL error leaked or changed unexpectedly: %q", got)
	}
}
