package plugin

import (
	"container/list"
	"sort"
	"strings"
	"sync"

	"cpa-access-guard/internal/policy"
)

// Bound cursor memory even when an all-models binding receives arbitrary model
// names. Eviction changes fairness only; authorization always precedes a pick.
const nativeRoundRobinCapacity = 4096

type nativeRoundRobinKey struct {
	callerScope string
	providers   string
	model       string
}

type nativeRoundRobinCursor struct {
	key    nativeRoundRobinKey
	lastID string
}

type nativeRoundRobinState struct {
	mu      sync.Mutex
	cursors map[nativeRoundRobinKey]*list.Element
	recent  list.List
}

func nativeRoundRobinRoute(callerScope string, req SchedulerPickRequest) nativeRoundRobinKey {
	providers := schedulerRequestProviders(req)
	seen := make(map[string]struct{}, len(providers))
	canonical := make([]string, 0, len(providers))
	for _, provider := range providers {
		provider = policy.CanonicalNativeProvider(provider)
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		canonical = append(canonical, provider)
	}
	sort.Strings(canonical)
	return nativeRoundRobinKey{
		callerScope: callerScope,
		providers:   strings.Join(canonical, "\x00"),
		model:       policy.CanonicalNativeModel(req.Model),
	}
}

// pickNativeRoundRobin receives only authorized, host-offered candidates. It
// never expands that pool or changes CPA's retry, cooldown, or session state.
// Keeping the last selected ID, rather than an index into the current slice,
// preserves progress when retries, cooldowns, or edits shrink/reorder the pool.
func (a *App) pickNativeRoundRobin(callerScope string, req SchedulerPickRequest, candidates []SchedulerAuthCandidate, priority int) string {
	ids := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Priority == priority {
			ids = append(ids, candidate.ID)
		}
	}
	sort.Strings(ids)
	key := nativeRoundRobinRoute(callerScope, req)
	state := &a.nativeRoundRobin
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.cursors == nil {
		state.cursors = make(map[nativeRoundRobinKey]*list.Element)
	}
	element := state.cursors[key]
	if element == nil {
		if len(state.cursors) >= nativeRoundRobinCapacity {
			oldest := state.recent.Back()
			delete(state.cursors, oldest.Value.(*nativeRoundRobinCursor).key)
			state.recent.Remove(oldest)
		}
		element = state.recent.PushFront(&nativeRoundRobinCursor{key: key})
		state.cursors[key] = element
	} else {
		state.recent.MoveToFront(element)
	}
	cursor := element.Value.(*nativeRoundRobinCursor)
	index := sort.Search(len(ids), func(i int) bool { return ids[i] > cursor.lastID })
	if index == len(ids) {
		index = 0
	}
	cursor.lastID = ids[index]
	return cursor.lastID
}

func (a *App) clearNativeRoundRobin() {
	state := &a.nativeRoundRobin
	state.mu.Lock()
	state.cursors = nil
	state.recent.Init()
	state.mu.Unlock()
}

// pruneNativeRoundRobin discards deleted/disabled scopes and revoked model
// routes. Auth-list edits retain the last ID so new and removed credentials
// take effect on the next pick without restarting the rotation at the front.
func (a *App) pruneNativeRoundRobin() {
	constraints := make(map[string]policy.NativeKeyConstraint)
	for _, binding := range a.store.NativeKeyBindingsSnapshot() {
		if !binding.Enabled || !binding.RoundRobin {
			continue
		}
		if constraint, ok := a.store.ResolveNativeKeyConstraint(binding.CallerScope, "", ""); ok {
			constraints[binding.CallerScope] = constraint
		}
	}
	state := &a.nativeRoundRobin
	state.mu.Lock()
	defer state.mu.Unlock()
	for key, element := range state.cursors {
		constraint, exists := constraints[key.callerScope]
		allowed := false
		if exists {
			for _, provider := range strings.Split(key.providers, "\x00") {
				if constraint.AllowsModel(provider, key.model) {
					allowed = true
					break
				}
			}
		}
		if !allowed {
			delete(state.cursors, key)
			state.recent.Remove(element)
		}
	}
}
