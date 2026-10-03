// Package mywhoosh is the source connector for MyWhoosh.
//
// MyWhoosh has no public API. This client talks to the private HTTP API used
// by the MyWhoosh apps, as described in ADR 0002. The API is undocumented and
// may change without notice, so every endpoint lives in this file.
package mywhoosh

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mywhoosh2garmin/internal/activity"
	"mywhoosh2garmin/internal/fit"
)

// Default endpoints of the MyWhoosh private API.
const (
	DefaultLoginURL      = "https://services.mywhoosh.com/http-service/api/login"
	DefaultActivitiesURL = "https://service14.mywhoosh.com/v2/rider/profile/activities"
	DefaultDownloadURL   = "https://service14.mywhoosh.com/v2/rider/profile/download-activity-file"
)

// loginAction is the action code the MyWhoosh apps send with a login request.
const loginAction = 1001

// maxFITSize caps the size of a downloaded FIT file. Indoor rides are a few
// megabytes at most.
const maxFITSize = 64 << 20

// maxErrorBody caps how much of an error response is included in errors.
const maxErrorBody = 512

// Client is a MyWhoosh API client. It implements activity.Source.
// The zero value is not usable; create one with New.
type Client struct {
	HTTP          *http.Client
	LoginURL      string
	ActivitiesURL string
	DownloadURL   string

	email    string
	password string
	deviceID string

	accessToken string
	whooshID    string
}

var _ activity.Source = (*Client)(nil)

// New returns a client for the given account using the default endpoints.
func New(email, password string) *Client {
	return &Client{
		HTTP:          &http.Client{Timeout: 60 * time.Second},
		LoginURL:      DefaultLoginURL,
		ActivitiesURL: DefaultActivitiesURL,
		DownloadURL:   DefaultDownloadURL,
		email:         email,
		password:      password,
		deviceID:      newUUID(),
	}
}

// ErrUnauthorized is returned when MyWhoosh rejects the access token.
var ErrUnauthorized = errors.New("mywhoosh: unauthorized")

type loginRequest struct {
	Username      string `json:"Username"`
	Password      string `json:"Password"`
	Platform      string `json:"Platform"`
	Action        int    `json:"Action"`
	CorrelationID string `json:"CorrelationId"`
	DeviceID      string `json:"DeviceId"`
	Authorization string `json:"Authorization"`
}

type loginResponse struct {
	Success     bool            `json:"Success"`
	Message     string          `json:"Message"`
	AccessToken string          `json:"AccessToken"`
	WhooshID    json.RawMessage `json:"WhooshId"`
}

// Login authenticates with MyWhoosh. List and Download call it on demand, so
// calling it explicitly is optional.
func (c *Client) Login(ctx context.Context) error {
	body := loginRequest{
		Username:      c.email,
		Password:      c.password,
		Platform:      "Android",
		Action:        loginAction,
		CorrelationID: newUUID(),
		DeviceID:      c.deviceID,
	}
	var resp loginResponse
	if err := c.postJSON(ctx, c.LoginURL, "", body, &resp); err != nil {
		return fmt.Errorf("mywhoosh login: %w", err)
	}
	if !resp.Success {
		msg := resp.Message
		if msg == "" {
			msg = "unknown error"
		}
		return fmt.Errorf("mywhoosh login failed: %s", msg)
	}
	whooshID := jsonScalar(resp.WhooshID)
	if resp.AccessToken == "" || whooshID == "" {
		return errors.New("mywhoosh login: response is missing the access token or WhooshId")
	}
	c.accessToken = resp.AccessToken
	c.whooshID = whooshID
	return nil
}

type activitiesRequest struct {
	Page     int    `json:"page"`
	Limit    int    `json:"limit"`
	SortDate string `json:"sortDate"`
}

// List returns up to limit of the most recent activities, newest first.
func (c *Client) List(ctx context.Context, limit int) ([]activity.Activity, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("mywhoosh: limit must be positive, got %d", limit)
	}
	var raw json.RawMessage
	err := c.authorized(ctx, func() error {
		return c.postJSON(ctx, c.ActivitiesURL, c.accessToken,
			activitiesRequest{Page: 1, Limit: limit, SortDate: "DESC"}, &raw)
	})
	if err != nil {
		return nil, fmt.Errorf("mywhoosh list activities: %w", err)
	}
	items, err := extractActivityList(raw)
	if err != nil {
		return nil, fmt.Errorf("mywhoosh list activities: %w", err)
	}
	activities := make([]activity.Activity, 0, len(items))
	for _, item := range items {
		a := parseActivity(item)
		if a.ID == "" {
			continue
		}
		activities = append(activities, a)
	}
	if len(activities) > limit {
		activities = activities[:limit]
	}
	return activities, nil
}

type downloadRequest struct {
	Key    string `json:"key"`
	FileID string `json:"fileId"`
}

type downloadResponse struct {
	Error   bool   `json:"error"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

// Download returns the FIT file of the activity.
func (c *Client) Download(ctx context.Context, a activity.Activity) ([]byte, error) {
	if a.FileRef == "" {
		return nil, fmt.Errorf("mywhoosh: activity %s has no file to download", a.ID)
	}
	var resp downloadResponse
	err := c.authorized(ctx, func() error {
		return c.postJSON(ctx, c.DownloadURL, c.accessToken,
			downloadRequest{Key: c.whooshID, FileID: a.FileRef}, &resp)
	})
	if err != nil {
		return nil, fmt.Errorf("mywhoosh download activity %s: %w", a.ID, err)
	}
	if resp.Error {
		return nil, fmt.Errorf("mywhoosh download activity %s: %s", a.ID, resp.Message)
	}
	if !strings.HasPrefix(resp.Data, "https://") && !strings.HasPrefix(resp.Data, "http://") {
		return nil, fmt.Errorf("mywhoosh download activity %s: response has no download URL", a.ID)
	}

	data, err := c.fetch(ctx, resp.Data)
	if err != nil {
		return nil, fmt.Errorf("mywhoosh download activity %s: %w", a.ID, err)
	}
	if err := fit.Validate(data); err != nil {
		return nil, fmt.Errorf("mywhoosh download activity %s: %w", a.ID, err)
	}
	return data, nil
}

// authorized logs in if needed, runs call, and retries it once after logging
// in again if the access token was rejected.
func (c *Client) authorized(ctx context.Context, call func() error) error {
	if c.accessToken == "" {
		if err := c.Login(ctx); err != nil {
			return err
		}
	}
	err := call()
	if !errors.Is(err, ErrUnauthorized) {
		return err
	}
	if err := c.Login(ctx); err != nil {
		return err
	}
	return call()
}

func (c *Client) postJSON(ctx context.Context, url, token string, in, out any) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return httpError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// fetch downloads a pre-signed URL. It sends no credentials.
func (c *Client) fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFITSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFITSize {
		return nil, fmt.Errorf("file is larger than %d bytes", maxFITSize)
	}
	return data, nil
}

// extractActivityList finds the list of activities in a response. The API has
// returned it as {"data":{"results":[...]}}, {"data":[...]} or a bare list.
func extractActivityList(raw json.RawMessage) ([]map[string]json.RawMessage, error) {
	var list []map[string]json.RawMessage
	if json.Unmarshal(raw, &list) == nil {
		return list, nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Data) == 0 {
		return nil, errors.New("unexpected response format")
	}
	if json.Unmarshal(envelope.Data, &list) == nil {
		return list, nil
	}
	var page struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(envelope.Data, &page); err != nil {
		return nil, errors.New("unexpected response format")
	}
	return page.Results, nil
}

// parseActivity maps a raw activity to the domain type. Field names vary
// between API versions, so several candidates are tried in order.
func parseActivity(item map[string]json.RawMessage) activity.Activity {
	return activity.Activity{
		ID:        firstScalar(item, "id", "_id"),
		Name:      firstScalar(item, "name", "title"),
		StartTime: parseTime(firstScalar(item, "date", "startTime", "createdAt", "timestamp")),
		FileRef:   firstScalar(item, "activityFileId"),
	}
}

func firstScalar(item map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		if v := jsonScalar(item[k]); v != "" {
			return v
		}
	}
	return ""
}

// jsonScalar returns a JSON string or number as text, and "" for anything else.
func jsonScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseTime accepts the date formats seen in the API, including Unix time in
// seconds or milliseconds. It returns the zero time if s cannot be parsed.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			return time.UnixMilli(n).UTC()
		}
		return time.Unix(n, 0).UTC()
	}
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func httpError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// newUUID returns a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand never fails on supported platforms.
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
