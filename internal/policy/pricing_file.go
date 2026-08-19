package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Standalone model-price catalog persisted next to (or independently of)
// state_file. Auto-sync from models.dev writes here; billing falls back to
// this table when an alias has no token prices.

const modelPricingFileVersion = 1

// ModelPrice is one catalog row: USD per 1M tokens for a normalized model id.
// On disk it uses the cc-switch shape (all six fields always present; costs
// as decimal strings; cache write is cacheCreationCostPerMillion).
type ModelPrice struct {
	ID                        string
	DisplayName               string
	Provider                  string // in-memory only: official-provider preference
	InputPricePerMillion      float64
	OutputPricePerMillion     float64
	CacheReadPricePerMillion  float64
	CacheWritePricePerMillion float64
}

type modelPriceJSON struct {
	ModelID                     string `json:"modelId"`
	DisplayName                 string `json:"displayName"`
	InputCostPerMillion         string `json:"inputCostPerMillion"`
	OutputCostPerMillion        string `json:"outputCostPerMillion"`
	CacheReadCostPerMillion     string `json:"cacheReadCostPerMillion"`
	CacheCreationCostPerMillion string `json:"cacheCreationCostPerMillion"`
}

func formatPrice(value float64) string {
	if value != value || value <= 0 || value >= 1e12 {
		return "0"
	}
	s := strconv.FormatFloat(value, 'f', 6, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}

func parsePrice(s string) float64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0
	}
	return f
}

func (p ModelPrice) MarshalJSON() ([]byte, error) {
	name := strings.TrimSpace(p.DisplayName)
	if name == "" {
		name = p.ID
	}
	return json.Marshal(modelPriceJSON{
		ModelID:                     p.ID,
		DisplayName:                 name,
		InputCostPerMillion:         formatPrice(p.InputPricePerMillion),
		OutputCostPerMillion:        formatPrice(p.OutputPricePerMillion),
		CacheReadCostPerMillion:     formatPrice(p.CacheReadPricePerMillion),
		CacheCreationCostPerMillion: formatPrice(p.CacheWritePricePerMillion),
	})
}

func (p *ModelPrice) UnmarshalJSON(data []byte) error {
	var wire modelPriceJSON
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	p.ID = strings.TrimSpace(wire.ModelID)
	p.DisplayName = strings.TrimSpace(wire.DisplayName)
	p.InputPricePerMillion = parsePrice(wire.InputCostPerMillion)
	p.OutputPricePerMillion = parsePrice(wire.OutputCostPerMillion)
	p.CacheReadPricePerMillion = parsePrice(wire.CacheReadCostPerMillion)
	p.CacheWritePricePerMillion = parsePrice(wire.CacheCreationCostPerMillion)
	return nil
}

// ModelsDevSyncFile stores only sync outcome timestamps. Selection is always
// the official-family common set; enable/interval live in plugin YAML.
type ModelsDevSyncFile struct {
	LastSyncAt    *int64  `json:"lastSyncAt"`
	LastSyncError *string `json:"lastSyncError"`
}

func DefaultModelsDevSyncFile() ModelsDevSyncFile {
	return ModelsDevSyncFile{}
}

func normalizeSyncFile(cfg ModelsDevSyncFile) ModelsDevSyncFile {
	if cfg.LastSyncError != nil {
		trimmed := strings.TrimSpace(*cfg.LastSyncError)
		if trimmed == "" {
			cfg.LastSyncError = nil
		} else if len(trimmed) > 1000 {
			trimmed = trimmed[:1000]
			cfg.LastSyncError = &trimmed
		} else {
			cfg.LastSyncError = &trimmed
		}
	}
	return cfg
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// ModelPricingFile matches cc-switch ~/.cc-switch/model-pricing.json.
type ModelPricingFile struct {
	Version         int               `json:"version"`
	ModelsDevSync   ModelsDevSyncFile `json:"modelsDevSync"`
	Models          []ModelPrice      `json:"models"`
	DeletedModelIDs []string          `json:"deletedModelIds"`
}

var pricingBareKeyPreference = []string{"openai", "anthropic", "xai", "google"}

func hasTokenPrices(input, output, cacheRead, cacheWrite float64) bool {
	return input > 0 || output > 0 || cacheRead > 0 || cacheWrite > 0
}

func (p ModelPrice) hasTokenPrices() bool {
	return hasTokenPrices(p.InputPricePerMillion, p.OutputPricePerMillion, p.CacheReadPricePerMillion, p.CacheWritePricePerMillion)
}

func (p ModelPrice) sheet() PriceSheet {
	return PriceSheet{
		Input:      p.InputPricePerMillion,
		Output:     p.OutputPricePerMillion,
		CacheRead:  p.CacheReadPricePerMillion,
		CacheWrite: p.CacheWritePricePerMillion,
		Priced:     true,
	}
}

func preferredPricingProvider(provider string) int {
	provider = strings.ToLower(strings.TrimSpace(provider))
	for i, name := range pricingBareKeyPreference {
		if provider == name {
			return i
		}
	}
	return len(pricingBareKeyPreference)
}

// putPrice stores one row per normalized model id, matching cc-switch.
// Catalog merges prefer official providers (openai/anthropic/xai/google)
// over reseller listings of the same id. force replaces unconditionally
// (manual alias prices).
func putPrice(index map[string]ModelPrice, price ModelPrice, force bool) {
	price.ID = NormalizeModelIDForPricing(price.ID)
	price.Provider = strings.ToLower(strings.TrimSpace(price.Provider))
	price.DisplayName = strings.TrimSpace(price.DisplayName)
	if price.ID == "" {
		return
	}
	if !force && !price.hasTokenPrices() {
		return
	}
	if price.InputPricePerMillion < 0 || price.OutputPricePerMillion < 0 ||
		price.CacheReadPricePerMillion < 0 || price.CacheWritePricePerMillion < 0 {
		return
	}
	existing, ok := index[price.ID]
	if price.DisplayName == "" {
		if ok {
			price.DisplayName = existing.DisplayName
		}
		if price.DisplayName == "" {
			price.DisplayName = price.ID
		}
	}
	if !ok || force {
		index[price.ID] = price
		return
	}
	if strings.EqualFold(existing.Provider, price.Provider) ||
		preferredPricingProvider(price.Provider) < preferredPricingProvider(existing.Provider) {
		index[price.ID] = price
	}
}

func uniquePrices(index map[string]ModelPrice) []ModelPrice {
	out := make([]ModelPrice, 0, len(index))
	for _, price := range index {
		if price.ID == "" {
			continue
		}
		out = append(out, price)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func indexPrices(rows []ModelPrice) map[string]ModelPrice {
	index := make(map[string]ModelPrice, len(rows))
	for _, row := range rows {
		putPrice(index, row, false)
	}
	return index
}

func lookupPrice(index map[string]ModelPrice, modelID, _ string) (ModelPrice, bool) {
	if len(index) == 0 {
		return ModelPrice{}, false
	}
	id := NormalizeModelIDForPricing(modelID)
	if id == "" {
		return ModelPrice{}, false
	}
	price, ok := index[id]
	return price, ok
}

func LoadModelPricingFile(path string) (*ModelPricingFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file ModelPricingFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, err
	}
	if file.Version == 0 {
		file.Version = modelPricingFileVersion
	}
	file.ModelsDevSync = normalizeSyncFile(file.ModelsDevSync)
	if file.Models == nil {
		file.Models = []ModelPrice{}
	}
	file.DeletedModelIDs = uniqueSortedStrings(file.DeletedModelIDs)
	deleted := make(map[string]struct{}, len(file.DeletedModelIDs))
	for _, id := range file.DeletedModelIDs {
		deleted[id] = struct{}{}
	}
	kept := file.Models[:0]
	for _, model := range file.Models {
		if _, skip := deleted[model.ID]; skip {
			continue
		}
		kept = append(kept, model)
	}
	file.Models = kept
	return &file, nil
}

func SaveModelPricingFile(path string, file ModelPricingFile) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("pricing_file path is empty")
	}
	if file.Version == 0 {
		file.Version = modelPricingFileVersion
	}
	file.ModelsDevSync = normalizeSyncFile(file.ModelsDevSync)
	if file.Models == nil {
		file.Models = []ModelPrice{}
	}
	file.DeletedModelIDs = uniqueSortedStrings(file.DeletedModelIDs)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return atomicWriteStateFile(path, raw)
}

func seedPricesFromAliases(aliases []AliasMapping) []ModelPrice {
	index := make(map[string]ModelPrice)
	for _, alias := range aliases {
		if strings.EqualFold(alias.BillingMode, "per_call") {
			continue
		}
		if !hasTokenPrices(alias.InputPricePerMillion, alias.OutputPricePerMillion, alias.CacheReadPricePerMillion, alias.CacheWritePricePerMillion) {
			continue
		}
		provider := ""
		if len(alias.Targets) > 0 {
			provider = alias.Targets[0].Provider
		}
		putPrice(index, ModelPrice{
			ID:                        alias.Alias,
			DisplayName:               alias.Alias,
			Provider:                  provider,
			InputPricePerMillion:      alias.InputPricePerMillion,
			OutputPricePerMillion:     alias.OutputPricePerMillion,
			CacheReadPricePerMillion:  alias.CacheReadPricePerMillion,
			CacheWritePricePerMillion: alias.CacheWritePricePerMillion,
		}, true)
		for _, target := range alias.Targets {
			putPrice(index, ModelPrice{
				ID:                        target.TargetModel,
				DisplayName:               target.TargetModel,
				Provider:                  target.Provider,
				InputPricePerMillion:      alias.InputPricePerMillion,
				OutputPricePerMillion:     alias.OutputPricePerMillion,
				CacheReadPricePerMillion:  alias.CacheReadPricePerMillion,
				CacheWritePricePerMillion: alias.CacheWritePricePerMillion,
			}, true)
		}
	}
	return uniquePrices(index)
}

func loadPricingState(path string, aliases []AliasMapping) (map[string]ModelPrice, ModelsDevSyncFile, []string, error) {
	file, err := LoadModelPricingFile(path)
	if err == nil {
		return indexPrices(file.Models), file.ModelsDevSync, append([]string(nil), file.DeletedModelIDs...), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, ModelsDevSyncFile{}, nil, err
	}
	rows := seedPricesFromAliases(aliases)
	syncCfg := DefaultModelsDevSyncFile()
	if err := SaveModelPricingFile(path, ModelPricingFile{
		Version:         modelPricingFileVersion,
		ModelsDevSync:   syncCfg,
		Models:          rows,
		DeletedModelIDs: []string{},
	}); err != nil {
		return nil, ModelsDevSyncFile{}, nil, err
	}
	return indexPrices(rows), syncCfg, []string{}, nil
}

func (s *Store) PricingPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pricingPath
}

func (s *Store) PricingCatalogSize() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.prices)
}

func (s *Store) lookupCatalogPrice(modelID, provider string) (ModelPrice, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return lookupPrice(s.prices, modelID, provider)
}

// LookupCatalogSheet returns a price sheet from the standalone catalog.
// Tries modelID first, then fallbackIDs (alias name, target models).
func (s *Store) LookupCatalogSheet(modelID, provider string, fallbackIDs ...string) (PriceSheet, bool) {
	if price, ok := s.lookupCatalogPrice(modelID, provider); ok {
		return price.sheet(), true
	}
	for _, id := range fallbackIDs {
		if strings.TrimSpace(id) == "" || strings.EqualFold(id, modelID) {
			continue
		}
		if price, ok := s.lookupCatalogPrice(id, provider); ok {
			return price.sheet(), true
		}
	}
	return PriceSheet{}, false
}

func (s *Store) PricingSnapshot() []ModelPrice {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return uniquePrices(s.prices)
}

func (s *Store) UpsertModelPrice(price ModelPrice) error {
	price.ID = NormalizeModelIDForPricing(price.ID)
	if price.ID == "" {
		return errors.New("modelId is required")
	}
	if price.InputPricePerMillion < 0 || price.OutputPricePerMillion < 0 ||
		price.CacheReadPricePerMillion < 0 || price.CacheWritePricePerMillion < 0 {
		return errors.New("prices cannot be negative")
	}
	s.mu.Lock()
	next := make(map[string]ModelPrice, len(s.prices)+1)
	for k, v := range s.prices {
		next[k] = v
	}
	putPrice(next, price, true)
	deleted := withoutString(s.pricingDeleted, price.ID)
	path := s.pricingPath
	syncCfg := s.pricingSync
	s.prices = next
	s.pricingDeleted = deleted
	s.mu.Unlock()
	if path == "" {
		return nil
	}
	return s.persistPrices(path, next, syncCfg, deleted)
}

func (s *Store) DeleteModelPrice(modelID string) error {
	id := NormalizeModelIDForPricing(modelID)
	if id == "" {
		return errors.New("modelId is required")
	}
	s.mu.Lock()
	if _, ok := s.prices[id]; !ok {
		s.mu.Unlock()
		return errors.New("model pricing not found")
	}
	next := make(map[string]ModelPrice, len(s.prices))
	for k, v := range s.prices {
		if k != id {
			next[k] = v
		}
	}
	deleted := uniqueSortedStrings(append(append([]string(nil), s.pricingDeleted...), id))
	path := s.pricingPath
	syncCfg := s.pricingSync
	s.prices = next
	s.pricingDeleted = deleted
	s.mu.Unlock()
	if path == "" {
		return nil
	}
	return s.persistPrices(path, next, syncCfg, deleted)
}

func (s *Store) persistPrices(path string, index map[string]ModelPrice, syncCfg ModelsDevSyncFile, deleted []string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if deleted == nil {
		deleted = []string{}
	}
	return SaveModelPricingFile(path, ModelPricingFile{
		Version:         modelPricingFileVersion,
		ModelsDevSync:   syncCfg,
		Models:          uniquePrices(index),
		DeletedModelIDs: deleted,
	})
}

func (s *Store) RecordPricingSyncResult(syncedAt *int64, syncErr string) error {
	s.mu.Lock()
	cfg := s.pricingSync
	if syncedAt != nil {
		cfg.LastSyncAt = syncedAt
	}
	if strings.TrimSpace(syncErr) == "" {
		cfg.LastSyncError = nil
	} else {
		msg := strings.TrimSpace(syncErr)
		cfg.LastSyncError = &msg
	}
	cfg = normalizeSyncFile(cfg)
	path := s.pricingPath
	prices := s.prices
	deleted := append([]string(nil), s.pricingDeleted...)
	s.pricingSync = cfg
	s.mu.Unlock()
	if path == "" {
		return nil
	}
	return s.persistPrices(path, prices, cfg, deleted)
}

func withoutString(values []string, drop string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != drop {
			out = append(out, value)
		}
	}
	return uniqueSortedStrings(out)
}

// ReplaceCatalogPricing replaces the on-disk catalog with prices (cc-switch
// sync overwrites the selected set rather than keeping reseller leftovers).
func (s *Store) ReplaceCatalogPricing(prices []ModelPrice) (int, error) {
	next := make(map[string]ModelPrice, len(prices))
	for _, price := range prices {
		putPrice(next, price, true)
	}
	s.mu.Lock()
	path := s.pricingPath
	syncCfg := s.pricingSync
	deleted := append([]string(nil), s.pricingDeleted...)
	s.prices = next
	s.mu.Unlock()
	if path == "" {
		return len(next), nil
	}
	return len(next), s.persistPrices(path, next, syncCfg, deleted)
}

// MergeCatalogPricing upserts models.dev (or equivalent) rows into the
// standalone catalog. Existing rows for other models are kept. Returns the
// number of unique rows written from this batch.
func (s *Store) MergeCatalogPricing(entries []ModelsDevEntry) (int, error) {
	s.mu.Lock()
	next := make(map[string]ModelPrice, len(s.prices)+len(entries)*2)
	for k, v := range s.prices {
		next[k] = v
	}
	written := 0
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		id := NormalizeModelIDForPricing(entry.ModelID)
		if id == "" || !hasTokenPrices(entry.Input, entry.Output, entry.CacheRead, entry.CacheWrite) {
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = id
		}
		price := ModelPrice{
			ID:                        id,
			DisplayName:               name,
			Provider:                  entry.ProviderID,
			InputPricePerMillion:      entry.Input,
			OutputPricePerMillion:     entry.Output,
			CacheReadPricePerMillion:  entry.CacheRead,
			CacheWritePricePerMillion: entry.CacheWrite,
		}
		before, existed := next[id]
		putPrice(next, price, false)
		if after, ok := next[id]; ok && (!existed || after != before) {
			if _, dup := seen[id]; !dup {
				seen[id] = struct{}{}
				written++
			}
		}
	}
	deleted := s.pricingDeleted
	for id := range seen {
		deleted = withoutString(deleted, id)
	}
	path := s.pricingPath
	syncCfg := s.pricingSync
	s.prices = next
	s.pricingDeleted = deleted
	s.mu.Unlock()
	if path == "" {
		return written, nil
	}
	return written, s.persistPrices(path, next, syncCfg, deleted)
}

// upsertAliasIntoPricing writes one alias's token prices into the catalog
// (alias name + each target model). per_call aliases are ignored.
func (s *Store) upsertAliasIntoPricing(alias AliasMapping) error {
	if strings.EqualFold(alias.BillingMode, "per_call") {
		return nil
	}
	if !hasTokenPrices(alias.InputPricePerMillion, alias.OutputPricePerMillion, alias.CacheReadPricePerMillion, alias.CacheWritePricePerMillion) {
		return nil
	}
	s.mu.Lock()
	next := make(map[string]ModelPrice, len(s.prices)+2)
	for k, v := range s.prices {
		next[k] = v
	}
	provider := ""
	if len(alias.Targets) > 0 {
		provider = alias.Targets[0].Provider
	}
	putPrice(next, ModelPrice{
		ID:                        alias.Alias,
		DisplayName:               alias.Alias,
		Provider:                  provider,
		InputPricePerMillion:      alias.InputPricePerMillion,
		OutputPricePerMillion:     alias.OutputPricePerMillion,
		CacheReadPricePerMillion:  alias.CacheReadPricePerMillion,
		CacheWritePricePerMillion: alias.CacheWritePricePerMillion,
	}, true)
	for _, target := range alias.Targets {
		putPrice(next, ModelPrice{
			ID:                        target.TargetModel,
			DisplayName:               target.TargetModel,
			Provider:                  target.Provider,
			InputPricePerMillion:      alias.InputPricePerMillion,
			OutputPricePerMillion:     alias.OutputPricePerMillion,
			CacheReadPricePerMillion:  alias.CacheReadPricePerMillion,
			CacheWritePricePerMillion: alias.CacheWritePricePerMillion,
		}, true)
	}
	path := s.pricingPath
	syncCfg := s.pricingSync
	deleted := append([]string(nil), s.pricingDeleted...)
	s.prices = next
	s.mu.Unlock()
	if path == "" {
		return nil
	}
	return s.persistPrices(path, next, syncCfg, deleted)
}
