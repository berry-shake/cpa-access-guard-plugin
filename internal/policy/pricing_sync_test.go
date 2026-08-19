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

func TestSyncAliasPricesByAliasName(t *testing.T) {
	s := newSyncStore(t)
	if err := s.UpsertAlias(AliasMapping{
		Alias:   "gpt-5.6-sol",
		Targets: []AliasTarget{{Provider: "codex", TargetModel: "gpt-5.6-sol"}},
	}); err != nil {
		t.Fatal(err)
	}
	updated, unmatched, skipped := s.SyncAliasPrices([]ModelsDevEntry{
		{ProviderID: "openai", ModelID: "openai/gpt-5.6-sol", Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25},
	})
	if updated != 1 || unmatched != 0 || skipped != 0 {
		t.Fatalf("updated=%d unmatched=%d skipped=%d", updated, unmatched, skipped)
	}
	got := s.AliasesSnapshot()[0]
	if got.InputPricePerMillion != 5 || got.OutputPricePerMillion != 30 ||
		got.CacheReadPricePerMillion != 0.5 || got.CacheWritePricePerMillion != 6.25 {
		t.Fatalf("prices not synced: %+v", got)
	}
	// Targets preserved.
	if len(got.Targets) != 1 || got.Targets[0].TargetModel != "gpt-5.6-sol" {
		t.Fatalf("targets clobbered: %+v", got.Targets)
	}
}

func TestSyncAliasPricesByTargetModelFallback(t *testing.T) {
	s := newSyncStore(t)
	if err := s.UpsertAlias(AliasMapping{
		Alias:   "fast", // alias name not in catalog; target model matches
		Targets: []AliasTarget{{Provider: "xai", TargetModel: "grok-4.6"}},
	}); err != nil {
		t.Fatal(err)
	}
	updated, _, _ := s.SyncAliasPrices([]ModelsDevEntry{
		{ProviderID: "xai", ModelID: "grok-4.6", Input: 1, Output: 2},
	})
	if updated != 1 {
		t.Fatalf("updated=%d", updated)
	}
	if got := s.AliasesSnapshot()[0]; got.InputPricePerMillion != 1 {
		t.Fatalf("fallback match failed: %+v", got)
	}
}

func TestSyncAliasPricesProviderPreferenceAndSkips(t *testing.T) {
	s := newSyncStore(t)
	if err := s.UpsertAlias(AliasMapping{
		Alias: "claude-sonnet-5", BillingMode: "tokens",
		Targets: []AliasTarget{{Provider: "claude", TargetModel: "claude-sonnet-5"}},
	}); err != nil {
		t.Fatal(err)
	}
	// per_call alias must be skipped.
	if err := s.UpsertAlias(AliasMapping{
		Alias: "img", BillingMode: "per_call", PerCallUSD: 0.01,
		Targets: []AliasTarget{{Provider: "xai", TargetModel: "grok-imagine-image"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Unmatched alias.
	if err := s.UpsertAlias(AliasMapping{
		Alias: "mystery", Targets: []AliasTarget{{Provider: "codex", TargetModel: "no-such-model"}},
	}); err != nil {
		t.Fatal(err)
	}
	updated, unmatched, skipped := s.SyncAliasPrices([]ModelsDevEntry{
		// A router provider also lists the model; the anthropic one must win.
		{ProviderID: "ai-router", ModelID: "claude-sonnet-5", Input: 9, Output: 9},
		{ProviderID: "anthropic", ModelID: "anthropic/claude-sonnet-5", Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	})
	if updated != 1 || unmatched != 1 || skipped != 1 {
		t.Fatalf("updated=%d unmatched=%d skipped=%d", updated, unmatched, skipped)
	}
	got := s.AliasesSnapshot()
	// find claude alias
	for _, a := range got {
		if a.Alias == "claude-sonnet-5" {
			if a.InputPricePerMillion != 3 {
				t.Fatalf("provider preference failed: %+v", a)
			}
		}
	}
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

	entries, err := FetchModelsDevPricing(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ModelID != "gpt-5.6-sol" || entries[0].CacheWrite != 6.25 || entries[0].Name != "GPT 5.6 Sol" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestSelectCommonModelsDevMatchesCCSwitch(t *testing.T) {
	entries := []ModelsDevEntry{
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
	prices := ToModelPrices(got)
	if len(prices) != 2 || prices[0].ID != "gpt-5.6-sol" || prices[0].CacheWritePricePerMillion != 6.25 || prices[0].DisplayName != "GPT-5.6 Sol" {
		t.Fatalf("prices=%+v", prices)
	}
}
