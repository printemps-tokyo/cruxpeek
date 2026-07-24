package crux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// DefaultBaseURL is the CrUX API v1 base. Endpoints are appended as
// ":queryRecord" / ":queryHistoryRecord" on the "records" collection. Verified
// against https://developer.chrome.com/docs/crux/api .
const DefaultBaseURL = "https://chromeuxreport.googleapis.com/v1"

// ErrInsufficientData is returned when CrUX has no aggregated data for the
// requested url/origin (HTTP 404). This is not a failure of cruxpeek: it means
// the page or origin does not meet CrUX's minimum real-user sample threshold.
var ErrInsufficientData = errors.New("not enough real-user data in the Chrome UX Report for this url/origin")

// AuthError is returned for authentication/authorization failures (HTTP 401 or
// 403), typically a missing, invalid, or unauthorized API key, or the "Chrome
// UX Report API" not being enabled on the Google Cloud project.
type AuthError struct {
	StatusCode int
	Message    string
}

func (e *AuthError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("CrUX API authentication failed (HTTP %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("CrUX API authentication failed (HTTP %d)", e.StatusCode)
}

// APIError is returned for any other non-2xx CrUX response (e.g. 400 for a
// malformed request, or 429 when the 150 queries/min quota is exceeded).
type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("CrUX API error (HTTP %d): %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("CrUX API error (HTTP %d)", e.StatusCode)
}

// Client is a thin CrUX API client. It holds no key state in logs or errors:
// the key is only ever sent as the ?key= query parameter and is never printed.
type Client struct {
	HTTPClient *http.Client
	APIKey     string
	BaseURL    string
}

// NewClient builds a Client with the given API key and per-request timeout.
func NewClient(apiKey string, timeout time.Duration) *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: timeout},
		APIKey:     apiKey,
		BaseURL:    DefaultBaseURL,
	}
}

// QueryRecord calls records:queryRecord and returns the parsed record.
func (c *Client) QueryRecord(ctx context.Context, req QueryRequest) (*Record, error) {
	body, err := req.Body()
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, "records:queryRecord", body)
	if err != nil {
		return nil, err
	}
	var resp queryResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decoding CrUX response: %w", err)
	}
	return &resp.Record, nil
}

// QueryHistoryRecord calls records:queryHistoryRecord and returns the parsed
// history record. periods, when > 0, is sent as collectionPeriodCount (CrUX
// accepts 1..40; the default when omitted is 25 weeks).
func (c *Client) QueryHistoryRecord(ctx context.Context, req QueryRequest, periods int) (*HistoryRecord, error) {
	m := map[string]any{}
	if req.Origin != "" {
		m["origin"] = req.Origin
	} else {
		m["url"] = req.URL
	}
	if req.FormFactor != "" {
		m["formFactor"] = req.FormFactor
	}
	if len(req.Metrics) > 0 {
		m["metrics"] = req.Metrics
	}
	if periods > 0 {
		m["collectionPeriodCount"] = periods
	}
	body, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	raw, err := c.post(ctx, "records:queryHistoryRecord", body)
	if err != nil {
		return nil, err
	}
	var resp historyResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decoding CrUX history response: %w", err)
	}
	return &resp.Record, nil
}

// post issues the POST and maps HTTP status to typed errors. The key is passed
// as the ?key= query parameter, exactly as the CrUX API expects.
func (c *Client) post(ctx context.Context, endpoint string, body []byte) ([]byte, error) {
	base := c.BaseURL
	if base == "" {
		base = DefaultBaseURL
	}
	url := fmt.Sprintf("%s/%s?key=%s", base, endpoint, c.APIKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		return raw, nil
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrInsufficientData
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, &AuthError{StatusCode: resp.StatusCode, Message: apiErrorMessage(raw)}
	default:
		return nil, &APIError{StatusCode: resp.StatusCode, Message: apiErrorMessage(raw)}
	}
}

// apiErrorMessage extracts the human-readable message from a Google API error
// body of the shape {"error":{"code":...,"message":"..."}}. It returns "" if
// the body cannot be parsed, so the key (never in that field) is never leaked.
func apiErrorMessage(raw []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return ""
	}
	return e.Error.Message
}
