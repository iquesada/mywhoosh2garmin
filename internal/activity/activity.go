// Package activity defines the domain types shared by the sync logic and the
// connectors: an activity, where activities come from (Source) and where they
// go (Destination).
package activity

import (
	"context"
	"errors"
	"time"
)

// Activity is a workout recorded by a source platform.
type Activity struct {
	// ID uniquely identifies the activity within its source.
	ID string
	// Name is the human-readable title of the activity, if any.
	Name string
	// StartTime is when the activity started. It is zero if unknown.
	StartTime time.Time
	// FileRef is an opaque reference the source needs to download the
	// activity file. Callers must not interpret it.
	FileRef string
}

// Source lists activities and downloads their FIT files.
type Source interface {
	// List returns up to limit of the most recent activities, newest first.
	List(ctx context.Context, limit int) ([]Activity, error)
	// Download returns the raw FIT file of the activity.
	Download(ctx context.Context, a Activity) ([]byte, error)
}

// Destination receives FIT files.
type Destination interface {
	// Upload sends a FIT file. It returns ErrAlreadyExists when the
	// destination reports that the activity was uploaded before.
	Upload(ctx context.Context, filename string, fit []byte) error
}

// ErrAlreadyExists is returned by a Destination when the activity is already
// there.
var ErrAlreadyExists = errors.New("activity already exists in destination")
