package policy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultLiteLLMURL = "https://cdn.jsdelivr.net/gh/BerriAI/litellm@main/model_prices_and_context_window.json"

	PricingSourceManual    = "manual"
	PricingSourceAlias     = "alias"
	PricingSourceLegacy    = "legacy"
	PricingSourceLiteLLM   = "litellm"
	PricingSourceModelsDev = "models.dev"

	PricingStatusPriced        = "priced"
	PricingStatusKnownUnpriced = "known_unpriced"
	PricingStatusStale         = "stale"
	PricingModeText            = "text"
	PricingModeImageGeneration = "image_generation"
	pricingCatalogMaximumBytes = 64 << 20
)

// PricingCatalogEntry is the source-neutral representation produced by both
// remote parsers. Prices are USD per one million tokens.
type PricingCatalogEntry struct {
	Key           string
	ProviderID    string
	ModelID       string
	Name          string
	ReleaseDate   string
	Source        string
	SourceModelID string
	Status        string
	Mode          string
	Input         float64
	Output        float64
	CacheRead     float64
	CacheWrite    float64
	ImageInput    float64
	ImageOutput   float64
}

// ModelsDevEntry remains as an alias for callers and tests written before the
// dual-source sync. New code should use PricingCatalogEntry.
type ModelsDevEntry = PricingCatalogEntry

// PricingFetchResult reports both the raw catalog size and accepted canonical
// entries so management status can distinguish a healthy but filtered source
// from a fetch or decode failure.
type PricingFetchResult struct {
	Entries  []PricingCatalogEntry
	Fetched  int
	Accepted int
}

func fetchPricingPayload(ctx context.Context, rawURL, defaultURL, source string) ([]byte, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = defaultURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// url.Parse errors may quote the original URL, including signed query
		// parameters. Keep operator-facing errors useful without reflecting it.
		return nil, fmt.Errorf("%s: invalid URL", source)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, fmt.Errorf("%s: URL scheme must be http or https", source)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%s: URL host is required", source)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("%s: URL userinfo is not allowed", source)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid request URL", source)
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Do not persist or expose signed query parameters from an operator's
		// custom catalog URL through sync status or management responses.
		publicURL := *parsed
		publicURL.RawQuery = ""
		publicURL.Fragment = ""
		return nil, fmt.Errorf("%s: request failed for %s", source, publicURL.String())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", source, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, pricingCatalogMaximumBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%s: read: %w", source, err)
	}
	if len(body) > pricingCatalogMaximumBytes {
		return nil, fmt.Errorf("%s: response exceeds %d bytes", source, pricingCatalogMaximumBytes)
	}
	return body, nil
}

func validCatalogPrice(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value < 1e12
}

func validateCatalogEntry(entry PricingCatalogEntry) error {
	if NormalizeModelIDForPricing(entry.ModelID) == "" {
		return errors.New("model id is empty")
	}
	for _, value := range []float64{
		entry.Input, entry.Output, entry.CacheRead, entry.CacheWrite,
		entry.ImageInput, entry.ImageOutput,
	} {
		if !validCatalogPrice(value) {
			return errors.New("price is invalid")
		}
	}
	return nil
}

func entryHasTokenPrices(entry PricingCatalogEntry) bool {
	return hasTokenPrices(entry.Input, entry.Output, entry.CacheRead, entry.CacheWrite) ||
		entry.ImageInput > 0 || entry.ImageOutput > 0
}
