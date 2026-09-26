package policy

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	nativeBindingHistoryLimit = 50
	nativeBindingHistoryBytes = 4 << 20
	nativeBindingInputBytes   = 2 << 20
)

var (
	ErrNativeBindingRevisionMismatch = errors.New("native binding preview is stale")
	ErrNativeBindingBatchConflict    = errors.New("native binding operation conflicts with current state")
	ErrUnknownNativeBindingOperation = errors.New("unknown native binding operation")
	ErrInvalidNativeBindingBatch     = errors.New("invalid native binding operation")
)

type NativeBindingBatchInput struct {
	APIKeys          []string `json:"-" yaml:"-"`
	SelectedIndices  []int    `json:"-" yaml:"-"`
	AuthIDs          []string `json:"-" yaml:"-"`
	AvailableAuthIDs []string `json:"-" yaml:"-"`
	CatalogComplete  bool     `json:"-" yaml:"-"`
	ExpectedRevision string   `json:"-" yaml:"-"`
}

type NativeBindingRollbackInput struct {
	OperationID      string   `json:"-" yaml:"-"`
	APIKeys          []string `json:"-" yaml:"-"`
	AvailableAuthIDs []string `json:"-" yaml:"-"`
	CatalogComplete  bool     `json:"-" yaml:"-"`
	ExpectedRevision string   `json:"-" yaml:"-"`
}

type NativeBindingRestriction struct {
	Group   string   `json:"group,omitempty"`
	AuthIDs []string `json:"auth_ids,omitempty"`
}

type NativeBindingChange struct {
	BindingID  string                    `json:"binding_id"`
	Name       string                    `json:"name"`
	KeyPreview string                    `json:"key_preview"`
	Before     *NativeBindingRestriction `json:"before"`
	After      *NativeBindingRestriction `json:"after"`
}

type NativeBindingConflict struct {
	BindingID string `json:"binding_id"`
	Code      string `json:"code"`
}

type NativeBindingPreview struct {
	Revision  string                  `json:"revision"`
	Changes   []NativeBindingChange   `json:"changes"`
	Conflicts []NativeBindingConflict `json:"conflicts"`
	CanApply  bool                    `json:"can_apply"`
	Noop      bool                    `json:"noop"`
	Warnings  []string                `json:"warnings"`
}

type NativeBindingOperation struct {
	ID                string                `json:"id"`
	Kind              string                `json:"kind"`
	SourceOperationID string                `json:"source_operation_id,omitempty"`
	RevertedBy        string                `json:"reverted_by,omitempty"`
	CreatedAt         time.Time             `json:"created_at"`
	Changes           []NativeBindingChange `json:"changes"`
}

type NativeBindingMutationResult struct {
	Operation *NativeBindingOperation `json:"operation,omitempty"`
	Changed   int                     `json:"changed"`
	Noop      bool                    `json:"noop"`
}

type NativeBindingConflictError struct {
	Conflicts []NativeBindingConflict
}

func (e *NativeBindingConflictError) Error() string { return ErrNativeBindingBatchConflict.Error() }
func (e *NativeBindingConflictError) Unwrap() error { return ErrNativeBindingBatchConflict }

// History contains irreversible identities and policy templates, never API keys.
// Templates exist only for bindings created or removed by these operations.
// Public responses are projected separately so persistence fields cannot leak.
type nativeBindingRecordedChange struct {
	NativeBindingChange
	CallerScope       string            `json:"caller_scope"`
	BeforeFingerprint string            `json:"before_fingerprint,omitempty"`
	AfterFingerprint  string            `json:"after_fingerprint,omitempty"`
	BeforeCreatedAt   time.Time         `json:"before_created_at,omitempty"`
	AfterCreatedAt    time.Time         `json:"after_created_at,omitempty"`
	BeforeTemplate    *NativeKeyBinding `json:"before_template,omitempty"`
	AfterTemplate     *NativeKeyBinding `json:"after_template,omitempty"`
}

type nativeBindingHistoryRecord struct {
	ID                string                        `json:"id"`
	Kind              string                        `json:"kind"`
	SourceOperationID string                        `json:"source_operation_id,omitempty"`
	RevertedBy        string                        `json:"reverted_by,omitempty"`
	CreatedAt         time.Time                     `json:"created_at"`
	Changes           []nativeBindingRecordedChange `json:"changes"`
	Markers           []nativeBindingHistoryMarker  `json:"markers,omitempty"`
}

type nativeBindingHistoryMarker struct {
	OperationID string `json:"operation_id"`
	Before      string `json:"before"`
	After       string `json:"after"`
}

type nativeBindingBatchSnapshot struct {
	bindings []NativeKeyBinding
	history  []nativeBindingHistoryRecord
	rules    []ClassifyRule
}

type nativeBindingPlan struct {
	preview  NativeBindingPreview
	bindings []NativeKeyBinding
	changes  []nativeBindingRecordedChange
	source   string
	kind     string
}

func cloneNativeBindingRestriction(value *NativeBindingRestriction) *NativeBindingRestriction {
	if value == nil {
		return nil
	}
	return &NativeBindingRestriction{Group: value.Group, AuthIDs: append([]string(nil), value.AuthIDs...)}
}

func cloneNativeBindingChange(value NativeBindingChange) NativeBindingChange {
	value.Before = cloneNativeBindingRestriction(value.Before)
	value.After = cloneNativeBindingRestriction(value.After)
	return value
}

func cloneNativeBindingHistory(history []nativeBindingHistoryRecord) []nativeBindingHistoryRecord {
	out := make([]nativeBindingHistoryRecord, len(history))
	for i, record := range history {
		out[i] = record
		out[i].Markers = append([]nativeBindingHistoryMarker(nil), record.Markers...)
		out[i].Changes = make([]nativeBindingRecordedChange, len(record.Changes))
		for j, change := range record.Changes {
			out[i].Changes[j] = change
			out[i].Changes[j].NativeBindingChange = cloneNativeBindingChange(change.NativeBindingChange)
			if change.BeforeTemplate != nil {
				cloned := cloneNativeKeyBinding(*change.BeforeTemplate)
				out[i].Changes[j].BeforeTemplate = &cloned
			}
			if change.AfterTemplate != nil {
				cloned := cloneNativeKeyBinding(*change.AfterTemplate)
				out[i].Changes[j].AfterTemplate = &cloned
			}
		}
	}
	return out
}

func validateNativeBindingHistory(history []nativeBindingHistoryRecord) error {
	if len(history) > nativeBindingHistoryLimit {
		return errors.New("native binding history exceeds operation limit")
	}
	seen := make(map[string]bool, len(history))
	for _, record := range history {
		if record.ID == "" || len(record.ID) > 256 || seen[record.ID] || (record.Kind != "batch" && record.Kind != "rollback") || record.CreatedAt.IsZero() || len(record.Changes) == 0 || len(record.Changes) > 4096 {
			return errors.New("invalid native binding history record")
		}
		seen[record.ID] = true
		if len(record.Markers) > nativeBindingHistoryLimit {
			return errors.New("invalid native binding history markers")
		}
		for _, marker := range record.Markers {
			if marker.OperationID == "" || len(marker.OperationID) > 256 || len(marker.Before) > 256 || len(marker.After) > 256 {
				return errors.New("invalid native binding history marker")
			}
		}
		scopes := make(map[string]bool, len(record.Changes))
		for _, change := range record.Changes {
			if validateNativeCallerScope(change.CallerScope) != nil || change.BindingID == "" || scopes[change.CallerScope] || (change.Before == nil && change.After == nil) {
				return errors.New("invalid native binding history change")
			}
			scopes[change.CallerScope] = true
			for _, restriction := range []*NativeBindingRestriction{change.Before, change.After} {
				if restriction != nil && ((restriction.Group == "") == (len(restriction.AuthIDs) == 0)) {
					return errors.New("invalid native binding history restriction")
				}
			}
			for _, template := range []*NativeKeyBinding{change.BeforeTemplate, change.AfterTemplate} {
				if template != nil && (template.ID != change.BindingID || template.CallerScope != change.CallerScope) {
					return errors.New("invalid native binding history template")
				}
			}
		}
	}
	raw, err := json.Marshal(history)
	if err != nil || len(raw) > nativeBindingHistoryBytes {
		return errors.New("native binding history exceeds size limit")
	}
	return nil
}

func publicNativeBindingOperation(record nativeBindingHistoryRecord) NativeBindingOperation {
	out := NativeBindingOperation{ID: record.ID, Kind: record.Kind, SourceOperationID: record.SourceOperationID, RevertedBy: record.RevertedBy, CreatedAt: record.CreatedAt, Changes: make([]NativeBindingChange, len(record.Changes))}
	for i, change := range record.Changes {
		out.Changes[i] = cloneNativeBindingChange(change.NativeBindingChange)
	}
	return out
}

func (s *Store) NativeBindingHistorySnapshot() []NativeBindingOperation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]NativeBindingOperation, len(s.nativeBindingHistory))
	for i, record := range s.nativeBindingHistory {
		out[i] = publicNativeBindingOperation(record)
	}
	return out
}

func (s *Store) nativeBindingBatchSnapshot() nativeBindingBatchSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return nativeBindingBatchSnapshot{bindings: s.nativeKeyBindingsSnapshotLocked(), history: cloneNativeBindingHistory(s.nativeBindingHistory), rules: s.classifyRulesSnapshotLocked()}
}

func nativeBindingRestriction(binding *NativeKeyBinding) *NativeBindingRestriction {
	if binding == nil {
		return nil
	}
	if len(binding.AuthIDs) > 0 {
		return &NativeBindingRestriction{AuthIDs: append([]string(nil), binding.AuthIDs...)}
	}
	return &NativeBindingRestriction{Group: binding.Group}
}

func sameNativeBindingRestriction(left, right *NativeBindingRestriction) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Group == right.Group && strings.Join(normalizeNativeAuthIDs(left.AuthIDs), "\x00") == strings.Join(normalizeNativeAuthIDs(right.AuthIDs), "\x00")
}

func nativeBindingFingerprint(binding *NativeKeyBinding, restrictionIndependent bool) string {
	if binding == nil {
		return ""
	}
	copy := cloneNativeKeyBinding(*binding)
	copy.UpdatedAt = time.Time{}
	if restrictionIndependent {
		copy.Group, copy.AuthIDs = "", nil
	}
	data, _ := json.Marshal(copy)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func validateNativeBindingBatchInputs(keys, available []string, revision string) error {
	if len(keys) > 4096 || len(available) > 16384 || len(revision) > 256 {
		return ErrInvalidNativeBindingBatch
	}
	bytes := len(revision)
	for _, values := range [][]string{keys, available} {
		for _, value := range values {
			if len(value) > 4096 || strings.ContainsRune(value, '\x00') {
				return ErrInvalidNativeBindingBatch
			}
			bytes += len(value)
		}
	}
	if bytes > nativeBindingInputBytes {
		return ErrInvalidNativeBindingBatch
	}
	return nil
}

func newNativeBindingPlan(kind, source string) nativeBindingPlan {
	return nativeBindingPlan{kind: kind, source: source, preview: NativeBindingPreview{Changes: []NativeBindingChange{}, Conflicts: []NativeBindingConflict{}, Warnings: []string{}}}
}

func (plan *nativeBindingPlan) conflict(id, code string) {
	for _, conflict := range plan.preview.Conflicts {
		if conflict.BindingID == id && conflict.Code == code {
			return
		}
	}
	plan.preview.Conflicts = append(plan.preview.Conflicts, NativeBindingConflict{BindingID: id, Code: code})
}

func (plan *nativeBindingPlan) finish(snapshot nativeBindingBatchSnapshot, keys, available []string, complete bool, action any) {
	scopes := make([]string, len(keys))
	for i, key := range keys {
		scopes[i] = NativeCallerScope(key)
	}
	data, _ := json.Marshal(struct {
		Bindings   []NativeKeyBinding
		History    []nativeBindingHistoryRecord
		Rules      []ClassifyRule
		HostScopes []string
		Available  []string
		Complete   bool
		Action     any
	}{snapshot.bindings, snapshot.history, snapshot.rules, scopes, normalizeNativeAuthIDs(available), complete, action})
	digest := sha256.Sum256(data)
	plan.preview.Revision = hex.EncodeToString(digest[:])
	plan.preview.Noop = len(plan.changes) == 0 && len(plan.preview.Conflicts) == 0
	plan.preview.CanApply = len(plan.changes) > 0 && len(plan.preview.Conflicts) == 0
}

func nativeBatchBindingID(scope string) string {
	digest := sha256.Sum256([]byte("native-binding-batch:v1\x00" + scope))
	return "native-batch-" + hex.EncodeToString(digest[:12])
}

func (s *Store) planNativeBindingBatch(snapshot nativeBindingBatchSnapshot, input NativeBindingBatchInput) (nativeBindingPlan, error) {
	plan := newNativeBindingPlan("batch", "")
	if err := validateNativeBindingBatchInputs(input.APIKeys, input.AvailableAuthIDs, input.ExpectedRevision); err != nil {
		return plan, err
	}
	if len(input.SelectedIndices) == 0 || len(input.SelectedIndices) > 4096 || len(input.AuthIDs) == 0 || len(input.AuthIDs) > 16384 {
		return plan, ErrInvalidNativeBindingBatch
	}
	if err := validateNativeBindingBatchInputs(nil, input.AuthIDs, ""); err != nil {
		return plan, err
	}
	target := normalizeNativeAuthIDs(input.AuthIDs)
	if len(target) == 0 {
		return plan, ErrInvalidNativeBindingBatch
	}
	selected := append([]int(nil), input.SelectedIndices...)
	sort.Ints(selected)
	// Bound the expanded operation before copying the target list for every
	// selected key. Small request bodies can otherwise expand quadratically.
	targetJSON, _ := json.Marshal(target)
	if len(targetJSON) > nativeBindingHistoryBytes/len(selected) {
		return plan, fmt.Errorf("%w: expanded binding operation exceeds size limit", ErrInvalidNativeBindingBatch)
	}
	inputBytes := len(input.ExpectedRevision)
	for _, values := range [][]string{input.APIKeys, input.AvailableAuthIDs, input.AuthIDs} {
		for _, value := range values {
			inputBytes += len(value)
		}
	}
	if inputBytes > nativeBindingInputBytes {
		return plan, ErrInvalidNativeBindingBatch
	}
	seenScopes := make(map[string]bool, len(selected))
	for i, index := range selected {
		if index < 0 || index >= len(input.APIKeys) || (i > 0 && selected[i-1] == index) {
			return plan, ErrInvalidNativeBindingBatch
		}
		scope := NativeCallerScope(input.APIKeys[index])
		if scope == "" || seenScopes[scope] {
			return plan, ErrInvalidNativeBindingBatch
		}
		seenScopes[scope] = true
	}
	available := make(map[string]bool, len(input.AvailableAuthIDs))
	for _, id := range normalizeNativeAuthIDs(input.AvailableAuthIDs) {
		available[id] = true
	}
	if !input.CatalogComplete {
		plan.conflict("", "catalog_incomplete")
	}
	for _, id := range target {
		if !available[id] {
			plan.conflict("", "auth_unavailable")
		}
	}
	plan.bindings = append([]NativeKeyBinding(nil), snapshot.bindings...)
	byScope, byID := make(map[string]int), make(map[string]bool)
	for i, binding := range plan.bindings {
		byScope[binding.CallerScope], byID[binding.ID] = i, true
	}
	expandedBytes := len(targetJSON) * len(selected)
	for _, index := range selected {
		if position, ok := byScope[NativeCallerScope(input.APIKeys[index])]; ok {
			beforeJSON, _ := json.Marshal(nativeBindingRestriction(&plan.bindings[position]))
			expandedBytes += len(beforeJSON)
		}
		if expandedBytes > nativeBindingHistoryBytes {
			return plan, fmt.Errorf("%w: expanded binding operation exceeds size limit", ErrInvalidNativeBindingBatch)
		}
	}
	for _, index := range selected {
		scope := NativeCallerScope(input.APIKeys[index])
		var before *NativeKeyBinding
		var candidate NativeKeyBinding
		position, exists := byScope[scope]
		if exists {
			copy := cloneNativeKeyBinding(plan.bindings[position])
			before, candidate = &copy, cloneNativeKeyBinding(copy)
		} else {
			id := nativeBatchBindingID(scope)
			if byID[id] {
				plan.conflict(id, "binding_identity_changed")
				continue
			}
			candidate = NativeKeyBinding{ID: id, Name: "Native key " + NativeKeyPreview(input.APIKeys[index]), Enabled: true, CallerScope: scope, KeyPreview: NativeKeyPreview(input.APIKeys[index]), ModelAccess: NativeModelAccessPolicy{Mode: NativeModelAccessAll}}
		}
		candidate.Group, candidate.AuthIDs = "", append([]string(nil), target...)
		normalized := []NativeKeyBinding{candidate}
		if err := normalizeNativeKeyBindings(normalized); err != nil {
			return plan, fmt.Errorf("%w: invalid binding restriction", ErrInvalidNativeBindingBatch)
		}
		candidate = normalized[0]
		if sameNativeBindingRestriction(nativeBindingRestriction(before), nativeBindingRestriction(&candidate)) {
			continue
		}
		change := nativeBindingRecordedChange{NativeBindingChange: NativeBindingChange{BindingID: candidate.ID, Name: candidate.Name, KeyPreview: candidate.KeyPreview, Before: nativeBindingRestriction(before), After: nativeBindingRestriction(&candidate)}, CallerScope: scope, BeforeFingerprint: nativeBindingFingerprint(before, false)}
		if before != nil {
			change.BeforeCreatedAt = before.CreatedAt
		}
		if !exists {
			template := cloneNativeKeyBinding(candidate)
			change.AfterTemplate = &template
		}
		if exists {
			plan.bindings[position] = candidate
		} else {
			plan.bindings = append(plan.bindings, candidate)
			byID[candidate.ID] = true
		}
		plan.changes = append(plan.changes, change)
		plan.preview.Changes = append(plan.preview.Changes, cloneNativeBindingChange(change.NativeBindingChange))
	}
	plan.finish(snapshot, input.APIKeys, input.AvailableAuthIDs, input.CatalogComplete, struct {
		Kind     string
		Selected []int
		AuthIDs  []string
	}{"batch", selected, target})
	if err := validatePlannedNativeBindingHistory(plan.changes); err != nil {
		return plan, err
	}
	return plan, nil
}

func validatePlannedNativeBindingHistory(changes []nativeBindingRecordedChange) error {
	if len(changes) == 0 {
		return nil
	}
	raw, err := json.Marshal(changes)
	// Allow for finalized timestamps, fingerprints, and the operation envelope.
	if err != nil || len(raw)+len(changes)*512+1024 > nativeBindingHistoryBytes {
		return fmt.Errorf("%w: expanded binding operation exceeds size limit", ErrInvalidNativeBindingBatch)
	}
	return nil
}

func (s *Store) PreviewNativeBindingBatch(input NativeBindingBatchInput) (NativeBindingPreview, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	plan, err := s.planNativeBindingBatch(s.nativeBindingBatchSnapshot(), input)
	return plan.preview, err
}

func (s *Store) ApplyNativeBindingBatch(input NativeBindingBatchInput) (NativeBindingMutationResult, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	snapshot := s.nativeBindingBatchSnapshot()
	plan, err := s.planNativeBindingBatch(snapshot, input)
	if err != nil {
		return NativeBindingMutationResult{}, err
	}
	return s.commitNativeBindingPlan(snapshot, plan, input.ExpectedRevision)
}

func latestNativeBindingRecordedChange(history []nativeBindingHistoryRecord, scope string) *nativeBindingRecordedChange {
	for i := range history {
		for j := range history[i].Changes {
			if history[i].Changes[j].CallerScope == scope {
				return &history[i].Changes[j]
			}
		}
	}
	return nil
}

func (s *Store) planNativeBindingRollback(snapshot nativeBindingBatchSnapshot, input NativeBindingRollbackInput) (nativeBindingPlan, error) {
	operationID := strings.TrimSpace(input.OperationID)
	plan := newNativeBindingPlan("rollback", operationID)
	if err := validateNativeBindingBatchInputs(input.APIKeys, input.AvailableAuthIDs, input.ExpectedRevision); err != nil {
		return plan, err
	}
	if operationID == "" || len(operationID) > 256 {
		return plan, ErrInvalidNativeBindingBatch
	}
	var source *nativeBindingHistoryRecord
	for i := range snapshot.history {
		if snapshot.history[i].ID == operationID {
			source = &snapshot.history[i]
			break
		}
	}
	if source == nil {
		return plan, ErrUnknownNativeBindingOperation
	}
	if !input.CatalogComplete {
		plan.conflict("", "catalog_incomplete")
	}
	hostScopes, available := make(map[string]bool), make(map[string]bool)
	for _, key := range input.APIKeys {
		if scope := NativeCallerScope(key); scope != "" {
			hostScopes[scope] = true
		}
	}
	for _, id := range normalizeNativeAuthIDs(input.AvailableAuthIDs) {
		available[id] = true
	}
	byScope := make(map[string]NativeKeyBinding, len(snapshot.bindings))
	byID := make(map[string]string, len(snapshot.bindings))
	for _, binding := range snapshot.bindings {
		byScope[binding.CallerScope], byID[binding.ID] = binding, binding.CallerScope
	}
	for _, saved := range source.Changes {
		if !hostScopes[saved.CallerScope] {
			plan.conflict(saved.BindingID, "host_key_missing")
			continue
		}
		var current *NativeKeyBinding
		if value, ok := byScope[saved.CallerScope]; ok {
			cloned := cloneNativeKeyBinding(value)
			current = &cloned
		}
		instanceCreatedAt := saved.BeforeCreatedAt
		if saved.Before == nil {
			instanceCreatedAt = saved.AfterCreatedAt
		}
		if current != nil && (current.ID != saved.BindingID || !current.CreatedAt.Equal(instanceCreatedAt)) {
			plan.conflict(saved.BindingID, "binding_identity_changed")
			continue
		}
		target := saved.Before
		if sameNativeBindingRestriction(nativeBindingRestriction(current), target) {
			continue
		}
		latest := latestNativeBindingRecordedChange(snapshot.history, saved.CallerScope)
		if current == nil && target != nil && latest != nil && (latest.BindingID != saved.BindingID || !latest.BeforeCreatedAt.Equal(instanceCreatedAt)) {
			plan.conflict(saved.BindingID, "binding_identity_changed")
			continue
		}
		if latest == nil || !sameNativeBindingRestriction(nativeBindingRestriction(current), latest.After) ||
			(current != nil && (latest.BindingID != current.ID || !current.CreatedAt.Equal(latest.AfterCreatedAt))) {
			plan.conflict(saved.BindingID, "binding_changed")
			continue
		}
		var candidate *NativeKeyBinding
		if target == nil {
			if current == nil {
				continue
			}
			if saved.AfterTemplate == nil || nativeBindingFingerprint(current, true) != nativeBindingFingerprint(saved.AfterTemplate, true) {
				plan.conflict(saved.BindingID, "binding_modified")
				continue
			}
		} else {
			validTarget := true
			for _, id := range target.AuthIDs {
				if !available[id] {
					plan.conflict(saved.BindingID, "auth_unavailable")
					validTarget = false
				}
			}
			if IsClassifyGroup(target.Group) {
				groupExists := false
				for _, rule := range snapshot.rules {
					if rule.Enabled && FormatClassifyGroup(rule.Group) == target.Group {
						groupExists = true
						break
					}
				}
				if !groupExists {
					plan.conflict(saved.BindingID, "group_unavailable")
					validTarget = false
				}
			}
			if !validTarget {
				continue
			}
			if current == nil {
				if saved.BeforeTemplate == nil {
					plan.conflict(saved.BindingID, "binding_changed")
					continue
				}
				if scope, exists := byID[saved.BindingID]; exists && scope != saved.CallerScope {
					plan.conflict(saved.BindingID, "binding_identity_changed")
					continue
				}
				cloned := cloneNativeKeyBinding(*saved.BeforeTemplate)
				candidate = &cloned
			} else {
				cloned := cloneNativeKeyBinding(*current)
				candidate = &cloned
			}
			candidate.Group, candidate.AuthIDs = target.Group, append([]string(nil), target.AuthIDs...)
			normalized := []NativeKeyBinding{*candidate}
			if err := normalizeNativeKeyBindings(normalized); err != nil {
				plan.conflict(saved.BindingID, "invalid_restriction")
				continue
			}
			candidate = &normalized[0]
			if target.Group != "" && len(plan.preview.Warnings) == 0 {
				plan.preview.Warnings = append(plan.preview.Warnings, "group_uses_current_rules")
			}
		}
		change := nativeBindingRecordedChange{
			NativeBindingChange: NativeBindingChange{BindingID: saved.BindingID, Name: saved.Name, KeyPreview: saved.KeyPreview, Before: nativeBindingRestriction(current), After: nativeBindingRestriction(candidate)},
			CallerScope:         saved.CallerScope, BeforeFingerprint: nativeBindingFingerprint(current, false),
		}
		if current != nil {
			change.Name, change.KeyPreview, change.BeforeCreatedAt = current.Name, current.KeyPreview, current.CreatedAt
			if candidate == nil {
				cloned := cloneNativeKeyBinding(*current)
				change.BeforeTemplate = &cloned
			}
		}
		if current == nil && candidate != nil {
			cloned := cloneNativeKeyBinding(*candidate)
			change.AfterTemplate = &cloned
		}
		if candidate == nil {
			delete(byScope, saved.CallerScope)
			delete(byID, saved.BindingID)
		} else {
			byScope[saved.CallerScope], byID[saved.BindingID] = *candidate, saved.CallerScope
		}
		plan.changes = append(plan.changes, change)
		plan.preview.Changes = append(plan.preview.Changes, cloneNativeBindingChange(change.NativeBindingChange))
	}
	for _, binding := range byScope {
		plan.bindings = append(plan.bindings, binding)
	}
	sort.Slice(plan.bindings, func(i, j int) bool { return plan.bindings[i].ID < plan.bindings[j].ID })
	plan.finish(snapshot, input.APIKeys, input.AvailableAuthIDs, input.CatalogComplete, struct{ Kind, OperationID string }{"rollback", operationID})
	if err := validatePlannedNativeBindingHistory(plan.changes); err != nil {
		return plan, err
	}
	return plan, nil
}

func (s *Store) PreviewNativeBindingRollback(input NativeBindingRollbackInput) (NativeBindingPreview, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	plan, err := s.planNativeBindingRollback(s.nativeBindingBatchSnapshot(), input)
	return plan.preview, err
}

func (s *Store) ApplyNativeBindingRollback(input NativeBindingRollbackInput) (NativeBindingMutationResult, error) {
	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	snapshot := s.nativeBindingBatchSnapshot()
	plan, err := s.planNativeBindingRollback(snapshot, input)
	if err != nil {
		return NativeBindingMutationResult{}, err
	}
	return s.commitNativeBindingPlan(snapshot, plan, input.ExpectedRevision)
}

func markNativeBindingOperation(history []nativeBindingHistoryRecord, id, revertedBy string, depth int) {
	if depth > nativeBindingHistoryLimit {
		return
	}
	for i := range history {
		if history[i].ID != id {
			continue
		}
		history[i].RevertedBy = revertedBy
		if history[i].Kind == "rollback" && history[i].SourceOperationID != "" {
			prior := ""
			if revertedBy == "" {
				prior = history[i].ID
			}
			markNativeBindingOperation(history, history[i].SourceOperationID, prior, depth+1)
		}
		return
	}
}

func (s *Store) commitNativeBindingPlan(snapshot nativeBindingBatchSnapshot, plan nativeBindingPlan, expected string) (NativeBindingMutationResult, error) {
	if expected == "" || expected != plan.preview.Revision {
		return NativeBindingMutationResult{}, ErrNativeBindingRevisionMismatch
	}
	if len(plan.preview.Conflicts) > 0 {
		return NativeBindingMutationResult{}, &NativeBindingConflictError{Conflicts: plan.preview.Conflicts}
	}
	if len(plan.changes) == 0 {
		return NativeBindingMutationResult{Noop: true}, nil
	}
	now := time.Now().UTC()
	byID := make(map[string]*NativeKeyBinding, len(plan.bindings))
	for i := range plan.bindings {
		byID[plan.bindings[i].ID] = &plan.bindings[i]
	}
	for i := range plan.changes {
		change := &plan.changes[i]
		if candidate := byID[change.BindingID]; candidate != nil {
			if candidate.CreatedAt.IsZero() {
				candidate.CreatedAt = now
			}
			candidate.UpdatedAt = now
			change.AfterCreatedAt = candidate.CreatedAt
			change.AfterFingerprint = nativeBindingFingerprint(candidate, false)
			if change.AfterTemplate != nil {
				cloned := cloneNativeKeyBinding(*candidate)
				change.AfterTemplate = &cloned
			}
		}
	}
	if err := normalizeNativeKeyBindings(plan.bindings); err != nil {
		return NativeBindingMutationResult{}, fmt.Errorf("%w: invalid proposed bindings", ErrInvalidNativeBindingBatch)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return NativeBindingMutationResult{}, fmt.Errorf("create native binding operation: %w", err)
	}
	record := nativeBindingHistoryRecord{ID: hex.EncodeToString(nonce[:]), Kind: plan.kind, SourceOperationID: plan.source, CreatedAt: now, Changes: plan.changes}
	history := cloneNativeBindingHistory(snapshot.history)
	if plan.kind == "rollback" {
		markNativeBindingOperation(history, plan.source, record.ID, 0)
		for _, source := range snapshot.history {
			if source.ID != plan.source || source.Kind != "rollback" {
				continue
			}
			// Reversing a jump must restore every marker it changed, including
			// newer batches that were superseded along with its selected source.
			for _, marker := range source.Markers {
				for i := range history {
					if history[i].ID == marker.OperationID && snapshot.history[i].RevertedBy == marker.After {
						history[i].RevertedBy = marker.Before
					}
				}
			}
			break
		}
		// A jump to an older snapshot supersedes newer complete batches for the
		// same affected key set. Partial overlaps remain available via preview.
		affected := make(map[string]bool, len(plan.changes))
		for _, change := range plan.changes {
			affected[change.CallerScope] = true
		}
		for i := range history {
			if history[i].ID == plan.source {
				break
			}
			if history[i].Kind != "batch" {
				continue
			}
			complete := true
			for _, change := range history[i].Changes {
				if !affected[change.CallerScope] {
					complete = false
					break
				}
			}
			if complete {
				history[i].RevertedBy = record.ID
			}
		}
	}
	for i := range history {
		if history[i].RevertedBy != snapshot.history[i].RevertedBy {
			record.Markers = append(record.Markers, nativeBindingHistoryMarker{OperationID: history[i].ID, Before: snapshot.history[i].RevertedBy, After: history[i].RevertedBy})
		}
	}
	history = append([]nativeBindingHistoryRecord{record}, history...)
	if len(history) > nativeBindingHistoryLimit {
		history = history[:nativeBindingHistoryLimit]
	}
	for {
		raw, err := json.Marshal(history)
		if err != nil {
			return NativeBindingMutationResult{}, fmt.Errorf("%w: history serialization failed", ErrInvalidNativeBindingBatch)
		}
		if len(raw) <= nativeBindingHistoryBytes {
			break
		}
		if len(history) == 1 {
			return NativeBindingMutationResult{}, fmt.Errorf("%w: operation history exceeds size limit", ErrInvalidNativeBindingBatch)
		}
		history = history[:len(history)-1]
	}
	if err := validateNativeBindingHistory(history); err != nil {
		return NativeBindingMutationResult{}, fmt.Errorf("%w: %v", ErrInvalidNativeBindingBatch, err)
	}
	s.mu.RLock()
	keys, usage, aliases, rules, path := s.keysSnapshotLocked(), s.usageSnapshotLocked(), s.aliasesSnapshotLocked(), s.classifyRulesSnapshotLocked(), s.statePath
	s.mu.RUnlock()
	if err := s.saveStateWithNativeBindingHistory(path, keys, usage, aliases, rules, plan.bindings, history); err != nil {
		return NativeBindingMutationResult{}, fmt.Errorf("%w: %w", ErrNativeKeyBindingPersistence, err)
	}
	s.mu.Lock()
	s.replaceNativeKeyBindingsLocked(plan.bindings)
	s.nativeBindingHistory = cloneNativeBindingHistory(history)
	onChanged := s.onNativeKeyBindingsChanged
	s.mu.Unlock()
	if onChanged != nil {
		onChanged()
	}
	operation := publicNativeBindingOperation(record)
	return NativeBindingMutationResult{Operation: &operation, Changed: len(record.Changes)}, nil
}
