package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStorePersistsSyncedIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Len() != 0 || s.IsSynced("a") {
		t.Fatal("a new store must be empty")
	}
	if err := s.MarkSynced("a", time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.IsSynced("a") || reloaded.IsSynced("b") || reloaded.Len() != 1 {
		t.Errorf("reloaded store has wrong contents: %+v", reloaded.synced)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("state file permissions = %o, want 600", perm)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted a corrupt file")
	}
}
