// Command cruxpeek fetches real-user Core Web Vitals for a URL or origin from
// the Chrome UX Report (CrUX) API and grades them against the official p75
// thresholds. It groups the three Core Web Vitals (LCP, INP, CLS) prominently,
// shows FCP and TTFB as secondary diagnostics, and returns a CI-friendly exit
// code. It is a read-only client: it sends only the url/origin you name (and
// your API key) to Google's CrUX endpoint.
//
// API: https://developer.chrome.com/docs/crux/api
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/printemps-tokyo/cruxpeek/internal/crux"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// newClient builds the CrUX client. It is a package var so tests can inject a
// client pointed at an httptest server instead of the live API.
var newClient = crux.NewClient

const usage = `cruxpeek - real-user Core Web Vitals from the Chrome UX Report, graded

Usage:
  cruxpeek [flags] <url>

The URL may omit its scheme; https:// is assumed when none is given. By
default cruxpeek queries the exact URL; use --origin for the origin aggregate.

Flags:
  --origin            Query the origin-level aggregate, not the exact URL
  --device <d>        Form factor: phone | desktop | tablet | all (default all)
  --metrics <list>    Comma-separated metric keys to restrict the query
  --history [weeks]   Show a p75 trend with a sparkline (weeks 1-40, default 25)
  --json              Emit structured JSON instead of the text report
  --strict            Also fail (exit 1) when a Core Web Vital needs improvement
  --timeout <dur>     Per-request timeout (default 15s), e.g. 5s, 1500ms
  --version           Print version and exit
  --help              Show this help and exit

Authentication:
  Set CRUX_API_KEY to a Google Cloud API key with the "Chrome UX Report API"
  enabled. Get one free at https://developer.chrome.com/docs/crux/api (150
  queries/min, no billing required). The key is never printed.

Exit codes:
  0  no Core Web Vital (LCP, INP, CLS) is "poor" at p75
  1  a Core Web Vital is "poor" (or, with --strict, "needs improvement")
  2  invalid arguments, or a missing/invalid API key
  3  CrUX has not enough real-user data for the url/origin

Examples:
  cruxpeek example.com
  cruxpeek --origin --device phone example.com
  cruxpeek --history 12 https://example.com/
  cruxpeek --json example.com
`

// config is the parsed command line.
type config struct {
	target       string
	origin       bool
	device       string
	metrics      []string
	history      bool
	historyWeeks int
	jsonOut      bool
	strict       bool
	timeout      time.Duration
	showVersion  bool
	showHelp     bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv("CRUX_API_KEY")))
}

// run parses args, performs the query, writes output, and returns the exit
// code. It is separate from main so it can be exercised in tests with an
// injected API key and output buffers.
func run(args []string, stdout, stderr io.Writer, apiKey string) int {
	cfg, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "cruxpeek: %v\n\nRun 'cruxpeek --help' for usage.\n", err)
		return 2
	}
	if cfg.showHelp {
		fmt.Fprint(stdout, usage)
		return 0
	}
	if cfg.showVersion {
		fmt.Fprintf(stdout, "cruxpeek %s\n", version)
		return 0
	}
	if cfg.target == "" {
		fmt.Fprintf(stderr, "cruxpeek: missing <url>\n\nRun 'cruxpeek --help' for usage.\n")
		return 2
	}

	formFactor, err := crux.FormFactor(cfg.device)
	if err != nil {
		fmt.Fprintf(stderr, "cruxpeek: %v\n", err)
		return 2
	}

	req := crux.QueryRequest{FormFactor: formFactor, Metrics: cfg.metrics}
	if cfg.origin {
		origin, oerr := crux.OriginOf(cfg.target)
		if oerr != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", oerr)
			return 2
		}
		req.Origin = origin
	} else {
		u, uerr := crux.NormalizeURL(cfg.target)
		if uerr != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", uerr)
			return 2
		}
		req.URL = u
	}

	if strings.TrimSpace(apiKey) == "" {
		fmt.Fprint(stderr, missingKeyMessage)
		return 2
	}

	client := newClient(apiKey, cfg.timeout)
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout+5*time.Second)
	defer cancel()

	if cfg.history {
		return runHistory(ctx, client, req, cfg, stdout, stderr)
	}
	return runRecord(ctx, client, req, cfg, stdout, stderr)
}

func runRecord(ctx context.Context, client *crux.Client, req crux.QueryRequest, cfg config, stdout, stderr io.Writer) int {
	rec, err := client.QueryRecord(ctx, req)
	if err != nil {
		return handleQueryError(err, req, stderr)
	}
	report := crux.BuildReport(req, rec)
	if cfg.jsonOut {
		if err := writeJSON(stdout, report.ToJSON(cfg.strict)); err != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", err)
			return 2
		}
	} else {
		writeReport(stdout, report, cfg.strict)
	}
	return report.ExitCode(cfg.strict)
}

func runHistory(ctx context.Context, client *crux.Client, req crux.QueryRequest, cfg config, stdout, stderr io.Writer) int {
	rec, err := client.QueryHistoryRecord(ctx, req, cfg.historyWeeks)
	if err != nil {
		return handleQueryError(err, req, stderr)
	}
	report := crux.BuildHistoryReport(req, rec)
	if cfg.jsonOut {
		if err := writeJSON(stdout, report.ToJSON()); err != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", err)
			return 2
		}
	} else {
		writeHistoryReport(stdout, report)
	}
	return 0
}

// handleQueryError maps a client error to a message and exit code.
func handleQueryError(err error, req crux.QueryRequest, stderr io.Writer) int {
	if errors.Is(err, crux.ErrInsufficientData) {
		scope := "URL"
		if req.IsOrigin() {
			scope = "origin"
		}
		fmt.Fprintf(stderr, "cruxpeek: not enough real-user data in the Chrome UX Report for this %s (%s).\n", scope, req.Identifier())
		fmt.Fprintf(stderr, "  CrUX only reports pages/origins with a sufficient sample of real Chrome users.\n")
		if !req.IsOrigin() {
			fmt.Fprintf(stderr, "  Try --origin for the origin-level aggregate, which has broader coverage.\n")
		}
		return 3
	}
	var authErr *crux.AuthError
	if errors.As(err, &authErr) {
		fmt.Fprintf(stderr, "cruxpeek: %v\n", authErr)
		fmt.Fprintf(stderr, "  Check that CRUX_API_KEY is a valid key with the \"Chrome UX Report API\" enabled.\n")
		return 2
	}
	fmt.Fprintf(stderr, "cruxpeek: %v\n", err)
	return 2
}

const missingKeyMessage = `cruxpeek: CRUX_API_KEY is not set.

cruxpeek needs a Google Cloud API key with the "Chrome UX Report API" enabled.
To get one (free, 150 queries/min, no billing required):

  1. Open https://developer.chrome.com/docs/crux/api
  2. Create/select a Google Cloud project and create an API key
  3. Enable the "Chrome UX Report API" for that project
  4. Export it:  export CRUX_API_KEY=your_key_here

Then run cruxpeek again.
`

// parseArgs is a small hand-rolled parser: --history takes an optional numeric
// argument, which stdlib flag cannot express cleanly.
func parseArgs(args []string) (config, error) {
	cfg := config{
		device:  "all",
		timeout: 15 * time.Second,
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--help" || arg == "-h":
			cfg.showHelp = true
		case arg == "--version" || arg == "-V":
			cfg.showVersion = true
		case arg == "--json":
			cfg.jsonOut = true
		case arg == "--strict":
			cfg.strict = true
		case arg == "--origin":
			cfg.origin = true
		case arg == "--history":
			cfg.history = true
			// Optional numeric argument: only consumed if the next token parses
			// as an integer, so "cruxpeek --history example.com" still works.
			if i+1 < len(args) {
				if n, perr := strconv.Atoi(args[i+1]); perr == nil {
					if n < 1 || n > 40 {
						return cfg, fmt.Errorf("invalid --history weeks %d: must be between 1 and 40", n)
					}
					cfg.historyWeeks = n
					i++
				}
			}
		case strings.HasPrefix(arg, "--history="):
			cfg.history = true
			v := arg[len("--history="):]
			n, perr := strconv.Atoi(v)
			if perr != nil {
				return cfg, fmt.Errorf("invalid --history %q: want a number of weeks", v)
			}
			if n < 1 || n > 40 {
				return cfg, fmt.Errorf("invalid --history weeks %d: must be between 1 and 40", n)
			}
			cfg.historyWeeks = n
		case arg == "--device" || strings.HasPrefix(arg, "--device="):
			v, err := valueFor(arg, &i, args, "--device")
			if err != nil {
				return cfg, err
			}
			cfg.device = v
		case arg == "--metrics" || strings.HasPrefix(arg, "--metrics="):
			v, err := valueFor(arg, &i, args, "--metrics")
			if err != nil {
				return cfg, err
			}
			cfg.metrics = splitMetrics(v)
		case arg == "--timeout" || strings.HasPrefix(arg, "--timeout="):
			v, err := valueFor(arg, &i, args, "--timeout")
			if err != nil {
				return cfg, err
			}
			d, perr := time.ParseDuration(v)
			if perr != nil {
				return cfg, fmt.Errorf("invalid --timeout %q: %w", v, perr)
			}
			if d <= 0 {
				return cfg, fmt.Errorf("invalid --timeout %q: must be positive", v)
			}
			cfg.timeout = d
		case strings.HasPrefix(arg, "-") && arg != "-":
			return cfg, fmt.Errorf("unknown flag %q", arg)
		default:
			if cfg.target != "" {
				return cfg, fmt.Errorf("unexpected extra argument %q", arg)
			}
			cfg.target = arg
		}
	}
	return cfg, nil
}

// valueFor returns the value for a flag given either "--flag=value" or the
// following argument.
func valueFor(arg string, i *int, args []string, name string) (string, error) {
	if eq := strings.IndexByte(arg, '='); eq >= 0 {
		return arg[eq+1:], nil
	}
	if *i+1 >= len(args) {
		return "", fmt.Errorf("flag %s needs a value", name)
	}
	*i++
	return args[*i], nil
}

// splitMetrics splits and trims a comma-separated metric list.
func splitMetrics(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// assessmentLabel returns a fixed-width, human-friendly assessment label.
func assessmentLabel(a crux.Assessment) string {
	switch a {
	case crux.Good:
		return "GOOD"
	case crux.NeedsImprovement:
		return "NEEDS IMPROVEMENT"
	case crux.Poor:
		return "POOR"
	default:
		return "NO DATA"
	}
}

// writeReport renders the human-readable graded report.
func writeReport(w io.Writer, r *crux.Report, strict bool) {
	scope := "URL"
	if r.IsOrigin {
		scope = "Origin"
	}
	fmt.Fprintf(w, "%s: %s\n", scope, r.Identifier)
	fmt.Fprintf(w, "Form factor: %s\n", crux.FormFactorLabel(r.FormFactor))
	if r.CollectionPeriod != "" {
		fmt.Fprintf(w, "Collection period: %s\n", r.CollectionPeriod)
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Core Web Vitals")
	for _, m := range r.Metrics {
		if m.Def.Core {
			writeMetric(w, m)
		}
	}
	fmt.Fprintln(w)

	fmt.Fprintln(w, "Other metrics")
	for _, m := range r.Metrics {
		if !m.Def.Core {
			writeMetric(w, m)
		}
	}
	fmt.Fprintln(w)

	if r.ExitCode(strict) == 0 {
		fmt.Fprintln(w, "Assessment: PASS (no Core Web Vital is poor at p75)")
	} else {
		what := "poor"
		if strict {
			what = "poor or needs-improvement"
		}
		fmt.Fprintf(w, "Assessment: FAIL (a Core Web Vital is %s at p75)\n", what)
	}
}

// writeMetric renders one metric line plus its distribution bar.
func writeMetric(w io.Writer, m crux.MetricReport) {
	if !m.Present {
		fmt.Fprintf(w, "  %-5s %-26s  no data\n", m.Def.Label, m.Def.Name)
		return
	}
	fmt.Fprintf(w, "  %-5s %-26s  p75 %-10s  %s\n",
		m.Def.Label, m.Def.Name, m.Def.FormatValue(m.P75), assessmentLabel(m.Assessment))
	fmt.Fprintf(w, "        good %s   good %.0f%% / ni %.0f%% / poor %.0f%%\n",
		distBar(m.DistGood, m.DistNI, m.DistPoor), m.DistGood, m.DistNI, m.DistPoor)
}

// distBar renders a 20-cell stacked bar of the good/ni/poor split.
func distBar(good, ni, poor float64) string {
	const width = 20
	g := int(good/100*width + 0.5)
	n := int(ni/100*width + 0.5)
	p := width - g - n
	if p < 0 {
		p = 0
	}
	return strings.Repeat("#", g) + strings.Repeat("=", n) + strings.Repeat(".", p)
}

// writeHistoryReport renders the p75 trend with a sparkline per metric.
func writeHistoryReport(w io.Writer, r *crux.HistoryReport) {
	scope := "URL"
	if r.IsOrigin {
		scope = "Origin"
	}
	fmt.Fprintf(w, "%s: %s\n", scope, r.Identifier)
	fmt.Fprintf(w, "Form factor: %s\n", crux.FormFactorLabel(r.FormFactor))
	if len(r.Periods) > 0 {
		fmt.Fprintf(w, "Trend: %s to %s (%d weeks)\n", r.Periods[0], r.Periods[len(r.Periods)-1], len(r.Periods))
	}
	fmt.Fprintln(w)

	for _, m := range r.Metrics {
		if !m.Present {
			fmt.Fprintf(w, "  %-5s %-26s  no data\n", m.Def.Label, m.Def.Name)
			continue
		}
		fmt.Fprintf(w, "  %-5s %-26s  %s   %s -> %s\n",
			m.Def.Label, m.Def.Name, m.Sparkline,
			m.Def.FormatValue(m.First.Value), m.Def.FormatValue(m.Last.Value))
	}
}
