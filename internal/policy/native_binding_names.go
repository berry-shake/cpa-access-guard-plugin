package policy

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidNativeBindingNames = errors.New("invalid native binding name updates")

type NativeBindingNameUpdate struct {
	ID           string `json:"-" yaml:"-"`
	APIKey       string `json:"-" yaml:"-"`
	ExpectedName string `json:"-" yaml:"-"`
	Name         string `json:"-" yaml:"-"`
}

type NativeBindingNameSyncResult struct {
	UpdatedIDs []string `json:"updated_ids"`
	SkippedIDs []string `json:"skipped_ids"`
}

// SyncNativeBindingNames changes names on existing bindings only. A row is
// skipped if its ID, key identity, or previous name no longer matches.
func (s *Store) SyncNativeBindingNames(updates []NativeBindingNameUpdate) (NativeBindingNameSyncResult, error) {
	if len(updates) == 0 {
		return NativeBindingNameSyncResult{}, ErrInvalidNativeBindingNames
	}
	seen := make(map[string]bool, len(updates))
	for _, update := range updates {
		if strings.TrimSpace(update.ID) == "" || strings.TrimSpace(update.APIKey) == "" || seen[update.ID] {
			return NativeBindingNameSyncResult{}, ErrInvalidNativeBindingNames
		}
		seen[update.ID] = true
	}

	s.updateMu.Lock()
	defer s.updateMu.Unlock()
	result := NativeBindingNameSyncResult{UpdatedIDs: []string{}, SkippedIDs: []string{}}
	names := make(map[string]string, len(updates))
	s.mu.RLock()
	for _, update := range updates {
		binding := s.nativeKeyBindings[update.ID]
		name := strings.TrimSpace(update.Name)
		if binding == nil || binding.CallerScope != NativeCallerScope(update.APIKey) ||
			binding.Name != update.ExpectedName || name == "" || name == binding.Name {
			result.SkippedIDs = append(result.SkippedIDs, update.ID)
			continue
		}
		names[update.ID] = name
		result.UpdatedIDs = append(result.UpdatedIDs, update.ID)
	}
	s.mu.RUnlock()
	if len(names) == 0 {
		return result, nil
	}

	// writeFullStateLocked samples usage under the persistence lock so a
	// preceding flush cannot have saved newer counters than this write.
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.RLock()
	bindings := s.nativeKeyBindingsSnapshotLocked()
	history, path := cloneNativeBindingHistory(s.nativeBindingHistory), s.statePath
	s.mu.RUnlock()

	now := time.Now().UTC()
	for i := range bindings {
		if name, ok := names[bindings[i].ID]; ok {
			bindings[i].Name = name
			bindings[i].UpdatedAt = now
		}
	}
	if err := s.writeFullStateLocked(path, bindings, history); err != nil {
		return NativeBindingNameSyncResult{}, fmt.Errorf("%w: %w", ErrNativeKeyBindingPersistence, err)
	}
	s.mu.Lock()
	s.replaceNativeKeyBindingsLocked(bindings)
	s.mu.Unlock()
	return result, nil
}
