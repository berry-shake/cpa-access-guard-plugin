package policy

import (
	"context"
	"encoding/json"
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

// PricingFetchResult reports both the raw catalog size and accepted canonical
// entries so management status can distinguish a healthy but filtered source
// from a fetch or decode failure.
type PricingFetchResult struct {
	Entries  []PricingCatalogEntry
	Fetched  int
	Accepted int
}

// fetchPricingPayload downloads one catalog and hands the response body to
// decode as a stream, so a multi-megabyte catalog is never buffered whole.
// The body is capped at pricingCatalogMaximumBytes. decode errors are reported
// as "<source>: decode: ...".
func fetchPricingPayload(ctx context.Context, rawURL, defaultURL, source string, decode func(io.Reader) error) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		rawURL = defaultURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		// url.Parse errors may quote the original URL, including signed query
		// parameters. Keep operator-facing errors useful without reflecting it.
		return fmt.Errorf("%s: invalid URL", source)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("%s: URL scheme must be http or https", source)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%s: URL host is required", source)
	}
	if parsed.User != nil {
		return fmt.Errorf("%s: URL userinfo is not allowed", source)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return fmt.Errorf("%s: invalid request URL", source)
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
		return fmt.Errorf("%s: request failed for %s", source, publicURL.String())
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: HTTP %d", source, resp.StatusCode)
	}
	body := &cappedReader{r: resp.Body, remaining: pricingCatalogMaximumBytes}
	if err := decode(body); err != nil {
		if body.exceeded {
			return fmt.Errorf("%s: response exceeds %d bytes", source, pricingCatalogMaximumBytes)
		}
		return fmt.Errorf("%s: decode: %w", source, err)
	}
	return nil
}

// cappedReader fails once more than remaining bytes are read, unlike
// io.LimitReader which silently truncates into a confusing decode error.
type cappedReader struct {
	r         io.Reader
	remaining int64
	exceeded  bool
}

var errPricingPayloadTooLarge = errors.New("response too large")

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.remaining <= 0 {
		// Probe for one more byte to distinguish an exact-size body from an
		// oversized one.
		var probe [1]byte
		n, err := c.r.Read(probe[:])
		if n > 0 {
			c.exceeded = true
			return 0, errPricingPayloadTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > c.remaining {
		p = p[:c.remaining]
	}
	n, err := c.r.Read(p)
	c.remaining -= int64(n)
	return n, err
}

// decodeJSONObjectMembers walks a top-level JSON object and calls member for
// every key with the decoder positioned at its value. member must consume
// exactly that value (dec.Decode does).
func decodeJSONObjectMembers(r io.Reader, member func(key string, dec *json.Decoder) error) error {
	dec := json.NewDecoder(r)
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return errors.New("top-level value is not an object")
	}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("object key is not a string")
		}
		if err := member(key, dec); err != nil {
			return err
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	return nil
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
