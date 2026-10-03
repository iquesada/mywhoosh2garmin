// Package garmin is the destination connector for Garmin Connect.
//
// Garmin offers no API for personal projects. This package uses the same
// unofficial flow as the Garmin Connect mobile app, as described in ADR 0002:
// a browser sign-in that yields a service ticket, which is exchanged for
// OAuth2 tokens, and the upload service of the Connect API. The flow is
// undocumented and may change without notice, so every endpoint and client
// id lives in this package.
package garmin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Default endpoints and client id of the Garmin sign-in flow.
const (
	DefaultSigninURL = "https://sso.garmin.com/sso/signin"
	DefaultTokenURL  = "https://diauth.garmin.com/di-oauth2-service/oauth/token"

	// DefaultClientID is the public OAuth client id of the Garmin Connect
	// Android app. Garmin rotates it from time to time.
	DefaultClientID = "GARMIN_CONNECT_MOBILE_ANDROID_DI_2025Q2"

	serviceTicketGrant = "https://connectapi.garmin.com/di-oauth2-service/oauth/grant/service_ticket"
	ssoHost            = "https://sso.garmin.com/sso"
)

// Headers that identify requests as coming from the Garmin Connect app.
const (
	appUserAgent    = "GCM-Android-5.23"
	appGarminUA     = "com.garmin.android.apps.connectmobile/5.23; ; Google/sdk_gphone64_arm64/google; Android/33; Dalvik/2.1.0"
	appBuildVersion = "10861"
)

// DefaultLoginTimeout is how long BrowserLogin waits for the user to sign in.
const DefaultLoginTimeout = 5 * time.Minute

// Tokens are the OAuth2 credentials for the Connect API.
type Tokens struct {
	AccessToken      string    `json:"access_token"`
	RefreshToken     string    `json:"refresh_token"`
	ExpiresAt        time.Time `json:"expires_at"`
	RefreshExpiresAt time.Time `json:"refresh_expires_at,omitempty"`
	// ClientID is the client id that issued the tokens. Refresh tokens are
	// bound to it.
	ClientID string `json:"client_id"`
}

// Expired reports whether the access token expires within the given margin.
func (t *Tokens) Expired(now time.Time, margin time.Duration) bool {
	return t.ExpiresAt.IsZero() || !now.Add(margin).Before(t.ExpiresAt)
}

// ErrLoginRequired means there are no usable tokens and the user must run the
// login command.
var ErrLoginRequired = errors.New("garmin: not logged in or session expired; run the login command")

// Auth obtains and refreshes tokens.
type Auth struct {
	HTTP      *http.Client
	SigninURL string
	TokenURL  string
	ClientID  string
	// OpenBrowser opens a URL for the user. If it fails, the URL is printed
	// so the user can open it by hand.
	OpenBrowser func(url string) error
	// Out receives instructions for the user.
	Out io.Writer
	// Now returns the current time. It exists for tests.
	Now func() time.Time
}

// NewAuth returns an Auth with the default endpoints.
func NewAuth(out io.Writer) *Auth {
	return &Auth{
		HTTP:        &http.Client{Timeout: 60 * time.Second},
		SigninURL:   DefaultSigninURL,
		TokenURL:    DefaultTokenURL,
		ClientID:    DefaultClientID,
		OpenBrowser: openBrowser,
		Out:         out,
		Now:         time.Now,
	}
}

// BrowserLogin asks the user to sign in to Garmin in their browser. Garmin
// redirects the browser to a temporary local server with a service ticket,
// which is exchanged for tokens. The user's password never reaches this
// program.
func (a *Auth) BrowserLogin(ctx context.Context) (*Tokens, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultLoginTimeout)
		defer cancel()
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("garmin login: start local callback server: %w", err)
	}
	callbackURL := "http://" + listener.Addr().String() + "/"
	tickets := make(chan string, 1)
	server := &http.Server{
		Handler:           ticketHandler(tickets),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	signinURL := a.signinURL(callbackURL)
	fmt.Fprintln(a.Out, "Sign in to Garmin Connect in your browser.")
	if a.OpenBrowser == nil || a.OpenBrowser(signinURL) != nil {
		fmt.Fprintln(a.Out, "Open this URL to continue:")
	} else {
		fmt.Fprintln(a.Out, "If the browser did not open, use this URL:")
	}
	fmt.Fprintln(a.Out, signinURL)

	select {
	case ticket := <-tickets:
		return a.Exchange(ctx, ticket, callbackURL)
	case <-ctx.Done():
		return nil, fmt.Errorf("garmin login: timed out waiting for the browser sign-in: %w", ctx.Err())
	}
}

func (a *Auth) signinURL(callbackURL string) string {
	q := url.Values{
		"service":                         {callbackURL},
		"source":                          {callbackURL},
		"gauthHost":                       {ssoHost},
		"redirectAfterAccountLoginUrl":    {callbackURL},
		"redirectAfterAccountCreationUrl": {callbackURL},
	}
	return a.SigninURL + "?" + q.Encode()
}

// ticketHandler receives the browser redirect and passes on the ticket.
func ticketHandler(tickets chan<- string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ticket := r.URL.Query().Get("ticket")
		if ticket == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<!doctype html><title>mywhoosh2garmin</title>"+
			"<p>Signed in to Garmin Connect. You can close this tab and return to the terminal.</p>")
		select {
		case tickets <- ticket:
		default: // Keep only the first ticket.
		}
	})
}

// Exchange trades a single-use service ticket for tokens. serviceURL must be
// the service URL the ticket was issued for.
func (a *Auth) Exchange(ctx context.Context, ticket, serviceURL string) (*Tokens, error) {
	form := url.Values{
		"client_id":      {a.ClientID},
		"service_ticket": {ticket},
		"grant_type":     {serviceTicketGrant},
		"service_url":    {serviceURL},
	}
	t, err := a.requestTokens(ctx, a.ClientID, form)
	if err != nil {
		return nil, fmt.Errorf("garmin login: exchange ticket: %w", err)
	}
	return t, nil
}

// Refresh returns new tokens obtained with the refresh token.
func (a *Auth) Refresh(ctx context.Context, old *Tokens) (*Tokens, error) {
	if old.RefreshToken == "" {
		return nil, ErrLoginRequired
	}
	if !old.RefreshExpiresAt.IsZero() && !a.now().Before(old.RefreshExpiresAt) {
		return nil, ErrLoginRequired
	}
	clientID := old.ClientID
	if clientID == "" {
		clientID = a.ClientID
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {clientID},
		"refresh_token": {old.RefreshToken},
	}
	t, err := a.requestTokens(ctx, clientID, form)
	var se *statusError
	if errors.As(err, &se) && (se.code == http.StatusBadRequest || se.code == http.StatusUnauthorized) {
		// Garmin rejected the refresh token: it expired or was revoked.
		return nil, fmt.Errorf("%w (%v)", ErrLoginRequired, err)
	}
	if err != nil {
		return nil, fmt.Errorf("garmin: refresh tokens: %w", err)
	}
	// Garmin may omit the refresh token when it does not rotate it.
	if t.RefreshToken == "" {
		t.RefreshToken = old.RefreshToken
		t.RefreshExpiresAt = old.RefreshExpiresAt
	}
	return t, nil
}

type tokenResponse struct {
	AccessToken           string `json:"access_token"`
	RefreshToken          string `json:"refresh_token"`
	ExpiresIn             int    `json:"expires_in"`
	RefreshTokenExpiresIn int    `json:"refresh_token_expires_in"`
}

func (a *Auth) requestTokens(ctx context.Context, clientID string, form url.Values) (*Tokens, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	setAppHeaders(req)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(clientID+":")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" || tr.ExpiresIn <= 0 {
		return nil, errors.New("token response is missing the access token or its lifetime")
	}
	now := a.now()
	t := &Tokens{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    now.Add(time.Duration(tr.ExpiresIn) * time.Second),
		ClientID:     clientID,
	}
	if tr.RefreshTokenExpiresIn > 0 {
		t.RefreshExpiresAt = now.Add(time.Duration(tr.RefreshTokenExpiresIn) * time.Second)
	}
	return t, nil
}

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func setAppHeaders(req *http.Request) {
	req.Header.Set("User-Agent", appUserAgent)
	req.Header.Set("X-Garmin-User-Agent", appGarminUA)
	req.Header.Set("X-Garmin-Paired-App-Version", appBuildVersion)
	req.Header.Set("X-Garmin-Client-Platform", "Android")
	req.Header.Set("X-App-Ver", appBuildVersion)
	req.Header.Set("X-Lang", "en")
	req.Header.Set("X-GCExperience", "GC5")
}

func openBrowser(u string) error {
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd":
		return exec.Command("xdg-open", u).Start()
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return fmt.Errorf("cannot open a browser on %s", runtime.GOOS)
	}
}
