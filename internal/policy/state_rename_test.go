package policy

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestMigrateLegacyDefaultStateFileCopiesAndPreservesLegacy(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, legacyDefaultStateFileName)
	renamedPath := filepath.Join(dir, defaultStateFileName)
	raw := []byte(`{"version":1,"keys":[],"native_key_bindings":[],"aliases":[],"classify_rules":[],"usage":{}}`)
	if err := os.WriteFile(legacyPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyDefaultStateFile(defaultStateFileName, renamedPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, err := os.ReadFile(renamedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("renamed state changed during copy: got %q want %q", got, raw)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatalf("legacy recovery copy was removed: %v", err)
	}
	info, err := os.Stat(renamedPath)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("renamed state mode = %o, want 600", gotMode)
	}
}

func TestMigrateLegacyDefaultStateFileDoesNotOverwriteRenamedState(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, legacyDefaultStateFileName)
	renamedPath := filepath.Join(dir, defaultStateFileName)
	if err := os.WriteFile(legacyPath, []byte(`{"version":1,"keys":[{"id":"legacy"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"version":1,"keys":[{"id":"renamed"}]}`)
	if err := os.WriteFile(renamedPath, want, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyDefaultStateFile(defaultStateFileName, renamedPath); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, err := os.ReadFile(renamedPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("existing renamed state was overwritten: got %q want %q", got, want)
	}
}

func TestMigrateLegacyDefaultStateFileRejectsCorruptLegacyState(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, legacyDefaultStateFileName)
	renamedPath := filepath.Join(dir, defaultStateFileName)
	if err := os.WriteFile(legacyPath, []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyDefaultStateFile(defaultStateFileName, renamedPath); err == nil {
		t.Fatal("expected corrupt legacy state migration to fail")
	}
	if _, err := os.Stat(renamedPath); !os.IsNotExist(err) {
		t.Fatalf("renamed state should not be created, stat err=%v", err)
	}
}

func TestMigrateLegacyDefaultStateFileSkipsCustomPath(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, legacyDefaultStateFileName)
	renamedPath := filepath.Join(dir, defaultStateFileName)
	if err := os.WriteFile(legacyPath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := migrateLegacyDefaultStateFile("custom-state.json", renamedPath); err != nil {
		t.Fatalf("migrate custom path: %v", err)
	}
	if _, err := os.Stat(renamedPath); !os.IsNotExist(err) {
		t.Fatalf("custom path unexpectedly migrated legacy state, stat err=%v", err)
	}
}
