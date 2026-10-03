// Package state records which activities have already been synced, so that
// running the tool repeatedly never uploads an activity twice.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"mywhoosh2garmin/internal/fsutil"
)

// Store is the set of synced activity ids, backed by a JSON file.
type Store struct {
	path   string
	synced map[string]time.Time
}

type fileFormat struct {
	// Synced maps a source activity id to the time it was synced.
	Synced map[string]time.Time `json:"synced"`
}

// Load reads the state file at path. A missing file yields an empty store.
func Load(path string) (*Store, error) {
	s := &Store{path: path, synced: map[string]time.Time{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	var f fileFormat
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("read state from %s: %w", path, err)
	}
	for id, at := range f.Synced {
		s.synced[id] = at
	}
	return s, nil
}

// IsSynced reports whether the activity was already synced.
func (s *Store) IsSynced(id string) bool {
	_, ok := s.synced[id]
	return ok
}

// MarkSynced records the activity as synced at the given time and saves the
// store to disk.
func (s *Store) MarkSynced(id string, at time.Time) error {
	s.synced[id] = at.UTC()
	return s.save()
}

// Len returns the number of synced activities.
func (s *Store) Len() int {
	return len(s.synced)
}

func (s *Store) save() error {
	data, err := json.MarshalIndent(fileFormat{Synced: s.synced}, "", "  ")
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(s.path, data, 0o600); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}
