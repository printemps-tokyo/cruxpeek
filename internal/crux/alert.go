package crux

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Budgets are user-set p75 ceilings, keyed by metric key. They are stricter
// (or looser) house limits on top of the official thresholds, for any metric
// including FCP and TTFB. A metric breaches its budget when p75 > budget.
type Budgets map[string]float64

// ParseBudgets reads "lcp=2000,inp=150,cls=0.08,ttfb=600". Labels are
// case-insensitive; timing values are milliseconds, CLS is unitless.
func ParseBudgets(spec string) (Budgets, error) {
	out := Budgets{}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid budget %q: want metric=value, e.g. lcp=2000", part)
		}
		def, found := defByLabel(strings.TrimSpace(name))
		if !found {
			return nil, fmt.Errorf("invalid budget %q: unknown metric %q (lcp, inp, cls, fcp, ttfb)", part, name)
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || v <= 0 || v != v {
			return nil, fmt.Errorf("invalid budget %q: want a positive number", part)
		}
		if _, dup := out[def.Key]; dup {
			return nil, fmt.Errorf("budget for %s given twice", def.Label)
		}
		out[def.Key] = v
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("empty --budget")
	}
	return out, nil
}

func defByLabel(label string) (MetricDef, bool) {
	for _, d := range MetricDefs {
		if strings.EqualFold(d.Label, label) || strings.EqualFold(d.Key, label) {
			return d, true
		}
	}
	return MetricDef{}, false
}

// Breach is one metric over a limit.
type Breach struct {
	Identifier string     `json:"identifier"`
	Metric     string     `json:"metric"` // label, e.g. "LCP"
	Key        string     `json:"key"`
	P75        float64    `json:"p75"`
	Unit       string     `json:"unit"`
	Assessment Assessment `json:"assessment"`
	// Rule is "poor" (above the official poor threshold), "needs-improvement"
	// (above the good threshold, under --strict), or "budget".
	Rule  string  `json:"rule"`
	Limit float64 `json:"limit"` // the threshold or budget that was exceeded
}

// Breaches lists every limit a report exceeds: a poor Core Web Vital, a Core
// Web Vital that needs improvement (strict), and any metric over its budget.
// A metric over both an official threshold and a budget is listed once, with
// the official rule.
func (r *Report) Breaches(strict bool, budgets Budgets) []Breach {
	var out []Breach
	for _, m := range r.Metrics {
		if !m.Present {
			continue
		}
		b := Breach{
			Identifier: r.Identifier,
			Metric:     m.Def.Label,
			Key:        m.Def.Key,
			P75:        m.P75,
			Unit:       m.Def.Unit,
			Assessment: m.Assessment,
		}
		switch {
		case m.Def.Core && m.Assessment == Poor:
			b.Rule, b.Limit = "poor", m.Def.Poor
		case m.Def.Core && strict && m.Assessment == NeedsImprovement:
			b.Rule, b.Limit = "needs-improvement", m.Def.Good
		default:
			limit, ok := budgets[m.Def.Key]
			if !ok || m.P75 <= limit {
				continue
			}
			b.Rule, b.Limit = "budget", limit
		}
		out = append(out, b)
	}
	return out
}

// Describe renders a breach as a short phrase, e.g.
// "LCP p75 4200 ms (poor: > 4000 ms)".
func (b Breach) Describe() string {
	def, _ := defByLabel(b.Metric)
	op := ">"
	if b.Rule == "needs-improvement" {
		op = "> good"
	}
	return fmt.Sprintf("%s p75 %s (%s: %s %s)", b.Metric, def.FormatValue(b.P75), b.Rule, op, def.FormatValue(b.Limit))
}

// Alert is the --notify-json document: only what crossed a limit, plus a
// ready-made one-line summary in "text" (the field a Slack incoming webhook
// reads).
type Alert struct {
	Status   string   `json:"status"` // "ok", "breach", or "error"
	Checked  int      `json:"checked"`
	Breaches []Breach `json:"breaches"`
	NoData   []string `json:"no_data"`
	Errors   []string `json:"errors"`
	Text     string   `json:"text"`
}

// BuildAlert summarizes the breaches across several targets.
func BuildAlert(checked int, breaches []Breach, noData, errs []string) Alert {
	a := Alert{
		Checked:  checked,
		Breaches: breaches,
		NoData:   noData,
		Errors:   errs,
	}
	if a.Breaches == nil {
		a.Breaches = []Breach{}
	}
	if a.NoData == nil {
		a.NoData = []string{}
	}
	if a.Errors == nil {
		a.Errors = []string{}
	}
	targets := "target"
	if checked != 1 {
		targets = "targets"
	}
	switch {
	case len(breaches) > 0:
		a.Status = "breach"
		byTarget := map[string][]string{}
		var order []string
		for _, b := range breaches {
			if _, seen := byTarget[b.Identifier]; !seen {
				order = append(order, b.Identifier)
			}
			byTarget[b.Identifier] = append(byTarget[b.Identifier], b.Describe())
		}
		sort.Strings(order)
		parts := make([]string, 0, len(order))
		for _, id := range order {
			parts = append(parts, id+": "+strings.Join(byTarget[id], ", "))
		}
		a.Text = fmt.Sprintf("cruxpeek: %d limit(s) exceeded across %d %s. %s", len(breaches), checked, targets, strings.Join(parts, "; "))
	case len(errs) > 0:
		a.Status = "error"
		a.Text = fmt.Sprintf("cruxpeek: %d of %d %s could not be checked: %s", len(errs), checked, targets, strings.Join(errs, "; "))
	default:
		a.Status = "ok"
		a.Text = fmt.Sprintf("cruxpeek: %d %s within limits", checked, targets)
	}
	if len(errs) > 0 && a.Status == "breach" {
		a.Text += fmt.Sprintf(" (could not check: %s)", strings.Join(errs, "; "))
	}
	if len(noData) > 0 && a.Status != "error" {
		a.Text += fmt.Sprintf(" (no CrUX data for %s)", strings.Join(noData, ", "))
	}
	return a
}
