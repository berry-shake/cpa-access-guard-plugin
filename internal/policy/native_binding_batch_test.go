package policy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	batchTestKeyA = "sk-batch-alpha-private-0123456789abcdef"
	batchTestKeyB = "sk-batch-bravo-private-0123456789abcdef"
	batchTestKeyC = "sk-batch-charlie-private-0123456789abcdef"
	batchTestKeyD = "sk-batch-delta-private-0123456789abcdef"
)

func batchTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	store := NewStore()
	if err := store.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	store.SetClock(func() time.Time { return time.Date(2026, time.September, 26, 12, 0, 0, 0, time.UTC) })
	return store, path
}

func batchTestInput() NativeBindingBatchInput {
	return NativeBindingBatchInput{
		APIKeys:          []string{batchTestKeyA, batchTestKeyB, batchTestKeyC},
		SelectedIndices:  []int{0, 1},
		AuthIDs:          []string{"codex-c.json", "codex-d.json"},
		AvailableAuthIDs: []string{"codex-a.json", "codex-b.json", "codex-c.json", "codex-d.json"},
		CatalogComplete:  true,
	}
}

func batchTestCreate(t *testing.T, store *Store, id, key string) NativeKeyBinding {
	t.Helper()
	binding, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: id, Name: "Original " + id, APIKey: key, Enabled: true,
		AuthIDs: []string{"codex-a.json", "codex-b.json"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return binding
}

func batchTestFind(t *testing.T, store *Store, scope string) NativeKeyBinding {
	t.Helper()
	for _, binding := range store.NativeKeyBindingsSnapshot() {
		if binding.CallerScope == scope {
			return binding
		}
	}
	t.Fatalf("binding for requested test scope not found")
	return NativeKeyBinding{}
}

func batchTestApply(t *testing.T, store *Store, input NativeBindingBatchInput) NativeBindingMutationResult {
	t.Helper()
	preview, err := store.PreviewNativeBindingBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply || preview.Noop || len(preview.Conflicts) != 0 || preview.Revision == "" {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	input.ExpectedRevision = preview.Revision
	result, err := store.ApplyNativeBindingBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation == nil || result.Operation.ID == "" || result.Noop || result.Changed == 0 {
		t.Fatalf("unexpected apply result: %+v", result)
	}
	return result
}

func batchTestRollbackInput(operationID string) NativeBindingRollbackInput {
	input := batchTestInput()
	return NativeBindingRollbackInput{
		OperationID: operationID, APIKeys: input.APIKeys,
		AvailableAuthIDs: input.AvailableAuthIDs, CatalogComplete: true,
	}
}

func batchTestRollback(t *testing.T, store *Store, input NativeBindingRollbackInput) NativeBindingMutationResult {
	t.Helper()
	preview, err := store.PreviewNativeBindingRollback(input)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.CanApply || preview.Noop || len(preview.Conflicts) != 0 {
		t.Fatalf("unexpected rollback preview: %+v", preview)
	}
	input.ExpectedRevision = preview.Revision
	result, err := store.ApplyNativeBindingRollback(input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Operation == nil || result.Operation.ID == "" || result.Noop || result.Changed == 0 {
		t.Fatalf("unexpected rollback result: %+v", result)
	}
	return result
}

func batchTestReadState(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func batchTestAssertUnchanged(t *testing.T, store *Store, path string, bindings []NativeKeyBinding, history []NativeBindingOperation, disk []byte) {
	t.Helper()
	if got := store.NativeKeyBindingsSnapshot(); !reflect.DeepEqual(got, bindings) {
		t.Fatalf("rejected or read-only operation changed bindings: got %+v, want %+v", got, bindings)
	}
	if got := store.NativeBindingHistorySnapshot(); !reflect.DeepEqual(got, history) {
		t.Fatalf("rejected or read-only operation changed history: got %+v, want %+v", got, history)
	}
	if !bytes.Equal(batchTestReadState(t, path), disk) {
		t.Fatal("rejected or read-only operation changed durable state")
	}
}

func TestNativeBindingBatchMixedSelectionPreservesPolicyAndUsage(t *testing.T) {
	store, path := batchTestStore(t)
	first, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "existing", Name: "Translation key", APIKey: batchTestKeyA,
		Enabled: true, RoundRobin: true, AuthIDs: []string{"codex-a.json"},
		RPM: intPtr(2), DailyUSD: floatPtr(9), WeeklyUSD: floatPtr(31),
		ModelAccess: &NativeModelAccessPolicy{Mode: NativeModelAccessAllowlist, Models: []NativeAllowedModel{{Provider: "codex", Model: "spark-xxx"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unselected := batchTestCreate(t, store, "unselected", batchTestKeyC)
	orphan := batchTestCreate(t, store, "orphan", batchTestKeyD)
	if err := store.UpsertModelPrice(ModelPrice{ID: "spark-xxx", InputPricePerMillion: 1}); err != nil {
		t.Fatal(err)
	}
	store.RecordUsage(batchTestKeyA, "spark-xxx", "spark-xxx", false, UsageDetail{InputTokens: 1_000_000})
	if _, limited := store.CheckNativeKeyQuota(first.CallerScope); limited {
		t.Fatal("initial RPM request should pass")
	}
	usageBefore := store.NativeBindingUsage(first)
	if usageBefore.DailyUSDUsed != 1 || usageBefore.DailyCalls != 1 || usageBefore.RPMUsed != 1 {
		t.Fatalf("usage fixture is not exercising quota preservation: %+v", usageBefore)
	}
	var callbacks int
	store.SetOnNativeKeyBindingsChanged(func() {
		callbacks++
		if len(store.NativeKeyBindingsSnapshot()) != 4 {
			t.Error("callback observed a partially applied batch")
		}
	})
	result := batchTestApply(t, store, batchTestInput())
	if result.Changed != 2 || len(result.Operation.Changes) != 2 || callbacks != 1 {
		t.Fatalf("batch must publish once: result=%+v callbacks=%d", result, callbacks)
	}
	after := batchTestFind(t, store, first.CallerScope)
	want := cloneNativeKeyBinding(first)
	want.AuthIDs = []string{"codex-c.json", "codex-d.json"}
	want.Group = encodeNativeAuthIDsGroup(want.AuthIDs)
	want.UpdatedAt = after.UpdatedAt
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("batch changed unrelated binding policy: got %+v, want %+v", after, want)
	}
	if got := store.NativeBindingUsage(after); !reflect.DeepEqual(got, usageBefore) {
		t.Fatalf("batch reset usage or RPM: got %+v, want %+v", got, usageBefore)
	}
	if _, limited := store.CheckNativeKeyQuota(first.CallerScope); limited {
		t.Fatal("second RPM request should pass")
	}
	if decision, limited := store.CheckNativeKeyQuota(first.CallerScope); !limited || decision.Reason != "rpm_exceeded" {
		t.Fatalf("batch reset the RPM window: %+v, limited=%v", decision, limited)
	}
	created := batchTestFind(t, store, NativeCallerScope(batchTestKeyB))
	if !created.Enabled || created.RoundRobin || created.RPM != 0 || created.DailyUSD != 0 || created.WeeklyUSD != 0 || created.ModelAccess.Mode != NativeModelAccessAll || !reflect.DeepEqual(created.AuthIDs, want.AuthIDs) {
		t.Fatalf("new binding does not use baseline defaults: %+v", created)
	}
	if got := batchTestFind(t, store, unselected.CallerScope); !reflect.DeepEqual(got, unselected) {
		t.Fatalf("unselected binding changed: %+v", got)
	}
	if got := batchTestFind(t, store, orphan.CallerScope); !reflect.DeepEqual(got, orphan) {
		t.Fatalf("orphan binding changed: %+v", got)
	}
	reloaded := NewStore()
	if err := reloaded.Configure(Config{Enabled: true, StateFile: path}); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.NativeKeyBindingsSnapshot(); !reflect.DeepEqual(got, store.NativeKeyBindingsSnapshot()) {
		t.Fatalf("durable batch differs after reload: %+v", got)
	}
}

func TestNativeBindingBatchPreservesDisabledBinding(t *testing.T) {
	store, _ := batchTestStore(t)
	first := batchTestCreate(t, store, "disabled", batchTestKeyA)
	if _, err := store.UpdateNativeKeyBinding(first.ID, UpdateNativeKeyBindingInput{Enabled: nativeBoolPtr(false), RoundRobin: nativeBoolPtr(true)}); err != nil {
		t.Fatal(err)
	}
	batchTestApply(t, store, batchTestInput())
	after := batchTestFind(t, store, first.CallerScope)
	if after.Enabled || !after.RoundRobin {
		t.Fatalf("batch changed disabled or round-robin policy: %+v", after)
	}
}

func TestNativeBindingBatchPreviewIsReadOnlyAndPublicJSONIsRedacted(t *testing.T) {
	store, path := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
	input := batchTestInput()
	preview, err := store.PreviewNativeBindingBatch(input)
	if err != nil || !preview.CanApply || len(preview.Changes) != 2 {
		t.Fatalf("preview=%+v err=%v", preview, err)
	}
	batchTestAssertUnchanged(t, store, path, before, history, disk)
	for _, value := range []any{input, preview} {
		batchTestAssertPublicJSON(t, value, input.APIKeys)
	}
	result := batchTestApply(t, store, input)
	rollback := batchTestRollbackInput(result.Operation.ID)
	rollbackPreview, err := store.PreviewNativeBindingRollback(rollback)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{result, store.NativeBindingHistorySnapshot(), rollback, rollbackPreview} {
		batchTestAssertPublicJSON(t, value, input.APIKeys)
	}
	state := batchTestReadState(t, path)
	for _, key := range input.APIKeys {
		if bytes.Contains(state, []byte(key)) {
			t.Fatal("plaintext host API key leaked into persisted history")
		}
	}
}

func batchTestAssertPublicJSON(t *testing.T, value any, keys []string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range keys {
		if bytes.Contains(raw, []byte(key)) || bytes.Contains(raw, []byte(NativeCallerScope(key))) {
			t.Fatalf("public JSON leaks API key or irreversible scope: %s", raw)
		}
	}
	if bytes.Contains(raw, []byte(`"caller_scope"`)) || bytes.Contains(raw, []byte(`"api_keys"`)) {
		t.Fatalf("public JSON exposes private identity fields: %s", raw)
	}
}

func TestNativeBindingBatchSameTargetDoesNotWriteHistory(t *testing.T) {
	store, path := batchTestStore(t)
	input := batchTestInput()
	batchTestApply(t, store, input)
	before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
	var callbacks int
	store.SetOnNativeKeyBindingsChanged(func() { callbacks++ })
	input.AuthIDs = []string{" codex-d.json ", "codex-c.json", "codex-c.json"}
	preview, err := store.PreviewNativeBindingBatch(input)
	if err != nil || !preview.Noop || len(preview.Changes) != 0 || len(preview.Conflicts) != 0 {
		t.Fatalf("same-set preview=%+v err=%v", preview, err)
	}
	input.ExpectedRevision = preview.Revision
	result, err := store.ApplyNativeBindingBatch(input)
	if err != nil || !result.Noop || result.Changed != 0 || result.Operation != nil {
		t.Fatalf("same-set apply=%+v err=%v", result, err)
	}
	batchTestAssertUnchanged(t, store, path, before, history, disk)
	if callbacks != 0 {
		t.Fatalf("no-op emitted %d change callbacks", callbacks)
	}
}

func TestNativeBindingBatchRejectsInvalidSelection(t *testing.T) {
	tests := []struct {
		name string
		edit func(*NativeBindingBatchInput)
	}{
		{"no selected keys", func(in *NativeBindingBatchInput) { in.SelectedIndices = nil }},
		{"negative index", func(in *NativeBindingBatchInput) { in.SelectedIndices = []int{-1} }},
		{"out of range", func(in *NativeBindingBatchInput) { in.SelectedIndices = []int{len(in.APIKeys)} }},
		{"duplicate index", func(in *NativeBindingBatchInput) { in.SelectedIndices = []int{0, 0} }},
		{"empty host list", func(in *NativeBindingBatchInput) { in.APIKeys = nil }},
		{"blank selected key", func(in *NativeBindingBatchInput) { in.APIKeys[0] = " \t " }},
		{"same host key at two indices", func(in *NativeBindingBatchInput) { in.APIKeys[1] = " " + in.APIKeys[0] + " " }},
		{"no target credentials", func(in *NativeBindingBatchInput) { in.AuthIDs = nil }},
		{"blank target credentials", func(in *NativeBindingBatchInput) { in.AuthIDs = []string{" ", "\t"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, path := batchTestStore(t)
			batchTestCreate(t, store, "existing", batchTestKeyA)
			before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
			input := batchTestInput()
			tt.edit(&input)
			if _, err := store.PreviewNativeBindingBatch(input); !errors.Is(err, ErrInvalidNativeBindingBatch) {
				t.Fatalf("preview error=%v, want invalid batch", err)
			}
			input.ExpectedRevision = "not-a-valid-revision"
			if _, err := store.ApplyNativeBindingBatch(input); err == nil {
				t.Fatal("invalid selection was applied")
			}
			batchTestAssertUnchanged(t, store, path, before, history, disk)
		})
	}
}

func TestNativeBindingBatchRejectsOversizedInputAndExpansion(t *testing.T) {
	tests := []struct {
		name string
		edit func(*NativeBindingBatchInput)
	}{
		{"too many host keys", func(in *NativeBindingBatchInput) { in.APIKeys = make([]string, 4097) }},
		{"too many selected keys", func(in *NativeBindingBatchInput) { in.SelectedIndices = make([]int, 4097) }},
		{"too many credentials", func(in *NativeBindingBatchInput) { in.AuthIDs = make([]string, 16385) }},
		{"overlong credential ID", func(in *NativeBindingBatchInput) { in.AuthIDs = []string{strings.Repeat("a", 4097)} }},
		{"nul in host key", func(in *NativeBindingBatchInput) { in.APIKeys[0] += "\x00suffix" }},
		{"nul in credential ID", func(in *NativeBindingBatchInput) { in.AuthIDs = []string{"codex-a.json\x00suffix"} }},
		{"aggregate input budget", func(in *NativeBindingBatchInput) {
			in.APIKeys = make([]string, 600)
			for i := range in.APIKeys {
				in.APIKeys[i] = strings.Repeat("k", 4000) + fmt.Sprint(i)
			}
		}},
		{"small request expands beyond history budget", func(in *NativeBindingBatchInput) {
			in.APIKeys = make([]string, 256)
			in.SelectedIndices = make([]int, len(in.APIKeys))
			for i := range in.APIKeys {
				in.APIKeys[i] = fmt.Sprintf("sk-batch-expansion-%04d", i)
				in.SelectedIndices[i] = i
			}
			in.AuthIDs = make([]string, 256)
			for i := range in.AuthIDs {
				in.AuthIDs[i] = strings.Repeat("a", 128) + fmt.Sprint(i)
			}
			in.AvailableAuthIDs = append([]string(nil), in.AuthIDs...)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, path := batchTestStore(t)
			before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
			input := batchTestInput()
			tt.edit(&input)
			if _, err := store.PreviewNativeBindingBatch(input); !errors.Is(err, ErrInvalidNativeBindingBatch) {
				t.Fatalf("unsafe-size preview error=%v, want invalid batch", err)
			}
			batchTestAssertUnchanged(t, store, path, before, history, disk)
		})
	}
}

func TestNativeBindingBatchCatalogConflictIsAtomic(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		name := "missing selected credential"
		if incomplete {
			name = "incomplete catalog"
		}
		t.Run(name, func(t *testing.T) {
			store, path := batchTestStore(t)
			batchTestCreate(t, store, "existing", batchTestKeyA)
			before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
			input := batchTestInput()
			if incomplete {
				input.CatalogComplete = false
			} else {
				input.AvailableAuthIDs = []string{"codex-a.json", "codex-b.json", "codex-c.json"}
			}
			preview, err := store.PreviewNativeBindingBatch(input)
			if err != nil || preview.CanApply || len(preview.Conflicts) == 0 {
				t.Fatalf("conflict preview=%+v err=%v", preview, err)
			}
			input.ExpectedRevision = preview.Revision
			if _, err := store.ApplyNativeBindingBatch(input); !errors.Is(err, ErrNativeBindingBatchConflict) {
				t.Fatalf("apply error=%v, want batch conflict", err)
			}
			batchTestAssertUnchanged(t, store, path, before, history, disk)
		})
	}
}

func TestNativeBindingBatchRequiresCurrentPreviewRevision(t *testing.T) {
	tests := []struct {
		name   string
		change func(*testing.T, *Store, *NativeBindingBatchInput)
	}{
		{"missing revision", func(_ *testing.T, _ *Store, in *NativeBindingBatchInput) { in.ExpectedRevision = "" }},
		{"different revision", func(_ *testing.T, _ *Store, in *NativeBindingBatchInput) { in.ExpectedRevision = "stale" }},
		{"host key order changed", func(_ *testing.T, _ *Store, in *NativeBindingBatchInput) {
			in.APIKeys[0], in.APIKeys[1] = in.APIKeys[1], in.APIKeys[0]
		}},
		{"host key added", func(_ *testing.T, _ *Store, in *NativeBindingBatchInput) {
			in.APIKeys = append(in.APIKeys, batchTestKeyD)
		}},
		{"catalog changed", func(_ *testing.T, _ *Store, in *NativeBindingBatchInput) {
			in.AvailableAuthIDs = append(in.AvailableAuthIDs, "codex-e.json")
		}},
		{"target changed", func(_ *testing.T, _ *Store, in *NativeBindingBatchInput) { in.AuthIDs = []string{"codex-a.json"} }},
		{"selection changed", func(_ *testing.T, _ *Store, in *NativeBindingBatchInput) { in.SelectedIndices = []int{0} }},
		{"binding edited", func(t *testing.T, s *Store, _ *NativeBindingBatchInput) {
			if _, err := s.UpdateNativeKeyBinding("existing", UpdateNativeKeyBindingInput{Name: nativeStringPtr("Edited separately")}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, path := batchTestStore(t)
			batchTestCreate(t, store, "existing", batchTestKeyA)
			input := batchTestInput()
			preview, err := store.PreviewNativeBindingBatch(input)
			if err != nil {
				t.Fatal(err)
			}
			input.ExpectedRevision = preview.Revision
			tt.change(t, store, &input)
			before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
			if _, err := store.ApplyNativeBindingBatch(input); !errors.Is(err, ErrNativeBindingRevisionMismatch) {
				t.Fatalf("apply error=%v, want stale revision", err)
			}
			batchTestAssertUnchanged(t, store, path, before, history, disk)
		})
	}
}

func TestNativeBindingBatchPersistenceFailurePublishesNothing(t *testing.T) {
	store, path := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	input := batchTestInput()
	preview, err := store.PreviewNativeBindingBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = preview.Revision
	before, history := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot()
	var callbacks int
	store.SetOnNativeKeyBindingsChanged(func() { callbacks++ })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyNativeBindingBatch(input); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("apply error=%v, want persistence failure", err)
	}
	if !reflect.DeepEqual(store.NativeKeyBindingsSnapshot(), before) || !reflect.DeepEqual(store.NativeBindingHistorySnapshot(), history) || callbacks != 0 {
		t.Fatal("failed persistence published policy, history, or a change callback")
	}
}

func TestNativeBindingRollbackAndRedoPreserveLaterPolicy(t *testing.T) {
	store, _ := batchTestStore(t)
	original := batchTestCreate(t, store, "existing", batchTestKeyA)
	result := batchTestApply(t, store, batchTestInput())
	created := batchTestFind(t, store, NativeCallerScope(batchTestKeyB))
	if _, err := store.UpdateNativeKeyBinding(original.ID, UpdateNativeKeyBindingInput{
		Name: nativeStringPtr("New display name"), Enabled: nativeBoolPtr(false), RoundRobin: nativeBoolPtr(true),
		RPM: intPtr(77), DailyUSD: floatPtr(13), WeeklyUSD: floatPtr(53),
		ModelAccess: &NativeModelAccessPolicy{Mode: NativeModelAccessAllowlist, Models: []NativeAllowedModel{{Provider: "codex", Model: "spark-xxx"}}},
	}); err != nil {
		t.Fatal(err)
	}
	policyAfterEdit := batchTestFind(t, store, original.CallerScope)
	rolled := batchTestRollback(t, store, batchTestRollbackInput(result.Operation.ID))
	if rolled.Changed != 2 || rolled.Operation.SourceOperationID != result.Operation.ID {
		t.Fatalf("rollback lost operation link or changes: %+v", rolled)
	}
	if len(store.NativeKeyBindingsSnapshot()) != 1 {
		t.Fatal("rollback did not remove the batch-created binding")
	}
	afterRollback := batchTestFind(t, store, original.CallerScope)
	want := policyAfterEdit
	want.AuthIDs, want.Group, want.UpdatedAt = original.AuthIDs, original.Group, afterRollback.UpdatedAt
	if !reflect.DeepEqual(afterRollback, want) {
		t.Fatalf("rollback reverted unrelated later policy: got %+v, want %+v", afterRollback, want)
	}
	redone := batchTestRollback(t, store, batchTestRollbackInput(rolled.Operation.ID))
	if redone.Changed != 2 || redone.Operation.SourceOperationID != rolled.Operation.ID {
		t.Fatalf("redo lost operation link or changes: %+v", redone)
	}
	afterRedo := batchTestFind(t, store, original.CallerScope)
	want = policyAfterEdit
	want.UpdatedAt = afterRedo.UpdatedAt
	if !reflect.DeepEqual(afterRedo, want) {
		t.Fatalf("redo reverted unrelated later policy: got %+v, want %+v", afterRedo, want)
	}
	recreated := batchTestFind(t, store, created.CallerScope)
	if recreated.ID != created.ID || !recreated.Enabled || recreated.RoundRobin || recreated.RPM != 0 || recreated.DailyUSD != 0 || recreated.WeeklyUSD != 0 || recreated.ModelAccess.Mode != NativeModelAccessAll {
		t.Fatalf("redo did not recreate the baseline binding safely: %+v", recreated)
	}
	history := store.NativeBindingHistorySnapshot()
	if len(history) != 3 || history[0].ID != redone.Operation.ID || history[1].ID != rolled.Operation.ID || history[2].ID != result.Operation.ID {
		t.Fatalf("history is not newest first: %+v", history)
	}
}

func TestNativeBindingRollbackAlreadyRestoredIsNoop(t *testing.T) {
	store, path := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	result := batchTestApply(t, store, batchTestInput())
	rollback := batchTestRollbackInput(result.Operation.ID)
	batchTestRollback(t, store, rollback)
	before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
	preview, err := store.PreviewNativeBindingRollback(rollback)
	if err != nil || !preview.Noop || len(preview.Changes) != 0 || len(preview.Conflicts) != 0 {
		t.Fatalf("repeated rollback preview=%+v err=%v", preview, err)
	}
	rollback.ExpectedRevision = preview.Revision
	repeated, err := store.ApplyNativeBindingRollback(rollback)
	if err != nil || !repeated.Noop || repeated.Changed != 0 || repeated.Operation != nil {
		t.Fatalf("repeated rollback result=%+v err=%v", repeated, err)
	}
	batchTestAssertUnchanged(t, store, path, before, history, disk)
}

func TestNativeBindingRollbackCreatedBindingPolicyEditsConflict(t *testing.T) {
	tests := []struct {
		name  string
		input UpdateNativeKeyBindingInput
	}{
		{"name", UpdateNativeKeyBindingInput{Name: nativeStringPtr("Changed name")}},
		{"enabled", UpdateNativeKeyBindingInput{Enabled: nativeBoolPtr(false)}},
		{"round robin", UpdateNativeKeyBindingInput{RoundRobin: nativeBoolPtr(true)}},
		{"RPM", UpdateNativeKeyBindingInput{RPM: intPtr(12)}},
		{"daily quota", UpdateNativeKeyBindingInput{DailyUSD: floatPtr(1)}},
		{"weekly quota", UpdateNativeKeyBindingInput{WeeklyUSD: floatPtr(7)}},
		{"model policy", UpdateNativeKeyBindingInput{ModelAccess: &NativeModelAccessPolicy{Mode: NativeModelAccessAllowlist, Models: []NativeAllowedModel{{Provider: "codex", Model: "spark-xxx"}}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, path := batchTestStore(t)
			batchTestCreate(t, store, "existing", batchTestKeyA)
			result := batchTestApply(t, store, batchTestInput())
			created := batchTestFind(t, store, NativeCallerScope(batchTestKeyB))
			if _, err := store.UpdateNativeKeyBinding(created.ID, tt.input); err != nil {
				t.Fatal(err)
			}
			batchTestAssertRollbackConflict(t, store, path, batchTestRollbackInput(result.Operation.ID))
		})
	}
}

func batchTestAssertRollbackConflict(t *testing.T, store *Store, path string, input NativeBindingRollbackInput) {
	t.Helper()
	before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
	preview, err := store.PreviewNativeBindingRollback(input)
	if err != nil || preview.CanApply || preview.Noop || len(preview.Conflicts) == 0 {
		t.Fatalf("rollback should expose conflict: preview=%+v err=%v", preview, err)
	}
	input.ExpectedRevision = preview.Revision
	if _, err := store.ApplyNativeBindingRollback(input); !errors.Is(err, ErrNativeBindingBatchConflict) {
		t.Fatalf("rollback error=%v, want conflict", err)
	}
	batchTestAssertUnchanged(t, store, path, before, history, disk)
}

func TestNativeBindingRollbackRejectsChangedIdentityOrCredential(t *testing.T) {
	tests := []struct {
		name string
		edit func(*testing.T, *Store, *NativeBindingRollbackInput)
	}{
		{"host key removed", func(_ *testing.T, _ *Store, in *NativeBindingRollbackInput) {
			in.APIKeys = []string{batchTestKeyB, batchTestKeyC}
		}},
		{"host key rotated", func(_ *testing.T, _ *Store, in *NativeBindingRollbackInput) { in.APIKeys[0] = batchTestKeyD }},
		{"binding rotated", func(t *testing.T, s *Store, _ *NativeBindingRollbackInput) {
			if _, err := s.UpdateNativeKeyBinding("existing", UpdateNativeKeyBindingInput{APIKey: batchTestKeyD}); err != nil {
				t.Fatal(err)
			}
		}},
		{"existing binding deleted", func(t *testing.T, s *Store, _ *NativeBindingRollbackInput) {
			if err := s.DeleteNativeKeyBinding("existing"); err != nil {
				t.Fatal(err)
			}
		}},
		{"restriction changed", func(t *testing.T, s *Store, _ *NativeBindingRollbackInput) {
			if _, err := s.UpdateNativeKeyBinding("existing", UpdateNativeKeyBindingInput{AuthIDs: nativeStringsPtr("codex-b.json")}); err != nil {
				t.Fatal(err)
			}
		}},
		{"original credential deleted", func(_ *testing.T, _ *Store, in *NativeBindingRollbackInput) {
			in.AvailableAuthIDs = []string{"codex-b.json", "codex-c.json", "codex-d.json"}
		}},
		{"catalog incomplete", func(_ *testing.T, _ *Store, in *NativeBindingRollbackInput) { in.CatalogComplete = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, path := batchTestStore(t)
			batchTestCreate(t, store, "existing", batchTestKeyA)
			result := batchTestApply(t, store, batchTestInput())
			rollback := batchTestRollbackInput(result.Operation.ID)
			tt.edit(t, store, &rollback)
			batchTestAssertRollbackConflict(t, store, path, rollback)
		})
	}
}

func TestNativeBindingRollbackOlderOperationRestoresBeforeKnownLaterBatch(t *testing.T) {
	store, _ := batchTestStore(t)
	original := batchTestCreate(t, store, "existing", batchTestKeyA)
	input := batchTestInput()
	input.SelectedIndices = []int{0}
	first := batchTestApply(t, store, input)
	input.AuthIDs = []string{"codex-b.json"}
	batchTestApply(t, store, input)
	batchTestRollback(t, store, batchTestRollbackInput(first.Operation.ID))
	after := batchTestFind(t, store, original.CallerScope)
	if !reflect.DeepEqual(after.AuthIDs, original.AuthIDs) || after.Group != original.Group {
		t.Fatalf("earlier batch rollback did not restore its original restriction: %+v", after)
	}
}

func TestNativeBindingRollbackOriginalCreationAfterLaterBatches(t *testing.T) {
	store, _ := batchTestStore(t)
	input := batchTestInput()
	first := batchTestApply(t, store, input)
	input.AuthIDs = []string{"codex-b.json"}
	batchTestApply(t, store, input)
	input.APIKeys = append(input.APIKeys, batchTestKeyD)
	input.SelectedIndices = []int{3}
	batchTestApply(t, store, input)
	newKeyBinding := batchTestFind(t, store, NativeCallerScope(batchTestKeyD))
	rollback := batchTestRollbackInput(first.Operation.ID)
	rollback.APIKeys = input.APIKeys
	result := batchTestRollback(t, store, rollback)
	if result.Changed != 2 {
		t.Fatalf("original creation rollback changed %d keys, want 2", result.Changed)
	}
	bindings := store.NativeKeyBindingsSnapshot()
	if len(bindings) != 1 || !reflect.DeepEqual(bindings[0], newKeyBinding) {
		t.Fatalf("rollback touched a key introduced by a later batch: %+v", bindings)
	}
}

func TestNativeBindingRollbackRedoRestoresMarkersForKnownLaterBatches(t *testing.T) {
	store, _ := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	batchTestCreate(t, store, "unrelated", batchTestKeyC)
	input := batchTestInput()
	input.SelectedIndices = []int{2}
	input.AuthIDs = []string{"codex-d.json"}
	previous := batchTestApply(t, store, input)
	previousUndo := batchTestRollback(t, store, batchTestRollbackInput(previous.Operation.ID))
	input.SelectedIndices = []int{0}
	input.AuthIDs = []string{"codex-b.json"}
	first := batchTestApply(t, store, input)
	input.AuthIDs = []string{"codex-c.json"}
	second := batchTestApply(t, store, input)
	rollback := batchTestRollback(t, store, batchTestRollbackInput(first.Operation.ID))
	assertMarkers := func(firstReverted, secondReverted bool) {
		t.Helper()
		history := store.NativeBindingHistorySnapshot()
		found := make(map[string]NativeBindingOperation, len(history))
		for _, operation := range history {
			found[operation.ID] = operation
		}
		if (found[first.Operation.ID].RevertedBy != "") != firstReverted || (found[second.Operation.ID].RevertedBy != "") != secondReverted {
			t.Fatalf("earlier/later batch markers disagree: first=%+v second=%+v", found[first.Operation.ID], found[second.Operation.ID])
		}
		if found[previous.Operation.ID].RevertedBy != previousUndo.Operation.ID {
			t.Fatalf("pre-existing unrelated rollback marker changed: %+v", found[previous.Operation.ID])
		}
	}
	assertMarkers(true, true)
	redo := batchTestRollback(t, store, batchTestRollbackInput(rollback.Operation.ID))
	assertMarkers(false, false)
	for _, operation := range store.NativeBindingHistorySnapshot() {
		if operation.Kind == "batch" && operation.RevertedBy == "" {
			if operation.ID != second.Operation.ID {
				t.Fatalf("undo-last would choose %q, want latest active batch %q", operation.ID, second.Operation.ID)
			}
			break
		}
	}
	undoRedo := batchTestRollback(t, store, batchTestRollbackInput(redo.Operation.ID))
	assertMarkers(true, true)
	batchTestRollback(t, store, batchTestRollbackInput(undoRedo.Operation.ID))
	assertMarkers(false, false)
	after := batchTestFind(t, store, NativeCallerScope(batchTestKeyA))
	if !reflect.DeepEqual(after.AuthIDs, []string{"codex-c.json"}) {
		t.Fatalf("marker round trip did not restore latest restriction: %+v", after)
	}
}

func TestNativeBindingRollbackEarlierBatchStillRejectsManualRestriction(t *testing.T) {
	store, path := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	input := batchTestInput()
	input.SelectedIndices = []int{0}
	first := batchTestApply(t, store, input)
	input.AuthIDs = []string{"codex-b.json"}
	batchTestApply(t, store, input)
	if _, err := store.UpdateNativeKeyBinding("existing", UpdateNativeKeyBindingInput{AuthIDs: nativeStringsPtr("codex-d.json")}); err != nil {
		t.Fatal(err)
	}
	batchTestAssertRollbackConflict(t, store, path, batchTestRollbackInput(first.Operation.ID))
}

func TestNativeBindingRollbackCreatedBindingMetadataOnlyChangeIsSafe(t *testing.T) {
	store, _ := batchTestStore(t)
	result := batchTestApply(t, store, batchTestInput())
	created := batchTestFind(t, store, NativeCallerScope(batchTestKeyB))
	if _, err := store.UpdateNativeKeyBinding(created.ID, UpdateNativeKeyBindingInput{Name: nativeStringPtr(created.Name)}); err != nil {
		t.Fatal(err)
	}
	batchTestRollback(t, store, batchTestRollbackInput(result.Operation.ID))
	if len(store.NativeKeyBindingsSnapshot()) != 0 {
		t.Fatal("an UpdatedAt-only change prevented safe removal of a batch-created binding")
	}
}

func TestNativeBindingRollbackCannotReviveEarlierBindingInstance(t *testing.T) {
	store, path := batchTestStore(t)
	input := batchTestInput()
	input.SelectedIndices = []int{0}
	first := batchTestApply(t, store, input)
	firstInstance := batchTestFind(t, store, NativeCallerScope(batchTestKeyA))
	firstRemoval := batchTestRollback(t, store, batchTestRollbackInput(first.Operation.ID))
	second := batchTestApply(t, store, input)
	secondInstance := batchTestFind(t, store, NativeCallerScope(batchTestKeyA))
	if firstInstance.ID != secondInstance.ID {
		t.Fatal("fixture must recreate the stable binding ID to exercise instance identity")
	}
	batchTestRollback(t, store, batchTestRollbackInput(second.Operation.ID))
	batchTestAssertRollbackConflict(t, store, path, batchTestRollbackInput(firstRemoval.Operation.ID))
}

func TestNativeBindingRollbackPreservesPostBatchUsageAndRPM(t *testing.T) {
	store, _ := batchTestStore(t)
	original := batchTestCreate(t, store, "existing", batchTestKeyA)
	if _, err := store.UpdateNativeKeyBinding(original.ID, UpdateNativeKeyBindingInput{RPM: intPtr(2)}); err != nil {
		t.Fatal(err)
	}
	input := batchTestInput()
	input.SelectedIndices = []int{0}
	result := batchTestApply(t, store, input)
	store.RecordUsage(batchTestKeyA, "spark-xxx", "spark-xxx", false, UsageDetail{InputTokens: 1})
	if _, limited := store.CheckNativeKeyQuota(original.CallerScope); limited {
		t.Fatal("first quota request should pass")
	}
	before := store.NativeBindingUsage(batchTestFind(t, store, original.CallerScope))
	batchTestRollback(t, store, batchTestRollbackInput(result.Operation.ID))
	after := store.NativeBindingUsage(batchTestFind(t, store, original.CallerScope))
	if !reflect.DeepEqual(after, before) || after.DailyCalls != 1 || after.RPMUsed != 1 {
		t.Fatalf("rollback changed post-batch usage: got %+v, want %+v", after, before)
	}
}

func TestNativeBindingRollbackPersistenceFailurePublishesNothing(t *testing.T) {
	store, path := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	result := batchTestApply(t, store, batchTestInput())
	rollback := batchTestRollbackInput(result.Operation.ID)
	preview, err := store.PreviewNativeBindingRollback(rollback)
	if err != nil {
		t.Fatal(err)
	}
	rollback.ExpectedRevision = preview.Revision
	before, history := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot()
	var callbacks int
	store.SetOnNativeKeyBindingsChanged(func() { callbacks++ })
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyNativeBindingRollback(rollback); !errors.Is(err, ErrNativeKeyBindingPersistence) {
		t.Fatalf("rollback error=%v, want persistence failure", err)
	}
	if !reflect.DeepEqual(store.NativeKeyBindingsSnapshot(), before) || !reflect.DeepEqual(store.NativeBindingHistorySnapshot(), history) || callbacks != 0 {
		t.Fatal("failed rollback persistence published policy, history, or a change callback")
	}
}

func TestNativeBindingRollbackGroupRestoreWarnsAboutCurrentMembership(t *testing.T) {
	store, _ := batchTestStore(t)
	if err := store.UpsertClassifyRule(ClassifyRule{Name: "translation", Field: "email", Pattern: "@example\\.com$", Group: "translation", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	original, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{
		ID: "group", APIKey: batchTestKeyA, Enabled: true, Group: "classify:translation",
	})
	if err != nil {
		t.Fatal(err)
	}
	input := batchTestInput()
	input.SelectedIndices = []int{0}
	result := batchTestApply(t, store, input)
	rollback := batchTestRollbackInput(result.Operation.ID)
	preview, err := store.PreviewNativeBindingRollback(rollback)
	if err != nil || !preview.CanApply || len(preview.Warnings) == 0 {
		t.Fatalf("group rollback must warn about current rule membership: %+v err=%v", preview, err)
	}
	batchTestRollback(t, store, rollback)
	restored := batchTestFind(t, store, original.CallerScope)
	if restored.Group != original.Group || len(restored.AuthIDs) != 0 {
		t.Fatalf("group restriction was not restored: %+v", restored)
	}
}

func TestNativeBindingRollbackRejectsMissingOrDisabledClassification(t *testing.T) {
	for _, disable := range []bool{false, true} {
		name := "deleted rule"
		if disable {
			name = "disabled rule"
		}
		t.Run(name, func(t *testing.T) {
			store, path := batchTestStore(t)
			rule := ClassifyRule{Name: "translation", Field: "email", Pattern: "@example\\.com$", Group: "translation", Enabled: true}
			if err := store.UpsertClassifyRule(rule); err != nil {
				t.Fatal(err)
			}
			if _, err := store.CreateNativeKeyBinding(CreateNativeKeyBindingInput{ID: "group", APIKey: batchTestKeyA, Enabled: true, Group: "classify:translation"}); err != nil {
				t.Fatal(err)
			}
			input := batchTestInput()
			input.SelectedIndices = []int{0}
			result := batchTestApply(t, store, input)
			if disable {
				rule.Enabled = false
				if err := store.UpsertClassifyRule(rule); err != nil {
					t.Fatal(err)
				}
			} else if err := store.DeleteClassifyRule(rule.Name); err != nil {
				t.Fatal(err)
			}
			batchTestAssertRollbackConflict(t, store, path, batchTestRollbackInput(result.Operation.ID))
		})
	}
}

func TestNativeBindingRollbackRequiresCurrentRevisionAndKnownOperation(t *testing.T) {
	store, path := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	result := batchTestApply(t, store, batchTestInput())
	rollback := batchTestRollbackInput(result.Operation.ID)
	preview, err := store.PreviewNativeBindingRollback(rollback)
	if err != nil {
		t.Fatal(err)
	}
	before, history, disk := store.NativeKeyBindingsSnapshot(), store.NativeBindingHistorySnapshot(), batchTestReadState(t, path)
	for _, revision := range []string{"", "stale"} {
		rollback.ExpectedRevision = revision
		if _, err := store.ApplyNativeBindingRollback(rollback); !errors.Is(err, ErrNativeBindingRevisionMismatch) {
			t.Fatalf("rollback revision %q error=%v", revision, err)
		}
	}
	rollback.ExpectedRevision = preview.Revision
	rollback.APIKeys = append(rollback.APIKeys, batchTestKeyD)
	if _, err := store.ApplyNativeBindingRollback(rollback); !errors.Is(err, ErrNativeBindingRevisionMismatch) {
		t.Fatalf("host changes must invalidate rollback preview: %v", err)
	}
	rollback = batchTestRollbackInput("missing-operation")
	if _, err := store.PreviewNativeBindingRollback(rollback); !errors.Is(err, ErrUnknownNativeBindingOperation) {
		t.Fatalf("unknown rollback preview error=%v", err)
	}
	rollback.ExpectedRevision = preview.Revision
	if _, err := store.ApplyNativeBindingRollback(rollback); !errors.Is(err, ErrUnknownNativeBindingOperation) {
		t.Fatalf("unknown rollback apply error=%v", err)
	}
	batchTestAssertUnchanged(t, store, path, before, history, disk)
}

func TestNativeBindingBatchSnapshotsCannotMutateHistory(t *testing.T) {
	store, _ := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	result := batchTestApply(t, store, batchTestInput())
	want, err := json.Marshal(store.NativeBindingHistorySnapshot())
	if err != nil {
		t.Fatal(err)
	}
	result.Operation.Changes[0].After.AuthIDs[0] = "mutated-from-result"
	history := store.NativeBindingHistorySnapshot()
	history[0].Changes[0].After.AuthIDs[0] = "mutated-from-snapshot"
	for i := range history[0].Changes {
		if history[0].Changes[i].Before != nil && len(history[0].Changes[i].Before.AuthIDs) > 0 {
			history[0].Changes[i].Before.AuthIDs[0] = "mutated-before"
		}
	}
	got, err := json.Marshal(store.NativeBindingHistorySnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("history snapshot or mutation result leaks mutable history slices")
	}
	batchTestRollback(t, store, batchTestRollbackInput(history[0].ID))
}

func TestNativeBindingBatchConcurrentApplyPublishesOnlyOnce(t *testing.T) {
	store, _ := batchTestStore(t)
	batchTestCreate(t, store, "existing", batchTestKeyA)
	input := batchTestInput()
	preview, err := store.PreviewNativeBindingBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = preview.Revision
	var callbacks atomic.Int32
	store.SetOnNativeKeyBindingsChanged(func() { callbacks.Add(1) })
	const workers = 12
	start := make(chan struct{})
	errorsCh := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errApply := store.ApplyNativeBindingBatch(input)
			errorsCh <- errApply
		}()
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	var successes, stale int
	for errApply := range errorsCh {
		if errApply == nil {
			successes++
		} else if errors.Is(errApply, ErrNativeBindingRevisionMismatch) {
			stale++
		} else {
			t.Errorf("unexpected concurrent apply error: %v", errApply)
		}
	}
	if successes != 1 || stale != workers-1 || callbacks.Load() != 1 || len(store.NativeBindingHistorySnapshot()) != 1 {
		t.Fatalf("concurrent stale previews applied more than once: successes=%d stale=%d callbacks=%d", successes, stale, callbacks.Load())
	}
}

func TestNativeBindingBatchConcurrentSnapshotsNeverExposePartialPolicy(t *testing.T) {
	store, _ := batchTestStore(t)
	batchTestCreate(t, store, "first", batchTestKeyA)
	batchTestCreate(t, store, "second", batchTestKeyB)
	input := batchTestInput()
	preview, err := store.PreviewNativeBindingBatch(input)
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedRevision = preview.Revision
	store.persistMu.Lock()
	persistenceBlocked := true
	defer func() {
		if persistenceBlocked {
			store.persistMu.Unlock()
		}
	}()
	applyDone := make(chan error, 1)
	go func() {
		_, errApply := store.ApplyNativeBindingBatch(input)
		applyDone <- errApply
	}()
	stop := make(chan struct{})
	readerDone := make(chan struct{})
	beforeSeen := make(chan struct{})
	afterSeen := make(chan struct{})
	violations := make(chan error, 1)
	go func() {
		defer close(readerDone)
		var beforeReads int
		var observedAfter bool
		for {
			snapshot := store.NativeKeyBindingsSnapshot()
			if len(snapshot) != 2 {
				violations <- fmt.Errorf("reader observed %d bindings, want 2", len(snapshot))
				return
			}
			allBefore, allAfter := true, true
			for _, binding := range snapshot {
				allBefore = allBefore && reflect.DeepEqual(binding.AuthIDs, []string{"codex-a.json", "codex-b.json"})
				allAfter = allAfter && reflect.DeepEqual(binding.AuthIDs, []string{"codex-c.json", "codex-d.json"})
			}
			if !allBefore && !allAfter {
				violations <- fmt.Errorf("reader observed a partially published batch: %+v", snapshot)
				return
			}
			if allBefore {
				beforeReads++
				if beforeReads == 32 {
					close(beforeSeen)
				}
			}
			if allAfter && !observedAfter {
				observedAfter = true
				close(afterSeen)
			}
			select {
			case <-stop:
				return
			default:
			}
		}
	}()
	defer func() {
		close(stop)
		<-readerDone
	}()
	select {
	case <-beforeSeen:
	case errSnapshot := <-violations:
		t.Fatal(errSnapshot)
	}
	for _, binding := range store.NativeKeyBindingsSnapshot() {
		if !reflect.DeepEqual(binding.AuthIDs, []string{"codex-a.json", "codex-b.json"}) {
			t.Fatal("policy was published before persistence completed")
		}
	}
	store.persistMu.Unlock()
	persistenceBlocked = false
	if errApply := <-applyDone; errApply != nil {
		t.Fatal(errApply)
	}
	select {
	case <-afterSeen:
	case errSnapshot := <-violations:
		t.Fatal(errSnapshot)
	}
	select {
	case errSnapshot := <-violations:
		t.Fatal(errSnapshot)
	default:
	}
}

func TestNativeBindingBatchHistoryEvictsOldestAtLimit(t *testing.T) {
	store, _ := batchTestStore(t)
	input := batchTestInput()
	input.SelectedIndices = []int{0}
	var firstID, lastID string
	for i := 0; i < 51; i++ {
		input.AuthIDs = []string{"codex-a.json"}
		if i%2 == 1 {
			input.AuthIDs = []string{"codex-b.json"}
		}
		result := batchTestApply(t, store, input)
		if firstID == "" {
			firstID = result.Operation.ID
		}
		lastID = result.Operation.ID
	}
	history := store.NativeBindingHistorySnapshot()
	if len(history) != 50 || history[0].ID != lastID {
		t.Fatalf("bounded history is not newest-first: length=%d latest=%q", len(history), history[0].ID)
	}
	for _, operation := range history {
		if operation.ID == firstID {
			t.Fatal("oldest operation was not evicted")
		}
	}
	if _, err := store.PreviewNativeBindingRollback(batchTestRollbackInput(firstID)); !errors.Is(err, ErrUnknownNativeBindingOperation) {
		t.Fatalf("evicted operation remains restorable: %v", err)
	}
}

func TestNativeBindingBatchAuthIDsKeepExactCase(t *testing.T) {
	store, _ := batchTestStore(t)
	input := batchTestInput()
	input.SelectedIndices = []int{0}
	input.AuthIDs = []string{"Tenant/Codex-A.json"}
	input.AvailableAuthIDs = []string{"tenant/codex-a.json"}
	preview, err := store.PreviewNativeBindingBatch(input)
	if err != nil || preview.CanApply || len(preview.Conflicts) == 0 {
		t.Fatalf("auth IDs must not be matched by lowercase or display name: %+v err=%v", preview, err)
	}
	input.AvailableAuthIDs = append(input.AvailableAuthIDs, "Tenant/Codex-A.json")
	batchTestApply(t, store, input)
	binding := batchTestFind(t, store, NativeCallerScope(batchTestKeyA))
	if len(binding.AuthIDs) != 1 || !strings.EqualFold(binding.AuthIDs[0], input.AuthIDs[0]) || binding.AuthIDs[0] != input.AuthIDs[0] {
		t.Fatalf("exact credential identity changed: %+v", binding.AuthIDs)
	}
}
