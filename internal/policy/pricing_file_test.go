package policy

import (
	"bytes"
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

func TestConfigureSeedsPricingFileFromAliases(t *testing.T) {
	dir := t.TempDir()
	s := NewStore()
	if err := s.Configure(Config{
		Enabled:   true,
		StateFile: filepath.Join(dir, "state.json"),
		Aliases: []AliasMapping{{
			Alias:                     "gpt-5.6-sol",
			InputPricePerMillion:      1.25,
			OutputPricePerMillion:     10,
			CacheReadPricePerMillion:  0.125,
			CacheWritePricePerMillion: 1.56,
			Targets:                   []AliasTarget{{Provider: "codex", TargetModel: "gpt-5.6-sol"}},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	path := s.PricingPath()
	if filepath.Base(path) != defaultPricingFileName {
		t.Fatalf("pricing path basename=%q", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var file ModelPricingFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Models) == 0 {
		t.Fatal("expected seeded catalog rows")
	}
	sheet, ok := s.LookupCatalogSheet("gpt-5.6-sol", "codex")
	if !ok || sheet.Input != 1.25 || sheet.Output != 10 || sheet.CacheWrite != 1.56 {
		t.Fatalf("catalog lookup failed: ok=%v sheet=%+v", ok, sheet)
	}
}

func TestMergeCatalogPricingPersistsAndLooksUp(t *testing.T) {
	s := NewStore()
	if err := s.Configure(Config{Enabled: true, StateFile: t.TempDir() + "/state.json"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.MergeCatalogPricing([]ModelsDevEntry{
		{ProviderID: "openai", ModelID: "gpt-5.6-sol", Name: "GPT 5.6 Sol", Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25},
		{ProviderID: "ai-router", ModelID: "gpt-5.6-sol", Name: "GPT 5.6 Sol", Input: 9, Output: 9},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("written=%d want 1 unique model id", n)
	}
	sheet, ok := s.LookupCatalogSheet("gpt-5.6-sol", "")
	if !ok || sheet.Input != 5 {
		t.Fatalf("should keep official openai price: %+v ok=%v", sheet, ok)
	}
	sheet, ok = s.LookupCatalogSheet("gpt-5.6-sol", "ai-router")
	if !ok || sheet.Input != 5 {
		t.Fatalf("same model id must not keep a second reseller price: %+v ok=%v", sheet, ok)
	}
	file, err := LoadModelPricingFile(s.PricingPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(file.Models) != 1 || file.Models[0].ID != "gpt-5.6-sol" {
		t.Fatalf("file=%+v", file)
	}
	envelope, err := os.ReadFile(s.PricingPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(envelope, []byte(`"modelsDevSync"`)) || !bytes.Contains(envelope, []byte(`"deletedModelIds"`)) {
		t.Fatalf("cc-switch envelope missing: %s", envelope)
	}
	if file.Models[0].InputPricePerMillion != 5 || file.Models[0].CacheWritePricePerMillion != 6.25 {
		t.Fatalf("prices=%+v", file.Models[0])
	}
	if file.Models[0].DisplayName != "GPT 5.6 Sol" {
		t.Fatalf("displayName=%q", file.Models[0].DisplayName)
	}
	raw, err := os.ReadFile(s.PricingPath())
	if err != nil {
		t.Fatal(err)
	}
	assertCCSwitchModelJSON(t, raw, "gpt-5.6-sol")
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

func TestReplaceCatalogPricingDropsResellerRows(t *testing.T) {
	s := NewStore()
	if err := s.Configure(Config{Enabled: true, StateFile: t.TempDir() + "/state.json"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MergeCatalogPricing([]ModelsDevEntry{
		{ProviderID: "relay", ModelID: "openai-gpt-5.6-sol", Name: "OpenAI GPT-5.6 Sol", Input: 5, Output: 30},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.ReplaceCatalogPricing(ToModelPrices([]ModelsDevEntry{
		{ProviderID: "openai", ModelID: "gpt-5.6-sol", Name: "GPT-5.6 Sol", Input: 5, Output: 30, CacheRead: 0.5, CacheWrite: 6.25},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("n=%d", n)
	}
	rows := s.PricingSnapshot()
	if len(rows) != 1 || rows[0].ID != "gpt-5.6-sol" || rows[0].CacheWritePricePerMillion != 6.25 {
		t.Fatalf("rows=%+v", rows)
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

func TestUpsertAliasWritesPricingFile(t *testing.T) {
	s := NewStore()
	if err := s.Configure(Config{Enabled: true, StateFile: t.TempDir() + "/state.json"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAlias(AliasMapping{
		Alias:                 "fast",
		InputPricePerMillion:  2,
		OutputPricePerMillion: 8,
		Targets:               []AliasTarget{{Provider: "xai", TargetModel: "grok-4.6"}},
	}); err != nil {
		t.Fatal(err)
	}
	sheet, ok := s.LookupCatalogSheet("grok-4.6", "xai")
	if !ok || sheet.Input != 2 {
		t.Fatalf("target model missing from catalog: ok=%v %+v", ok, sheet)
	}
	if _, ok := s.LookupCatalogSheet("fast", "xai"); !ok {
		t.Fatal("alias name should also be indexed")
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

func TestApplyPricingSourcesClearsAutomaticAliasShadow(t *testing.T) {
	s := newSyncStore(t)
	if err := s.UpsertAlias(AliasMapping{
		Alias: "fast", PricingMode: "auto",
		InputPricePerMillion: 3, OutputPricePerMillion: 12,
		Targets: []AliasTarget{{Provider: "codex", TargetModel: "gpt-5.3-codex-spark"}},
	}); err != nil {
		t.Fatal(err)
	}
	lite := PricingFetchResult{Fetched: 1, Accepted: 1, Entries: []PricingCatalogEntry{{
		ProviderID: "openai", ModelID: "gpt-5.3-codex-spark", Source: PricingSourceLiteLLM,
		Status: PricingStatusKnownUnpriced, Mode: PricingModeText,
	}}}
	if _, err := s.ApplyPricingSources(time.Now(), lite, nil, PricingFetchResult{}, errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	if updated, unmatched, skipped := s.SyncAliasPricesFromCatalog(); updated != 1 || unmatched != 0 || skipped != 0 {
		t.Fatalf("updated=%d unmatched=%d skipped=%d", updated, unmatched, skipped)
	}
	got := s.AliasesSnapshot()[0]
	if got.InputPricePerMillion != 0 || got.OutputPricePerMillion != 0 {
		t.Fatalf("alias retained old price: %+v", got)
	}
	if _, ok := s.LookupCatalogSheet("fast", "codex", "gpt-5.3-codex-spark"); ok {
		t.Fatal("legacy alias shadow bypassed the known-unpriced source row")
	}
}

func TestAutomaticAliasUpdateDoesNotOverwriteSourceCatalog(t *testing.T) {
	s := newSyncStore(t)
	lite := PricingFetchResult{Fetched: 1, Accepted: 1, Entries: []PricingCatalogEntry{{
		ProviderID: "openai", ModelID: "gpt-5.6-sol", Source: PricingSourceLiteLLM,
		Status: PricingStatusPriced, Mode: PricingModeText, Input: 4, Output: 20,
	}}}
	if _, err := s.ApplyPricingSources(time.Now(), lite, nil, PricingFetchResult{}, errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAlias(AliasMapping{
		Alias: "gpt-5.6-sol", PricingMode: "auto",
		InputPricePerMillion: 9, OutputPricePerMillion: 99,
		Targets: []AliasTarget{{Provider: "codex", TargetModel: "gpt-5.6-sol"}},
	}); err != nil {
		t.Fatal(err)
	}
	row := indexPrices(s.PricingSnapshot())["gpt-5.6-sol"]
	if row.Source != PricingSourceLiteLLM || row.InputPricePerMillion != 4 || row.OutputPricePerMillion != 20 {
		t.Fatalf("automatic alias replaced sourced catalog row: %+v", row)
	}
}

func TestAliasCanSwitchFromManualBackToAutomaticPricing(t *testing.T) {
	s := newSyncStore(t)
	alias := AliasMapping{
		Alias: "gpt-5.6-sol", PricingMode: "manual",
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Targets: []AliasTarget{{Provider: "codex", TargetModel: "gpt-5.6-sol"}},
	}
	if err := s.UpsertAlias(alias); err != nil {
		t.Fatal(err)
	}
	if got := indexPrices(s.PricingSnapshot())["gpt-5.6-sol"]; got.Source != PricingSourceAlias {
		t.Fatalf("manual alias source=%+v", got)
	}
	alias.PricingMode = "auto"
	if err := s.UpsertAlias(alias); err != nil {
		t.Fatal(err)
	}
	lite := PricingFetchResult{Fetched: 1, Accepted: 1, Entries: []PricingCatalogEntry{{
		ProviderID: "openai", ModelID: "gpt-5.6-sol", Source: PricingSourceLiteLLM,
		Status: PricingStatusPriced, Mode: PricingModeText, Input: 4, Output: 20,
	}}}
	if _, err := s.ApplyPricingSources(time.Now(), lite, nil, PricingFetchResult{}, errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	got := indexPrices(s.PricingSnapshot())["gpt-5.6-sol"]
	if got.Source != PricingSourceLiteLLM || got.InputPricePerMillion != 4 {
		t.Fatalf("auto mode did not release manual alias row: %+v", got)
	}
}

func TestAliasReleasesManualCatalogRowsWhenAutomaticPricesAreCleared(t *testing.T) {
	s := newSyncStore(t)
	alias := AliasMapping{
		Alias: "fast", PricingMode: "manual",
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Targets: []AliasTarget{{Provider: "codex", TargetModel: "gpt-5.6-sol"}},
	}
	if err := s.UpsertAlias(alias); err != nil {
		t.Fatal(err)
	}
	alias.PricingMode = "auto"
	alias.InputPricePerMillion = 0
	alias.OutputPricePerMillion = 0
	if err := s.UpsertAlias(alias); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fast", "gpt-5.6-sol"} {
		if row, ok := indexPrices(s.PricingSnapshot())[id]; ok && row.Source == PricingSourceAlias {
			t.Fatalf("manual row %q survived switch to empty automatic pricing: %+v", id, row)
		}
	}

	lite := PricingFetchResult{Fetched: 1, Accepted: 1, Entries: []PricingCatalogEntry{{
		ProviderID: "openai", ModelID: "gpt-5.6-sol", Source: PricingSourceLiteLLM,
		Status: PricingStatusPriced, Mode: PricingModeText, Input: 4, Output: 20,
	}}}
	if _, err := s.ApplyPricingSources(time.Now(), lite, nil, PricingFetchResult{}, errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	if got := indexPrices(s.PricingSnapshot())["gpt-5.6-sol"]; got.Source != PricingSourceLiteLLM || got.InputPricePerMillion != 4 {
		t.Fatalf("automatic source could not reclaim cleared row: %+v", got)
	}
}

func TestDeleteAliasRemovesOrphanedManualCatalogRows(t *testing.T) {
	s := newSyncStore(t)
	if err := s.UpsertAlias(AliasMapping{
		Alias: "fast", PricingMode: "manual",
		InputPricePerMillion: 1, OutputPricePerMillion: 2,
		Targets: []AliasTarget{{Provider: "codex", TargetModel: "gpt-5.6-sol"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAlias("fast"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"fast", "gpt-5.6-sol"} {
		if row, ok := indexPrices(s.PricingSnapshot())[id]; ok && row.Source == PricingSourceAlias {
			t.Fatalf("orphaned manual row %q survived alias deletion: %+v", id, row)
		}
	}
}

func TestAliasPricingHonorsAndCanExplicitlyClearTombstone(t *testing.T) {
	s := newSyncStore(t)
	if err := s.UpsertModelPrice(ModelPrice{
		ID: "gpt-5.6-sol", InputPricePerMillion: 1, OutputPricePerMillion: 2,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteModelPrice("gpt-5.6-sol"); err != nil {
		t.Fatal(err)
	}
	auto := AliasMapping{
		Alias: "gpt-5.6-sol", PricingMode: "auto",
		InputPricePerMillion: 4, OutputPricePerMillion: 20,
		Targets: []AliasTarget{{Provider: "codex", TargetModel: "gpt-5.6-sol"}},
	}
	if err := s.UpsertAlias(auto); err != nil {
		t.Fatal(err)
	}
	if _, exists := indexPrices(s.PricingSnapshot())["gpt-5.6-sol"]; exists {
		t.Fatal("automatic alias bypassed a deletion tombstone")
	}
	if got := s.PricingDeletedSnapshot(); len(got) != 1 || got[0] != "gpt-5.6-sol" {
		t.Fatalf("automatic alias changed tombstones: %v", got)
	}

	auto.PricingMode = "manual"
	if err := s.UpsertAlias(auto); err != nil {
		t.Fatal(err)
	}
	row := indexPrices(s.PricingSnapshot())["gpt-5.6-sol"]
	if row.Source != PricingSourceAlias || row.InputPricePerMillion != 4 {
		t.Fatalf("manual alias did not replace tombstone: %+v", row)
	}
	if got := s.PricingDeletedSnapshot(); len(got) != 0 {
		t.Fatalf("manual alias did not clear tombstone: %v", got)
	}
	file, err := LoadModelPricingFile(s.PricingPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(file.DeletedModelIDs) != 0 || indexPrices(file.Models)["gpt-5.6-sol"].Source != PricingSourceAlias {
		t.Fatalf("manual alias state did not persist: %+v", file)
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
