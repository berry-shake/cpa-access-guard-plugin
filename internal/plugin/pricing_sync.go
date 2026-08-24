package plugin

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpa-access-guard/internal/policy"
)

// Dual-source pricing auto-sync driver: a background loop in the plugin App
// that always runs after configure (startup + interval) and stops on
// reconfigure/shutdown, mirroring the usage flusher's lifecycle.

const defaultPricingSyncInterval = 24 * time.Hour

type pricingSyncer struct {
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
	app      *App
}

type pricingSyncStatus struct {
	Enabled         bool                      `json:"enabled"`
	IntervalHours   int                       `json:"interval_hours"`
	URL             string                    `json:"url,omitempty"`
	LiteLLMURL      string                    `json:"litellm_url,omitempty"`
	ModelsDevURL    string                    `json:"models_dev_url,omitempty"`
	PricingFile     string                    `json:"pricing_file,omitempty"`
	CatalogSize     int                       `json:"catalog_size"`
	Sources         policy.PricingSyncState   `json:"sources"`
	LastResult      *policy.PricingSyncResult `json:"last_result,omitempty"`
	NextRunAt       time.Time                 `json:"next_run_at,omitempty"`
	liteLLMRawURL   string
	modelsDevRawURL string
}

func (a *App) startPricingSyncer(cfg policy.PricingSyncConfig) {
	var old *pricingSyncer
	liteURL, modelsDevURL := resolvedPricingURLs(cfg)
	a.pricingSyncMu.Lock()
	if a.pricingSyncer != nil &&
		a.pricingSyncStatus.IntervalHours == cfg.IntervalHours &&
		a.pricingSyncStatus.liteLLMRawURL == liteURL &&
		a.pricingSyncStatus.modelsDevRawURL == modelsDevURL &&
		a.pricingSyncStatus.PricingFile == a.store.PricingPath() {
		// CPA may deliver the same lifecycle configuration more than once at
		// startup. Keep the existing loop so an unchanged config performs one
		// startup download instead of fetching both catalogs repeatedly.
		a.pricingSyncStatus.CatalogSize = a.store.PricingCatalogSize()
		a.pricingSyncStatus.Sources = a.store.PricingSyncStateSnapshot()
		a.pricingSyncMu.Unlock()
		a.store.SyncAliasPricesFromCatalog()
		return
	}
	old = a.pricingSyncer
	a.pricingSyncer = nil
	a.pricingSyncMu.Unlock()
	if old != nil {
		// Release the mutex before waiting: the old loop records status
		// under the same lock.
		old.stop()
		<-old.doneCh
	}

	a.pricingSyncMu.Lock()
	defer a.pricingSyncMu.Unlock()
	a.pricingSyncStatus = pricingSyncStatus{
		Enabled:         true,
		IntervalHours:   cfg.IntervalHours,
		URL:             safePricingURL(modelsDevURL),
		LiteLLMURL:      safePricingURL(liteURL),
		ModelsDevURL:    safePricingURL(modelsDevURL),
		PricingFile:     a.store.PricingPath(),
		CatalogSize:     a.store.PricingCatalogSize(),
		Sources:         a.store.PricingSyncStateSnapshot(),
		liteLLMRawURL:   liteURL,
		modelsDevRawURL: modelsDevURL,
	}
	if skipBackgroundPricingSync() {
		return
	}
	interval := defaultPricingSyncInterval
	if cfg.IntervalHours > 0 {
		interval = time.Duration(cfg.IntervalHours) * time.Hour
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	s := &pricingSyncer{stopCh: stopCh, doneCh: doneCh, app: a}
	a.pricingSyncer = s
	go s.loop(liteURL, modelsDevURL, interval)
}

func resolvedPricingURLs(cfg policy.PricingSyncConfig) (string, string) {
	liteURL := strings.TrimSpace(cfg.LiteLLMURL)
	if liteURL == "" {
		liteURL = policy.DefaultLiteLLMURL
	}
	modelsDevURL := strings.TrimSpace(cfg.ModelsDevURL)
	if modelsDevURL == "" {
		modelsDevURL = strings.TrimSpace(cfg.URL)
	}
	if modelsDevURL == "" {
		modelsDevURL = policy.DefaultModelsDevURL
	}
	return liteURL, modelsDevURL
}

func safePricingURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

func skipBackgroundPricingSync() bool {
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test")
}

func (s *pricingSyncer) stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *pricingSyncer) loop(liteURL, modelsDevURL string, interval time.Duration) {
	defer close(s.doneCh)
	// Immediate first sync, then on every interval tick.
	s.run(liteURL, modelsDevURL, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.run(liteURL, modelsDevURL, interval)
		}
	}
}

// run executes one sync and records the outcome (also used by the manual
// management-API trigger).
func (a *App) runPricingSync(liteURL, modelsDevURL string) policy.PricingSyncResult {
	// A timer tick and an operator-triggered sync may arrive together. Keep one
	// fetch/apply transaction in flight so an older run cannot finish last and
	// overwrite newer source status or last-seen timestamps.
	a.pricingRunMu.Lock()
	defer a.pricingRunMu.Unlock()

	result := policy.PricingSyncResult{At: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	type fetchOutcome struct {
		result policy.PricingFetchResult
		err    error
	}
	liteCh := make(chan fetchOutcome, 1)
	modelsDevCh := make(chan fetchOutcome, 1)
	go func() {
		fetched, err := policy.FetchLiteLLMPricing(ctx, liteURL)
		liteCh <- fetchOutcome{result: fetched, err: err}
	}()
	go func() {
		fetched, err := policy.FetchModelsDevCatalog(ctx, modelsDevURL)
		modelsDevCh <- fetchOutcome{result: fetched, err: err}
	}()
	lite := <-liteCh
	modelsDev := <-modelsDevCh
	applied, err := a.store.ApplyPricingSources(result.At, lite.result, lite.err, modelsDev.result, modelsDev.err)
	if err != nil {
		result.Error = err.Error()
		a.recordPricingSync(result)
		log.Printf("access-guard: pricing file write failed: %v", err)
		return result
	}
	result.CatalogUpdated = applied.Updated
	result.Catalog = applied.Catalog
	result.LiteLLM = applied.LiteLLM
	result.ModelsDev = applied.ModelsDev
	result.Manual = applied.Manual
	result.Legacy = applied.Legacy
	result.KnownUnpriced = applied.KnownUnpriced
	result.Stale = applied.Stale
	result.Sources = a.store.PricingSyncStateSnapshot()
	if lite.err == nil || modelsDev.err == nil {
		result.Updated, result.Unmatched, result.Skipped = a.store.SyncAliasPricesFromCatalog()
	}
	if lite.err != nil || modelsDev.err != nil {
		result.Partial = lite.err == nil || modelsDev.err == nil
	}
	if lite.err != nil && modelsDev.err != nil {
		result.Error = fmt.Sprintf("both pricing sources failed: litellm: %v; models.dev: %v", lite.err, modelsDev.err)
	}
	result.PricingFile = a.store.PricingPath()
	a.recordPricingSync(result)
	if result.Error != "" {
		log.Printf("access-guard: pricing sync failed: %s", result.Error)
	} else {
		log.Printf("access-guard: pricing sync ok: %d aliases updated, %d unmatched, %d skipped, %d catalog rows (%d LiteLLM, %d models.dev fallback, %d manual, %d stale)",
			result.Updated, result.Unmatched, result.Skipped, result.Catalog, result.LiteLLM, result.ModelsDev, result.Manual, result.Stale)
	}
	return result
}

func (s *pricingSyncer) run(liteURL, modelsDevURL string, interval time.Duration) {
	a := s.app
	a.runPricingSync(liteURL, modelsDevURL)
	a.pricingSyncMu.Lock()
	a.pricingSyncStatus.NextRunAt = time.Now().Add(interval)
	a.pricingSyncMu.Unlock()
}

func (a *App) recordPricingSync(result policy.PricingSyncResult) {
	cp := result
	a.pricingSyncMu.Lock()
	a.pricingSyncStatus.LastResult = &cp
	a.pricingSyncStatus.PricingFile = a.store.PricingPath()
	a.pricingSyncStatus.CatalogSize = a.store.PricingCatalogSize()
	a.pricingSyncStatus.Sources = a.store.PricingSyncStateSnapshot()
	a.pricingSyncMu.Unlock()
}

func (a *App) pricingSyncSnapshot() pricingSyncStatus {
	a.pricingSyncMu.Lock()
	defer a.pricingSyncMu.Unlock()
	status := a.pricingSyncStatus
	status.PricingFile = a.store.PricingPath()
	status.CatalogSize = a.store.PricingCatalogSize()
	status.Sources = a.store.PricingSyncStateSnapshot()
	return status
}
