package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const nativeCallerScopeDomain = "cli-proxy-api:caller-scope:v1\x00"

var (
	ErrUnknownNativeKeyBinding     = errors.New("unknown native key binding")
	ErrNativeKeyBindingExists      = errors.New("native key binding already exists")
	ErrNativeKeyBindingPersistence = errors.New("persist native key binding")
)

// NativeKeyBinding maps the irreversible identity of a CPA built-in API key
// to one auth-file group. It is an authorization constraint, not an
// authentication credential: the original API key is intentionally absent.
type NativeKeyBinding struct {
	ID          string `yaml:"id" json:"id"`
	Name        string `yaml:"name" json:"name"`
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	CallerScope string `yaml:"caller_scope" json:"caller_scope"`
	KeyPreview  string `yaml:"key_preview,omitempty" json:"key_preview,omitempty"`
	Group       string `yaml:"group" json:"group"`
	// Optional usage limits, same semantics as the downstream KeyConfig
	// equivalents: 0 = unlimited. USD usage is priced from the standalone
	// model-pricing JSON, not alias mappings.
	RPM       int       `yaml:"rpm,omitempty" json:"rpm,omitempty"`
	DailyUSD  float64   `yaml:"daily_usd,omitempty" json:"daily_usd,omitempty"`
	WeeklyUSD float64   `yaml:"weekly_usd,omitempty" json:"weekly_usd,omitempty"`
	CreatedAt time.Time `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	UpdatedAt time.Time `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
}

// CreateNativeKeyBindingInput is deliberately separate from the persisted
// NativeKeyBinding. APIKey is consumed once to derive CallerScope and
// KeyPreview and can never be serialized accidentally.
type CreateNativeKeyBindingInput struct {
	ID        string   `json:"-" yaml:"-"`
	Name      string   `json:"-" yaml:"-"`
	Enabled   bool     `json:"-" yaml:"-"`
	APIKey    string   `json:"-" yaml:"-"`
	Group     string   `json:"-" yaml:"-"`
	RPM       *int     `json:"-" yaml:"-"`
	DailyUSD  *float64 `json:"-" yaml:"-"`
	WeeklyUSD *float64 `json:"-" yaml:"-"`
}

// UpdateNativeKeyBindingInput replaces the mutable display/policy fields. An
// empty APIKey keeps the existing scope; a non-empty APIKey rotates the
// binding to that native CPA key without persisting the plaintext.
type UpdateNativeKeyBindingInput struct {
	Name      *string  `json:"-" yaml:"-"`
	Enabled   *bool    `json:"-" yaml:"-"`
	APIKey    string   `json:"-" yaml:"-"`
	Group     *string  `json:"-" yaml:"-"`
	RPM       *int     `json:"-" yaml:"-"`
	DailyUSD  *float64 `json:"-" yaml:"-"`
	WeeklyUSD *float64 `json:"-" yaml:"-"`
}

// NativeCallerScope mirrors CLIProxyAPI's session.CallerScope exactly. The
// input is trimmed, domain-separated, and SHA-256 hashed. Empty input returns
// an empty string, matching the host implementation.
func NativeCallerScope(rawAPIKey string) string {
	rawAPIKey = strings.TrimSpace(rawAPIKey)
	if rawAPIKey == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(nativeCallerScopeDomain + rawAPIKey))
	return hex.EncodeToString(sum[:])
}

// NativeKeyPreview gives operators enough context to identify a normal API
// key without ever exposing a short key verbatim.
func NativeKeyPreview(rawAPIKey string) string {
	rawAPIKey = strings.TrimSpace(rawAPIKey)
	if rawAPIKey == "" {
		return ""
	}
	if len(rawAPIKey) <= 12 {
		return "<redacted>"
	}
	return PreviewKey(rawAPIKey)
}

func normalizeNativeKeyBindings(bindings []NativeKeyBinding) error {
	ids := make(map[string]struct{}, len(bindings))
	scopes := make(map[string]string, len(bindings))
	for i := range bindings {
		binding := &bindings[i]
		binding.ID = strings.ToLower(strings.TrimSpace(binding.ID))
		binding.Name = strings.TrimSpace(binding.Name)
		binding.CallerScope = strings.ToLower(strings.TrimSpace(binding.CallerScope))
		binding.KeyPreview = strings.TrimSpace(binding.KeyPreview)
		binding.Group = strings.ToLower(strings.TrimSpace(binding.Group))

		if binding.ID == "" {
			return fmt.Errorf("native key binding %d: id is required", i)
		}
		if _, exists := ids[binding.ID]; exists {
			return fmt.Errorf("duplicate native key binding id %q", binding.ID)
		}
		ids[binding.ID] = struct{}{}
		if binding.Name == "" {
			binding.Name = binding.ID
		}
		if err := validateNativeCallerScope(binding.CallerScope); err != nil {
			return fmt.Errorf("native key binding %q: %w", binding.ID, err)
		}
		if priorID, exists := scopes[binding.CallerScope]; exists {
			return fmt.Errorf("native key bindings %q and %q have the same caller_scope", priorID, binding.ID)
		}
		scopes[binding.CallerScope] = binding.ID
		if binding.Group == "" {
			return fmt.Errorf("native key binding %q: group is required", binding.ID)
		}
		if strings.HasPrefix(binding.Group, ClassifyGroupPrefix) {
			suffix := strings.TrimSpace(strings.TrimPrefix(binding.Group, ClassifyGroupPrefix))
			if suffix == "" {
				return fmt.Errorf("native key binding %q: classify group suffix is required", binding.ID)
			}
			binding.Group = ClassifyGroupPrefix + suffix
		}
		if binding.RPM < 0 {
			return fmt.Errorf("native key binding %q: rpm must be >= 0", binding.ID)
		}
		if binding.DailyUSD < 0 {
			return fmt.Errorf("native key binding %q: daily_usd must be >= 0", binding.ID)
		}
		if binding.WeeklyUSD < 0 {
			return fmt.Errorf("native key binding %q: weekly_usd must be >= 0", binding.ID)
		}
	}
	return nil
}

func validateNativeCallerScope(scope string) error {
	if scope == "" {
		return errors.New("caller_scope is required")
	}
	if len(scope) != sha256.Size*2 {
		return fmt.Errorf("caller_scope must be a %d-character SHA-256 hex string", sha256.Size*2)
	}
	if _, err := hex.DecodeString(scope); err != nil {
		return errors.New("caller_scope must be a SHA-256 hex string")
	}
	return nil
}

// CreateNativeKeyBinding creates and synchronously persists a binding. The
// plaintext APIKey is required for creation and is discarded after deriving
// the host-compatible scope and a redacted preview.
func (s *Store) CreateNativeKeyBinding(input CreateNativeKeyBindingInput) (NativeKeyBinding, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	rawAPIKey := strings.TrimSpace(input.APIKey)
	callerScope := NativeCallerScope(rawAPIKey)
	if callerScope == "" {
		return NativeKeyBinding{}, errors.New("api key is required")
	}
	now := time.Now().UTC()
	candidate := NativeKeyBinding{
		ID:          input.ID,
		Name:        input.Name,
		Enabled:     input.Enabled,
		CallerScope: callerScope,
		KeyPreview:  NativeKeyPreview(rawAPIKey),
		Group:       input.Group,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if input.RPM != nil {
		candidate.RPM = *input.RPM
	}
	if input.DailyUSD != nil {
		candidate.DailyUSD = *input.DailyUSD
	}
	if input.WeeklyUSD != nil {
		candidate.WeeklyUSD = *input.WeeklyUSD
	}

	s.mu.RLock()
	existing := s.nativeKeyBindingsSnapshotLocked()
	_, idExists := s.nativeKeyBindings[strings.ToLower(strings.TrimSpace(input.ID))]
	keys := s.keysSnapshotLocked()
	usage := s.usageSnapshotLocked()
	aliases := s.aliasesSnapshotLocked()
	rules := s.classifyRulesSnapshotLocked()
	path := s.statePath
	s.mu.RUnlock()
	if idExists {
		return NativeKeyBinding{}, fmt.Errorf("%w: %s", ErrNativeKeyBindingExists, strings.TrimSpace(input.ID))
	}
	existing = append(existing, candidate)
	if err := normalizeNativeKeyBindings(existing); err != nil {
		return NativeKeyBinding{}, err
	}
	candidate = existing[len(existing)-1]

	// Persist before publishing the new authorization policy. A failed disk
	// write must not leave a binding active only in memory.
	if err := s.saveStateWithNativeBindings(path, keys, usage, aliases, rules, existing); err != nil {
		return NativeKeyBinding{}, fmt.Errorf("%w: %w", ErrNativeKeyBindingPersistence, err)
	}
	s.mu.Lock()
	s.replaceNativeKeyBindingsLocked(existing)
	s.mu.Unlock()
	return candidate, nil
}

// UpdateNativeKeyBinding replaces a binding's mutable policy fields and
// synchronously persists it. Supplying APIKey rotates CallerScope and
// KeyPreview; leaving APIKey empty retains both existing values.
func (s *Store) UpdateNativeKeyBinding(id string, input UpdateNativeKeyBindingInput) (NativeKeyBinding, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return NativeKeyBinding{}, errors.New("id is required")
	}
	s.mu.RLock()
	existing := s.nativeKeyBindingsSnapshotLocked()
	keys := s.keysSnapshotLocked()
	usage := s.usageSnapshotLocked()
	aliases := s.aliasesSnapshotLocked()
	rules := s.classifyRulesSnapshotLocked()
	path := s.statePath
	s.mu.RUnlock()
	index := -1
	for i := range existing {
		if existing[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return NativeKeyBinding{}, ErrUnknownNativeKeyBinding
	}

	candidate := existing[index]
	if input.Name != nil {
		candidate.Name = *input.Name
	}
	if input.Enabled != nil {
		candidate.Enabled = *input.Enabled
	}
	if input.Group != nil {
		candidate.Group = *input.Group
	}
	if input.RPM != nil {
		candidate.RPM = *input.RPM
	}
	if input.DailyUSD != nil {
		candidate.DailyUSD = *input.DailyUSD
	}
	if input.WeeklyUSD != nil {
		candidate.WeeklyUSD = *input.WeeklyUSD
	}
	if rawAPIKey := strings.TrimSpace(input.APIKey); rawAPIKey != "" {
		candidate.CallerScope = NativeCallerScope(rawAPIKey)
		candidate.KeyPreview = NativeKeyPreview(rawAPIKey)
	}
	candidate.UpdatedAt = time.Now().UTC()
	existing[index] = candidate
	if err := normalizeNativeKeyBindings(existing); err != nil {
		return NativeKeyBinding{}, err
	}
	candidate = existing[index]

	// Persist before replacing the live lookup indexes. This also makes a
	// failed disable/group/key rotation leave the last durable policy active.
	if err := s.saveStateWithNativeBindings(path, keys, usage, aliases, rules, existing); err != nil {
		return NativeKeyBinding{}, fmt.Errorf("%w: %w", ErrNativeKeyBindingPersistence, err)
	}
	s.mu.Lock()
	s.replaceNativeKeyBindingsLocked(existing)
	s.mu.Unlock()
	return candidate, nil
}

// DeleteNativeKeyBinding removes and synchronously persists a binding.
func (s *Store) DeleteNativeKeyBinding(id string) error {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()

	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return errors.New("id is required")
	}
	s.mu.RLock()
	if _, exists := s.nativeKeyBindings[id]; !exists {
		s.mu.RUnlock()
		return ErrUnknownNativeKeyBinding
	}
	existing := s.nativeKeyBindingsSnapshotLocked()
	keys := s.keysSnapshotLocked()
	usage := s.usageSnapshotLocked()
	aliases := s.aliasesSnapshotLocked()
	rules := s.classifyRulesSnapshotLocked()
	path := s.statePath
	s.mu.RUnlock()
	next := make([]NativeKeyBinding, 0, len(existing)-1)
	for _, binding := range existing {
		if binding.ID != id {
			next = append(next, binding)
		}
	}
	// Keep the old restriction live until its removal is durable. This is
	// especially important because an in-memory-only delete would fail open.
	if err := s.saveStateWithNativeBindings(path, keys, usage, aliases, rules, next); err != nil {
		return fmt.Errorf("%w: %w", ErrNativeKeyBindingPersistence, err)
	}
	s.mu.Lock()
	s.replaceNativeKeyBindingsLocked(next)
	s.mu.Unlock()
	return nil
}

// NativeKeyBindingsSnapshot returns a stable ID-sorted copy for management
// and persistence callers.
func (s *Store) NativeKeyBindingsSnapshot() []NativeKeyBinding {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nativeKeyBindingsSnapshotLocked()
}

// nativeKeyBindingsSnapshotLocked returns a stable ID-sorted copy. Caller
// must hold s.mu for reading or writing.
func (s *Store) nativeKeyBindingsSnapshotLocked() []NativeKeyBinding {
	bindings := make([]NativeKeyBinding, 0, len(s.nativeKeyBindings))
	for _, binding := range s.nativeKeyBindings {
		bindings = append(bindings, *binding)
	}
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].ID < bindings[j].ID })
	return bindings
}

// rebuildNativeKeyBindingsByScopeLocked rebuilds the scheduler lookup index.
// Caller must hold s.mu for writing.
func (s *Store) rebuildNativeKeyBindingsByScopeLocked() {
	s.nativeKeyBindingsByScope = make(map[string]*NativeKeyBinding, len(s.nativeKeyBindings))
	for _, binding := range s.nativeKeyBindings {
		s.nativeKeyBindingsByScope[binding.CallerScope] = binding
	}
}

// replaceNativeKeyBindingsLocked atomically rebuilds both native binding
// indexes from a normalized snapshot. Caller must hold s.mu for writing.
func (s *Store) replaceNativeKeyBindingsLocked(bindings []NativeKeyBinding) {
	s.nativeKeyBindings = make(map[string]*NativeKeyBinding, len(bindings))
	s.nativeKeyBindingsByScope = make(map[string]*NativeKeyBinding, len(bindings))
	for i := range bindings {
		binding := bindings[i]
		s.nativeKeyBindings[binding.ID] = &binding
		s.nativeKeyBindingsByScope[binding.CallerScope] = &binding
	}
}

// ResolveNativeKeyGroup resolves a CPA-provided caller_scope into the single
// scheduler group authorized for that native key. provider and model are
// reserved for future per-route bindings; the first version intentionally
// applies one group to every request made by the key. The bool is true only
// for an existing, enabled binding.
func (s *Store) ResolveNativeKeyGroup(callerScope, provider, model string) (string, bool) {
	_ = provider
	_ = model
	callerScope = strings.ToLower(strings.TrimSpace(callerScope))
	if callerScope == "" {
		return "", false
	}
	s.mu.RLock()
	binding := s.nativeKeyBindingsByScope[callerScope]
	if binding == nil || !binding.Enabled {
		s.mu.RUnlock()
		return "", false
	}
	group := binding.Group
	s.mu.RUnlock()
	return group, true
}
