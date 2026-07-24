package crux

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

func parseRecord(t *testing.T, name string) *Record {
	t.Helper()
	var resp queryResponse
	if err := json.Unmarshal(loadFixture(t, name), &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", name, err)
	}
	return &resp.Record
}

func TestParseGoodURL(t *testing.T) {
	rec := parseRecord(t, "good_url.json")
	if rec.Key.URL != "https://example.com/" {
		t.Errorf("key url: got %q", rec.Key.URL)
	}
	lcp, ok := rec.Metrics[KeyLCP]
	if !ok {
		t.Fatal("missing LCP")
	}
	if !lcp.Percentiles.P75.Valid || lcp.Percentiles.P75.Value != 2100 {
		t.Errorf("LCP p75: got %+v", lcp.Percentiles.P75)
	}
	if len(lcp.Histogram) != 3 {
		t.Errorf("LCP histogram bins: got %d", len(lcp.Histogram))
	}
}

func TestParseCLSAsString(t *testing.T) {
	rec := parseRecord(t, "good_url.json")
	cls := rec.Metrics[KeyCLS]
	if !cls.Percentiles.P75.Valid {
		t.Fatal("CLS p75 not valid")
	}
	if cls.Percentiles.P75.Value != 0.05 {
		t.Errorf("CLS p75: want 0.05, got %v", cls.Percentiles.P75.Value)
	}
}

func TestP75ValueUnmarshal(t *testing.T) {
	cases := []struct {
		in    string
		value float64
		valid bool
	}{
		{`2500`, 2500, true},
		{`"0.05"`, 0.05, true},
		{`0.1`, 0.1, true},
		{`null`, 0, false},
		{`""`, 0, false},
	}
	for _, c := range cases {
		var p P75Value
		if err := json.Unmarshal([]byte(c.in), &p); err != nil {
			t.Errorf("unmarshal %q: %v", c.in, err)
			continue
		}
		if p.Value != c.value || p.Valid != c.valid {
			t.Errorf("unmarshal %q = {%v %v}, want {%v %v}", c.in, p.Value, p.Valid, c.value, c.valid)
		}
	}
}

func TestGradeBoundaries(t *testing.T) {
	cases := []struct {
		name            string
		p75, good, poor float64
		want            Assessment
	}{
		{"LCP exactly good boundary", 2500, 2500, 4000, Good},
		{"LCP just over good", 2501, 2500, 4000, NeedsImprovement},
		{"LCP exactly poor boundary is NI", 4000, 2500, 4000, NeedsImprovement},
		{"LCP just over poor", 4001, 2500, 4000, Poor},
		{"INP good", 200, 200, 500, Good},
		{"INP poor", 501, 200, 500, Poor},
		{"CLS good boundary", 0.10, 0.10, 0.25, Good},
		{"CLS poor boundary is NI", 0.25, 0.10, 0.25, NeedsImprovement},
		{"CLS poor", 0.26, 0.10, 0.25, Poor},
		{"TTFB good", 800, 800, 1800, Good},
		{"FCP NI", 2000, 1800, 3000, NeedsImprovement},
	}
	for _, c := range cases {
		if got := Grade(c.p75, c.good, c.poor); got != c.want {
			t.Errorf("%s: Grade(%v,%v,%v) = %q, want %q", c.name, c.p75, c.good, c.poor, got, c.want)
		}
	}
}

func TestMetricDefsThresholds(t *testing.T) {
	want := map[string][2]float64{
		KeyLCP:  {2500, 4000},
		KeyINP:  {200, 500},
		KeyCLS:  {0.10, 0.25},
		KeyFCP:  {1800, 3000},
		KeyTTFB: {800, 1800},
	}
	got := map[string][2]float64{}
	for _, d := range MetricDefs {
		got[d.Key] = [2]float64{d.Good, d.Poor}
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s thresholds: got %v, want %v", k, got[k], v)
		}
	}
	// LCP, INP, CLS must be flagged core; FCP, TTFB not.
	core := map[string]bool{}
	for _, d := range MetricDefs {
		core[d.Key] = d.Core
	}
	if !core[KeyLCP] || !core[KeyINP] || !core[KeyCLS] {
		t.Error("LCP/INP/CLS must be core")
	}
	if core[KeyFCP] || core[KeyTTFB] {
		t.Error("FCP/TTFB must not be core")
	}
}

func TestBuildReportGoodDistributionSums(t *testing.T) {
	rec := parseRecord(t, "good_url.json")
	req := QueryRequest{URL: "https://example.com/"}
	rep := BuildReport(req, rec)
	if len(rep.Metrics) != 5 {
		t.Fatalf("want 5 metrics, got %d", len(rep.Metrics))
	}
	for _, m := range rep.Metrics {
		if m.Assessment != Good {
			t.Errorf("%s: want good, got %q", m.Def.Label, m.Assessment)
		}
		sum := m.DistGood + m.DistNI + m.DistPoor
		if sum < 99.9 || sum > 100.1 {
			t.Errorf("%s distribution sum = %.4f, want ~100", m.Def.Label, sum)
		}
	}
	if rep.ExitCode(false) != 0 {
		t.Errorf("all-good exit: want 0, got %d", rep.ExitCode(false))
	}
	if rep.CollectionPeriod != "2024-05-26 to 2024-06-22" {
		t.Errorf("collection period: got %q", rep.CollectionPeriod)
	}
}

func TestBuildReportPoorExit(t *testing.T) {
	rec := parseRecord(t, "poor_url.json")
	rep := BuildReport(QueryRequest{URL: "https://slow.example.com/"}, rec)
	for _, m := range rep.Metrics {
		if m.Def.Core && m.Assessment != Poor {
			t.Errorf("core %s: want poor, got %q", m.Def.Label, m.Assessment)
		}
	}
	if rep.ExitCode(false) != 1 {
		t.Errorf("poor exit: want 1, got %d", rep.ExitCode(false))
	}
	if rep.ExitCode(true) != 1 {
		t.Errorf("poor strict exit: want 1, got %d", rep.ExitCode(true))
	}
}

func TestBuildReportStrictNeedsImprovement(t *testing.T) {
	rec := parseRecord(t, "origin_phone.json")
	rep := BuildReport(QueryRequest{Origin: "https://example.com"}, rec)
	// LCP p75 2600 -> needs-improvement; others good.
	var lcp MetricReport
	for _, m := range rep.Metrics {
		if m.Def.Key == KeyLCP {
			lcp = m
		}
	}
	if lcp.Assessment != NeedsImprovement {
		t.Fatalf("LCP: want needs-improvement, got %q", lcp.Assessment)
	}
	if rep.ExitCode(false) != 0 {
		t.Errorf("NI non-strict exit: want 0, got %d", rep.ExitCode(false))
	}
	if rep.ExitCode(true) != 1 {
		t.Errorf("NI strict exit: want 1, got %d", rep.ExitCode(true))
	}
	if rep.FormFactor != "PHONE" {
		t.Errorf("form factor: got %q", rep.FormFactor)
	}
}

func TestBuildReportMissingMetrics(t *testing.T) {
	rec := parseRecord(t, "desktop_partial.json")
	rep := BuildReport(QueryRequest{URL: "https://example.com/"}, rec)
	present := map[string]bool{}
	for _, m := range rep.Metrics {
		present[m.Def.Key] = m.Present
	}
	if !present[KeyLCP] || !present[KeyCLS] {
		t.Error("LCP and CLS should be present")
	}
	if present[KeyINP] || present[KeyFCP] || present[KeyTTFB] {
		t.Error("INP/FCP/TTFB should be absent in partial fixture")
	}
	// INP absent, LCP+CLS good -> pass.
	if rep.ExitCode(false) != 0 {
		t.Errorf("partial all-good exit: want 0, got %d", rep.ExitCode(false))
	}
}

func TestFormFactorMapping(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"all", "", false},
		{"phone", "PHONE", false},
		{"Desktop", "DESKTOP", false},
		{"TABLET", "TABLET", false},
		{"watch", "", true},
	}
	for _, c := range cases {
		got, err := FormFactor(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("FormFactor(%q): expected error", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("FormFactor(%q) = (%q,%v), want %q", c.in, got, err, c.want)
		}
	}
}

func TestQueryBodyURLvsOrigin(t *testing.T) {
	urlBody, err := QueryRequest{URL: "https://example.com/", FormFactor: "PHONE"}.Body()
	if err != nil {
		t.Fatal(err)
	}
	var um map[string]any
	_ = json.Unmarshal(urlBody, &um)
	if um["url"] != "https://example.com/" {
		t.Errorf("url body: got %v", um["url"])
	}
	if _, ok := um["origin"]; ok {
		t.Error("url query must not carry origin")
	}
	if um["formFactor"] != "PHONE" {
		t.Errorf("formFactor: got %v", um["formFactor"])
	}

	originBody, _ := QueryRequest{Origin: "https://example.com"}.Body()
	var om map[string]any
	_ = json.Unmarshal(originBody, &om)
	if om["origin"] != "https://example.com" {
		t.Errorf("origin body: got %v", om["origin"])
	}
	if _, ok := om["url"]; ok {
		t.Error("origin query must not carry url")
	}
	if _, ok := om["formFactor"]; ok {
		t.Error("combined query must omit formFactor")
	}
}

func TestQueryBodyMetricsFilter(t *testing.T) {
	body, _ := QueryRequest{URL: "https://x/", Metrics: []string{KeyLCP, KeyCLS}}.Body()
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	arr, ok := m["metrics"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("metrics filter: got %v", m["metrics"])
	}
}

func TestNormalizeAndOrigin(t *testing.T) {
	if got, _ := NormalizeURL("example.com/x"); got != "https://example.com/x" {
		t.Errorf("normalize: got %q", got)
	}
	if got, _ := OriginOf("example.com/some/path?q=1"); got != "https://example.com" {
		t.Errorf("origin: got %q", got)
	}
	if _, err := NormalizeURL("ftp://x"); err == nil {
		t.Error("ftp scheme should error")
	}
}

func TestSparkline(t *testing.T) {
	series := []P75Value{
		{Value: 2600, Valid: true},
		{Value: 2500, Valid: true},
		{Value: 2700, Valid: true},
		{Valid: false},
		{Value: 2100, Valid: true},
	}
	got := Sparkline(series)
	runes := []rune(got)
	if len(runes) != 5 {
		t.Fatalf("sparkline length: want 5 runes, got %d (%q)", len(runes), got)
	}
	if runes[3] != ' ' {
		t.Errorf("null entry should render as a gap, got %q", string(runes[3]))
	}
	// Highest value (2700) -> top bar; lowest present (2100) -> bottom bar.
	if runes[2] != '█' {
		t.Errorf("max should be full block, got %q", string(runes[2]))
	}
	if runes[4] != '▁' {
		t.Errorf("min should be lowest block, got %q", string(runes[4]))
	}
	// Flat series -> mid bar, no panic.
	flat := Sparkline([]P75Value{{Value: 5, Valid: true}, {Value: 5, Valid: true}})
	if len([]rune(flat)) != 2 {
		t.Errorf("flat sparkline length wrong: %q", flat)
	}
	if Sparkline(nil) != "" {
		t.Error("nil series should render empty")
	}
}

func TestBuildHistoryReport(t *testing.T) {
	var resp historyResponse
	if err := json.Unmarshal(loadFixture(t, "history.json"), &resp); err != nil {
		t.Fatalf("unmarshal history: %v", err)
	}
	rep := BuildHistoryReport(QueryRequest{URL: "https://example.com/"}, &resp.Record)
	if len(rep.Periods) != 6 {
		t.Fatalf("periods: want 6, got %d", len(rep.Periods))
	}
	if rep.Periods[0] != "2024-05-11" || rep.Periods[5] != "2024-06-22" {
		t.Errorf("period labels: got %v", rep.Periods)
	}
	var inp HistoryMetricReport
	found := false
	for _, m := range rep.Metrics {
		if m.Def.Key == KeyINP {
			inp, found = m, true
		}
	}
	if !found {
		t.Fatal("INP history missing")
	}
	// INP series has a null at index 2 -> gap in sparkline.
	if inp.First.Value != 220 || inp.Last.Value != 170 {
		t.Errorf("INP first/last: got %v/%v", inp.First.Value, inp.Last.Value)
	}
	if !strings.Contains(inp.Sparkline, " ") {
		t.Errorf("INP sparkline should contain a gap for the null week: %q", inp.Sparkline)
	}
}

// --- client layer (httptest, offline) ---

func newTestClient(srv *httptest.Server) *Client {
	c := NewClient("test-key", 5*time.Second)
	c.BaseURL = srv.URL + "/v1"
	return c
}

func TestClientQueryRecordSuccess(t *testing.T) {
	var gotBody map[string]any
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write(loadFixture(t, "good_url.json"))
	}))
	defer srv.Close()

	rec, err := newTestClient(srv).QueryRecord(context.Background(), QueryRequest{URL: "https://example.com/"})
	if err != nil {
		t.Fatalf("QueryRecord: %v", err)
	}
	if gotKey != "test-key" {
		t.Errorf("key not sent as query param: got %q", gotKey)
	}
	if gotBody["url"] != "https://example.com/" {
		t.Errorf("request body url: got %v", gotBody["url"])
	}
	if rec.Metrics[KeyLCP].Percentiles.P75.Value != 2100 {
		t.Errorf("parsed LCP: got %v", rec.Metrics[KeyLCP].Percentiles.P75.Value)
	}
}

func TestClientInsufficientData404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"chrome ux report data not found"}}`))
	}))
	defer srv.Close()

	_, err := newTestClient(srv).QueryRecord(context.Background(), QueryRequest{URL: "https://none.example/"})
	if !errors.Is(err, ErrInsufficientData) {
		t.Errorf("404 should map to ErrInsufficientData, got %v", err)
	}
}

func TestClientAuthError(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			w.Write([]byte(`{"error":{"code":403,"message":"API key not valid"}}`))
		}))
		_, err := newTestClient(srv).QueryRecord(context.Background(), QueryRequest{URL: "https://x/"})
		srv.Close()
		var authErr *AuthError
		if !errors.As(err, &authErr) {
			t.Errorf("status %d should map to AuthError, got %v", code, err)
			continue
		}
		if authErr.Message != "API key not valid" {
			t.Errorf("auth message: got %q", authErr.Message)
		}
	}
}

func TestClientHistorySendsPeriodCount(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write(loadFixture(t, "history.json"))
	}))
	defer srv.Close()

	rec, err := newTestClient(srv).QueryHistoryRecord(context.Background(), QueryRequest{URL: "https://example.com/"}, 12)
	if err != nil {
		t.Fatalf("QueryHistoryRecord: %v", err)
	}
	if gotBody["collectionPeriodCount"].(float64) != 12 {
		t.Errorf("collectionPeriodCount: got %v", gotBody["collectionPeriodCount"])
	}
	if len(rec.CollectionPeriods) != 6 {
		t.Errorf("history periods parsed: got %d", len(rec.CollectionPeriods))
	}
}

func TestToJSONShape(t *testing.T) {
	rec := parseRecord(t, "good_url.json")
	rep := BuildReport(QueryRequest{URL: "https://example.com/"}, rec)
	b, err := json.Marshal(rep.ToJSON(false))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"query", "metrics", "exit_code"} {
		if _, ok := m[k]; !ok {
			t.Errorf("JSON missing top-level key %q", k)
		}
	}
	metrics := m["metrics"].([]any)
	if len(metrics) != 5 {
		t.Errorf("json metrics: want 5, got %d", len(metrics))
	}
	first := metrics[0].(map[string]any)
	for _, k := range []string{"key", "label", "name", "unit", "core", "present", "p75", "assessment", "thresholds", "distribution"} {
		if _, ok := first[k]; !ok {
			t.Errorf("json metric missing key %q", k)
		}
	}
	if m["exit_code"].(float64) != 0 {
		t.Errorf("json exit_code: want 0, got %v", m["exit_code"])
	}
}

func TestFormatValue(t *testing.T) {
	lcp := MetricDefs[0]
	if lcp.FormatValue(2100) != "2100 ms" {
		t.Errorf("ms format: got %q", lcp.FormatValue(2100))
	}
	var cls MetricDef
	for _, d := range MetricDefs {
		if d.Key == KeyCLS {
			cls = d
		}
	}
	if cls.FormatValue(0.05) != "0.05" {
		t.Errorf("cls format: got %q", cls.FormatValue(0.05))
	}
}
