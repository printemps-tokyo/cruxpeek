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
	"text/tabwriter"
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
  cruxpeek [flags] <url> [<url>...]

The URL may omit its scheme; https:// is assumed when none is given. By
default cruxpeek queries the exact URL; use --origin for the origin aggregate.
With several URLs (or --from-file) it prints one comparison table.

Flags:
  --origin            Query the origin-level aggregate, not the exact URL
  --device <d>        Form factor: phone | desktop | tablet | all (default all)
  --metrics <list>    Comma-separated metric keys to restrict the query
  --history [weeks]   Show a p75 trend with a sparkline (weeks 1-40, default 25)
  --json              Emit structured JSON instead of the text report (an
                      array for several URLs)
  --strict            Also fail (exit 1) when a Core Web Vital needs improvement
  --budget <list>     Your own p75 limits, e.g. lcp=2000,inp=150,cls=0.08,ttfb=600
                      (ms; CLS unitless); a metric over its budget fails (exit 1)
  --from-file <file>  Read URLs from a file, one per line (# comments allowed)
  --notify-json       Emit only what exceeded a limit, as JSON with a one-line
                      "text" summary (ready for a chat webhook)
  --timeout <dur>     Per-request timeout (default 15s), e.g. 5s, 1500ms
  --version           Print version and exit
  --help              Show this help and exit

Authentication:
  Set CRUX_API_KEY to a Google Cloud API key with the "Chrome UX Report API"
  enabled. Get one free at https://developer.chrome.com/docs/crux/api (150
  queries/min, no billing required). The key is never printed.

Exit codes (with several URLs, the first that applies: 2, 1, 3, 0):
  0  no Core Web Vital (LCP, INP, CLS) is "poor" at p75, and no budget is exceeded
  1  a Core Web Vital is "poor" (or, with --strict, "needs improvement"), or a
     metric exceeds its --budget
  2  invalid arguments, a missing/invalid API key, or a failed request
  3  CrUX has not enough real-user data for the url/origin

Examples:
  cruxpeek example.com
  cruxpeek --origin --device phone example.com
  cruxpeek --history 12 https://example.com/
  cruxpeek --json example.com
  cruxpeek --origin example.com example.org example.net
  cruxpeek --from-file urls.txt --budget lcp=2000 --notify-json
`

// config is the parsed command line.
type config struct {
	targets      []string
	fromFile     string
	budgets      crux.Budgets
	notifyJSON   bool
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
	if cfg.fromFile != "" {
		lines, ferr := readTargets(cfg.fromFile)
		if ferr != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", ferr)
			return 2
		}
		cfg.targets = append(cfg.targets, lines...)
	}
	if len(cfg.targets) == 0 {
		fmt.Fprintf(stderr, "cruxpeek: missing <url>\n\nRun 'cruxpeek --help' for usage.\n")
		return 2
	}
	if cfg.history && (len(cfg.targets) > 1 || cfg.notifyJSON) {
		fmt.Fprintf(stderr, "cruxpeek: --history takes a single URL and no --notify-json\n")
		return 2
	}

	formFactor, err := crux.FormFactor(cfg.device)
	if err != nil {
		fmt.Fprintf(stderr, "cruxpeek: %v\n", err)
		return 2
	}

	reqs := make([]crux.QueryRequest, 0, len(cfg.targets))
	for _, target := range cfg.targets {
		req, rerr := buildRequest(target, cfg, formFactor)
		if rerr != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", rerr)
			return 2
		}
		reqs = append(reqs, req)
	}

	if strings.TrimSpace(apiKey) == "" {
		fmt.Fprint(stderr, missingKeyMessage)
		return 2
	}

	client := newClient(apiKey, cfg.timeout)

	if len(reqs) > 1 || cfg.notifyJSON {
		return runMany(client, reqs, cfg, stdout, stderr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout+5*time.Second)
	defer cancel()
	if cfg.history {
		return runHistory(ctx, client, reqs[0], cfg, stdout, stderr)
	}
	return runRecord(ctx, client, reqs[0], cfg, stdout, stderr)
}

// buildRequest turns one command-line target into a CrUX query.
func buildRequest(target string, cfg config, formFactor string) (crux.QueryRequest, error) {
	req := crux.QueryRequest{FormFactor: formFactor, Metrics: cfg.metrics}
	if cfg.origin {
		origin, err := crux.OriginOf(target)
		if err != nil {
			return req, err
		}
		req.Origin = origin
		return req, nil
	}
	u, err := crux.NormalizeURL(target)
	if err != nil {
		return req, err
	}
	req.URL = u
	return req, nil
}

// readTargets reads one url per line, skipping blanks and # comments.
func readTargets(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s lists no URLs", path)
	}
	return out, nil
}

// outcome is one target's result in a multi-target run.
type outcome struct {
	req      crux.QueryRequest
	report   *crux.Report
	breaches []crux.Breach
	noData   bool
	err      error
}

// runMany queries every target in order (sequentially, to stay well inside
// the API's per-minute quota) and prints a comparison, a JSON array, or the
// notification document.
func runMany(client *crux.Client, reqs []crux.QueryRequest, cfg config, stdout, stderr io.Writer) int {
	outcomes := make([]outcome, 0, len(reqs))
	for _, req := range reqs {
		ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout+5*time.Second)
		rec, err := client.QueryRecord(ctx, req)
		cancel()
		o := outcome{req: req}
		switch {
		case errors.Is(err, crux.ErrInsufficientData):
			o.noData = true
		case err != nil:
			o.err = err
		default:
			o.report = crux.BuildReport(req, rec)
			o.breaches = o.report.Breaches(cfg.strict, cfg.budgets)
		}
		outcomes = append(outcomes, o)
		var authErr *crux.AuthError
		if errors.As(err, &authErr) {
			// The same key serves every query; stop instead of repeating it.
			fmt.Fprintf(stderr, "cruxpeek: %v\n", authErr)
			fmt.Fprintf(stderr, "  Check that CRUX_API_KEY is a valid key with the \"Chrome UX Report API\" enabled.\n")
			return 2
		}
	}

	code := 0
	var breaches []crux.Breach
	var noData, errs []string
	for _, o := range outcomes {
		switch {
		case o.err != nil:
			errs = append(errs, fmt.Sprintf("%s: %v", o.req.Identifier(), o.err))
		case o.noData:
			noData = append(noData, o.req.Identifier())
		default:
			breaches = append(breaches, o.breaches...)
		}
	}
	switch {
	case len(errs) > 0:
		code = 2
	case len(breaches) > 0:
		code = 1
	case len(noData) > 0:
		code = 3
	}

	switch {
	case cfg.notifyJSON:
		if err := writeJSON(stdout, crux.BuildAlert(len(outcomes), breaches, noData, errs)); err != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", err)
			return 2
		}
	case cfg.jsonOut:
		items := make([]any, 0, len(outcomes))
		for _, o := range outcomes {
			switch {
			case o.report != nil:
				j := o.report.ToJSON(cfg.strict)
				j.ExitCode = 0
				if len(o.breaches) > 0 {
					j.ExitCode = 1
				}
				items = append(items, j)
			case o.noData:
				items = append(items, map[string]any{"identifier": o.req.Identifier(), "error": "not enough real-user data", "exit_code": 3})
			default:
				items = append(items, map[string]any{"identifier": o.req.Identifier(), "error": o.err.Error(), "exit_code": 2})
			}
		}
		if err := writeJSON(stdout, items); err != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", err)
			return 2
		}
	default:
		writeComparison(stdout, outcomes, cfg)
	}
	return code
}

// writeComparison renders one row per target and the limits each exceeded.
func writeComparison(w io.Writer, outcomes []outcome, cfg config) {
	scope := "url"
	if cfg.origin {
		scope = "origin"
	}
	ff := ""
	if len(outcomes) > 0 {
		ff = crux.FormFactorLabel(outcomes[0].req.FormFactor)
	}
	fmt.Fprintf(w, "Comparison (%s, %s)\n\n", scope, ff)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "  #\tTarget"
	for _, d := range crux.MetricDefs {
		header += "\t" + d.Label
	}
	fmt.Fprintln(tw, header+"\tResult")
	for i, o := range outcomes {
		row := fmt.Sprintf("  %d\t%s", i+1, o.req.Identifier())
		switch {
		case o.report != nil:
			for _, m := range o.report.Metrics {
				row += "\t" + cell(m)
			}
			if len(o.breaches) > 0 {
				row += "\tFAIL"
			} else {
				row += "\tPASS"
			}
		case o.noData:
			row += strings.Repeat("\t-", len(crux.MetricDefs)) + "\tNO DATA"
		default:
			row += strings.Repeat("\t-", len(crux.MetricDefs)) + "\tERROR"
		}
		fmt.Fprintln(tw, row)
	}
	tw.Flush()

	var lines []string
	for _, o := range outcomes {
		for _, b := range o.breaches {
			lines = append(lines, fmt.Sprintf("  %s  %s", o.req.Identifier(), b.Describe()))
		}
		if o.err != nil {
			lines = append(lines, fmt.Sprintf("  %s  error: %v", o.req.Identifier(), o.err))
		}
	}
	fmt.Fprintln(w)
	if len(lines) == 0 {
		fmt.Fprintln(w, "Limits: none exceeded")
		return
	}
	fmt.Fprintln(w, "Limits exceeded")
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
}

// cell is a compact metric value plus its rating, e.g. "2100 ms good".
func cell(m crux.MetricReport) string {
	if !m.Present {
		return "-"
	}
	rating := map[crux.Assessment]string{crux.Good: "good", crux.NeedsImprovement: "ni", crux.Poor: "POOR"}[m.Assessment]
	return m.Def.FormatValue(m.P75) + " " + rating
}

func runRecord(ctx context.Context, client *crux.Client, req crux.QueryRequest, cfg config, stdout, stderr io.Writer) int {
	rec, err := client.QueryRecord(ctx, req)
	if err != nil {
		return handleQueryError(err, req, stderr)
	}
	report := crux.BuildReport(req, rec)
	breaches := report.Breaches(cfg.strict, cfg.budgets)
	code := 0
	if len(breaches) > 0 {
		code = 1
	}
	if cfg.jsonOut {
		j := report.ToJSON(cfg.strict)
		j.ExitCode = code
		if err := writeJSON(stdout, j); err != nil {
			fmt.Fprintf(stderr, "cruxpeek: %v\n", err)
			return 2
		}
	} else {
		writeReport(stdout, report, cfg.strict)
		writeBudgets(stdout, report, breaches, cfg.budgets)
	}
	return code
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
		case arg == "--notify-json":
			cfg.notifyJSON = true
		case arg == "--budget" || strings.HasPrefix(arg, "--budget="):
			v, err := valueFor(arg, &i, args, "--budget")
			if err != nil {
				return cfg, err
			}
			b, berr := crux.ParseBudgets(v)
			if berr != nil {
				return cfg, berr
			}
			cfg.budgets = b
		case arg == "--from-file" || strings.HasPrefix(arg, "--from-file="):
			v, err := valueFor(arg, &i, args, "--from-file")
			if err != nil {
				return cfg, err
			}
			cfg.fromFile = v
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
			cfg.targets = append(cfg.targets, arg)
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

// writeBudgets adds the --budget verdicts under the single-target report.
func writeBudgets(w io.Writer, r *crux.Report, breaches []crux.Breach, budgets crux.Budgets) {
	if len(budgets) == 0 {
		return
	}
	over := map[string]bool{}
	for _, b := range breaches {
		if b.Rule == "budget" {
			over[b.Key] = true
		}
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Budgets")
	for _, m := range r.Metrics {
		limit, ok := budgets[m.Def.Key]
		if !ok {
			continue
		}
		switch {
		case !m.Present:
			fmt.Fprintf(w, "  %-5s budget %-10s  no data\n", m.Def.Label, m.Def.FormatValue(limit))
		case over[m.Def.Key]:
			fmt.Fprintf(w, "  %-5s budget %-10s  OVER (p75 %s)\n", m.Def.Label, m.Def.FormatValue(limit), m.Def.FormatValue(m.P75))
		case m.P75 > limit:
			fmt.Fprintf(w, "  %-5s budget %-10s  over, already counted above (p75 %s)\n", m.Def.Label, m.Def.FormatValue(limit), m.Def.FormatValue(m.P75))
		default:
			fmt.Fprintf(w, "  %-5s budget %-10s  within (p75 %s)\n", m.Def.Label, m.Def.FormatValue(limit), m.Def.FormatValue(m.P75))
		}
	}
	if len(over) > 0 {
		fmt.Fprintln(w, "Assessment: FAIL (over budget)")
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
