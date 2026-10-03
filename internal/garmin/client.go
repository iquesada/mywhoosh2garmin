package garmin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"

	"mywhoosh2garmin/internal/activity"
)

// DefaultUploadURL is the Connect API endpoint that imports activity files.
const DefaultUploadURL = "https://connectapi.garmin.com/upload-service/upload"

// refreshMargin renews the access token shortly before it expires.
const refreshMargin = 2 * time.Minute

// maxErrorBody caps how much of an error response is included in errors.
const maxErrorBody = 512

// Client uploads activities to Garmin Connect. It implements
// activity.Destination.
type Client struct {
	HTTP      *http.Client
	UploadURL string
	Auth      *Auth
	Store     TokenStore

	tokens *Tokens
}

var _ activity.Destination = (*Client)(nil)

// NewClient returns a client that uses the given auth and token store.
func NewClient(auth *Auth, store TokenStore) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: 2 * time.Minute},
		UploadURL: DefaultUploadURL,
		Auth:      auth,
		Store:     store,
	}
}

// Upload sends a FIT file to Garmin Connect. It returns
// activity.ErrAlreadyExists if Garmin already has the activity.
func (c *Client) Upload(ctx context.Context, filename string, fit []byte) error {
	if err := c.ensureTokens(ctx, false); err != nil {
		return err
	}
	err := c.upload(ctx, filename, fit)
	if !errors.Is(err, errUnauthorized) {
		return err
	}
	// The access token was rejected before its expiry; refresh and retry once.
	if err := c.ensureTokens(ctx, true); err != nil {
		return err
	}
	err = c.upload(ctx, filename, fit)
	if errors.Is(err, errUnauthorized) {
		return ErrLoginRequired
	}
	return err
}

var errUnauthorized = errors.New("garmin: unauthorized")

func (c *Client) upload(ctx context.Context, filename string, fit []byte) error {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(fit); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.UploadURL, &body)
	if err != nil {
		return err
	}
	setAppHeaders(req)
	req.Header.Set("Authorization", "Bearer "+c.tokens.AccessToken)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("garmin upload %s: %w", filename, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusConflict:
		_, _ = io.Copy(io.Discard, resp.Body)
		return activity.ErrAlreadyExists
	case resp.StatusCode == http.StatusUnauthorized:
		_, _ = io.Copy(io.Discard, resp.Body)
		return errUnauthorized
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("garmin upload %s: %w", filename, httpError(resp))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// ensureTokens loads tokens and refreshes them when they are about to expire
// or when force is set.
func (c *Client) ensureTokens(ctx context.Context, force bool) error {
	if c.tokens == nil {
		t, err := c.Store.Load()
		if err != nil {
			return err
		}
		c.tokens = t
	}
	if !force && !c.tokens.Expired(c.Auth.now(), refreshMargin) {
		return nil
	}
	t, err := c.Auth.Refresh(ctx, c.tokens)
	if err != nil {
		return err
	}
	if err := c.Store.Save(t); err != nil {
		return err
	}
	c.tokens = t
	return nil
}

// statusError is an unexpected HTTP response.
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.code, e.body)
}

func httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return &statusError{code: resp.StatusCode, body: strings.TrimSpace(string(body))}
}
