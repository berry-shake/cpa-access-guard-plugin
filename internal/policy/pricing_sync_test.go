package policy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormalizeModelIDForPricing(t *testing.T) {
	cases := map[string]string{
		"openai/gpt-5.6-sol":      "gpt-5.6-sol",
		"gpt-5.6-sol:2026-08-01":  "gpt-5.6-sol",
		"Claude-Sonnet-5[1M]":     "claude-sonnet-5",
		"anthropic/claude@latest": "claude-latest",
		"  grok-4.6  ":            "grok-4.6",
	}
	for in, want := range cases {
		if got := NormalizeModelIDForPricing(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func newSyncStore(t *testing.T) *Store {
	t.Helper()
	s := NewStore()
	if err := s.Configure(Config{Enabled: true, StateFile: t.TempDir() + "/state.json"}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestFetchModelsDevPricingFlattensTextModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"openai": {
				"name": "OpenAI",
				"models": {
					"gpt-5.6-sol": {
						"name": "GPT 5.6 Sol",
						"cost": {"input": 5, "output": 30, "cache_read": 0.5, "cache_write": 6.25},
						"modalities": {"output": ["text"]}
					},
					"gpt-image-1": {
						"name": "GPT Image",
						"cost": {"input": 8, "output": 0},
						"modalities": {"output": ["image"]}
					}
				}
			}
		}`))
	}))
	defer srv.Close()

	result, err := FetchModelsDevCatalog(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	entries := result.Entries
	if len(entries) != 1 || entries[0].ModelID != "gpt-5.6-sol" || entries[0].CacheWrite != 6.25 || entries[0].Name != "GPT 5.6 Sol" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestSelectCommonModelsDevMatchesCCSwitch(t *testing.T) {
	entries := []PricingCatalogEntry{
		{Key: "openai/gpt-5.6-sol", ProviderID: "openai", ModelID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", ReleaseDate: "2026-08-01", Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25},
		{Key: "openai/gpt-5.6-luna", ProviderID: "openai", ModelID: "gpt-5.6-luna", Name: "GPT-5.6 Luna", ReleaseDate: "2026-07-01", Input: 0.2, Output: 1.2},
		{Key: "relay/openai-gpt-5.6-sol", ProviderID: "relay", ModelID: "openai-gpt-5.6-sol", Name: "OpenAI GPT-5.6 Sol", ReleaseDate: "2026-08-02", Input: 5, Output: 30},
		{Key: "openai/openai-gpt-5.6-sol", ProviderID: "openai", ModelID: "openai-gpt-5.6-sol", Name: "OpenAI GPT-5.6 Sol", ReleaseDate: "2026-08-02", Input: 5, Output: 30},
	}
	got := SelectCommonModelsDev(entries)
	if len(got) != 2 {
		t.Fatalf("selected=%+v", got)
	}
	if got[0].ModelID != "gpt-5.6-sol" || got[1].ModelID != "gpt-5.6-luna" {
		t.Fatalf("order/ids=%+v", got)
	}
}
