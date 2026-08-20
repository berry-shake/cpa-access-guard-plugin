package plugin

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpa-access-guard/internal/policy"
)

// models.dev pricing auto-sync driver: a background loop in the plugin App
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
	Enabled       bool                      `json:"enabled"`
	IntervalHours int                       `json:"interval_hours"`
	URL           string                    `json:"url,omitempty"`
	PricingFile   string                    `json:"pricing_file,omitempty"`
	CatalogSize   int                       `json:"catalog_size"`
	LastResult    *policy.PricingSyncResult `json:"last_result,omitempty"`
	NextRunAt     time.Time                 `json:"next_run_at,omitempty"`
}

func (a *App) startPricingSyncer(cfg policy.PricingSyncConfig) {
	var old *pricingSyncer
	a.pricingSyncMu.Lock()
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
		Enabled:       true,
		IntervalHours: cfg.IntervalHours,
		URL:           cfg.URL,
		PricingFile:   a.store.PricingPath(),
		CatalogSize:   a.store.PricingCatalogSize(),
	}
	if skipBackgroundPricingSync() {
		return
	}
	interval := defaultPricingSyncInterval
	if cfg.IntervalHours > 0 {
		interval = time.Duration(cfg.IntervalHours) * time.Hour
	}
	url := cfg.URL
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	s := &pricingSyncer{stopCh: stopCh, doneCh: doneCh, app: a}
	a.pricingSyncer = s
	go s.loop(url, interval)
}

func skipBackgroundPricingSync() bool {
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test")
}

func (s *pricingSyncer) stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
}

func (s *pricingSyncer) loop(url string, interval time.Duration) {
	defer close(s.doneCh)
	// Immediate first sync, then on every interval tick.
	s.run(url, interval)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-t.C:
			s.run(url, interval)
		}
	}
}

// run executes one sync and records the outcome (also used by the manual
// management-API trigger).
func (a *App) runPricingSync(url string) policy.PricingSyncResult {
	result := policy.PricingSyncResult{At: time.Now().UTC()}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	entries, err := policy.FetchModelsDevPricing(ctx, url)
	if err != nil {
		result.Error = err.Error()
		_ = a.store.RecordPricingSyncResult(nil, err.Error())
		a.recordPricingSync(result)
		log.Printf("access-guard: pricing sync failed: %v", err)
		return result
	}
	selected := policy.SelectCommonModelsDev(entries)
	result.Updated, result.Unmatched, result.Skipped = a.store.SyncAliasPrices(selected)
	catalog, err := a.store.MergeCatalogPricing(selected)
	if err != nil {
		result.Error = err.Error()
		_ = a.store.RecordPricingSyncResult(nil, err.Error())
		a.recordPricingSync(result)
		log.Printf("access-guard: pricing file write failed: %v", err)
		return result
	}
	now := time.Now().UnixMilli()
	_ = a.store.RecordPricingSyncResult(&now, "")
	result.Catalog = catalog
	result.PricingFile = a.store.PricingPath()
	a.recordPricingSync(result)
	log.Printf("access-guard: pricing sync ok: %d aliases updated, %d unmatched, %d skipped, %d catalog rows (fetched %d)",
		result.Updated, result.Unmatched, result.Skipped, result.Catalog, len(entries))
	return result
}

func (s *pricingSyncer) run(url string, interval time.Duration) {
	a := s.app
	a.runPricingSync(url)
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
	a.pricingSyncMu.Unlock()
}

func (a *App) pricingSyncSnapshot() pricingSyncStatus {
	a.pricingSyncMu.Lock()
	defer a.pricingSyncMu.Unlock()
	status := a.pricingSyncStatus
	status.PricingFile = a.store.PricingPath()
	status.CatalogSize = a.store.PricingCatalogSize()
	return status
}
