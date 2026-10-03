package garmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"mywhoosh2garmin/internal/fsutil"
)

// TokenStore persists tokens between runs.
type TokenStore interface {
	// Load returns the stored tokens, or ErrLoginRequired if there are none.
	Load() (*Tokens, error)
	Save(*Tokens) error
}

// FileStore keeps tokens in a JSON file that only the current user can read.
type FileStore struct {
	Path string
}

var _ TokenStore = FileStore{}

// Load implements TokenStore.
func (s FileStore) Load() (*Tokens, error) {
	data, err := os.ReadFile(s.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrLoginRequired
	}
	if err != nil {
		return nil, fmt.Errorf("read Garmin tokens: %w", err)
	}
	var t Tokens
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("read Garmin tokens from %s: %w", s.Path, err)
	}
	if t.AccessToken == "" {
		return nil, ErrLoginRequired
	}
	return &t, nil
}

// Save implements TokenStore.
func (s FileStore) Save(t *Tokens) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := fsutil.WriteFileAtomic(s.Path, data, 0o600); err != nil {
		return fmt.Errorf("save Garmin tokens: %w", err)
	}
	return nil
}
