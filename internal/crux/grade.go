package crux

import (
	"fmt"
	"strings"
)

// Assessment is the p75 quality rating for a metric.
type Assessment string

const (
	// Good means the p75 is at or below the metric's "good" threshold.
	Good Assessment = "good"
	// NeedsImprovement means the p75 is between the good and poor thresholds.
	NeedsImprovement Assessment = "needs-improvement"
	// Poor means the p75 is above the metric's "poor" threshold.
	Poor Assessment = "poor"
	// Unknown means CrUX returned no p75 for the metric.
	Unknown Assessment = "unknown"
)

// MetricDef describes a CrUX metric and its official p75 assessment thresholds.
//
// Thresholds are the Google/web.dev Core Web Vitals boundaries assessed at the
// 75th percentile (p75). A value is "good" when p75 <= Good, "poor" when
// p75 > Poor, and "needs improvement" in between. Source:
//
//	https://web.dev/articles/vitals   (Core Web Vitals: LCP, INP, CLS)
//	https://web.dev/articles/fcp      https://web.dev/articles/ttfb
type MetricDef struct {
	Key   string  // exact CrUX API metric key
	Label string  // short label, e.g. "LCP"
	Name  string  // full name, e.g. "Largest Contentful Paint"
	Unit  string  // "ms", or "" for the unitless CLS
	Good  float64 // p75 <= Good is good
	Poor  float64 // p75 > Poor is poor
	Core  bool    // true for the three Core Web Vitals (LCP, INP, CLS)
}

// MetricDefs is the metric table in display order: the three Core Web Vitals
// first (LCP, INP, CLS), then the secondary diagnostics (FCP, TTFB).
//
// INP (interaction_to_next_paint) is the current responsiveness Core Web Vital;
// it replaced First Input Delay (FID) in March 2024, so cruxpeek does not query
// first_input_delay as a core metric.
var MetricDefs = []MetricDef{
	{Key: KeyLCP, Label: "LCP", Name: "Largest Contentful Paint", Unit: "ms", Good: 2500, Poor: 4000, Core: true},
	{Key: KeyINP, Label: "INP", Name: "Interaction to Next Paint", Unit: "ms", Good: 200, Poor: 500, Core: true},
	{Key: KeyCLS, Label: "CLS", Name: "Cumulative Layout Shift", Unit: "", Good: 0.10, Poor: 0.25, Core: true},
	{Key: KeyFCP, Label: "FCP", Name: "First Contentful Paint", Unit: "ms", Good: 1800, Poor: 3000, Core: false},
	{Key: KeyTTFB, Label: "TTFB", Name: "Time to First Byte", Unit: "ms", Good: 800, Poor: 1800, Core: false},
}

// Grade classifies a p75 value against a metric's good/poor thresholds.
//
// The boundaries are inclusive on the "good" side and exclusive on the "poor"
// side: exactly Good is good, exactly Poor is still needs-improvement, and only
// strictly greater than Poor is poor. For example an LCP of exactly 2500ms is
// good, 4000ms is needs-improvement, and 4001ms is poor.
func Grade(p75, good, poor float64) Assessment {
	switch {
	case p75 <= good:
		return Good
	case p75 > poor:
		return Poor
	default:
		return NeedsImprovement
	}
}

// MetricReport is the graded view of one metric.
type MetricReport struct {
	Def        MetricDef
	Present    bool       // CrUX returned a p75 for this metric
	P75        float64    // the p75 value (ms, or unitless for CLS)
	Assessment Assessment // good / needs-improvement / poor / unknown
	DistGood   float64    // percent of visits in the good bin (0..100)
	DistNI     float64    // percent in the needs-improvement bin
	DistPoor   float64    // percent in the poor bin
}

// Report is the graded, display-ready result of a single CrUX query.
type Report struct {
	Identifier       string // the url or origin queried
	IsOrigin         bool
	FormFactor       string // CrUX enum value ("" = combined)
	CollectionPeriod string // "YYYY-MM-DD to YYYY-MM-DD", if present
	Metrics          []MetricReport
}

// BuildReport grades a CrUX record into a Report, walking MetricDefs so the
// output order and set of metrics are stable regardless of map iteration order.
func BuildReport(req QueryRequest, rec *Record) *Report {
	rep := &Report{
		Identifier: req.Identifier(),
		IsOrigin:   req.IsOrigin(),
		FormFactor: rec.Key.FormFactor,
	}
	if rec.CollectionPeriod != nil {
		rep.CollectionPeriod = rec.CollectionPeriod.FirstDate.String() + " to " + rec.CollectionPeriod.LastDate.String()
	}
	for _, def := range MetricDefs {
		mr := MetricReport{Def: def, Assessment: Unknown}
		md, ok := rec.Metrics[def.Key]
		if ok && md.Percentiles.P75.Valid {
			mr.Present = true
			mr.P75 = md.Percentiles.P75.Value
			mr.Assessment = Grade(mr.P75, def.Good, def.Poor)
			if len(md.Histogram) == 3 {
				mr.DistGood = md.Histogram[0].Density * 100
				mr.DistNI = md.Histogram[1].Density * 100
				mr.DistPoor = md.Histogram[2].Density * 100
			}
		}
		rep.Metrics = append(rep.Metrics, mr)
	}
	return rep
}

// ExitCode returns the process exit code implied by the graded core metrics:
//
//	0  no Core Web Vital is poor (and, under strict, none is needs-improvement)
//	1  a Core Web Vital is poor, or (strict) needs improvement
//
// Only the three Core Web Vitals (LCP, INP, CLS) influence the code; FCP and
// TTFB are reported but never fail the build. Metrics CrUX did not return are
// skipped. Auth/argument (2) and insufficient-data (3) codes are decided by the
// caller, not here.
func (r *Report) ExitCode(strict bool) int {
	for _, m := range r.Metrics {
		if !m.Def.Core || !m.Present {
			continue
		}
		if m.Assessment == Poor {
			return 1
		}
		if strict && m.Assessment == NeedsImprovement {
			return 1
		}
	}
	return 0
}

// FormatValue renders a p75 value with its unit: integer milliseconds for the
// timing metrics, or two-decimal unitless for CLS.
func (d MetricDef) FormatValue(v float64) string {
	if d.Unit == "ms" {
		return fmt.Sprintf("%.0f ms", v)
	}
	return fmt.Sprintf("%.2f", v)
}

// sparkBars are the eight block glyphs used, lowest to highest.
var sparkBars = []rune("▁▂▃▄▅▆▇█")

// Sparkline renders a p75 series as a Unicode block-glyph sparkline. Values are
// scaled between the series min and max (so relative movement is visible even
// for small ranges); invalid entries (weeks CrUX had no data) render as a gap.
// A flat or single-point series renders at a mid-height bar.
func Sparkline(series []P75Value) string {
	if len(series) == 0 {
		return ""
	}
	min, max := 0.0, 0.0
	have := false
	for _, v := range series {
		if !v.Valid {
			continue
		}
		if !have {
			min, max, have = v.Value, v.Value, true
			continue
		}
		if v.Value < min {
			min = v.Value
		}
		if v.Value > max {
			max = v.Value
		}
	}
	if !have {
		return strings.Repeat(" ", len(series))
	}
	var b strings.Builder
	span := max - min
	for _, v := range series {
		if !v.Valid {
			b.WriteRune(' ')
			continue
		}
		var idx int
		if span == 0 {
			idx = len(sparkBars) / 2
		} else {
			idx = int((v.Value - min) / span * float64(len(sparkBars)-1))
			if idx < 0 {
				idx = 0
			}
			if idx >= len(sparkBars) {
				idx = len(sparkBars) - 1
			}
		}
		b.WriteRune(sparkBars[idx])
	}
	return b.String()
}

// HistoryMetricReport is the trend view of one metric across time.
type HistoryMetricReport struct {
	Def       MetricDef
	Series    []P75Value // p75 per collection period, oldest first
	Sparkline string
	First     P75Value // first present value
	Last      P75Value // last present value
	Present   bool     // at least one period had data
}

// HistoryReport is the display-ready result of a history query.
type HistoryReport struct {
	Identifier string
	IsOrigin   bool
	FormFactor string
	Periods    []string // collection-period end dates, oldest first
	Metrics    []HistoryMetricReport
}

// BuildHistoryReport turns a history record into per-metric trend series and
// sparklines, in MetricDefs order.
func BuildHistoryReport(req QueryRequest, rec *HistoryRecord) *HistoryReport {
	rep := &HistoryReport{
		Identifier: req.Identifier(),
		IsOrigin:   req.IsOrigin(),
		FormFactor: rec.Key.FormFactor,
	}
	for _, cp := range rec.CollectionPeriods {
		rep.Periods = append(rep.Periods, cp.LastDate.String())
	}
	for _, def := range MetricDefs {
		hm, ok := rec.Metrics[def.Key]
		if !ok {
			continue
		}
		mr := HistoryMetricReport{Def: def, Series: hm.PercentilesTimeseries.P75s}
		mr.Sparkline = Sparkline(mr.Series)
		for _, v := range mr.Series {
			if v.Valid {
				if !mr.Present {
					mr.First = v
					mr.Present = true
				}
				mr.Last = v
			}
		}
		rep.Metrics = append(rep.Metrics, mr)
	}
	return rep
}
