package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// models.dev pricing auto-sync.
//
// The catalog (https://models.dev/api.json) publishes per-provider model
// costs as USD per 1M tokens, including cache_read and cache_write. In the
// dual-source pipeline this catalog is fallback-only and contributes every
// accepted canonical text model that LiteLLM does not know.

// DefaultModelsDevURL is the public pricing catalog endpoint.
const DefaultModelsDevURL = "https://models.dev/api.json"

type modelsDevCost struct {
	Input      *float64 `json:"input"`
	Output     *float64 `json:"output"`
	CacheRead  *float64 `json:"cache_read"`
	CacheWrite *float64 `json:"cache_write"`
}

type modelsDevModel struct {
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	ReleaseDate string         `json:"release_date"`
	Cost        *modelsDevCost `json:"cost"`
	Modalities  struct {
		Output []string `json:"output"`
	} `json:"modalities"`
}

type modelsDevProvider struct {
	Name   string                    `json:"name"`
	Models map[string]modelsDevModel `json:"models"`
}

var modelsDevNonTextMarkers = []string{
	"audio", "deprecated", "embedding", "image", "moderation",
	"realtime", "transcribe", "tts", "video",
}

// NormalizeModelIDForPricing mirrors the cc-switch normalization: keep the
// last "/" segment, drop anything after ":", "@" → "-", lowercase, strip a
// trailing "[1m]" context tag.
func NormalizeModelIDForPricing(modelID string) string {
	if i := strings.LastIndex(modelID, "/"); i >= 0 {
		modelID = modelID[i+1:]
	}
	if i := strings.Index(modelID, ":"); i >= 0 {
		modelID = modelID[:i]
	}
	modelID = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(modelID, "@", "-")))
	modelID = strings.TrimSuffix(modelID, "[1m]")
	return strings.TrimSpace(modelID)
}

func modelsDevIsTextModel(modelID string, m modelsDevModel) bool {
	if strings.EqualFold(strings.TrimSpace(m.Status), "deprecated") {
		return false
	}
	// Output modalities must include text and no non-text output kind.
	if len(m.Modalities.Output) > 0 {
		hasText := false
		for _, modality := range m.Modalities.Output {
			switch strings.ToLower(modality) {
			case "text":
				hasText = true
			case "audio", "image", "video":
				return false
			}
		}
		if !hasText {
			return false
		}
	}
	searchable := strings.ToLower(modelID + " " + m.Name)
	for _, marker := range modelsDevNonTextMarkers {
		if strings.Contains(searchable, marker) {
			return false
		}
	}
	return true
}

// FetchModelsDevCatalog downloads and flattens the fallback catalog into the
// canonical official-family text rows accepted by the plugin.
func FetchModelsDevCatalog(ctx context.Context, rawURL string) (PricingFetchResult, error) {
	body, err := fetchPricingPayload(ctx, rawURL, DefaultModelsDevURL, PricingSourceModelsDev)
	if err != nil {
		return PricingFetchResult{}, err
	}
	var catalog map[string]modelsDevProvider
	if err := json.Unmarshal(body, &catalog); err != nil {
		return PricingFetchResult{}, fmt.Errorf("models.dev: decode: %w", err)
	}
	var entries []PricingCatalogEntry
	fetched := 0
	for providerID, provider := range catalog {
		for modelID, model := range provider.Models {
			fetched++
			if !modelsDevIsTextModel(modelID, model) {
				continue
			}
			cost := model.Cost
			if cost == nil {
				continue
			}
			var input, output float64
			if cost.Input != nil {
				input = *cost.Input
			}
			if cost.Output != nil {
				output = *cost.Output
			}
			if cost.Input == nil && cost.Output == nil {
				continue
			}
			name := strings.TrimSpace(model.Name)
			if name == "" {
				name = modelID
			}
			entry := PricingCatalogEntry{
				Key:           providerID + "/" + modelID,
				ProviderID:    providerID,
				ModelID:       modelID,
				Name:          name,
				ReleaseDate:   strings.TrimSpace(model.ReleaseDate),
				Source:        PricingSourceModelsDev,
				SourceModelID: providerID + "/" + modelID,
				Status:        PricingStatusPriced,
				Mode:          PricingModeText,
				Input:         input,
				Output:        output,
			}
			if cost.CacheRead != nil {
				entry.CacheRead = *cost.CacheRead
			}
			if cost.CacheWrite != nil {
				entry.CacheWrite = *cost.CacheWrite
			}
			if err := validateCatalogEntry(entry); err != nil {
				continue
			}
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ReleaseDate != entries[j].ReleaseDate {
			return entries[i].ReleaseDate > entries[j].ReleaseDate
		}
		return entries[i].Name < entries[j].Name
	})
	selected := SelectCommonModelsDev(entries)
	if len(selected) == 0 {
		return PricingFetchResult{}, fmt.Errorf("models.dev: catalog contained no accepted canonical models")
	}
	return PricingFetchResult{Entries: selected, Fetched: fetched, Accepted: len(selected)}, nil
}

// FetchModelsDevPricing is retained for callers written before the dual-source
// result type was introduced.
func FetchModelsDevPricing(ctx context.Context, rawURL string) ([]ModelsDevEntry, error) {
	result, err := FetchModelsDevCatalog(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	return result.Entries, nil
}

type commonFamilyRule struct {
	providers map[string]struct{}
	match     func(modelID string) bool
}

func newProviderSet(ids ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}

var commonFamilyRules = []commonFamilyRule{
	{providers: newProviderSet("anthropic"), match: func(id string) bool { return strings.HasPrefix(id, "claude-") }},
	{providers: newProviderSet("openai"), match: func(id string) bool {
		return strings.HasPrefix(id, "gpt-") || strings.HasPrefix(id, "o1-") || strings.HasPrefix(id, "o3-") || strings.HasPrefix(id, "o4-")
	}},
	{providers: newProviderSet("google"), match: func(id string) bool { return strings.HasPrefix(id, "gemini-") }},
	{providers: newProviderSet("xai"), match: func(id string) bool { return strings.HasPrefix(id, "grok-") }},
	{providers: newProviderSet("deepseek"), match: func(id string) bool { return strings.HasPrefix(id, "deepseek-") }},
	{providers: newProviderSet("alibaba"), match: func(id string) bool { return strings.HasPrefix(id, "qwen") }},
	{providers: newProviderSet("xiaomi"), match: func(id string) bool { return strings.HasPrefix(id, "mimo-") }},
	{providers: newProviderSet("longcat"), match: func(id string) bool { return strings.HasPrefix(id, "longcat-") }},
	{providers: newProviderSet("moonshotai"), match: func(id string) bool { return strings.HasPrefix(id, "kimi-") }},
	{providers: newProviderSet("minimax-cn"), match: func(id string) bool { return strings.HasPrefix(id, "minimax-m") }},
	{providers: newProviderSet("zai"), match: func(id string) bool { return strings.HasPrefix(id, "glm-") }},
}

func commonModelKeys(entries []ModelsDevEntry) map[string]struct{} {
	wanted := make(map[string]struct{})
	for _, rule := range commonFamilyRules {
		for _, entry := range entries {
			if _, ok := rule.providers[entry.ProviderID]; !ok {
				continue
			}
			if !rule.match(strings.ToLower(entry.ModelID)) {
				continue
			}
			wanted[entry.Key] = struct{}{}
		}
	}
	return wanted
}

// SelectCommonModelsDev keeps every matching official-family text model from
// the canonical provider. The old six-newest cap caused supported Codex models
// to disappear from pricing and is intentionally gone.
func SelectCommonModelsDev(entries []ModelsDevEntry) []ModelsDevEntry {
	wanted := commonModelKeys(entries)
	out := make([]ModelsDevEntry, 0, len(wanted))
	for _, entry := range entries {
		if _, ok := wanted[entry.Key]; ok {
			out = append(out, entry)
		}
	}
	return out
}

// ToModelPrices is cc-switch toModelPricing: first normalized id wins.
func ToModelPrices(entries []ModelsDevEntry) []ModelPrice {
	seen := make(map[string]struct{}, len(entries))
	out := make([]ModelPrice, 0, len(entries))
	for _, entry := range entries {
		id := NormalizeModelIDForPricing(entry.ModelID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			name = id
		}
		out = append(out, ModelPrice{
			ID:                        id,
			DisplayName:               name,
			Provider:                  entry.ProviderID,
			InputPricePerMillion:      entry.Input,
			OutputPricePerMillion:     entry.Output,
			CacheReadPricePerMillion:  entry.CacheRead,
			CacheWritePricePerMillion: entry.CacheWrite,
		})
	}
	return out
}

// pricingProviderAliases maps our alias provider ids to the catalog's
// provider ids for preferential matching.
var pricingProviderAliases = map[string][]string{
	"codex":  {"openai"},
	"claude": {"anthropic"},
	"xai":    {"xai"},
	"gemini": {"google"},
}

// PricingSyncResult reports one sync run.
type PricingSyncResult struct {
	At             time.Time        `json:"at"`
	Updated        int              `json:"updated"`
	Unmatched      int              `json:"unmatched"`
	Skipped        int              `json:"skipped"`
	CatalogUpdated int              `json:"catalog_updated"`
	Catalog        int              `json:"catalog"`
	LiteLLM        int              `json:"litellm"`
	ModelsDev      int              `json:"models_dev"`
	Manual         int              `json:"manual"`
	Legacy         int              `json:"legacy"`
	KnownUnpriced  int              `json:"known_unpriced"`
	Stale          int              `json:"stale"`
	Sources        PricingSyncState `json:"sources"`
	Partial        bool             `json:"partial,omitempty"`
	PricingFile    string           `json:"pricing_file,omitempty"`
	Error          string           `json:"error,omitempty"`
}

// SyncAliasPrices matches every existing tokens-billed alias against the
// catalog and overwrites its price fields. Only aliases whose name (or
// normalized name) matches a catalog entry are touched; per_call aliases are
// skipped. Prices propagate to downstream keys on the next resolve (the
// alias table is the single source of truth).
func (s *Store) SyncAliasPrices(entries []ModelsDevEntry) (updated, unmatched, skipped int) {
	// Index by normalized id, preferring the provider that matches each
	// alias's target provider at lookup time.
	byNormalized := make(map[string][]ModelsDevEntry, len(entries))
	for _, entry := range entries {
		n := NormalizeModelIDForPricing(entry.ModelID)
		if n == "" {
			continue
		}
		byNormalized[n] = append(byNormalized[n], entry)
	}

	aliases := s.AliasesSnapshot()
	for _, alias := range aliases {
		if strings.EqualFold(alias.BillingMode, "per_call") || strings.EqualFold(alias.PricingMode, "manual") {
			skipped++
			continue
		}
		n := NormalizeModelIDForPricing(alias.Alias)
		candidates := byNormalized[n]
		if len(candidates) == 0 {
			// Try the first target's model id when the alias name itself
			// doesn't match (e.g. alias "fast" targeting "gpt-5.6-sol").
			for _, target := range alias.Targets {
				if tn := NormalizeModelIDForPricing(target.TargetModel); tn != "" {
					if candidates = byNormalized[tn]; len(candidates) > 0 {
						break
					}
				}
			}
		}
		if len(candidates) == 0 {
			unmatched++
			continue
		}
		entry := pickPricingEntry(candidates, alias.Targets)
		if entry.Status == PricingStatusKnownUnpriced || entry.Mode == PricingModeImageGeneration {
			// An automatic alias must not retain a formerly published price after
			// the primary source explicitly reports no public text-token price.
			alias.InputPricePerMillion = 0
			alias.OutputPricePerMillion = 0
			alias.CacheReadPricePerMillion = 0
			alias.CacheWritePricePerMillion = 0
		} else {
			alias.InputPricePerMillion = entry.Input
			alias.OutputPricePerMillion = entry.Output
			alias.CacheReadPricePerMillion = entry.CacheRead
			alias.CacheWritePricePerMillion = entry.CacheWrite
		}
		if err := s.upsertAliasFromPricingSync(alias); err != nil {
			skipped++
			continue
		}
		updated++
	}
	return updated, unmatched, skipped
}

// SyncAliasPricesFromCatalog updates auto-priced token aliases from the
// already merged, last-known-good catalog. This prevents a transient primary
// failure from stamping fallback prices over a retained LiteLLM row.
func (s *Store) SyncAliasPricesFromCatalog() (updated, unmatched, skipped int) {
	rows := s.PricingSnapshot()
	entries := make([]PricingCatalogEntry, 0, len(rows))
	for _, row := range rows {
		if row.Status != PricingStatusKnownUnpriced && row.Mode != PricingModeImageGeneration && !row.hasTokenPrices() {
			continue
		}
		entries = append(entries, PricingCatalogEntry{
			Key:           row.Provider + "/" + row.ID,
			ProviderID:    row.Provider,
			ModelID:       row.ID,
			Name:          row.DisplayName,
			Source:        row.Source,
			SourceModelID: row.SourceModelID,
			Status:        row.Status,
			Mode:          row.Mode,
			Input:         row.InputPricePerMillion,
			Output:        row.OutputPricePerMillion,
			CacheRead:     row.CacheReadPricePerMillion,
			CacheWrite:    row.CacheWritePricePerMillion,
		})
	}
	return s.SyncAliasPrices(entries)
}

// pickPricingEntry prefers a catalog entry whose provider matches one of the
// alias's targets (through the alias provider-id mapping); otherwise the
// first candidate.
func pickPricingEntry(candidates []ModelsDevEntry, targets []AliasTarget) ModelsDevEntry {
	if len(candidates) == 1 || len(targets) == 0 {
		return candidates[0]
	}
	wanted := make(map[string]struct{}, 2)
	for _, target := range targets {
		wanted[strings.ToLower(strings.TrimSpace(target.Provider))] = struct{}{}
	}
	for _, candidate := range candidates {
		for aliasProvider, catalogProviders := range pricingProviderAliases {
			if _, ok := wanted[aliasProvider]; !ok {
				continue
			}
			for _, cp := range catalogProviders {
				if strings.EqualFold(candidate.ProviderID, cp) {
					return candidate
				}
			}
		}
	}
	return candidates[0]
}
