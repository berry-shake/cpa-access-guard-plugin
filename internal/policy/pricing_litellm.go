package policy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
)

type liteLLMModel struct {
	ModelName                   string   `json:"model_name"`
	Provider                    string   `json:"litellm_provider"`
	Mode                        string   `json:"mode"`
	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost"`
	InputCostPerImageToken      *float64 `json:"input_cost_per_image_token"`
	OutputCostPerImageToken     *float64 `json:"output_cost_per_image_token"`
}

var liteLLMCanonicalProviders = map[string]string{
	"openai":      "openai",
	"chatgpt":     "openai",
	"anthropic":   "anthropic",
	"gemini":      "google",
	"google":      "google",
	"xai":         "xai",
	"deepseek":    "deepseek",
	"mistral":     "mistral",
	"cohere":      "cohere",
	"moonshot":    "moonshotai",
	"minimax":     "minimax-cn",
	"zai":         "zai",
	"huggingface": "huggingface",
}

func liteLLMModeAccepted(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "chat", "completion", "responses", "image_generation", "image_edit":
		return true
	default:
		return false
	}
}

func liteLLMMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "image_generation", "image_edit":
		return PricingModeImageGeneration
	default:
		return PricingModeText
	}
}

func millionPrice(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value * 1_000_000
}

type rankedLiteLLMEntry struct {
	entry PricingCatalogEntry
	rank  int
}

// FetchLiteLLMPricing downloads LiteLLM's flat model registry and selects
// canonical direct-provider rows. Marketplace, regional, Azure and Bedrock
// variants stay out of the generic catalog because their prices are not the
// direct provider price used by the corresponding CPA provider.
func FetchLiteLLMPricing(ctx context.Context, rawURL string) (PricingFetchResult, error) {
	selected := make(map[string]rankedLiteLLMEntry)
	fetched := 0
	err := fetchPricingPayload(ctx, rawURL, DefaultLiteLLMURL, PricingSourceLiteLLM, func(body io.Reader) error {
		return decodeJSONObjectMembers(body, func(sourceID string, dec *json.Decoder) error {
			fetched++
			if sourceID == "sample_spec" || sourceID == "fallback_generalizations" || strings.Contains(sourceID, ":") {
				var skipped json.RawMessage
				return dec.Decode(&skipped)
			}
			var model liteLLMModel
			if err := dec.Decode(&model); err != nil {
				// A mistyped field only disqualifies this model. The decoder
				// has already consumed the whole value, so keep streaming.
				var typeErr *json.UnmarshalTypeError
				if errors.As(err, &typeErr) {
					return nil
				}
				return err
			}
			selectLiteLLMEntry(selected, sourceID, model)
			return nil
		})
	})
	if err != nil {
		return PricingFetchResult{}, err
	}
	if len(selected) == 0 {
		return PricingFetchResult{}, errors.New("litellm: catalog contained no accepted canonical models")
	}
	entries := make([]PricingCatalogEntry, 0, len(selected))
	for _, candidate := range selected {
		entries = append(entries, candidate.entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].ProviderID != entries[j].ProviderID {
			return entries[i].ProviderID < entries[j].ProviderID
		}
		return entries[i].ModelID < entries[j].ModelID
	})
	return PricingFetchResult{Entries: entries, Fetched: fetched, Accepted: len(entries)}, nil
}

// selectLiteLLMEntry keeps the best-ranked row per canonical provider/model.
func selectLiteLLMEntry(selected map[string]rankedLiteLLMEntry, sourceID string, model liteLLMModel) {
	provider, ok := liteLLMCanonicalProviders[strings.ToLower(strings.TrimSpace(model.Provider))]
	if !ok || !liteLLMModeAccepted(model.Mode) {
		return
	}
	modelID := NormalizeModelIDForPricing(sourceID)
	if modelID == "" {
		return
	}
	name := strings.TrimSpace(model.ModelName)
	if name == "" {
		name = modelID
	}
	entry := PricingCatalogEntry{
		Key:           provider + "/" + modelID,
		ProviderID:    provider,
		ModelID:       modelID,
		Name:          name,
		Source:        PricingSourceLiteLLM,
		SourceModelID: sourceID,
		Mode:          liteLLMMode(model.Mode),
		Input:         millionPrice(model.InputCostPerToken),
		Output:        millionPrice(model.OutputCostPerToken),
		CacheRead:     millionPrice(model.CacheReadInputTokenCost),
		CacheWrite:    millionPrice(model.CacheCreationInputTokenCost),
		ImageInput:    millionPrice(model.InputCostPerImageToken),
		ImageOutput:   millionPrice(model.OutputCostPerImageToken),
	}
	if err := validateCatalogEntry(entry); err != nil {
		return
	}
	if entryHasTokenPrices(entry) {
		entry.Status = PricingStatusPriced
	} else {
		entry.Status = PricingStatusKnownUnpriced
	}
	// Priced rows beat known-but-unpriced metadata. Direct canonical keys
	// beat provider-prefixed aliases. chatgpt rows are metadata-only unless
	// no priced OpenAI row exists (for example Codex Spark research preview).
	rank := 0
	if entry.Status != PricingStatusPriced {
		rank += 10
	}
	if strings.EqualFold(strings.TrimSpace(model.Provider), "chatgpt") {
		rank += 5
	}
	if strings.Contains(sourceID, "/") {
		rank++
	}
	key := provider + "\x00" + modelID
	if prior, exists := selected[key]; !exists || rank < prior.rank ||
		(rank == prior.rank && sourceID < prior.entry.SourceModelID) {
		selected[key] = rankedLiteLLMEntry{entry: entry, rank: rank}
	}
}
