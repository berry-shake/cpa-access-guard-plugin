package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResolvePricingPathDefaultsNextToState(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "cpa-access-guard-state.json")
	got, err := ResolvePricingPath("", state)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, defaultPricingFileName)
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestResolvePricingPathExplicit(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "custom-prices.json")
	got, err := ResolvePricingPath(explicit, filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got != explicit {
		t.Fatalf("got %q want %q", got, explicit)
	}
}

func TestModelPriceJSONMatchesCCSwitch(t *testing.T) {
	raw, err := json.Marshal(ModelPrice{
		ID:                        "gpt-5.5",
		DisplayName:               "GPT-5.5",
		InputPricePerMillion:      5,
		OutputPricePerMillion:     30,
		CacheReadPricePerMillion:  0.5,
		CacheWritePricePerMillion: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"modelId":                     "gpt-5.5",
		"displayName":                 "GPT-5.5",
		"inputCostPerMillion":         "5",
		"outputCostPerMillion":        "30",
		"cacheReadCostPerMillion":     "0.5",
		"cacheCreationCostPerMillion": "0",
	}
	if len(got) != len(want) {
		t.Fatalf("keys=%v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s=%v want %v raw=%s", k, got[k], v, raw)
		}
	}
	var back ModelPrice
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != "gpt-5.5" || back.DisplayName != "GPT-5.5" || back.InputPricePerMillion != 5 || back.CacheWritePricePerMillion != 0 {
		t.Fatalf("roundtrip=%+v", back)
	}
}

func assertCCSwitchModelJSON(t *testing.T, raw []byte, modelID string) {
	t.Helper()
	var file struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	for _, m := range file.Models {
		if m["modelId"] == modelID {
			row = m
			break
		}
	}
	if row == nil {
		t.Fatalf("model %s missing in %s", modelID, raw)
	}
	for _, key := range []string{"modelId", "displayName", "inputCostPerMillion", "outputCostPerMillion", "cacheReadCostPerMillion", "cacheCreationCostPerMillion"} {
		if _, ok := row[key]; !ok {
			t.Fatalf("missing %s in %v", key, row)
		}
		if _, ok := row[key].(string); !ok {
			t.Fatalf("%s must be string, got %T %v", key, row[key], row[key])
		}
	}
	if source, _ := row["source"].(string); source == "" {
		t.Fatalf("pricing source metadata should be persisted: %v", row)
	}
}

func TestUpsertAndDeleteModelPrice(t *testing.T) {
	s := NewStore()
	if err := s.Configure(Config{Enabled: true, StateFile: t.TempDir() + "/state.json"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertModelPrice(ModelPrice{
		ID: "GPT-5.5", DisplayName: "GPT-5.5",
		InputPricePerMillion: 5, OutputPricePerMillion: 30, CacheReadPricePerMillion: 0.5,
	}); err != nil {
		t.Fatal(err)
	}
	rows := s.PricingSnapshot()
	if len(rows) != 1 || rows[0].ID != "gpt-5.5" || rows[0].InputPricePerMillion != 5 {
		t.Fatalf("snapshot=%+v", rows)
	}
	raw, err := os.ReadFile(s.PricingPath())
	if err != nil {
		t.Fatal(err)
	}
	assertCCSwitchModelJSON(t, raw, "gpt-5.5")
	if err := s.DeleteModelPrice("gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	if got := s.PricingSnapshot(); len(got) != 0 {
		t.Fatalf("expected empty after delete, got %+v", got)
	}
	file, err := LoadModelPricingFile(s.PricingPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(file.DeletedModelIDs) != 1 || file.DeletedModelIDs[0] != "gpt-5.5" {
		t.Fatalf("deletedModelIds=%v", file.DeletedModelIDs)
	}
}

func TestApplyPricingSourcesPrecedenceAndProtections(t *testing.T) {
	s := newSyncStore(t)
	at := time.Unix(1_800_000_000, 0).UTC()
	lite := PricingFetchResult{Fetched: 3, Accepted: 3, Entries: []PricingCatalogEntry{
		{ProviderID: "openai", ModelID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", Source: PricingSourceLiteLLM, Status: PricingStatusPriced, Mode: PricingModeText, Input: 4, Output: 20, CacheRead: 0.4, CacheWrite: 5},
		{ProviderID: "openai", ModelID: "gpt-5.3-codex-spark", Name: "GPT-5.3 Codex Spark", Source: PricingSourceLiteLLM, Status: PricingStatusKnownUnpriced, Mode: PricingModeText},
		{ProviderID: "openai", ModelID: "gpt-image-2", Name: "GPT Image 2", Source: PricingSourceLiteLLM, Status: PricingStatusPriced, Mode: PricingModeImageGeneration, ImageInput: 8, ImageOutput: 30},
	}}
	fallback := PricingFetchResult{Fetched: 3, Accepted: 3, Entries: []PricingCatalogEntry{
		{ProviderID: "openai", ModelID: "gpt-5.6-sol", Source: PricingSourceModelsDev, Status: PricingStatusPriced, Mode: PricingModeText, Input: 5, Output: 30},
		{ProviderID: "openai", ModelID: "gpt-5.3-codex-spark", Source: PricingSourceModelsDev, Status: PricingStatusPriced, Mode: PricingModeText, Input: 9, Output: 90},
		{ProviderID: "openai", ModelID: "gpt-5.6-luna", Source: PricingSourceModelsDev, Status: PricingStatusPriced, Mode: PricingModeText, Input: 0.2, Output: 1.2},
	}}
	result, err := s.ApplyPricingSources(at, lite, nil, fallback, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Catalog != 4 || result.LiteLLM != 3 || result.ModelsDev != 1 || result.KnownUnpriced != 1 {
		t.Fatalf("result=%+v", result)
	}
	rows := indexPrices(s.PricingSnapshot())
	if got := rows["gpt-5.6-sol"]; got.Source != PricingSourceLiteLLM || got.InputPricePerMillion != 4 {
		t.Fatalf("primary did not win: %+v", got)
	}
	if got := rows["gpt-5.3-codex-spark"]; got.Source != PricingSourceLiteLLM || got.Status != PricingStatusKnownUnpriced {
		t.Fatalf("known-unpriced row did not block fallback: %+v", got)
	}
	if _, ok := s.LookupCatalogSheet("gpt-5.3-codex-spark", "codex"); ok {
		t.Fatal("known-unpriced model must not be billed")
	}
	if _, ok := s.LookupCatalogSheet("gpt-image-2", "codex"); ok {
		t.Fatal("image token rates must not be treated as text billing")
	}

	if err := s.UpsertModelPrice(ModelPrice{ID: "gpt-5.6-sol", InputPricePerMillion: 1, OutputPricePerMillion: 2}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModelPrice("gpt-5.6-luna"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPricingSources(at.Add(time.Hour), lite, nil, fallback, nil); err != nil {
		t.Fatal(err)
	}
	rows = indexPrices(s.PricingSnapshot())
	if got := rows["gpt-5.6-sol"]; got.Source != PricingSourceManual || got.InputPricePerMillion != 1 {
		t.Fatalf("manual override was replaced: %+v", got)
	}
	if _, ok := rows["gpt-5.6-luna"]; ok {
		t.Fatal("deleted automatic model was resurrected")
	}
	if err := s.RestoreModelPriceAutomatic("gpt-5.6-luna"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPricingSources(at.Add(2*time.Hour), lite, nil, fallback, nil); err != nil {
		t.Fatal(err)
	}
	rows = indexPrices(s.PricingSnapshot())
	if got := rows["gpt-5.6-luna"]; got.Source != PricingSourceModelsDev || got.InputPricePerMillion != 0.2 {
		t.Fatalf("restored automatic model = %+v", got)
	}
}

func TestApplyPricingSourcesRetainsPrimaryOnFailure(t *testing.T) {
	s := newSyncStore(t)
	primary := PricingFetchResult{Fetched: 1, Accepted: 1, Entries: []PricingCatalogEntry{{
		ProviderID: "openai", ModelID: "gpt-5.6-sol", Source: PricingSourceLiteLLM,
		Status: PricingStatusPriced, Mode: PricingModeText, Input: 4, Output: 20,
	}}}
	fallback := PricingFetchResult{Fetched: 1, Accepted: 1, Entries: []PricingCatalogEntry{{
		ProviderID: "openai", ModelID: "gpt-5.6-sol", Source: PricingSourceModelsDev,
		Status: PricingStatusPriced, Mode: PricingModeText, Input: 5, Output: 30,
	}}}
	if _, err := s.ApplyPricingSources(time.Now(), primary, nil, fallback, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPricingSources(time.Now().Add(time.Hour), PricingFetchResult{}, errors.New("offline"), fallback, nil); err != nil {
		t.Fatal(err)
	}
	row := indexPrices(s.PricingSnapshot())["gpt-5.6-sol"]
	if row.Source != PricingSourceLiteLLM || row.Status != PricingStatusStale || row.InputPricePerMillion != 4 {
		t.Fatalf("last-known primary price was not retained: %+v", row)
	}
	state := s.PricingSyncStateSnapshot()
	if state.LiteLLM.LastSyncError == nil || state.ModelsDev.LastSyncError != nil {
		t.Fatalf("sync state=%+v", state)
	}
}

func TestApplyPricingSourcesUsesFallbackOnInitialPrimaryFailure(t *testing.T) {
	s := newSyncStore(t)
	fallback := PricingFetchResult{Fetched: 1, Accepted: 1, Entries: []PricingCatalogEntry{{
		ProviderID: "openai", ModelID: "gpt-5.6-luna", Source: PricingSourceModelsDev,
		Status: PricingStatusPriced, Mode: PricingModeText, Input: 0.2, Output: 1.2,
	}}}
	result, err := s.ApplyPricingSources(time.Now(), PricingFetchResult{}, errors.New("offline"), fallback, nil)
	if err != nil {
		t.Fatal(err)
	}
	row := indexPrices(s.PricingSnapshot())["gpt-5.6-luna"]
	if row.Source != PricingSourceModelsDev || row.InputPricePerMillion != 0.2 || result.ModelsDev != 1 {
		t.Fatalf("fallback row/result = %+v / %+v", row, result)
	}
}

func TestApplyPricingSourcesBothFailuresKeepLastKnownPrices(t *testing.T) {
	s := newSyncStore(t)
	primary := PricingFetchResult{Fetched: 1, Accepted: 1, Entries: []PricingCatalogEntry{{
		ProviderID: "openai", ModelID: "gpt-5.6-sol", Source: PricingSourceLiteLLM,
		Status: PricingStatusPriced, Mode: PricingModeText, Input: 4, Output: 20,
	}}}
	if _, err := s.ApplyPricingSources(time.Now(), primary, nil, PricingFetchResult{}, errors.New("fallback offline")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyPricingSources(time.Now().Add(time.Hour), PricingFetchResult{}, errors.New("primary offline"), PricingFetchResult{}, errors.New("fallback offline")); err != nil {
		t.Fatal(err)
	}
	row := indexPrices(s.PricingSnapshot())["gpt-5.6-sol"]
	if row.Source != PricingSourceLiteLLM || row.Status != PricingStatusStale || row.InputPricePerMillion != 4 || row.OutputPricePerMillion != 20 {
		t.Fatalf("last-known row = %+v", row)
	}
	state := s.PricingSyncStateSnapshot()
	if state.LiteLLM.LastSyncError == nil || state.ModelsDev.LastSyncError == nil {
		t.Fatalf("sync state = %+v", state)
	}
}

func TestLoadVersionOnePricingFileMigratesWithoutBreaking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	raw := []byte(`{
		"version": 1,
		"modelsDevSync": {"lastSyncAt": 1234},
		"models": [{
			"modelId": "gpt-5.5",
			"displayName": "GPT-5.5",
			"inputCostPerMillion": "5",
			"outputCostPerMillion": "30",
			"cacheReadCostPerMillion": "0.5",
			"cacheCreationCostPerMillion": "0"
		}],
		"deletedModelIds": ["gpt-5.4-mini"]
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := LoadModelPricingFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Models) != 1 || file.Models[0].Source != PricingSourceLegacy || file.Models[0].Status != PricingStatusPriced {
		t.Fatalf("models=%+v", file.Models)
	}
	if file.PricingSync.ModelsDev.LastSyncAt == nil || *file.PricingSync.ModelsDev.LastSyncAt != 1234 {
		t.Fatalf("pricing sync migration=%+v", file.PricingSync)
	}
	if len(file.DeletedModelIDs) != 1 || file.DeletedModelIDs[0] != "gpt-5.4-mini" {
		t.Fatalf("deleted=%v", file.DeletedModelIDs)
	}
}
