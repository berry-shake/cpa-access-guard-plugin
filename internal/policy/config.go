package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultStateFileName       = "cpa-access-guard-state.json"
	legacyDefaultStateFileName = "cpa-key-policy-state.json"
	defaultPricingFileName     = "cpa-access-guard-model-pricing.json"
)

type Config struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	StateFile string `yaml:"state_file" json:"state_file"`
	// PricingFile is the standalone model price catalog (USD per 1M tokens).
	// Empty = sibling of state_file named cpa-access-guard-model-pricing.json.
	// Relative paths resolve against the CPA process working directory, same
	// as state_file.
	PricingFile string `yaml:"pricing_file,omitempty" json:"pricing_file,omitempty"`
	// NativeKeyBindings constrain CPA's built-in downstream API keys by model
	// access and an exact auth-ID allow-list. They do not authenticate the
	// caller and never persist the plaintext API key: CallerScope is the
	// irreversible identity produced by NativeCallerScope.
	NativeKeyBindings []NativeKeyBinding `yaml:"native_key_bindings,omitempty" json:"native_key_bindings,omitempty"`
	// PricingSync controls the dual-source price catalog refresh. LiteLLM is
	// authoritative and models.dev only fills model IDs absent from LiteLLM.
	// Auto-sync always runs at startup and then every IntervalHours (default 24).
	PricingSync PricingSyncConfig `yaml:"pricing_sync,omitempty" json:"pricing_sync,omitempty"`
}

type PricingSyncConfig struct {
	IntervalHours int    `yaml:"interval_hours,omitempty" json:"interval_hours,omitempty"`
	LiteLLMURL    string `yaml:"litellm_url,omitempty" json:"litellm_url,omitempty"`
	ModelsDevURL  string `yaml:"models_dev_url,omitempty" json:"models_dev_url,omitempty"`
	// URL is the legacy fork.7 models.dev override. Keep accepting it so an
	// in-place upgrade never changes a custom fallback endpoint silently.
	URL string `yaml:"url,omitempty" json:"url,omitempty"`
}

// UsageState holds one account's dollar usage in rolling daily / weekly
// windows, persisted in the state JSON. Older releases also kept a per-model
// breakdown under "by_alias"; nothing reads it any more, so it is ignored on
// load and dropped by the next write.
type UsageState struct {
	Daily  UsageWindow `json:"daily"`
	Weekly UsageWindow `json:"weekly"`
	// WeeklyResetAnchor fixes native-key weekly periods to the last successful
	// manual reset. Missing anchors retain the legacy first-use window behavior.
	WeeklyResetAnchor *time.Time `json:"weekly_reset_anchor,omitempty"`
}

// UsageWindow tracks a dollar total bound to a window-start timestamp, plus
// cache-specific counters for reporting (not used for limit enforcement).
//
// Cache fields are reported alongside the daily/weekly usage so the UI can show
// cache spend and hit-rate without re-deriving it. They accumulate only cache
// HITS — cache-creation (write) tokens are intentionally excluded, since their
// pricing and meaning differ across providers and they are not "reads".
//
//   - CacheReadTokens: cache-hit input tokens billed in this window.
//   - CacheCostUSD:    the dollar portion billed at the cache-read price
//     (only when a cache price was explicitly configured; 0
//     when cache hits were folded into the input-price line).
//   - InputTokens:     non-cache input tokens billed in this window, i.e. the
//     prompt tokens charged at the regular input price. For
//     subset providers this is InputTokens - cacheRead; for
//     additive providers it is InputTokens + cacheCreation.
//     Used as the denominator of hit-rate = cacheRead /
//     (cacheRead + InputTokens).
type UsageWindow struct {
	TotalUSD          float64   `json:"total_usd"`
	WindowStart       time.Time `json:"window_start,omitempty"`
	CacheReadTokens   int64     `json:"cache_read_tokens,omitempty"`
	CacheCostUSD      float64   `json:"cache_cost_usd,omitempty"`
	CacheWriteTokens  int64     `json:"cache_write_tokens,omitempty"`
	CacheWriteCostUSD float64   `json:"cache_write_cost_usd,omitempty"`
	InputTokens       int64     `json:"input_tokens,omitempty"`
	// OutputTokens is the non-cache completion-token count billed in this
	// window (tokens charged at the output price). Reported for display on the
	// per-alias detail page; not used for limit enforcement.
	OutputTokens int64 `json:"output_tokens,omitempty"`
	// CallCount is the number of successful requests billed into this window —
	// both token-billed and per-call-billed requests increment it. Failed
	// requests do NOT increment it (a per-call charge only applies to HTTP-200
	// outcomes). Reported for display only; not used for limit enforcement.
	CallCount int64 `json:"call_count,omitempty"`
}

type State struct {
	Version   int                    `json:"version"`
	Usage     map[string]*UsageState `json:"usage,omitempty"`
	UpdatedAt time.Time              `json:"updated_at"`
	// Do not add omitempty here. A missing field identifies a legacy state file
	// and lets Configure bootstrap bindings from config.yaml once, while an
	// explicit [] records that all bindings were deliberately deleted.
	NativeKeyBindings []NativeKeyBinding `json:"native_key_bindings"`
	// NativeBindingHistory records bounded credential-policy changes without
	// restoring usage accounting or unrelated key settings.
	NativeBindingHistory []nativeBindingHistoryRecord `json:"native_binding_history,omitempty"`
}

func DefaultConfig() Config {
	return Config{
		Enabled:   true,
		StateFile: defaultStateFileName,
	}
}

func DecodeConfig(raw []byte) (Config, error) {
	cfg := DefaultConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, err
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = DefaultConfig().StateFile
	}
	if err := normalizeConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalizeConfig(cfg *Config) error {
	return normalizeNativeKeyBindings(cfg.NativeKeyBindings)
}

func ResolveStatePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultConfig().StateFile
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// ResolvePricingPath mirrors ResolveStatePath. An empty requested path places
// the default filename next to the resolved state file so a custom state_file
// directory also holds the price catalog unless pricing_file is set explicitly.
func ResolvePricingPath(path, statePath string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		dir := filepath.Dir(strings.TrimSpace(statePath))
		if dir == "" || dir == "." {
			return ResolveStatePath(defaultPricingFileName)
		}
		return filepath.Join(dir, defaultPricingFileName), nil
	}
	return ResolveStatePath(path)
}

// migrateLegacyDefaultStateFile preserves state for installations that relied
// on the pre-rename default filename. It copies rather than moves the legacy
// file, leaving a recovery backup while all future writes use the new name.
// Explicit custom state_file paths are never considered for this migration.
func migrateLegacyDefaultStateFile(requestedPath, resolvedPath string) error {
	requestedPath = strings.TrimSpace(requestedPath)
	if requestedPath == "" {
		requestedPath = defaultStateFileName
	}
	if requestedPath != defaultStateFileName {
		return nil
	}
	if _, err := os.Stat(resolvedPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect renamed state file: %w", err)
	}

	legacyPath := filepath.Join(filepath.Dir(resolvedPath), legacyDefaultStateFileName)
	if filepath.Clean(legacyPath) == filepath.Clean(resolvedPath) {
		return nil
	}
	raw, err := os.ReadFile(legacyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read legacy state file: %w", err)
	}
	if _, err = LoadState(legacyPath); err != nil {
		return fmt.Errorf("validate legacy state file: %w", err)
	}
	if err = atomicWriteStateFile(resolvedPath, raw); err != nil {
		return fmt.Errorf("copy legacy state file to renamed path: %w", err)
	}
	return nil
}

func LoadState(path string) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state State
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	if err := validateNativeBindingHistory(state.NativeBindingHistory); err != nil {
		return nil, fmt.Errorf("invalid native binding history: %w", err)
	}
	if state.Version == 0 {
		state.Version = 1
	}
	if state.Usage == nil {
		state.Usage = make(map[string]*UsageState)
	}
	return &state, nil
}

// SaveState atomically writes the usage ledger and native bindings to the
// state file, preserving native binding history already on disk.
func SaveState(path string, usage map[string]*UsageState, nativeBindings []NativeKeyBinding) error {
	var history []nativeBindingHistoryRecord
	if current, err := LoadState(path); err == nil {
		history = current.NativeBindingHistory
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return saveStateWithNativeHistory(path, usage, nativeBindings, history)
}

// saveStateWithNativeHistory writes an explicit policy and history snapshot in
// one atomic replacement. The caller serializes concurrent state mutations.
func saveStateWithNativeHistory(path string, usage map[string]*UsageState, nativeBindings []NativeKeyBinding, history []nativeBindingHistoryRecord) error {
	if err := validateNativeBindingHistory(history); err != nil {
		return fmt.Errorf("invalid native binding history: %w", err)
	}
	// Always persist an explicit array, including when empty. This preserves the
	// distinction between an old state file with no native_key_bindings field
	// and a new state in which the user deliberately deleted every binding.
	cleanBindings := make([]NativeKeyBinding, 0, len(nativeBindings))
	cleanBindings = append(cleanBindings, nativeBindings...)
	return writeState(path, State{Usage: usage, NativeKeyBindings: cleanBindings, NativeBindingHistory: history})
}

// SaveUsageOnly atomically replaces the usage ledger in the state file and
// keeps the bindings and history already on disk. Binding mutations persist
// synchronously through SaveState, so the periodic usage flush never writes a
// policy snapshot of its own.
func SaveUsageOnly(path string, usage map[string]*UsageState) error {
	var history []nativeBindingHistoryRecord
	// A brand-new state gets an explicit [], while a legacy state that exists
	// but omits the field keeps nil so Configure can detect and migrate it.
	nativeBindings := make([]NativeKeyBinding, 0)
	if cur, err := LoadState(path); err == nil {
		nativeBindings = cur.NativeKeyBindings
		history = cur.NativeBindingHistory
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeState(path, State{Usage: usage, NativeKeyBindings: nativeBindings, NativeBindingHistory: history})
}

func writeState(path string, state State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	state.Version = 1
	state.UpdatedAt = time.Now().UTC()
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteStateFile(path, raw)
}

func atomicWriteStateFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(raw); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}
