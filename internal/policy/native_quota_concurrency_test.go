package policy

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNativeQuotaResetQueuesNewUsageAndRPMUntilCommit(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistence_failure_%t", fail), func(t *testing.T) {
			store := newNativeQuotaStore(t, filepath.Join(t.TempDir(), "state.json"))
			binding := createTestBinding(t, store, "selected", func(input *CreateNativeKeyBindingInput) {
				input.RPM = intPtr(100)
			})
			account := nativeUsageLedgerID(binding.CallerScope)
			usage, limiter := store.usage, store.limiter
			usage.RecordCost(account, "model", 1, 0, 0, 0, 0, 10, 3, 1)
			usage.RecordCost("other", "model", 2, 0, 0, 0, 0, 20, 6, 1)
			limiter.Allow(account, 100)
			if err := store.FlushUsage(); err != nil {
				t.Fatal(err)
			}

			entered, release := make(chan struct{}), make(chan struct{})
			result := make(chan error, 1)
			persistCalls := 0
			go func() {
				store.persistMu.Lock()
				defer store.persistMu.Unlock()
				result <- resetNativeQuotaAccounts([]string{account}, usage, limiter, func(proposed map[string]*UsageState) error {
					persistCalls++
					if proposed[account] == nil || proposed[account].Weekly.CallCount != 0 || proposed[account].WeeklyResetAnchor == nil || proposed["other"] == nil {
						return errors.New("invalid proposed snapshot")
					}
					// This callback runs with both locks held. Live state must still
					// be untouched until the write is accepted.
					if usage.entries[account].Daily.CallCount != 1 || limiter.buckets[account].count != 1 {
						return errors.New("runtime counters changed before persistence")
					}
					close(entered)
					<-release
					if fail {
						return errors.New("injected persistence failure")
					}
					return SaveUsageOnly(store.statePath, proposed)
				})
			}()
			select {
			case <-entered:
			case err := <-result:
				t.Fatalf("transaction failed before barrier: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("transaction did not reach persistence")
			}
			if usage.mu.TryLock() {
				usage.mu.Unlock()
				close(release)
				t.Fatal("usage not protected during persistence")
			}
			if limiter.mu.TryLock() {
				limiter.mu.Unlock()
				close(release)
				t.Fatal("RPM not protected during persistence")
			}

			started := make(chan struct{}, 3)
			finished := make(chan struct{}, 3)
			go func() {
				started <- struct{}{}
				store.RecordUsage(nativeTestKey, "", "model", false, UsageDetail{InputTokens: 10})
				finished <- struct{}{}
			}()
			go func() {
				started <- struct{}{}
				usage.RecordCost("other", "model", 2, 0, 0, 0, 0, 20, 6, 1)
				finished <- struct{}{}
			}()
			go func() {
				started <- struct{}{}
				limiter.Allow(account, 100)
				finished <- struct{}{}
			}()
			for i := 0; i < 3; i++ {
				<-started
			}
			select {
			case <-finished:
				close(release)
				t.Fatal("new work completed before persistence released its locks")
			default:
			}
			close(release)
			if err := <-result; (err != nil) != fail {
				t.Fatalf("reset err=%v, want failure=%t", err, fail)
			}
			if persistCalls != 1 {
				t.Fatalf("persistence calls=%d, want 1", persistCalls)
			}
			for i := 0; i < 3; i++ {
				select {
				case <-finished:
				case <-time.After(5 * time.Second):
					t.Fatal("queued work did not complete")
				}
			}
			want := int64(1)
			if fail {
				want++
			}
			snapshot := usage.snapshot()
			if got := snapshot[account].Daily.CallCount; got != want {
				t.Fatalf("post-transaction usage=%d, want %d", got, want)
			}
			if got := limiter.SnapshotID(account); got != int(want) {
				t.Fatalf("post-transaction RPM=%d, want %d", got, want)
			}
			if snapshot["other"].Daily.CallCount != 2 {
				t.Fatal("new usage for another account was lost")
			}
			if err := store.FlushUsage(); err != nil {
				t.Fatal(err)
			}
			state, err := LoadState(store.statePath)
			if err != nil || state.Usage[account].Daily.CallCount != want || state.Usage["other"].Daily.CallCount != 2 {
				t.Fatalf("persisted post-transaction records differ: state=%+v err=%v", state, err)
			}
		})
	}
}

// waitForQuotaFlushPersistenceWait uses a scheduler barrier, not elapsed time,
// to ensure the flush is already blocked on the held persistence mutex. This
// catches the old implementation that sampled usage before joining that queue.
func waitForQuotaFlushPersistenceWait(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	buffer := make([]byte, 1<<20)
	for time.Now().Before(deadline) {
		stacks := string(buffer[:runtime.Stack(buffer, true)])
		for _, stack := range strings.Split(stacks, "\n\n") {
			if strings.Contains(stack, "TestNativeQuotaQueuedFlushCannotResurrectReset.func") &&
				strings.Contains(stack, "sync.(*Mutex).Lock") &&
				strings.Contains(stack, ".FlushUsage(") {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatal("flush did not block on persistence lock")
}

func TestNativeQuotaQueuedFlushCannotResurrectReset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	store := newNativeQuotaStore(t, path)
	binding := createTestBinding(t, store, "reset", nil)
	account := nativeUsageLedgerID(binding.CallerScope)
	store.RecordUsage(nativeTestKey, "", "model", false, UsageDetail{InputTokens: 10})
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	store.persistMu.Lock()
	locked := true
	defer func() {
		if locked {
			store.persistMu.Unlock()
		}
	}()
	flushResult := make(chan error, 1)
	go func() { flushResult <- store.FlushUsage() }()
	waitForQuotaFlushPersistenceWait(t)
	if err := resetNativeQuotaAccounts([]string{account}, store.usage, store.limiter, func(snapshot map[string]*UsageState) error {
		return SaveUsageOnly(path, snapshot)
	}); err != nil {
		t.Fatal(err)
	}
	// This finalized record belongs after the reset. The queued flush must
	// sample it, not resurrect the pre-reset record it saw before waiting.
	store.RecordUsage(nativeTestKey, "", "new-model", false, UsageDetail{InputTokens: 3})
	store.persistMu.Unlock()
	locked = false
	if err := <-flushResult; err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	usage := state.Usage[account]
	if usage == nil || usage.Daily.CallCount != 1 || len(usage.ByAlias) != 1 || usage.ByAlias["new-model"].Daily.CallCount != 1 {
		t.Fatalf("queued flush restored stale usage: %+v", usage)
	}
	reloaded := newNativeQuotaStore(t, path)
	if got := reloaded.usage.snapshot()[account]; got == nil || got.ByAlias["new-model"].Daily.CallCount != 1 {
		t.Fatalf("reset did not survive restart: %+v", got)
	}
}

func TestNativeQuotaUsageSnapshotOwnsModelMaps(t *testing.T) {
	usage := newUsageLedger(time.Now)
	usage.RecordCost("account", "model", 1, 0, 0, 0, 0, 10, 3, 1)
	snapshot := usage.snapshot()
	delete(snapshot["account"].ByAlias, "model")
	if got := usage.snapshot()["account"].ByAlias["model"].Daily.CallCount; got != 1 {
		t.Fatal("snapshot mutation changed the ledger")
	}
	snapshot = usage.snapshot()
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		for i := 0; i < 300; i++ {
			usage.RecordCost("account", fmt.Sprintf("model-%d", i), 1, 0, 0, 0, 0, 10, 3, 1)
		}
	}()
	for i := 0; i < 100; i++ {
		if _, err := json.Marshal(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	wait.Wait()
	if len(snapshot["account"].ByAlias) != 1 || snapshot["account"].Daily.CallCount != 1 {
		t.Fatal("concurrent recording mutated an existing persistence snapshot")
	}
}

func TestNativeQuotaConcurrentPublicResetRecordAndFlush(t *testing.T) {
	store := newNativeQuotaStore(t, filepath.Join(t.TempDir(), "state.json"))
	selected := createTestBinding(t, store, "selected", nil)
	unselectedKey := nativeTestKey + "-unselected"
	unselected := createTestBindingWithKey(t, store, "unselected", unselectedKey, nil)
	start := make(chan struct{})
	errors := make(chan error, 3)
	go func() {
		<-start
		for i := 0; i < 40; i++ {
			if _, err := store.ResetNativeKeyQuotas([]string{selected.ID}); err != nil {
				errors <- err
				return
			}
		}
		errors <- nil
	}()
	go func() {
		<-start
		for i := 0; i < 40; i++ {
			if err := store.FlushUsage(); err != nil {
				errors <- err
				return
			}
		}
		errors <- nil
	}()
	go func() {
		<-start
		for i := 0; i < 500; i++ {
			store.RecordUsage(nativeTestKey, "", "model", false, UsageDetail{InputTokens: 10})
			store.RecordUsage(unselectedKey, "", "model", false, UsageDetail{InputTokens: 10})
		}
		errors <- nil
	}()
	close(start)
	for i := 0; i < 3; i++ {
		select {
		case err := <-errors:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("reset/usage/flush deadlock")
		}
	}
	if got := store.NativeBindingUsage(unselected).DailyCalls; got != 500 {
		t.Fatalf("unselected finalized records lost: %d", got)
	}
	if err := store.ResetNativeKeyQuota(selected.ID); err != nil {
		t.Fatal(err)
	}
	store.RecordUsage(nativeTestKey, "", "after-reset", false, UsageDetail{InputTokens: 10})
	if err := store.FlushUsage(); err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(store.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if got := state.Usage[nativeUsageLedgerID(selected.CallerScope)]; got == nil || got.Daily.CallCount != 1 || got.ByAlias["after-reset"].Daily.CallCount != 1 {
		t.Fatalf("post-reset record missing or old counters restored: %+v", got)
	}
	if state.Usage[nativeUsageLedgerID(unselected.CallerScope)].Daily.CallCount != 500 {
		t.Fatal("persisted unselected usage changed")
	}
}
