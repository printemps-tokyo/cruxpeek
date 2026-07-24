package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/printemps-tokyo/cruxpeek/internal/crux"
)

func TestParseArgsDefaults(t *testing.T) {
	cfg, err := parseArgs([]string{"example.com"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if cfg.target != "example.com" {
		t.Errorf("target: got %q", cfg.target)
	}
	if cfg.device != "all" {
		t.Errorf("default device: got %q", cfg.device)
	}
	if cfg.timeout != 15*time.Second {
		t.Errorf("default timeout: got %v", cfg.timeout)
	}
	if cfg.origin || cfg.history || cfg.jsonOut || cfg.strict {
		t.Errorf("unexpected default flags: %+v", cfg)
	}
}

func TestParseArgsHistoryOptionalArg(t *testing.T) {
	// --history with a following number consumes it.
	cfg, err := parseArgs([]string{"--history", "12", "example.com"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !cfg.history || cfg.historyWeeks != 12 || cfg.target != "example.com" {
		t.Errorf("history with weeks parsed wrong: %+v", cfg)
	}

	// --history immediately before the URL leaves weeks at 0 (default).
	cfg, err = parseArgs([]string{"--history", "example.com"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !cfg.history || cfg.historyWeeks != 0 || cfg.target != "example.com" {
		t.Errorf("history without weeks parsed wrong: %+v", cfg)
	}

	// --history=8 form.
	cfg, _ = parseArgs([]string{"--history=8", "example.com"})
	if !cfg.history || cfg.historyWeeks != 8 {
		t.Errorf("history= form parsed wrong: %+v", cfg)
	}
}

func TestParseArgsFlags(t *testing.T) {
	cfg, err := parseArgs([]string{"--origin", "--device", "phone", "--metrics", "largest_contentful_paint,cumulative_layout_shift", "--strict", "--json", "--timeout=5s", "example.com"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !cfg.origin || cfg.device != "phone" || !cfg.strict || !cfg.jsonOut {
		t.Errorf("flags not applied: %+v", cfg)
	}
	if len(cfg.metrics) != 2 {
		t.Errorf("metrics: got %v", cfg.metrics)
	}
	if cfg.timeout != 5*time.Second {
		t.Errorf("timeout: got %v", cfg.timeout)
	}
}

func TestParseArgsBadInputs(t *testing.T) {
	cases := [][]string{
		{"--timeout", "notaduration", "example.com"},
		{"--timeout", "-5s", "example.com"},
		{"--history=0", "example.com"},
		{"--history=99", "example.com"},
		{"--unknown"},
		{"--device"},
		{"a.com", "b.com"},
	}
	for _, args := range cases {
		if _, err := parseArgs(args); err == nil {
			t.Errorf("parseArgs(%v): expected error", args)
		}
	}
}

func TestRunHelpAndVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--help"}, &out, &errb, ""); code != 0 {
		t.Errorf("--help exit: got %d", code)
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Error("--help missing usage")
	}
	out.Reset()
	if code := run([]string{"--version"}, &out, &errb, ""); code != 0 {
		t.Errorf("--version exit: got %d", code)
	}
	if !strings.Contains(out.String(), "cruxpeek") {
		t.Error("--version missing name")
	}
}

func TestRunMissingAPIKey(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"example.com"}, &out, &errb, "")
	if code != 2 {
		t.Errorf("missing key exit: want 2, got %d", code)
	}
	if !strings.Contains(errb.String(), "CRUX_API_KEY is not set") {
		t.Errorf("missing key message not shown: %s", errb.String())
	}
	if strings.Contains(errb.String(), "example.com") == false {
		// The message need not echo the target, but must be actionable.
		if !strings.Contains(errb.String(), "Chrome UX Report API") {
			t.Errorf("missing key message not actionable: %s", errb.String())
		}
	}
}

func TestRunBadArgsExit2(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--device", "watch", "example.com"}, &out, &errb, "k"); code != 2 {
		t.Errorf("bad device exit: want 2, got %d", code)
	}
	if code := run([]string{"ftp://example.com"}, &out, &errb, "k"); code != 2 {
		t.Errorf("bad url exit: want 2, got %d", code)
	}
	if code := run([]string{}, &out, &errb, "k"); code != 2 {
		t.Errorf("missing target exit: want 2, got %d", code)
	}
}

// withTestServer overrides newClient to point at srv and restores it.
func withTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	orig := newClient
	newClient = func(apiKey string, timeout time.Duration) *crux.Client {
		c := orig(apiKey, timeout)
		c.BaseURL = srv.URL + "/v1"
		return c
	}
	t.Cleanup(func() {
		newClient = orig
		srv.Close()
	})
	return srv
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("internal", "crux", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return b
}

func TestRunEndToEndGoodExit0(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture(t, "good_url.json"))
	})
	var out, errb bytes.Buffer
	code := run([]string{"example.com"}, &out, &errb, "test-key")
	if code != 0 {
		t.Fatalf("good exit: want 0, got %d (stderr %s)", code, errb.String())
	}
	s := out.String()
	if !strings.Contains(s, "Core Web Vitals") || !strings.Contains(s, "LCP") {
		t.Errorf("report missing sections: %s", s)
	}
	if !strings.Contains(s, "PASS") {
		t.Errorf("report should say PASS: %s", s)
	}
}

func TestRunEndToEndPoorExit1(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture(t, "poor_url.json"))
	})
	var out, errb bytes.Buffer
	code := run([]string{"slow.example.com"}, &out, &errb, "test-key")
	if code != 1 {
		t.Fatalf("poor exit: want 1, got %d", code)
	}
	if !strings.Contains(out.String(), "FAIL") {
		t.Errorf("report should say FAIL: %s", out.String())
	}
}

func TestRunStrictNeedsImprovementExit1(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture(t, "origin_phone.json"))
	})
	var out, errb bytes.Buffer
	// Non-strict: NI does not fail.
	if code := run([]string{"--origin", "example.com"}, &out, &errb, "k"); code != 0 {
		t.Errorf("non-strict NI exit: want 0, got %d", code)
	}
	// Strict: NI fails.
	out.Reset()
	if code := run([]string{"--strict", "--origin", "example.com"}, &out, &errb, "k"); code != 1 {
		t.Errorf("strict NI exit: want 1, got %d", code)
	}
}

func TestRunInsufficientDataExit3(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"code":404,"message":"not found"}}`))
	})
	var out, errb bytes.Buffer
	code := run([]string{"unknown.example"}, &out, &errb, "k")
	if code != 3 {
		t.Errorf("404 exit: want 3, got %d", code)
	}
	if !strings.Contains(errb.String(), "not enough real-user data") {
		t.Errorf("insufficient-data message missing: %s", errb.String())
	}
}

func TestRunAuthErrorExit2(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":{"code":403,"message":"API key not valid"}}`))
	})
	var out, errb bytes.Buffer
	code := run([]string{"example.com"}, &out, &errb, "bad-key")
	if code != 2 {
		t.Errorf("auth error exit: want 2, got %d", code)
	}
	if strings.Contains(errb.String(), "bad-key") {
		t.Errorf("error output must not leak the API key: %s", errb.String())
	}
}

func TestRunJSONShape(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture(t, "good_url.json"))
	})
	var out, errb bytes.Buffer
	if code := run([]string{"--json", "example.com"}, &out, &errb, "k"); code != 0 {
		t.Fatalf("json exit: got %d", code)
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if _, ok := m["metrics"]; !ok {
		t.Errorf("json missing metrics: %s", out.String())
	}
}

func TestRunHistoryText(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Write(fixture(t, "history.json"))
	})
	var out, errb bytes.Buffer
	if code := run([]string{"--history", "6", "example.com"}, &out, &errb, "k"); code != 0 {
		t.Fatalf("history exit: got %d (stderr %s)", code, errb.String())
	}
	if !strings.Contains(out.String(), "Trend:") {
		t.Errorf("history output missing trend: %s", out.String())
	}
}
