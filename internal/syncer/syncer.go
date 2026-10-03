// Package syncer copies new activities from a source to a destination.
package syncer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"mywhoosh2garmin/internal/activity"
)

// State records which activities were synced.
type State interface {
	IsSynced(id string) bool
	MarkSynced(id string, at time.Time) error
}

// Syncer copies activities that have not been synced yet.
type Syncer struct {
	Source      activity.Source
	Destination activity.Destination
	State       State
	// Log receives one line per activity. It may be nil.
	Log io.Writer
	// DryRun lists what would be uploaded without downloading or uploading.
	DryRun bool
	// Now returns the current time. It exists for tests.
	Now func() time.Time
}

// Result counts what happened to each activity in a run.
type Result struct {
	// Uploaded activities were sent to the destination (or would be, in a
	// dry run).
	Uploaded int
	// AlreadySynced activities were skipped because the state lists them.
	AlreadySynced int
	// AlreadyInDestination activities were rejected as duplicates by the
	// destination and are now recorded in the state.
	AlreadyInDestination int
	// Failed activities could not be synced; they are retried on the next run.
	Failed int
}

// Run syncs up to limit of the most recent activities. Activities are
// processed oldest first. A failure on one activity does not stop the others;
// Run returns an error if any activity failed.
func (s *Syncer) Run(ctx context.Context, limit int) (Result, error) {
	var res Result
	activities, err := s.Source.List(ctx, limit)
	if err != nil {
		return res, err
	}

	var errs []error
	for i := len(activities) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		a := activities[i]
		if s.State.IsSynced(a.ID) {
			res.AlreadySynced++
			continue
		}
		if s.DryRun {
			s.logf("would upload %s", describe(a))
			res.Uploaded++
			continue
		}

		status, err := s.syncOne(ctx, a)
		switch {
		case err != nil:
			res.Failed++
			s.logf("failed %s: %v", describe(a), err)
			errs = append(errs, fmt.Errorf("activity %s: %w", a.ID, err))
		case status == uploaded:
			res.Uploaded++
			s.logf("uploaded %s", describe(a))
		case status == duplicate:
			res.AlreadyInDestination++
			s.logf("already in destination %s", describe(a))
		}
	}
	if len(errs) > 0 {
		return res, fmt.Errorf("%d of %d activities failed: %w", res.Failed, len(activities), errors.Join(errs...))
	}
	return res, nil
}

type outcome int

const (
	uploaded outcome = iota
	duplicate
)

func (s *Syncer) syncOne(ctx context.Context, a activity.Activity) (outcome, error) {
	data, err := s.Source.Download(ctx, a)
	if err != nil {
		return 0, err
	}
	result := uploaded
	err = s.Destination.Upload(ctx, filename(a), data)
	if errors.Is(err, activity.ErrAlreadyExists) {
		result = duplicate
	} else if err != nil {
		return 0, err
	}
	if err := s.State.MarkSynced(a.ID, s.now()); err != nil {
		return 0, err
	}
	return result, nil
}

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Syncer) logf(format string, args ...any) {
	if s.Log != nil {
		fmt.Fprintf(s.Log, format+"\n", args...)
	}
}

// filename builds the upload file name. The id comes from a remote API, so
// anything other than letters, digits, '-' and '_' is replaced.
func filename(a activity.Activity) string {
	id := []byte(a.ID)
	for i, c := range id {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			id[i] = '_'
		}
	}
	return "mywhoosh-" + string(id) + ".fit"
}

func describe(a activity.Activity) string {
	when := "unknown date"
	if !a.StartTime.IsZero() {
		when = a.StartTime.Local().Format("2006-01-02 15:04")
	}
	if a.Name == "" {
		return fmt.Sprintf("%s (%s)", a.ID, when)
	}
	return fmt.Sprintf("%s %q (%s)", a.ID, a.Name, when)
}
