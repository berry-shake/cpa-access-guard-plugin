package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Standalone model-price catalog persisted next to (or independently of)
// state_file. Dual-source auto-sync writes here; billing falls back to this
// table when an alias has no token prices.

const modelPricingFileVersion = 2

// ModelPrice is one catalog row: USD per 1M tokens for a normalized model id.
// On disk it uses the cc-switch shape (all six fields always present; costs
// as decimal strings; cache write is cacheCreationCostPerMillion).
type ModelPrice struct {
	ID                         string
	DisplayName                string
	Provider                   string
	Source                     string
	SourceModelID              string
	Status                     string
	Mode                       string
	LastSeenAt                 int64
	InputPricePerMillion       float64
	OutputPricePerMillion      float64
	CacheReadPricePerMillion   float64
	CacheWritePricePerMillion  float64
	ImageInputPricePerMillion  float64
	ImageOutputPricePerMillion float64
}

type modelPriceJSON struct {
	ModelID                     string `json:"modelId"`
	DisplayName                 string `json:"displayName"`
	InputCostPerMillion         string `json:"inputCostPerMillion"`
	OutputCostPerMillion        string `json:"outputCostPerMillion"`
	CacheReadCostPerMillion     string `json:"cacheReadCostPerMillion"`
	CacheCreationCostPerMillion string `json:"cacheCreationCostPerMillion"`
	ImageInputCostPerMillion    string `json:"imageInputCostPerMillion,omitempty"`
	ImageOutputCostPerMillion   string `json:"imageOutputCostPerMillion,omitempty"`
	Provider                    string `json:"provider,omitempty"`
	Source                      string `json:"source,omitempty"`
	SourceModelID               string `json:"sourceModelId,omitempty"`
	Status                      string `json:"status,omitempty"`
	Mode                        string `json:"mode,omitempty"`
	LastSeenAt                  int64  `json:"lastSeenAt,omitempty"`
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
		ImageInputCostPerMillion:    optionalPrice(p.ImageInputPricePerMillion),
		ImageOutputCostPerMillion:   optionalPrice(p.ImageOutputPricePerMillion),
		Provider:                    p.Provider,
		Source:                      p.Source,
		SourceModelID:               p.SourceModelID,
		Status:                      p.Status,
		Mode:                        p.Mode,
		LastSeenAt:                  p.LastSeenAt,
	})
}

func optionalPrice(value float64) string {
	if value <= 0 {
		return ""
	}
	return formatPrice(value)
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
	p.ImageInputPricePerMillion = parsePrice(wire.ImageInputCostPerMillion)
	p.ImageOutputPricePerMillion = parsePrice(wire.ImageOutputCostPerMillion)
	p.Provider = strings.TrimSpace(wire.Provider)
	p.Source = strings.TrimSpace(wire.Source)
	p.SourceModelID = strings.TrimSpace(wire.SourceModelID)
	p.Status = strings.TrimSpace(wire.Status)
	p.Mode = strings.TrimSpace(wire.Mode)
	p.LastSeenAt = wire.LastSeenAt
	return nil
}

// ModelsDevSyncFile stores only sync outcome timestamps. Selection is always
// the official-family common set; enable/interval live in plugin YAML.
type ModelsDevSyncFile struct {
	LastSyncAt    *int64  `json:"lastSyncAt"`
	LastSyncError *string `json:"lastSyncError"`
}

type PricingSourceSyncState struct {
	LastAttemptAt *int64  `json:"lastAttemptAt,omitempty"`
	LastSyncAt    *int64  `json:"lastSyncAt,omitempty"`
	LastSyncError *string `json:"lastSyncError,omitempty"`
	Fetched       int     `json:"fetched,omitempty"`
	Accepted      int     `json:"accepted,omitempty"`
}

type PricingSyncState struct {
	LiteLLM   PricingSourceSyncState `json:"litellm"`
	ModelsDev PricingSourceSyncState `json:"modelsDev"`
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

func normalizeSourceSyncState(cfg PricingSourceSyncState) PricingSourceSyncState {
	if cfg.LastSyncError != nil {
		trimmed := strings.TrimSpace(*cfg.LastSyncError)
		if trimmed == "" {
			cfg.LastSyncError = nil
		} else {
			if len(trimmed) > 1000 {
				trimmed = trimmed[:1000]
			}
			cfg.LastSyncError = &trimmed
		}
	}
	if cfg.Fetched < 0 {
		cfg.Fetched = 0
	}
	if cfg.Accepted < 0 {
		cfg.Accepted = 0
	}
	return cfg
}

func normalizePricingSyncState(cfg PricingSyncState) PricingSyncState {
	cfg.LiteLLM = normalizeSourceSyncState(cfg.LiteLLM)
	cfg.ModelsDev = normalizeSourceSyncState(cfg.ModelsDev)
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

func normalizeDeletedModelIDs(values []string) []string {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		if id := NormalizeModelIDForPricing(value); id != "" {
			normalized = append(normalized, id)
		}
	}
	return uniqueSortedStrings(normalized)
}

// ModelPricingFile matches cc-switch ~/.cc-switch/model-pricing.json.
type ModelPricingFile struct {
	Version         int               `json:"version"`
	ModelsDevSync   ModelsDevSyncFile `json:"modelsDevSync"`
	PricingSync     PricingSyncState  `json:"pricingSync,omitempty"`
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

func (p ModelPrice) hasAnyPrices() bool {
	return p.hasTokenPrices() || p.ImageInputPricePerMillion > 0 || p.ImageOutputPricePerMillion > 0
}

func normalizeModelPrice(price ModelPrice) ModelPrice {
	price.ID = NormalizeModelIDForPricing(price.ID)
	price.DisplayName = strings.TrimSpace(price.DisplayName)
	price.Provider = strings.ToLower(strings.TrimSpace(price.Provider))
	price.Source = strings.ToLower(strings.TrimSpace(price.Source))
	price.SourceModelID = strings.TrimSpace(price.SourceModelID)
	price.Status = strings.ToLower(strings.TrimSpace(price.Status))
	price.Mode = strings.ToLower(strings.TrimSpace(price.Mode))
	if price.Source == "" {
		price.Source = PricingSourceLegacy
	}
	if price.Mode == "" {
		price.Mode = PricingModeText
	}
	if price.Status == "" {
		if price.hasAnyPrices() || price.Source == PricingSourceManual || price.Source == PricingSourceAlias || price.Source == PricingSourceLegacy {
			price.Status = PricingStatusPriced
		} else {
			price.Status = PricingStatusKnownUnpriced
		}
	}
	if price.DisplayName == "" {
		price.DisplayName = price.ID
	}
	return price
}

func (p ModelPrice) sheet() PriceSheet {
	return PriceSheet{
		Input:      p.InputPricePerMillion,
		Output:     p.OutputPricePerMillion,
		CacheRead:  p.CacheReadPricePerMillion,
		CacheWrite: p.CacheWritePricePerMillion,
		Priced:     p.Status != PricingStatusKnownUnpriced && p.Mode != PricingModeImageGeneration,
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
	price = normalizeModelPrice(price)
	if price.ID == "" {
		return
	}
	if !force && !price.hasAnyPrices() && price.Status != PricingStatusKnownUnpriced && price.Status != PricingStatusPriced {
		return
	}
	if price.InputPricePerMillion < 0 || price.OutputPricePerMillion < 0 ||
		price.CacheReadPricePerMillion < 0 || price.CacheWritePricePerMillion < 0 ||
		price.ImageInputPricePerMillion < 0 || price.ImageOutputPricePerMillion < 0 {
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
	if existing.Source == PricingSourceManual || existing.Source == PricingSourceAlias || existing.Source == PricingSourceLegacy {
		return
	}
	if existing.Source == PricingSourceLiteLLM && price.Source == PricingSourceModelsDev {
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
	if !ok || price.Status == PricingStatusKnownUnpriced || price.Mode == PricingModeImageGeneration {
		return ModelPrice{}, false
	}
	return price, true
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
		file.Version = 1
	}
	file.ModelsDevSync = normalizeSyncFile(file.ModelsDevSync)
	file.PricingSync = normalizePricingSyncState(file.PricingSync)
	// fork.7 stored only a modelsDevSync summary. Carry it into the fallback
	// source state until the first dual-source attempt records richer data.
	if file.PricingSync.ModelsDev.LastSyncAt == nil && file.ModelsDevSync.LastSyncAt != nil {
		file.PricingSync.ModelsDev.LastSyncAt = file.ModelsDevSync.LastSyncAt
	}
	if file.PricingSync.ModelsDev.LastSyncError == nil && file.ModelsDevSync.LastSyncError != nil {
		file.PricingSync.ModelsDev.LastSyncError = file.ModelsDevSync.LastSyncError
	}
	if file.Models == nil {
		file.Models = []ModelPrice{}
	}
	file.DeletedModelIDs = normalizeDeletedModelIDs(file.DeletedModelIDs)
	deleted := make(map[string]struct{}, len(file.DeletedModelIDs))
	for _, id := range file.DeletedModelIDs {
		deleted[id] = struct{}{}
	}
	kept := file.Models[:0]
	for _, model := range file.Models {
		model = normalizeModelPrice(model)
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
	file.Version = modelPricingFileVersion
	file.ModelsDevSync = normalizeSyncFile(file.ModelsDevSync)
	file.PricingSync = normalizePricingSyncState(file.PricingSync)
	if file.Models == nil {
		file.Models = []ModelPrice{}
	}
	for i := range file.Models {
		file.Models[i] = normalizeModelPrice(file.Models[i])
	}
	file.DeletedModelIDs = normalizeDeletedModelIDs(file.DeletedModelIDs)
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

func loadPricingState(path string) (map[string]ModelPrice, PricingSyncState, []string, error) {
	file, err := LoadModelPricingFile(path)
	if err == nil {
		return indexPrices(file.Models), file.PricingSync, append([]string(nil), file.DeletedModelIDs...), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, PricingSyncState{}, nil, err
	}
	rows := []ModelPrice{}
	syncCfg := PricingSyncState{}
	if err := SaveModelPricingFile(path, ModelPricingFile{
		Version:         modelPricingFileVersion,
		ModelsDevSync:   DefaultModelsDevSyncFile(),
		PricingSync:     syncCfg,
		Models:          rows,
		DeletedModelIDs: []string{},
	}); err != nil {
		return nil, PricingSyncState{}, nil, err
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

func (s *Store) PricingDeletedSnapshot() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.pricingDeleted))
	copy(out, s.pricingDeleted)
	return out
}

func (s *Store) UpsertModelPrice(price ModelPrice) error {
	price.ID = NormalizeModelIDForPricing(price.ID)
	if price.ID == "" {
		return errors.New("modelId is required")
	}
	if price.InputPricePerMillion < 0 || price.OutputPricePerMillion < 0 ||
		price.CacheReadPricePerMillion < 0 || price.CacheWritePricePerMillion < 0 ||
		price.ImageInputPricePerMillion < 0 || price.ImageOutputPricePerMillion < 0 {
		return errors.New("prices cannot be negative")
	}
	price.Source = PricingSourceManual
	price.Status = PricingStatusPriced
	if strings.TrimSpace(price.Mode) == "" {
		price.Mode = PricingModeText
	}
	price.LastSeenAt = time.Now().UnixMilli()
	price = normalizeModelPrice(price)
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.mu.RLock()
	next := make(map[string]ModelPrice, len(s.prices)+1)
	for k, v := range s.prices {
		next[k] = v
	}
	deleted := withoutString(s.pricingDeleted, price.ID)
	path := s.pricingPath
	syncCfg := s.pricingSync
	s.mu.RUnlock()
	putPrice(next, price, true)
	if path != "" {
		if err := s.persistPrices(path, next, syncCfg, deleted); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.prices = next
	s.pricingDeleted = deleted
	s.mu.Unlock()
	return nil
}

func (s *Store) DeleteModelPrice(modelID string) error {
	id := NormalizeModelIDForPricing(modelID)
	if id == "" {
		return errors.New("modelId is required")
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.mu.RLock()
	if _, ok := s.prices[id]; !ok {
		s.mu.RUnlock()
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
	s.mu.RUnlock()
	if path != "" {
		if err := s.persistPrices(path, next, syncCfg, deleted); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.prices = next
	s.pricingDeleted = deleted
	s.mu.Unlock()
	return nil
}

// RestoreModelPriceAutomatic removes a manual/legacy row or deletion tombstone.
// The next source sync can then populate it again. Source-owned rows are also
// removed deliberately so operators can force a clean refresh.
func (s *Store) RestoreModelPriceAutomatic(modelID string) error {
	id := NormalizeModelIDForPricing(modelID)
	if id == "" {
		return errors.New("modelId is required")
	}
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	s.mu.RLock()
	next := make(map[string]ModelPrice, len(s.prices))
	for k, v := range s.prices {
		if k != id {
			next[k] = v
		}
	}
	deleted := withoutString(s.pricingDeleted, id)
	path := s.pricingPath
	syncCfg := s.pricingSync
	s.mu.RUnlock()
	if path != "" {
		if err := s.persistPrices(path, next, syncCfg, deleted); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.prices = next
	s.pricingDeleted = deleted
	s.mu.Unlock()
	return nil
}

func legacyModelsDevSync(syncCfg PricingSyncState) ModelsDevSyncFile {
	state := syncCfg.ModelsDev
	return normalizeSyncFile(ModelsDevSyncFile{
		LastSyncAt:    state.LastSyncAt,
		LastSyncError: state.LastSyncError,
	})
}

func (s *Store) persistPrices(path string, index map[string]ModelPrice, syncCfg PricingSyncState, deleted []string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	if deleted == nil {
		deleted = []string{}
	}
	return SaveModelPricingFile(path, ModelPricingFile{
		Version:         modelPricingFileVersion,
		ModelsDevSync:   legacyModelsDevSync(syncCfg),
		PricingSync:     syncCfg,
		Models:          uniquePrices(index),
		DeletedModelIDs: deleted,
	})
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

type PricingApplyResult struct {
	Updated       int `json:"updated"`
	Catalog       int `json:"catalog"`
	LiteLLM       int `json:"litellm"`
	ModelsDev     int `json:"models_dev"`
	Manual        int `json:"manual"`
	Legacy        int `json:"legacy"`
	KnownUnpriced int `json:"known_unpriced"`
	Stale         int `json:"stale"`
}

func modelPriceFromCatalogEntry(entry PricingCatalogEntry, seenAt int64) ModelPrice {
	status := strings.TrimSpace(entry.Status)
	if status == "" {
		if entryHasTokenPrices(entry) {
			status = PricingStatusPriced
		} else {
			status = PricingStatusKnownUnpriced
		}
	}
	return normalizeModelPrice(ModelPrice{
		ID:                         entry.ModelID,
		DisplayName:                entry.Name,
		Provider:                   entry.ProviderID,
		Source:                     entry.Source,
		SourceModelID:              entry.SourceModelID,
		Status:                     status,
		Mode:                       entry.Mode,
		LastSeenAt:                 seenAt,
		InputPricePerMillion:       entry.Input,
		OutputPricePerMillion:      entry.Output,
		CacheReadPricePerMillion:   entry.CacheRead,
		CacheWritePricePerMillion:  entry.CacheWrite,
		ImageInputPricePerMillion:  entry.ImageInput,
		ImageOutputPricePerMillion: entry.ImageOutput,
	})
}

func catalogPriceIndex(entries []PricingCatalogEntry, seenAt int64) map[string]ModelPrice {
	index := make(map[string]ModelPrice, len(entries))
	for _, entry := range entries {
		price := modelPriceFromCatalogEntry(entry, seenAt)
		if price.ID == "" {
			continue
		}
		putPrice(index, price, false)
	}
	return index
}

func samePriceValues(a, b ModelPrice) bool {
	return a.InputPricePerMillion == b.InputPricePerMillion &&
		a.OutputPricePerMillion == b.OutputPricePerMillion &&
		a.CacheReadPricePerMillion == b.CacheReadPricePerMillion &&
		a.CacheWritePricePerMillion == b.CacheWritePricePerMillion &&
		a.ImageInputPricePerMillion == b.ImageInputPricePerMillion &&
		a.ImageOutputPricePerMillion == b.ImageOutputPricePerMillion
}

func sourceAttemptState(prior PricingSourceSyncState, attemptedAt int64, result PricingFetchResult, sourceErr error) PricingSourceSyncState {
	prior.LastAttemptAt = &attemptedAt
	if sourceErr != nil {
		msg := strings.TrimSpace(sourceErr.Error())
		if len(msg) > 1000 {
			msg = msg[:1000]
		}
		prior.LastSyncError = &msg
		return normalizeSourceSyncState(prior)
	}
	prior.LastSyncAt = &attemptedAt
	prior.LastSyncError = nil
	prior.Fetched = result.Fetched
	prior.Accepted = result.Accepted
	return normalizeSourceSyncState(prior)
}

// ApplyPricingSources atomically publishes one dual-source sync. LiteLLM is
// authoritative. models.dev only fills IDs unknown to LiteLLM. Manual and
// rows (including alias rows from older releases), deletion tombstones, and
// unmatched legacy values are preserved.
// A failed source never replaces its last known rows with the fallback source.
func (s *Store) ApplyPricingSources(at time.Time, lite PricingFetchResult, liteErr error, fallback PricingFetchResult, fallbackErr error) (PricingApplyResult, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	atMillis := at.UTC().UnixMilli()
	liteIndex := catalogPriceIndex(lite.Entries, atMillis)
	fallbackIndex := catalogPriceIndex(fallback.Entries, atMillis)
	effective := make(map[string]ModelPrice, len(liteIndex)+len(fallbackIndex))
	if liteErr == nil {
		for id, price := range liteIndex {
			effective[id] = price
		}
	}
	if fallbackErr == nil {
		for id, price := range fallbackIndex {
			if liteErr == nil {
				if _, knownByPrimary := liteIndex[id]; knownByPrimary {
					continue
				}
			}
			effective[id] = price
		}
	}

	s.mu.RLock()
	next := make(map[string]ModelPrice, len(s.prices)+len(effective))
	for id, price := range s.prices {
		next[id] = price
	}
	deleted := append([]string(nil), s.pricingDeleted...)
	path := s.pricingPath
	syncState := s.pricingSync
	s.mu.RUnlock()
	deletedSet := make(map[string]struct{}, len(deleted))
	for _, id := range deleted {
		deletedSet[NormalizeModelIDForPricing(id)] = struct{}{}
	}
	// Mark source-owned rows stale before applying fresh candidates. They stay
	// usable as last-known-good prices when one or both sources fail.
	for id, current := range next {
		switch current.Source {
		case PricingSourceLiteLLM:
			if liteErr != nil || liteIndex[id].ID == "" {
				current.Status = PricingStatusStale
				next[id] = current
			}
		case PricingSourceModelsDev:
			if fallbackErr != nil || fallbackIndex[id].ID == "" {
				current.Status = PricingStatusStale
				next[id] = current
			}
		}
	}

	updated := 0
	for id, candidate := range effective {
		if _, blocked := deletedSet[id]; blocked {
			continue
		}
		current, exists := next[id]
		if exists {
			switch current.Source {
			case PricingSourceManual, PricingSourceAlias:
				continue
			case PricingSourceLegacy:
				// fork.7 had no provenance. Values matching either upstream are
				// safely recognized as prior auto-sync rows; custom values remain
				// protected as legacy manual overrides.
				matchesPriorSource := false
				if prior, ok := liteIndex[id]; ok && samePriceValues(current, prior) {
					matchesPriorSource = true
				}
				if prior, ok := fallbackIndex[id]; ok && samePriceValues(current, prior) {
					matchesPriorSource = true
				}
				if !matchesPriorSource {
					continue
				}
			case PricingSourceLiteLLM:
				if liteErr != nil {
					continue
				}
			case PricingSourceModelsDev:
				if candidate.Source == PricingSourceModelsDev && fallbackErr != nil {
					continue
				}
			}
		}
		if !exists || next[id] != candidate {
			updated++
		}
		next[id] = candidate
	}

	syncState.LiteLLM = sourceAttemptState(syncState.LiteLLM, atMillis, lite, liteErr)
	syncState.ModelsDev = sourceAttemptState(syncState.ModelsDev, atMillis, fallback, fallbackErr)
	if path != "" {
		if err := s.persistPrices(path, next, syncState, deleted); err != nil {
			return PricingApplyResult{}, err
		}
	}
	s.mu.Lock()
	s.prices = next
	s.pricingSync = syncState
	s.pricingDeleted = deleted
	s.mu.Unlock()

	result := PricingApplyResult{Updated: updated, Catalog: len(next)}
	for _, price := range next {
		switch price.Source {
		case PricingSourceLiteLLM:
			result.LiteLLM++
		case PricingSourceModelsDev:
			result.ModelsDev++
		case PricingSourceManual, PricingSourceAlias:
			result.Manual++
		case PricingSourceLegacy:
			result.Legacy++
		}
		if price.Status == PricingStatusKnownUnpriced {
			result.KnownUnpriced++
		}
		if price.Status == PricingStatusStale {
			result.Stale++
		}
	}
	return result, nil
}

func (s *Store) PricingSyncStateSnapshot() PricingSyncState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pricingSync
}
