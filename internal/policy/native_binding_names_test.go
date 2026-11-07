package policy

import (
	"bytes"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
)

func TestSyncNativeBindingNamesPreservesPolicyUsageAndHistory(t *testing.T) {
	store, path := batchTestStore(t)
	first, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "existing", Name: "Old name", APIKey: batchTestKeyA, Enabled: true, RoundRobin: true,
		AuthIDs: []string{"codex-a.json"}, RPM: intPtr(10), DailyUSD: floatPtr(3), WeeklyUSD: floatPtr(7),
		ModelAccess: &NativeModelAccessPolicy{Mode: NativeModelAccessAllowlist, Models: []NativeAllowedModel{{Provider: "codex", Model: "spark"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	batchTestApply(t, store, batchTestInput())
	first = batchTestFind(t, store, first.CallerScope)
	second := batchTestFind(t, store, NativeCallerScope(batchTestKeyB))
	if second.Name != "" {
		t.Fatalf("batch-created name = %q, want blank", second.Name)
	}
	if err := store.UpsertModelPrice(ModelPrice{ID: "spark", InputPricePerMillion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, limited := store.CheckNativeKeyQuota(first.CallerScope); limited {
		t.Fatal("first request unexpectedly limited")
	}
	store.RecordUsage(batchTestKeyA, "spark", "spark", false, UsageDetail{InputTokens: 1_000_000})
	usage := store.NativeBindingUsage(first)
	if usage.RPMUsed != 1 || usage.DailyUSDUsed != 1 {
		t.Fatalf("quota fixture not initialized: %+v", usage)
	}
	history := store.NativeBindingHistorySnapshot()
	result, err := store.SyncNativeBindingNames([]NativeBindingNameUpdate{
		{ID: first.ID, APIKey: batchTestKeyA, ExpectedName: "Old name", Name: "  Current name  "},
		{ID: second.ID, APIKey: batchTestKeyB, ExpectedName: "", Name: "Newly named"},
		{ID: "EXISTING", APIKey: batchTestKeyA, ExpectedName: "Old name", Name: "Mismatched ID"},
	})
	if err != nil || !reflect.DeepEqual(result.UpdatedIDs, []string{first.ID, second.ID}) || !reflect.DeepEqual(result.SkippedIDs, []string{"EXISTING"}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	afterFirst := batchTestFind(t, store, first.CallerScope)
	afterSecond := batchTestFind(t, store, second.CallerScope)
	first.Name, second.Name = "Current name", "Newly named"
	first.UpdatedAt, second.UpdatedAt = afterFirst.UpdatedAt, afterSecond.UpdatedAt
	if !reflect.DeepEqual(first, afterFirst) || !reflect.DeepEqual(second, afterSecond) {
		t.Fatal("name synchronization changed binding policies")
	}
	if store.NativeBindingUsage(first) != usage || !reflect.DeepEqual(history, store.NativeBindingHistorySnapshot()) {
		t.Fatal("name synchronization changed runtime usage or history")
	}
	disk := batchTestReadState(t, path)
	if bytes.Contains(disk, []byte(batchTestKeyA)) || bytes.Contains(disk, []byte(batchTestKeyB)) {
		t.Fatal("state contains a plaintext key")
	}
	restarted := NewStore()
	restarted.SetClock(store.usage.now)
	if err := restarted.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restarted.NativeKeyBindingsSnapshot(), store.NativeKeyBindingsSnapshot()) ||
		!reflect.DeepEqual(history, restarted.NativeBindingHistorySnapshot()) || restarted.NativeBindingUsage(first).DailyUSDUsed != usage.DailyUSDUsed {
		t.Fatal("restart did not preserve the synchronized names, usage, and history")
	}

	result, err = store.SyncNativeBindingNames([]NativeBindingNameUpdate{
		{ID: first.ID, APIKey: batchTestKeyA, ExpectedName: "Old name", Name: "Stale name"},
		{ID: second.ID, APIKey: batchTestKeyA, ExpectedName: second.Name, Name: "Wrong key"},
		{ID: "missing", APIKey: batchTestKeyA, Name: "Orphan"},
	})
	if err != nil || len(result.UpdatedIDs) != 0 || !reflect.DeepEqual(result.SkippedIDs, []string{first.ID, second.ID, "missing"}) {
		t.Fatalf("skipped result=%+v err=%v", result, err)
	}
	batchTestAssertUnchanged(t, store, path, restarted.NativeKeyBindingsSnapshot(), history, disk)
}

func TestSyncNativeBindingNamesPersistenceFailureAndNoops(t *testing.T) {
	store, path := batchTestStore(t)
	first := batchTestCreate(t, store, "first", batchTestKeyA)
	second := batchTestCreate(t, store, "second", batchTestKeyB)
	before := store.NativeKeyBindingsSnapshot()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	updates := []NativeBindingNameUpdate{
		{ID: first.ID, APIKey: batchTestKeyA, ExpectedName: first.Name, Name: "Updated first"},
		{ID: second.ID, APIKey: batchTestKeyB, ExpectedName: second.Name, Name: "Updated second"},
	}
	if _, err := store.SyncNativeBindingNames(updates); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("err=%v, want persistence failure", err)
	}
	updates[0].Name, updates[1].Name = "\t ", second.Name
	result, err := store.SyncNativeBindingNames(updates)
	if err != nil || len(result.UpdatedIDs) != 0 || !reflect.DeepEqual(result.SkippedIDs, []string{first.ID, second.ID}) {
		t.Fatalf("no-op result=%+v err=%v", result, err)
	}
	if !reflect.DeepEqual(before, store.NativeKeyBindingsSnapshot()) {
		t.Fatal("failed or skipped update changed live bindings")
	}
}

func TestSyncNativeBindingNamesSerializesConcurrentChanges(t *testing.T) {
	store, path := batchTestStore(t)
	binding := batchTestCreate(t, store, "race", batchTestKeyA)
	var group sync.WaitGroup
	start := make(chan struct{})
	results := make(chan NativeBindingNameSyncResult, 2)
	for _, name := range []string{"First writer", "Second writer"} {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, err := store.SyncNativeBindingNames([]NativeBindingNameUpdate{
				{ID: binding.ID, APIKey: batchTestKeyA, ExpectedName: binding.Name, Name: name},
			})
			if err != nil {
				t.Error(err)
			}
			results <- result
		}()
	}
	close(start)
	group.Wait()
	close(results)
	updated, skipped := 0, 0
	for result := range results {
		updated += len(result.UpdatedIDs)
		skipped += len(result.SkippedIDs)
	}
	if updated != 1 || skipped != 1 {
		t.Fatalf("concurrent result: %d updated, %d skipped", updated, skipped)
	}
	disk, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	name := batchTestFind(t, store, binding.CallerScope).Name
	if len(disk.NativeKeyBindings) != 1 || disk.NativeKeyBindings[0].Name != name || name == binding.Name {
		t.Fatal("concurrent synchronization did not persist the winning update")
	}
}
