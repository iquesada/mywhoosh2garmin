package garmin

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mywhoosh2garmin/internal/activity"
)

var testNow = time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)

// fakeGarmin imitates the Garmin token and upload endpoints.
type fakeGarmin struct {
	t      *testing.T
	server *httptest.Server

	tokenRequests  []url.Values
	refreshStatus  int // status for refresh requests; 0 means 200
	uploads        int
	uploadStatus   []int // statuses for successive uploads; default 200
	lastUploadName string
	lastUploadBody []byte
	issued         int
}

func newFakeGarmin(t *testing.T) *fakeGarmin {
	f := &fakeGarmin{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("POST /upload", f.upload)
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeGarmin) auth() *Auth {
	a := NewAuth(io.Discard)
	a.HTTP = f.server.Client()
	a.TokenURL = f.server.URL + "/token"
	a.SigninURL = f.server.URL + "/signin"
	a.Now = func() time.Time { return testNow }
	return a
}

func (f *fakeGarmin) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	f.tokenRequests = append(f.tokenRequests, r.PostForm)
	clientID := r.PostForm.Get("client_id")
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(clientID+":"))
	if r.Header.Get("Authorization") != wantAuth {
		f.t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), wantAuth)
	}
	if r.PostForm.Get("grant_type") == "refresh_token" && f.refreshStatus != 0 {
		http.Error(w, `{"error":"invalid_grant"}`, f.refreshStatus)
		return
	}
	f.issued++
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"access_token":"access-%d","refresh_token":"refresh-%d","expires_in":3600,"refresh_token_expires_in":7776000}`,
		f.issued, f.issued)
}

func (f *fakeGarmin) upload(w http.ResponseWriter, r *http.Request) {
	f.uploads++
	status := http.StatusOK
	if len(f.uploadStatus) > 0 {
		status, f.uploadStatus = f.uploadStatus[0], f.uploadStatus[1:]
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer access-") {
		f.t.Errorf("upload without bearer token: %q", r.Header.Get("Authorization"))
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		f.t.Fatalf("upload has no file part: %v", err)
	}
	f.lastUploadName = header.Filename
	f.lastUploadBody, _ = io.ReadAll(file)
	w.WriteHeader(status)
	fmt.Fprint(w, `{"detailedImportResult":{}}`)
}

// memoryStore is an in-memory TokenStore.
type memoryStore struct {
	tokens *Tokens
	saves  int
}

func (m *memoryStore) Load() (*Tokens, error) {
	if m.tokens == nil {
		return nil, ErrLoginRequired
	}
	t := *m.tokens
	return &t, nil
}

func (m *memoryStore) Save(t *Tokens) error {
	c := *t
	m.tokens = &c
	m.saves++
	return nil
}

func validTokens() *Tokens {
	return &Tokens{
		AccessToken:  "access-0",
		RefreshToken: "refresh-0",
		ExpiresAt:    testNow.Add(time.Hour),
		ClientID:     DefaultClientID,
	}
}

func (f *fakeGarmin) client(store TokenStore) *Client {
	c := NewClient(f.auth(), store)
	c.HTTP = f.server.Client()
	c.UploadURL = f.server.URL + "/upload"
	return c
}

func TestExchange(t *testing.T) {
	g := newFakeGarmin(t)
	tokens, err := g.auth().Exchange(context.Background(), "ST-123", "http://127.0.0.1:5000/")
	if err != nil {
		t.Fatal(err)
	}
	form := g.tokenRequests[0]
	if form.Get("service_ticket") != "ST-123" || form.Get("service_url") != "http://127.0.0.1:5000/" ||
		form.Get("grant_type") != serviceTicketGrant || form.Get("client_id") != DefaultClientID {
		t.Errorf("unexpected exchange form: %v", form)
	}
	want := Tokens{
		AccessToken:      "access-1",
		RefreshToken:     "refresh-1",
		ExpiresAt:        testNow.Add(time.Hour),
		RefreshExpiresAt: testNow.Add(90 * 24 * time.Hour),
		ClientID:         DefaultClientID,
	}
	if *tokens != want {
		t.Errorf("tokens = %+v, want %+v", *tokens, want)
	}
}

func TestBrowserLogin(t *testing.T) {
	g := newFakeGarmin(t)
	a := g.auth()
	a.OpenBrowser = func(signin string) error {
		u, err := url.Parse(signin)
		if err != nil {
			return err
		}
		service := u.Query().Get("service")
		if !strings.HasPrefix(service, "http://127.0.0.1:") {
			t.Errorf("service = %q, want a local callback", service)
		}
		// Simulate Garmin redirecting the browser after sign-in.
		go func() {
			resp, err := http.Get(service + "?ticket=ST-browser")
			if err != nil {
				t.Error(err)
				return
			}
			resp.Body.Close()
		}()
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tokens, err := a.BrowserLogin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "access-1" {
		t.Errorf("AccessToken = %q", tokens.AccessToken)
	}
	if got := g.tokenRequests[0].Get("service_ticket"); got != "ST-browser" {
		t.Errorf("exchanged ticket %q, want ST-browser", got)
	}
}

func TestBrowserLoginTimesOut(t *testing.T) {
	g := newFakeGarmin(t)
	a := g.auth()
	a.OpenBrowser = func(string) error { return errors.New("no browser") }
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := a.BrowserLogin(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("BrowserLogin() error = %v, want deadline exceeded", err)
	}
}

func TestRefreshKeepsRefreshTokenWhenNotRotated(t *testing.T) {
	g := newFakeGarmin(t)
	g.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"access_token":"new-access","expires_in":3600}`)
	})
	old := validTokens()
	old.RefreshExpiresAt = testNow.Add(time.Hour)
	tokens, err := g.auth().Refresh(context.Background(), old)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "new-access" || tokens.RefreshToken != "refresh-0" || !tokens.RefreshExpiresAt.Equal(old.RefreshExpiresAt) {
		t.Errorf("tokens = %+v", tokens)
	}
}

func TestRefreshRejectedRequiresLogin(t *testing.T) {
	g := newFakeGarmin(t)
	g.refreshStatus = http.StatusBadRequest
	_, err := g.auth().Refresh(context.Background(), validTokens())
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Refresh() error = %v, want ErrLoginRequired", err)
	}
}

func TestRefreshExpiredRefreshTokenRequiresLogin(t *testing.T) {
	g := newFakeGarmin(t)
	old := validTokens()
	old.RefreshExpiresAt = testNow.Add(-time.Minute)
	if _, err := g.auth().Refresh(context.Background(), old); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Refresh() error = %v, want ErrLoginRequired", err)
	}
	if len(g.tokenRequests) != 0 {
		t.Error("an expired refresh token must not be sent to Garmin")
	}
}

func TestUpload(t *testing.T) {
	g := newFakeGarmin(t)
	store := &memoryStore{tokens: validTokens()}
	if err := g.client(store).Upload(context.Background(), "ride.fit", []byte("fit-data")); err != nil {
		t.Fatal(err)
	}
	if g.lastUploadName != "ride.fit" || string(g.lastUploadBody) != "fit-data" {
		t.Errorf("uploaded %q with %q", g.lastUploadName, g.lastUploadBody)
	}
	if len(g.tokenRequests) != 0 || store.saves != 0 {
		t.Error("valid tokens must not be refreshed")
	}
}

func TestUploadConflictMeansAlreadyExists(t *testing.T) {
	g := newFakeGarmin(t)
	g.uploadStatus = []int{http.StatusConflict}
	err := g.client(&memoryStore{tokens: validTokens()}).Upload(context.Background(), "ride.fit", []byte("x"))
	if !errors.Is(err, activity.ErrAlreadyExists) {
		t.Fatalf("Upload() error = %v, want ErrAlreadyExists", err)
	}
}

func TestUploadRefreshesExpiredTokens(t *testing.T) {
	g := newFakeGarmin(t)
	expired := validTokens()
	expired.ExpiresAt = testNow.Add(time.Minute) // inside the refresh margin
	store := &memoryStore{tokens: expired}
	if err := g.client(store).Upload(context.Background(), "ride.fit", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if store.saves != 1 || store.tokens.AccessToken != "access-1" {
		t.Errorf("refreshed tokens were not saved: %+v (saves %d)", store.tokens, store.saves)
	}
	if got := g.tokenRequests[0].Get("refresh_token"); got != "refresh-0" {
		t.Errorf("refresh_token = %q", got)
	}
}

func TestUploadRetriesOnceAfterUnauthorized(t *testing.T) {
	g := newFakeGarmin(t)
	g.uploadStatus = []int{http.StatusUnauthorized, http.StatusOK}
	store := &memoryStore{tokens: validTokens()}
	if err := g.client(store).Upload(context.Background(), "ride.fit", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if g.uploads != 2 || store.saves != 1 {
		t.Errorf("uploads = %d, saves = %d; want 2 and 1", g.uploads, store.saves)
	}
}

func TestUploadWithoutSessionRequiresLogin(t *testing.T) {
	g := newFakeGarmin(t)
	err := g.client(&memoryStore{}).Upload(context.Background(), "ride.fit", []byte("x"))
	if !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Upload() error = %v, want ErrLoginRequired", err)
	}
}

func TestUploadServerError(t *testing.T) {
	g := newFakeGarmin(t)
	g.uploadStatus = []int{http.StatusInternalServerError}
	err := g.client(&memoryStore{tokens: validTokens()}).Upload(context.Background(), "ride.fit", []byte("x"))
	if err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("Upload() error = %v, want HTTP 500", err)
	}
}

func TestFileStoreRoundTrip(t *testing.T) {
	store := FileStore{Path: filepath.Join(t.TempDir(), "nested", "tokens.json")}
	if _, err := store.Load(); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("Load() on missing file = %v, want ErrLoginRequired", err)
	}
	want := validTokens()
	if err := store.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpiresAt.Equal(want.ExpiresAt) || got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}
