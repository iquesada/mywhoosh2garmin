package mywhoosh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mywhoosh2garmin/internal/activity"
)

var fitFile = []byte{14, 0x20, 0, 0, 0, 0, 0, 0, '.', 'F', 'I', 'T', 0, 0, 1, 2, 3}

// fakeAPI imitates the MyWhoosh private API.
type fakeAPI struct {
	t            *testing.T
	server       *httptest.Server
	logins       int
	rejectTokens int // number of authorized calls to reject with 401
	activities   string
	downloadData []byte
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		t: t,
		activities: `{"data":{"results":[
			{"id":"b2","name":"Tempo ride","date":"2026-10-02T18:30:00.000Z","activityFileId":"file-b2"},
			{"id":11,"title":"Recovery","startTime":1759300000,"activityFileId":"file-11"},
			{"name":"no id, ignored"}
		]}}`,
		downloadData: fitFile,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", f.login)
	mux.HandleFunc("POST /activities", f.authorized(f.list))
	mux.HandleFunc("POST /download", f.authorized(f.download))
	mux.HandleFunc("GET /s3/file.fit", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("pre-signed download must not carry credentials")
		}
		w.Write(f.downloadData)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAPI) client() *Client {
	c := New("rider@example.com", "secret")
	c.HTTP = f.server.Client()
	c.LoginURL = f.server.URL + "/login"
	c.ActivitiesURL = f.server.URL + "/activities"
	c.DownloadURL = f.server.URL + "/download"
	return c
}

func (f *fakeAPI) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		f.t.Fatalf("decode login: %v", err)
	}
	if req.Password != "secret" {
		w.Write([]byte(`{"Success":false,"Message":"Invalid credentials"}`))
		return
	}
	if req.Action != loginAction || req.DeviceID == "" || req.CorrelationID == "" {
		f.t.Errorf("unexpected login request: %+v", req)
	}
	f.logins++
	w.Write([]byte(`{"Success":true,"AccessToken":"token-` + string(rune('0'+f.logins)) + `","WhooshId":12345}`))
}

func (f *fakeAPI) authorized(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer token-") {
			http.Error(w, "missing token", http.StatusUnauthorized)
			return
		}
		if f.rejectTokens > 0 {
			f.rejectTokens--
			http.Error(w, "expired", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (f *fakeAPI) list(w http.ResponseWriter, r *http.Request) {
	var req activitiesRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.SortDate != "DESC" || req.Limit <= 0 {
		f.t.Errorf("unexpected list request: %+v", req)
	}
	w.Write([]byte(f.activities))
}

func (f *fakeAPI) download(w http.ResponseWriter, r *http.Request) {
	var req downloadRequest
	json.NewDecoder(r.Body).Decode(&req)
	if req.Key != "12345" || req.FileID != "file-b2" {
		f.t.Errorf("unexpected download request: %+v", req)
	}
	json.NewEncoder(w).Encode(downloadResponse{Data: f.server.URL + "/s3/file.fit"})
}

func TestListParsesActivities(t *testing.T) {
	api := newFakeAPI(t)
	got, err := api.client().List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []activity.Activity{
		{ID: "b2", Name: "Tempo ride", StartTime: time.Date(2026, 10, 2, 18, 30, 0, 0, time.UTC), FileRef: "file-b2"},
		{ID: "11", Name: "Recovery", StartTime: time.Unix(1759300000, 0).UTC(), FileRef: "file-11"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d activities, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("activity %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if api.logins != 1 {
		t.Errorf("logins = %d, want 1", api.logins)
	}
}

func TestListAcceptsOtherResponseShapes(t *testing.T) {
	for name, body := range map[string]string{
		"data list": `{"data":[{"id":"x","activityFileId":"f"}]}`,
		"bare list": `[{"id":"x","activityFileId":"f"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			api := newFakeAPI(t)
			api.activities = body
			got, err := api.client().List(context.Background(), 5)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].ID != "x" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestListLogsInAgainWhenTokenIsRejected(t *testing.T) {
	api := newFakeAPI(t)
	c := api.client()
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	api.rejectTokens = 1
	if _, err := c.List(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if api.logins != 2 {
		t.Errorf("logins = %d, want 2", api.logins)
	}
}

func TestLoginFailure(t *testing.T) {
	api := newFakeAPI(t)
	c := api.client()
	c.password = "wrong"
	err := c.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Invalid credentials") {
		t.Fatalf("Login() error = %v, want invalid credentials", err)
	}
}

func TestDownload(t *testing.T) {
	api := newFakeAPI(t)
	data, err := api.client().Download(context.Background(), activity.Activity{ID: "b2", FileRef: "file-b2"})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(fitFile) {
		t.Errorf("downloaded %q, want the FIT file", data)
	}
}

func TestDownloadRejectsNonFITContent(t *testing.T) {
	api := newFakeAPI(t)
	api.downloadData = []byte("<Error>AccessDenied</Error>")
	_, err := api.client().Download(context.Background(), activity.Activity{ID: "b2", FileRef: "file-b2"})
	if err == nil || !strings.Contains(err.Error(), "not a FIT file") {
		t.Fatalf("Download() error = %v, want FIT validation error", err)
	}
}

func TestDownloadRequiresFileRef(t *testing.T) {
	api := newFakeAPI(t)
	if _, err := api.client().Download(context.Background(), activity.Activity{ID: "b2"}); err == nil {
		t.Fatal("Download() without FileRef succeeded")
	}
}

func TestParseTime(t *testing.T) {
	tests := map[string]time.Time{
		"2026-10-02T18:30:00Z":      time.Date(2026, 10, 2, 18, 30, 0, 0, time.UTC),
		"2026-10-02T20:30:00+02:00": time.Date(2026, 10, 2, 18, 30, 0, 0, time.UTC),
		"2026-10-02 18:30:00":       time.Date(2026, 10, 2, 18, 30, 0, 0, time.UTC),
		"1759429800":                time.Unix(1759429800, 0).UTC(),
		"1759429800000":             time.Unix(1759429800, 0).UTC(),
		"":                          {},
		"yesterday":                 {},
	}
	for in, want := range tests {
		if got := parseTime(in); !got.Equal(want) {
			t.Errorf("parseTime(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestNewUUIDFormat(t *testing.T) {
	id := newUUID()
	if len(id) != 36 || id[14] != '4' || strings.Count(id, "-") != 4 {
		t.Fatalf("newUUID() = %q is not a version 4 UUID", id)
	}
}
