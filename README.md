# cruxpeek

Real-user Core Web Vitals for any URL or origin, graded against the official
p75 thresholds, straight from your terminal.

cruxpeek queries the [Chrome UX Report (CrUX) API](https://developer.chrome.com/docs/crux/api)
and prints a graded report: for each metric it shows the 75th-percentile (p75)
value observed from real Chrome users, its assessment (good / needs improvement
/ poor), and the good/NI/poor distribution from the metric histogram. The three
Core Web Vitals (LCP, INP, CLS) are grouped prominently; First Contentful Paint
and Time to First Byte are shown as secondary diagnostics. A CI-friendly exit
code lets you fail a build when a Core Web Vital is poor.

It is a read-only client written in Go with no third-party dependencies (the
standard library only). It sends only the url/origin you name, plus your API
key, to Google's CrUX endpoint. The key is never printed.

## What it measures

cruxpeek reports the official CrUX field data. Core Web Vitals are assessed at
the 75th percentile using the Google / web.dev thresholds. A value is **good**
when its p75 is at or below the good boundary, **poor** when it is above the
poor boundary, and **needs improvement** in between.

| Metric | Key (CrUX API) | Good (p75 <=) | Poor (p75 >) | Core |
| ------ | -------------- | ------------- | ------------ | ---- |
| LCP  - Largest Contentful Paint | `largest_contentful_paint`        | 2500 ms | 4000 ms | yes |
| INP  - Interaction to Next Paint | `interaction_to_next_paint`      | 200 ms  | 500 ms  | yes |
| CLS  - Cumulative Layout Shift  | `cumulative_layout_shift`         | 0.10    | 0.25    | yes |
| FCP  - First Contentful Paint   | `first_contentful_paint`          | 1800 ms | 3000 ms | no  |
| TTFB - Time to First Byte       | `experimental_time_to_first_byte` | 800 ms  | 1800 ms | no  |

Thresholds source: [web.dev - Core Web Vitals](https://web.dev/articles/vitals),
[FCP](https://web.dev/articles/fcp), [TTFB](https://web.dev/articles/ttfb).

INP is the current responsiveness Core Web Vital; it replaced First Input Delay
(FID) in March 2024, so cruxpeek does not treat FID as a core metric. cruxpeek
reports only these official CrUX metrics: you may see unofficial composite
"performance scores" or invented indices circulating in SEO blogs, but cruxpeek
does not compute or report any of those.

## Install

Download a release binary from the
[releases page](https://github.com/printemps-tokyo/cruxpeek/releases), or build
from source (Go 1.23+):

```bash
go install github.com/printemps-tokyo/cruxpeek@latest
```

Or clone and build:

```bash
git clone https://github.com/printemps-tokyo/cruxpeek
cd cruxpeek
go build -o cruxpeek .
```

## Get a free CrUX API key

The CrUX API is free (150 queries/min per Google Cloud project, no billing
required).

1. Open the [CrUX API docs](https://developer.chrome.com/docs/crux/api).
2. In the [Google Cloud Console](https://console.cloud.google.com/), create or
   select a project and create an API key (APIs & Services -> Credentials).
3. Enable the **Chrome UX Report API** for that project.
4. Export the key so cruxpeek can read it:

   ```bash
   export CRUX_API_KEY="your_key_here"
   ```

cruxpeek reads the key only from `CRUX_API_KEY` and never prints it.

## Quickstart

Query the exact URL:

```bash
cruxpeek example.com
```

```text
URL: https://example.com
Form factor: All form factors
Collection period: 2024-05-26 to 2024-06-22

Core Web Vitals
  LCP   Largest Contentful Paint    p75 2100 ms     GOOD
        good ##################==   good 90% / ni 8% / poor 2%
  INP   Interaction to Next Paint   p75 150 ms      GOOD
        good ###################=   good 95% / ni 4% / poor 1%
  CLS   Cumulative Layout Shift     p75 0.05        GOOD
        good ###################=   good 96% / ni 3% / poor 1%

Other metrics
  FCP   First Contentful Paint      p75 1500 ms     GOOD
        good ##################==   good 88% / ni 9% / poor 3%
  TTFB  Time to First Byte          p75 600 ms      GOOD
        good ################===.   good 82% / ni 14% / poor 4%

Assessment: PASS (no Core Web Vital is poor at p75)
```

In the distribution bar, `#` is the good portion, `=` needs improvement, and
`.` poor.

Query the origin-level aggregate (all pages on the origin), for phone users:

```bash
cruxpeek --origin --device phone example.com
```

The origin aggregate has much broader coverage than any single URL, so it is a
good fallback when an exact URL has too little data.

## History and sparklines

`--history` fetches the p75 timeseries from the CrUX History API (weekly
collection periods, up to the last 40 weeks) and renders a self-drawn sparkline
of p75 over time for each metric, oldest week on the left:

```bash
cruxpeek --history 6 example.com
```

```text
URL: https://example.com
Form factor: All form factors
Trend: 2024-05-11 to 2024-06-22 (6 weeks)

  LCP   Largest Contentful Paint    ▆▅█▄▃▁   2600 ms -> 2100 ms
  INP   Interaction to Next Paint   █▆ ▃▂▁   220 ms -> 170 ms
  CLS   Cumulative Layout Shift     █▇▆▄▃▁   0.12 -> 0.05
```

A blank in the sparkline is a week for which CrUX had no data. Pass a number
(1-40) to choose how many weeks to request; the API default is 25.

## Several URLs at once

Give several URLs (or `--from-file urls.txt`, one per line, `#` comments
allowed) and cruxpeek queries them one after another and prints one table:

```text
Comparison (url, All form factors)

  #  Target                LCP           INP          CLS        FCP           TTFB          Result
  1  https://fast.example  2100 ms good  150 ms good  0.05 good  1500 ms good  600 ms good   PASS
  2  https://slow.example  5000 ms POOR  600 ms POOR  0.30 POOR  3200 ms POOR  2000 ms POOR  FAIL
  3  https://none.example  -             -            -          -             -             NO DATA

Limits exceeded
  https://slow.example  LCP p75 5000 ms (poor: > 4000 ms)
  https://slow.example  INP p75 600 ms (poor: > 500 ms)
  https://slow.example  CLS p75 0.30 (poor: > 0.25)
```

(Values from the test fixtures.) `--json` prints an array with one report per
URL; a URL without data or with a failed request appears as
`{"identifier", "error", "exit_code"}`. Queries run sequentially, which keeps
a long list well inside the API's per-minute quota.

## Budgets

`--budget lcp=2000,inp=150,cls=0.08,ttfb=600` sets your own p75 ceilings (ms;
CLS unitless), for any of the five metrics. A metric whose p75 is over its
budget fails the run (exit 1) even when the official assessment is good. A Core
Web Vital that is already poor (or, with `--strict`, needs improvement) is
reported once, under the official rule. The single-URL report adds a
"Budgets" section with each budget's verdict.

## Notification JSON

`--notify-json` prints only what crossed a limit, in a small document meant for
cron jobs and chat webhooks:

```json
{
  "status": "breach",
  "checked": 3,
  "breaches": [
    {
      "identifier": "https://slow.example",
      "metric": "LCP",
      "key": "largest_contentful_paint",
      "p75": 5000,
      "unit": "ms",
      "assessment": "poor",
      "rule": "poor",
      "limit": 4000
    }
  ],
  "no_data": ["https://none.example"],
  "errors": [],
  "text": "cruxpeek: 3 limit(s) exceeded across 3 targets. https://slow.example: LCP p75 5000 ms (poor: > 4000 ms), ... (no CrUX data for https://none.example)"
}
```

`status` is `ok`, `breach`, or `error`; `rule` is `poor`, `needs-improvement`
(under `--strict`) or `budget`, and `limit` is the threshold or budget that was
exceeded. `text` is a one-line summary in the field a Slack incoming webhook
reads, so the document can be posted to one as it is. cruxpeek itself never
sends anything anywhere but the CrUX API.

## Exit-code contract

cruxpeek is designed for CI. Of the official assessments, only the three Core
Web Vitals (LCP, INP, CLS) influence the exit code; FCP and TTFB fail a build
only through a `--budget` you set. With several URLs, the code is the first
that applies of 2, 1, 3, 0.

| Exit | Meaning |
| ---- | ------- |
| 0 | No Core Web Vital is poor at p75 (and, with `--strict`, none needs improvement), and no metric is over its `--budget`. |
| 1 | A Core Web Vital is poor at p75, or with `--strict` also when one needs improvement, or a metric is over its `--budget`. |
| 2 | Invalid arguments, a missing/invalid API key, or (with several URLs) a failed request. |
| 3 | CrUX has not enough real-user data for the url/origin (the API returned 404). |

Example CI gate:

```bash
cruxpeek --strict https://example.com/ || echo "Core Web Vitals regressed"
```

## Options

```text
cruxpeek [flags] <url> [<url>...]

  --origin            Query the origin-level aggregate, not the exact URL
  --device <d>        Form factor: phone | desktop | tablet | all (default all)
  --metrics <list>    Comma-separated CrUX metric keys to restrict the query
  --history [weeks]   Show a p75 trend with a sparkline (weeks 1-40, default 25)
  --json              Emit structured JSON instead of the text report (an
                      array for several URLs)
  --strict            Also fail (exit 1) when a Core Web Vital needs improvement
  --budget <list>     Your own p75 limits, e.g. lcp=2000,inp=150,cls=0.08,ttfb=600
  --from-file <file>  Read URLs from a file, one per line (# comments allowed)
  --notify-json       Emit only what exceeded a limit, with a one-line "text"
  --timeout <dur>     Per-request timeout (default 15s), e.g. 5s, 1500ms
  --version           Print version and exit
  --help              Show this help and exit
```

The URL may omit its scheme; `https://` is assumed when none is given. With
`--origin`, cruxpeek reduces the input to its `scheme://host` origin. The
default `--device all` requests the combined aggregate across all form factors.

## JSON output

`--json` emits a stable, machine-readable report. Metrics are always listed in
LCP, INP, CLS, FCP, TTFB order; `p75` is `null` and `assessment` is `"unknown"`
for a metric CrUX did not return. Values are numbers (milliseconds, or the
unitless CLS); `distribution` values are percentages.

```json
{
  "query": {
    "identifier": "https://example.com",
    "scope": "url",
    "form_factor": "All form factors",
    "collection_period": "2024-05-26 to 2024-06-22"
  },
  "metrics": [
    {
      "key": "largest_contentful_paint",
      "label": "LCP",
      "name": "Largest Contentful Paint",
      "unit": "ms",
      "core": true,
      "present": true,
      "p75": 2100,
      "assessment": "good",
      "thresholds": { "good": 2500, "poor": 4000 },
      "distribution": { "good": 90.12, "needs_improvement": 7.88, "poor": 2 }
    }
  ],
  "exit_code": 0
}
```

With `--history --json`, the output instead carries `periods` (collection-period
end dates, oldest first) and, per metric, its `sparkline` string and a `p75s`
array aligned with `periods` (with `null` for weeks that had no data).

## Accuracy note

cruxpeek reports the official CrUX field metrics only: LCP, INP, CLS, FCP, and
TTFB, assessed at p75 against the published web.dev thresholds. INP is the
current responsiveness Core Web Vital that replaced FID. cruxpeek does not
compute unofficial composite scores. The request and response shapes are
implemented against the official docs:
[CrUX API](https://developer.chrome.com/docs/crux/api) and
[CrUX History API](https://developer.chrome.com/docs/crux/history-api).
CrUX data is a 28-day rolling aggregate of real Chrome users and is available
only for pages/origins that meet a minimum sample threshold; when a target has
too little data the API returns 404 and cruxpeek exits 3.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). The test suite is fully offline (CrUX
responses are captured as JSON fixtures and the client is exercised with
`httptest`), so no API key is needed to develop or run the tests.

## License

MIT. See [LICENSE](LICENSE).
