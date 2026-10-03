package syncer

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mywhoosh2garmin/internal/activity"
)

type fakeSource struct {
	activities []activity.Activity // newest first
	failOn     string
	downloads  []string
}

func (f *fakeSource) List(_ context.Context, limit int) ([]activity.Activity, error) {
	if limit < len(f.activities) {
		return f.activities[:limit], nil
	}
	return f.activities, nil
}

func (f *fakeSource) Download(_ context.Context, a activity.Activity) ([]byte, error) {
	f.downloads = append(f.downloads, a.ID)
	if a.ID == f.failOn {
		return nil, errors.New("download failed")
	}
	return []byte("fit-" + a.ID), nil
}

type fakeDestination struct {
	existing map[string]bool // file names Garmin already has
	uploads  []string
}

func (f *fakeDestination) Upload(_ context.Context, filename string, _ []byte) error {
	if f.existing[filename] {
		return activity.ErrAlreadyExists
	}
	f.uploads = append(f.uploads, filename)
	return nil
}

type fakeState map[string]time.Time

func (f fakeState) IsSynced(id string) bool { _, ok := f[id]; return ok }
func (f fakeState) MarkSynced(id string, at time.Time) error {
	f[id] = at
	return nil
}

func newSyncer(src *fakeSource, dst *fakeDestination, st fakeState) *Syncer {
	return &Syncer{
		Source:      src,
		Destination: dst,
		State:       st,
		Log:         &bytes.Buffer{},
		Now:         func() time.Time { return time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC) },
	}
}

func TestRunUploadsNewActivitiesOldestFirst(t *testing.T) {
	src := &fakeSource{activities: []activity.Activity{{ID: "3"}, {ID: "2"}, {ID: "1"}}}
	dst := &fakeDestination{existing: map[string]bool{"mywhoosh-2.fit": true}}
	st := fakeState{"1": time.Time{}}

	res, err := newSyncer(src, dst, st).Run(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := Result{Uploaded: 1, AlreadySynced: 1, AlreadyInDestination: 1}
	if res != want {
		t.Errorf("Result = %+v, want %+v", res, want)
	}
	if strings.Join(src.downloads, ",") != "2,3" {
		t.Errorf("downloads = %v, want oldest first and skipping synced", src.downloads)
	}
	if strings.Join(dst.uploads, ",") != "mywhoosh-3.fit" {
		t.Errorf("uploads = %v", dst.uploads)
	}
	if !st.IsSynced("2") || !st.IsSynced("3") {
		t.Error("uploaded and duplicate activities must be recorded as synced")
	}
}

func TestRunContinuesAfterFailure(t *testing.T) {
	src := &fakeSource{activities: []activity.Activity{{ID: "2"}, {ID: "1"}}, failOn: "1"}
	dst := &fakeDestination{}
	st := fakeState{}

	res, err := newSyncer(src, dst, st).Run(context.Background(), 10)
	if err == nil {
		t.Fatal("Run() must report the failed activity")
	}
	if res.Failed != 1 || res.Uploaded != 1 {
		t.Errorf("Result = %+v", res)
	}
	if st.IsSynced("1") {
		t.Error("a failed activity must not be marked as synced")
	}
}

func TestRunDryRunChangesNothing(t *testing.T) {
	src := &fakeSource{activities: []activity.Activity{{ID: "1"}}}
	dst := &fakeDestination{}
	st := fakeState{}
	s := newSyncer(src, dst, st)
	s.DryRun = true

	res, err := s.Run(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Uploaded != 1 || len(src.downloads) != 0 || len(dst.uploads) != 0 || len(st) != 0 {
		t.Errorf("dry run had side effects: res=%+v downloads=%v uploads=%v state=%v", res, src.downloads, dst.uploads, st)
	}
}

func TestFilenameSanitizesID(t *testing.T) {
	if got := filename(activity.Activity{ID: "../a b/7"}); got != "mywhoosh-___a_b_7.fit" {
		t.Errorf("filename() = %q", got)
	}
}
