// Package crux holds the pure, testable core of cruxpeek: the Chrome UX
// Report (CrUX) API request/response types, the official Core Web Vitals
// threshold table, the p75 grading and distribution math, the exit-code
// contract, and the history sparkline renderer.
//
// The data types and the grading/report/sparkline logic are plain structs and
// pure functions, so they can be exercised offline from JSON fixtures without
// a network connection or an API key. The thin HTTP client lives in client.go
// and is driven in tests with net/http/httptest.
//
// API reference (request/response shapes verified against the official docs):
//
//	https://developer.chrome.com/docs/crux/api          (records:queryRecord)
//	https://developer.chrome.com/docs/crux/history-api  (records:queryHistoryRecord)
package crux

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// P75Value is a CrUX percentile value. The API encodes most metrics as integer
// milliseconds but cumulative_layout_shift as a JSON string (a unitless double
// rendered to two decimal places), and history timeseries entries may be null
// for weeks without data. P75Value accepts all three forms:
//
//	2500      -> {Value: 2500, Valid: true}
//	"0.05"    -> {Value: 0.05, Valid: true}
//	null / "" -> {Valid: false}
//
// See https://developer.chrome.com/docs/crux/api (CLS "encoded as a string").
type P75Value struct {
	Value float64
	Valid bool
}

// UnmarshalJSON decodes a numeric, string-encoded, or null percentile value.
func (p *P75Value) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		p.Value, p.Valid = 0, false
		return nil
	}
	s = strings.Trim(s, `"`)
	if s == "" {
		p.Value, p.Valid = 0, false
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("parsing p75 value %q: %w", s, err)
	}
	p.Value, p.Valid = v, true
	return nil
}

// RecordKey echoes the queried url or origin and the form factor.
type RecordKey struct {
	FormFactor string `json:"formFactor,omitempty"`
	Origin     string `json:"origin,omitempty"`
	URL        string `json:"url,omitempty"`
}

// HistogramBin is one of the three density bins (good / needs-improvement /
// poor) of a metric histogram. Only Density (a fraction in [0,1]) is needed
// for the distribution breakdown; Start/End are kept as raw values because
// their type varies by metric (integer ms or string for CLS).
type HistogramBin struct {
	Start   json.RawMessage `json:"start,omitempty"`
	End     json.RawMessage `json:"end,omitempty"`
	Density float64         `json:"density"`
}

// Percentiles carries the p75 percentile CrUX reports for a metric.
type Percentiles struct {
	P75 P75Value `json:"p75"`
}

// MetricData is a single metric record: its three-bin histogram and its p75.
type MetricData struct {
	Histogram   []HistogramBin `json:"histogram"`
	Percentiles Percentiles    `json:"percentiles"`
}

// DateYMD is a CrUX collection-period date.
type DateYMD struct {
	Year  int `json:"year"`
	Month int `json:"month"`
	Day   int `json:"day"`
}

// String renders the date as YYYY-MM-DD.
func (d DateYMD) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// CollectionPeriod is the 28-day window a record aggregates over.
type CollectionPeriod struct {
	FirstDate DateYMD `json:"firstDate"`
	LastDate  DateYMD `json:"lastDate"`
}

// Record is the CrUX response record for records:queryRecord.
type Record struct {
	Key              RecordKey             `json:"key"`
	Metrics          map[string]MetricData `json:"metrics"`
	CollectionPeriod *CollectionPeriod     `json:"collectionPeriod,omitempty"`
}

// UrlNormalization reports how CrUX normalized the requested URL, if at all.
type UrlNormalization struct {
	OriginalURL   string `json:"originalUrl"`
	NormalizedURL string `json:"normalizedUrl"`
}

// queryResponse is the top-level records:queryRecord response envelope.
type queryResponse struct {
	Record                  Record            `json:"record"`
	UrlNormalizationDetails *UrlNormalization `json:"urlNormalizationDetails,omitempty"`
}

// HistoryMetric is a metric in a records:queryHistoryRecord response. Only the
// p75 timeseries is consumed; the histogram timeseries is ignored.
type HistoryMetric struct {
	PercentilesTimeseries PercentilesTimeseries `json:"percentilesTimeseries"`
}

// PercentilesTimeseries holds one p75 value per collection period, oldest
// first, aligned with HistoryRecord.CollectionPeriods. Entries may be null.
type PercentilesTimeseries struct {
	P75s []P75Value `json:"p75s"`
}

// HistoryRecord is the CrUX response record for records:queryHistoryRecord.
type HistoryRecord struct {
	Key               RecordKey                `json:"key"`
	Metrics           map[string]HistoryMetric `json:"metrics"`
	CollectionPeriods []CollectionPeriod       `json:"collectionPeriods"`
}

// historyResponse is the top-level records:queryHistoryRecord response envelope.
type historyResponse struct {
	Record                  HistoryRecord     `json:"record"`
	UrlNormalizationDetails *UrlNormalization `json:"urlNormalizationDetails,omitempty"`
}

// Metric API keys, exactly as CrUX names them.
const (
	KeyLCP  = "largest_contentful_paint"
	KeyINP  = "interaction_to_next_paint"
	KeyCLS  = "cumulative_layout_shift"
	KeyFCP  = "first_contentful_paint"
	KeyTTFB = "experimental_time_to_first_byte"
)

// FormFactor maps a user-facing device name to the CrUX formFactor enum. The
// empty string (returned for "" or "all") means the combined aggregate across
// all form factors, which is expressed by omitting formFactor from the request.
func FormFactor(device string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(device)) {
	case "", "all":
		return "", nil
	case "phone":
		return "PHONE", nil
	case "desktop":
		return "DESKTOP", nil
	case "tablet":
		return "TABLET", nil
	default:
		return "", fmt.Errorf("unknown device %q: use phone, desktop, tablet, or all", device)
	}
}

// FormFactorLabel returns a display label for a CrUX formFactor value.
func FormFactorLabel(ff string) string {
	switch ff {
	case "":
		return "All form factors"
	case "PHONE":
		return "Phone"
	case "DESKTOP":
		return "Desktop"
	case "TABLET":
		return "Tablet"
	default:
		return ff
	}
}

// QueryRequest is a resolved CrUX query. Exactly one of URL or Origin is set.
// FormFactor holds an already-mapped CrUX enum value ("" = combined). Metrics
// optionally restricts the response to specific metric keys.
type QueryRequest struct {
	URL        string
	Origin     string
	FormFactor string
	Metrics    []string
}

// IsOrigin reports whether this is an origin-level query.
func (q QueryRequest) IsOrigin() bool { return q.Origin != "" }

// Identifier returns the url or origin being queried, for display.
func (q QueryRequest) Identifier() string {
	if q.Origin != "" {
		return q.Origin
	}
	return q.URL
}

// Body constructs the JSON request body sent to CrUX. It sets either "url" or
// "origin" (never both), and includes "formFactor" and "metrics" only when set.
func (q QueryRequest) Body() ([]byte, error) {
	m := map[string]any{}
	switch {
	case q.Origin != "":
		m["origin"] = q.Origin
	case q.URL != "":
		m["url"] = q.URL
	default:
		return nil, fmt.Errorf("query requires a url or origin")
	}
	if q.FormFactor != "" {
		m["formFactor"] = q.FormFactor
	}
	if len(q.Metrics) > 0 {
		m["metrics"] = q.Metrics
	}
	return json.Marshal(m)
}

// NormalizeURL ensures raw carries an http(s) scheme (defaulting to https) and
// parses to an absolute URL with a host. It is used for the exact-URL query.
func NormalizeURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("empty URL")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported scheme %q: only http and https are supported", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("invalid URL %q: missing host", raw)
	}
	return u.String(), nil
}

// OriginOf returns the scheme://host origin of raw, defaulting the scheme to
// https. It is used for the --origin aggregate query.
func OriginOf(raw string) (string, error) {
	normalized, err := NormalizeURL(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(normalized)
	if err != nil {
		return "", err
	}
	return u.Scheme + "://" + u.Host, nil
}
