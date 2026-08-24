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

// nativeAuthIDsFailClosedGroup keeps direct-auth bindings fail-closed when an
// older plugin release ignores the additive auth_ids field. Custom classify
// groups always carry the "classify:" prefix, while built-in tiers never use
// this reserved value, so the legacy group-only scheduler cannot match it.
const nativeAuthIDsFailClosedGroup = "@access-guard/direct-auth-ids"

// nativeAuthIDsPersistedGroupPrefix redundantly stores exact auth IDs in a
// lowercase-safe encoding. Older releases lowercase Group and discard the
// additive auth_ids JSON field when rewriting state, so the encoded marker
// must survive both operations and remain impossible to match as a real
// scheduler group.
const nativeAuthIDsPersistedGroupPrefix = nativeAuthIDsFailClosedGroup + "/v1/"

var (
	ErrUnknownNativeKeyBinding     = errors.New("unknown native key binding")
	ErrNativeKeyBindingExists      = errors.New("native key binding already exists")
	ErrNativeKeyBindingPersistence = errors.New("persist native key binding")
)

// NativeKeyBinding maps the irreversible identity of a CPA built-in API key
// to either one auth-file group or an exact auth-ID allow-list. It is an
// authorization constraint, not an authentication credential: the original
// API key is intentionally absent.
type NativeKeyBinding struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Enabled     bool     `yaml:"enabled" json:"enabled"`
	CallerScope string   `yaml:"caller_scope" json:"caller_scope"`
	KeyPreview  string   `yaml:"key_preview,omitempty" json:"key_preview,omitempty"`
	Group       string   `yaml:"group" json:"group"`
	AuthIDs     []string `yaml:"auth_ids,omitempty" json:"auth_ids,omitempty"`
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
	AuthIDs   []string `json:"-" yaml:"-"`
	RPM       *int     `json:"-" yaml:"-"`
	DailyUSD  *float64 `json:"-" yaml:"-"`
	WeeklyUSD *float64 `json:"-" yaml:"-"`
}

// UpdateNativeKeyBindingInput replaces the mutable display/policy fields. An
// empty APIKey keeps the existing scope; a non-empty APIKey rotates the
// binding to that native CPA key without persisting the plaintext.
type UpdateNativeKeyBindingInput struct {
	Name      *string   `json:"-" yaml:"-"`
	Enabled   *bool     `json:"-" yaml:"-"`
	APIKey    string    `json:"-" yaml:"-"`
	Group     *string   `json:"-" yaml:"-"`
	AuthIDs   *[]string `json:"-" yaml:"-"`
	RPM       *int      `json:"-" yaml:"-"`
	DailyUSD  *float64  `json:"-" yaml:"-"`
	WeeklyUSD *float64  `json:"-" yaml:"-"`
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
		binding.AuthIDs = normalizeNativeAuthIDs(binding.AuthIDs)
		if len(binding.AuthIDs) == 0 {
			if recovered, ok := decodeNativeAuthIDsGroup(binding.Group); ok {
				binding.AuthIDs = recovered
			}
		}

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
		if len(binding.AuthIDs) > 0 {
			if binding.Group != "" && !isNativeAuthIDsGroup(binding.Group) {
				return fmt.Errorf("native key binding %q: group and auth_ids are mutually exclusive", binding.ID)
			}
			binding.Group = encodeNativeAuthIDsGroup(binding.AuthIDs)
		} else if binding.Group == "" {
			return fmt.Errorf("native key binding %q: group is required when auth_ids is empty", binding.ID)
		}
		if len(binding.AuthIDs) == 0 && strings.HasPrefix(binding.Group, ClassifyGroupPrefix) {
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

func isNativeAuthIDsGroup(group string) bool {
	group = strings.ToLower(strings.TrimSpace(group))
	return group == nativeAuthIDsFailClosedGroup || strings.HasPrefix(group, nativeAuthIDsPersistedGroupPrefix)
}

func encodeNativeAuthIDsGroup(authIDs []string) string {
	parts := make([]string, 0, len(authIDs))
	for _, authID := range normalizeNativeAuthIDs(authIDs) {
		parts = append(parts, hex.EncodeToString([]byte(authID)))
	}
	if len(parts) == 0 {
		return nativeAuthIDsFailClosedGroup
	}
	return nativeAuthIDsPersistedGroupPrefix + strings.Join(parts, ".")
}

func decodeNativeAuthIDsGroup(group string) ([]string, bool) {
	group = strings.ToLower(strings.TrimSpace(group))
	if !strings.HasPrefix(group, nativeAuthIDsPersistedGroupPrefix) {
		return nil, false
	}
	payload := strings.TrimPrefix(group, nativeAuthIDsPersistedGroupPrefix)
	if payload == "" {
		return nil, false
	}
	authIDs := make([]string, 0, strings.Count(payload, ".")+1)
	for _, part := range strings.Split(payload, ".") {
		if part == "" {
			return nil, false
		}
		raw, err := hex.DecodeString(part)
		if err != nil || len(raw) == 0 {
			return nil, false
		}
		authIDs = append(authIDs, string(raw))
	}
	authIDs = normalizeNativeAuthIDs(authIDs)
	return authIDs, len(authIDs) > 0
}

// NativeKeyBindingNeedsReselection reports an older direct-auth marker whose
// exact auth IDs are no longer recoverable. It remains fail-closed, but the
// management API should ask the operator to reselect credentials instead of
// exposing the internal marker as a normal group.
func NativeKeyBindingNeedsReselection(binding NativeKeyBinding) bool {
	return len(binding.AuthIDs) == 0 && isNativeAuthIDsGroup(binding.Group)
}

func normalizeNativeAuthIDs(authIDs []string) []string {
	if len(authIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(authIDs))
	normalized := make([]string, 0, len(authIDs))
	for _, authID := range authIDs {
		authID = strings.TrimSpace(authID)
		if authID == "" {
			continue
		}
		if _, exists := seen[authID]; exists {
			continue
		}
		seen[authID] = struct{}{}
		normalized = append(normalized, authID)
	}
	if len(normalized) == 0 {
		return nil
	}
	sort.Strings(normalized)
	return normalized
}

func cloneNativeKeyBinding(binding NativeKeyBinding) NativeKeyBinding {
	binding.AuthIDs = append([]string(nil), binding.AuthIDs...)
	return binding
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
		AuthIDs:     append([]string(nil), input.AuthIDs...),
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
	if input.Group != nil && input.AuthIDs != nil &&
		strings.TrimSpace(*input.Group) != "" && len(normalizeNativeAuthIDs(*input.AuthIDs)) > 0 {
		return NativeKeyBinding{}, errors.New("group and auth_ids are mutually exclusive")
	}
	if input.AuthIDs != nil && len(normalizeNativeAuthIDs(*input.AuthIDs)) == 0 &&
		(input.Group == nil || strings.TrimSpace(*input.Group) == "") {
		return NativeKeyBinding{}, errors.New("group is required when auth_ids is empty")
	}

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
		if strings.TrimSpace(*input.Group) != "" {
			candidate.AuthIDs = nil
		}
	}
	if input.AuthIDs != nil {
		candidate.AuthIDs = append([]string(nil), (*input.AuthIDs)...)
		if len(normalizeNativeAuthIDs(*input.AuthIDs)) > 0 {
			candidate.Group = ""
		}
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
		bindings = append(bindings, cloneNativeKeyBinding(*binding))
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
		binding := cloneNativeKeyBinding(bindings[i])
		s.nativeKeyBindings[binding.ID] = &binding
		s.nativeKeyBindingsByScope[binding.CallerScope] = &binding
	}
}

// NativeKeyConstraint is the single authorization boundary resolved for one
// enabled native key binding. Exactly one of Group or AuthIDs is populated.
type NativeKeyConstraint struct {
	Group   string
	AuthIDs []string
}

// ResolveNativeKeyConstraint resolves a CPA-provided caller_scope into either
// a group or an exact auth-ID allow-list. provider and model remain reserved for
// future per-route bindings.
func (s *Store) ResolveNativeKeyConstraint(callerScope, provider, model string) (NativeKeyConstraint, bool) {
	_ = provider
	_ = model
	callerScope = strings.ToLower(strings.TrimSpace(callerScope))
	if callerScope == "" {
		return NativeKeyConstraint{}, false
	}
	s.mu.RLock()
	binding := s.nativeKeyBindingsByScope[callerScope]
	if binding == nil || !binding.Enabled {
		s.mu.RUnlock()
		return NativeKeyConstraint{}, false
	}
	constraint := NativeKeyConstraint{
		AuthIDs: append([]string(nil), binding.AuthIDs...),
	}
	if len(constraint.AuthIDs) == 0 {
		constraint.Group = binding.Group
	}
	s.mu.RUnlock()
	return constraint, true
}

// ResolveNativeKeyGroup resolves a CPA-provided caller_scope into the single
// scheduler group authorized for that native key. provider and model are
// reserved for future per-route bindings; the first version intentionally
// applies one group to every request made by the key. The bool is true only
// for an existing, enabled group-mode binding.
func (s *Store) ResolveNativeKeyGroup(callerScope, provider, model string) (string, bool) {
	constraint, ok := s.ResolveNativeKeyConstraint(callerScope, provider, model)
	if !ok || len(constraint.AuthIDs) > 0 {
		return "", false
	}
	return constraint.Group, true
}
