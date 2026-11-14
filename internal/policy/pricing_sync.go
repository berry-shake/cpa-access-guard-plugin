package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

// modelsDevProviderCount only counts models; providers outside every
// official-family rule are never selected, so their model bodies are skipped.
type modelsDevProviderCount struct {
	Models map[string]struct{} `json:"models"`
}

// FetchModelsDevCatalog downloads and flattens the fallback catalog into the
// canonical official-family text rows accepted by the plugin. The catalog is
// decoded one provider at a time instead of buffering the whole document.
func FetchModelsDevCatalog(ctx context.Context, rawURL string) (PricingFetchResult, error) {
	var entries []PricingCatalogEntry
	fetched := 0
	err := fetchPricingPayload(ctx, rawURL, DefaultModelsDevURL, PricingSourceModelsDev, func(body io.Reader) error {
		return decodeJSONObjectMembers(body, func(providerID string, dec *json.Decoder) error {
			if _, common := commonFamilyProviders[providerID]; !common {
				var counted modelsDevProviderCount
				if err := dec.Decode(&counted); err != nil {
					return err
				}
				fetched += len(counted.Models)
				return nil
			}
			var provider modelsDevProvider
			if err := dec.Decode(&provider); err != nil {
				return err
			}
			entries = appendModelsDevEntries(entries, providerID, provider, &fetched)
			return nil
		})
	})
	if err != nil {
		return PricingFetchResult{}, err
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

func appendModelsDevEntries(entries []PricingCatalogEntry, providerID string, provider modelsDevProvider, fetched *int) []PricingCatalogEntry {
	for modelID, model := range provider.Models {
		*fetched++
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
	return entries
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

// commonFamilyProviders lists every models.dev provider some rule accepts.
var commonFamilyProviders = func() map[string]struct{} {
	out := make(map[string]struct{})
	for _, rule := range commonFamilyRules {
		for id := range rule.providers {
			out[id] = struct{}{}
		}
	}
	return out
}()

func commonModelKeys(entries []PricingCatalogEntry) map[string]struct{} {
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
func SelectCommonModelsDev(entries []PricingCatalogEntry) []PricingCatalogEntry {
	wanted := commonModelKeys(entries)
	out := make([]PricingCatalogEntry, 0, len(wanted))
	for _, entry := range entries {
		if _, ok := wanted[entry.Key]; ok {
			out = append(out, entry)
		}
	}
	return out
}

// PricingSyncResult reports one sync run.
type PricingSyncResult struct {
	At             time.Time        `json:"at"`
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
