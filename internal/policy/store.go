package policy

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Store holds the native key bindings, the usage ledger and the model price
// catalog. Native bindings are persisted in the state file; prices live in a
// separate pricing file.
type Store struct {
	mu        sync.RWMutex
	updateMu  sync.Mutex
	persistMu sync.Mutex
	enabled   bool
	statePath string
	// nativeKeyBindings authorize a CPA-native caller scope to an exact set of
	// auth IDs. They never authenticate the caller themselves.
	nativeKeyBindings        map[string]*NativeKeyBinding
	nativeKeyBindingsByScope map[string]*NativeKeyBinding
	nativeBindingHistory     []nativeBindingHistoryRecord
	limiter                  *RateLimiter
	usage                    *usageLedger
	// flusher periodically persists the usage ledger to the state file.
	flusher *usageFlusher
	// pricingPath / prices is the standalone model-price catalog (USD / 1M
	// tokens) used to bill native-key usage.
	pricingPath                string
	prices                     map[string]ModelPrice
	pricingSync                PricingSyncState
	pricingDeleted             []string
	onNativeKeyBindingsChanged func()
}

func NewStore() *Store {
	return &Store{
		enabled:                  DefaultConfig().Enabled,
		nativeKeyBindings:        make(map[string]*NativeKeyBinding),
		nativeKeyBindingsByScope: make(map[string]*NativeKeyBinding),
		limiter:                  NewRateLimiter(),
		usage:                    newUsageLedger(time.Now),
	}
}

// SetClock injects a clock for testing (limiter + usage windows).
func (s *Store) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	s.mu.Lock()
	s.limiter = NewRateLimiterWithClock(now)
	s.usage = newUsageLedger(now)
	s.mu.Unlock()
}

func (s *Store) Configure(cfg Config) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	if err := normalizeConfig(&cfg); err != nil {
		return err
	}
	statePath, err := ResolveStatePath(cfg.StateFile)
	if err != nil {
		return err
	}
	pricingPath, err := ResolvePricingPath(cfg.PricingFile, statePath)
	if err != nil {
		return err
	}

	// Flush in-memory usage to the *old* state path before loading the
	// (possibly different) new state file, so recent records are not lost.
	s.StopUsageFlusher()
	if err := migrateLegacyDefaultStateFile(cfg.StateFile, statePath); err != nil {
		return err
	}

	nativeBindings := cfg.NativeKeyBindings
	var loadedUsage map[string]*UsageState
	var loadedNativeBindingHistory []nativeBindingHistoryRecord
	// rewrite is set when the state on disk must be replaced right away: first
	// boot, a legacy state without native_key_bindings, recovered auth IDs, a
	// legacy binding without model_access, or usage of removed downstream keys.
	rewrite := false
	if state, errLoad := LoadState(statePath); errLoad == nil {
		loadedUsage = state.Usage
		loadedNativeBindingHistory = cloneNativeBindingHistory(state.NativeBindingHistory)
		if state.NativeKeyBindings != nil {
			nativeBindings = state.NativeKeyBindings
		} else {
			// Legacy states predate native_key_bindings. Bootstrap once from
			// config.yaml, then rewrite the state with an explicit array so a
			// later deletion cannot be resurrected from config.
			rewrite = true
		}
		recoverable := make([]bool, len(nativeBindings))
		for i := range nativeBindings {
			if strings.TrimSpace(nativeBindings[i].ModelAccess.Mode) == "" {
				rewrite = true
			}
			_, recoverable[i] = decodeNativeAuthIDsGroup(nativeBindings[i].Group)
			recoverable[i] = recoverable[i] && len(nativeBindings[i].AuthIDs) == 0
		}
		if errNorm := normalizeNativeKeyBindings(nativeBindings); errNorm != nil {
			return fmt.Errorf("load state: %w", errNorm)
		}
		for i := range nativeBindings {
			if recoverable[i] && len(nativeBindings[i].AuthIDs) > 0 {
				rewrite = true
			}
		}
		// Older releases also billed their own downstream keys. Those accounts
		// can no longer be read or reset, so drop them.
		for id := range loadedUsage {
			if !isNativeUsageLedgerID(id) {
				delete(loadedUsage, id)
				rewrite = true
			}
		}
	} else if !errors.Is(errLoad, os.ErrNotExist) {
		return fmt.Errorf("load state: %w", errLoad)
	} else {
		rewrite = true
	}

	priceMap, syncFile, deletedIDs, errPrice := loadPricingState(pricingPath)
	if errPrice != nil {
		return fmt.Errorf("load pricing file: %w", errPrice)
	}

	now := time.Now().UTC()
	nextNative := make(map[string]*NativeKeyBinding, len(nativeBindings))
	nextNativeByScope := make(map[string]*NativeKeyBinding, len(nativeBindings))
	for i := range nativeBindings {
		item := nativeBindings[i]
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		if item.UpdatedAt.IsZero() {
			item.UpdatedAt = item.CreatedAt
		}
		nextNative[item.ID] = &item
		nextNativeByScope[item.CallerScope] = &item
	}

	s.mu.Lock()
	// Guard against a flusher started after StopUsageFlusher above.
	if s.flusher != nil {
		s.flusher.stop()
		s.flusher = nil
	}
	// The persisted state is authoritative during reconfigure. updateMu
	// serializes this replacement with management mutations, so a deleted
	// binding cannot be resurrected from an older in-memory snapshot.
	s.enabled = cfg.Enabled
	s.statePath = statePath
	s.pricingPath = pricingPath
	s.prices = priceMap
	s.pricingSync = syncFile
	s.pricingDeleted = deletedIDs
	s.nativeKeyBindings = nextNative
	s.nativeKeyBindingsByScope = nextNativeByScope
	s.nativeBindingHistory = loadedNativeBindingHistory
	if s.limiter == nil {
		s.limiter = NewRateLimiter()
	}
	// Re-load usage into the (clock-bound) ledger for restart recovery. The
	// clock is preserved when set via SetClock; otherwise default time.Now.
	s.usage = newUsageLedger(s.usage.now)
	s.usage.loadFromState(loadedUsage)
	s.mu.Unlock()
	if rewrite {
		if errSave := s.saveState(statePath); errSave != nil {
			return fmt.Errorf("seed state: %w", errSave)
		}
	}
	return nil
}

func (s *Store) Enabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

func (s *Store) runtimeComponents() (*RateLimiter, *usageLedger) {
	s.mu.RLock()
	limiter := s.limiter
	usage := s.usage
	s.mu.RUnlock()
	return limiter, usage
}

func (s *Store) StatePath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.statePath
}

// RecordUsage bills one finished request reported by the host's usage.handle
// call. The config-api-key provider reports the plaintext native key, which
// identifies the binding through its caller scope. Requests from keys without
// an enabled binding are not tracked. Returns the billed amount in USD.
func (s *Store) RecordUsage(apiKey, alias, model string, failed bool, detail UsageDetail) float64 {
	if !s.Enabled() {
		return 0
	}
	binding := s.findNativeBindingByScope(NativeCallerScope(apiKey))
	if binding == nil || !binding.Enabled {
		return 0
	}
	return s.recordNativeUsage(*binding, alias, model, failed, detail)
}

// FlushUsage persists the usage ledger when it changed since the last
// successful flush. Called by the background flusher and at lifecycle points
// (reconfigure / shutdown).
func (s *Store) FlushUsage() error {
	// Acquire the persistence lock before sampling usage. Otherwise a flush
	// queued before a quota reset could later restore the cleared counters.
	// Do not acquire updateMu: Configure holds it while stopping the flusher.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	ledger := s.usage
	path := s.statePath
	s.mu.RUnlock()
	if path == "" || ledger == nil {
		return nil
	}
	usage, generation, dirty := ledger.dirtySnapshot()
	if !dirty {
		return nil
	}
	if err := SaveUsageOnly(path, usage); err != nil {
		return err
	}
	ledger.markFlushed(generation)
	return nil
}

func (s *Store) saveState(path string) error {
	// All callers must release s.mu before entering saveState. Taking the
	// binding snapshot here keeps every full-state mutation on the same path
	// without introducing an s.mu -> persistMu / persistMu -> s.mu inversion.
	return s.saveStateWithNativeBindings(path, s.NativeKeyBindingsSnapshot())
}

// saveStateWithNativeBindings writes an explicitly supplied native binding
// snapshot. Native binding mutations use it to persist a proposed policy
// before publishing that policy to live scheduler lookups.
func (s *Store) saveStateWithNativeBindings(path string, nativeBindings []NativeKeyBinding) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	// Capture history after acquiring persistMu so an ordinary state save
	// cannot publish a history snapshot taken before a completed batch save.
	// Callers must release s.mu before entering this method.
	s.mu.RLock()
	history := cloneNativeBindingHistory(s.nativeBindingHistory)
	s.mu.RUnlock()
	return s.writeFullStateLocked(path, nativeBindings, history)
}

// saveStateWithNativeBindingHistory commits proposed bindings and their
// matching history together before either snapshot is published in memory.
// Callers must release s.mu before entering this method.
func (s *Store) saveStateWithNativeBindingHistory(path string, nativeBindings []NativeKeyBinding, history []nativeBindingHistoryRecord) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	return s.writeFullStateLocked(path, nativeBindings, history)
}

// writeFullStateLocked samples the usage ledger and writes it together with
// the given policy. Sampling under persistMu means no flush can land between
// the sample and the write, so a successful write also counts as a flush.
// Caller must hold persistMu and must not hold s.mu.
func (s *Store) writeFullStateLocked(path string, nativeBindings []NativeKeyBinding, history []nativeBindingHistoryRecord) error {
	s.mu.RLock()
	ledger := s.usage
	s.mu.RUnlock()
	var usage map[string]*UsageState
	var generation uint64
	if ledger != nil {
		usage, generation = ledger.snapshotWithGeneration()
	}
	if err := saveStateWithNativeHistory(path, usage, nativeBindings, history); err != nil {
		return err
	}
	if ledger != nil {
		ledger.markFlushed(generation)
	}
	return nil
}

// StartUsageFlusher launches a goroutine that periodically persists the usage
// ledger to the state file. Idempotent. Returns a stop function; the plugin
// host should call it (or FlushUsage) at reconfigure/shutdown.
func (s *Store) StartUsageFlusher() func() {
	s.mu.Lock()
	if s.flusher != nil {
		stop := s.flusher.stop
		s.mu.Unlock()
		return stop
	}
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopCh) }) }
	f := &usageFlusher{stop: stop, stopCh: stopCh, doneCh: doneCh, store: s}
	s.flusher = f
	s.mu.Unlock()
	go f.loop()
	return stop
}

// StopUsageFlusher stops the background flusher and flushes once more.
func (s *Store) StopUsageFlusher() {
	s.mu.Lock()
	f := s.flusher
	s.flusher = nil
	s.mu.Unlock()
	if f != nil {
		f.stop()
		<-f.doneCh
	}
	_ = s.FlushUsage()
}

type usageFlusher struct {
	stop   func()
	stopCh chan struct{}
	doneCh chan struct{}
	store  *Store
}

func (f *usageFlusher) loop() {
	defer close(f.doneCh)
	t := time.NewTicker(usageFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-f.stopCh:
			return
		case <-t.C:
			_ = f.store.FlushUsage()
		}
	}
}

func (s *Store) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	enabledBindings := 0
	for _, binding := range s.nativeKeyBindings {
		if binding.Enabled {
			enabledBindings++
		}
	}
	return map[string]any{
		"enabled":                          s.enabled,
		"state_file":                       s.statePath,
		"pricing_file":                     s.pricingPath,
		"pricing_catalog_size":             len(s.prices),
		"native_key_binding_count":         len(s.nativeKeyBindings),
		"native_key_binding_enabled_count": enabledBindings,
	}
}
