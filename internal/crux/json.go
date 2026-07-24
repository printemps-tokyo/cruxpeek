package crux

// This file defines the stable, machine-readable shapes emitted by --json.

// JSONThresholds is a metric's good/poor p75 boundaries.
type JSONThresholds struct {
	Good float64 `json:"good"`
	Poor float64 `json:"poor"`
}

// JSONDistribution is the good/needs-improvement/poor split, in percent.
type JSONDistribution struct {
	Good             float64 `json:"good"`
	NeedsImprovement float64 `json:"needs_improvement"`
	Poor             float64 `json:"poor"`
}

// JSONMetric is one graded metric in the --json output.
type JSONMetric struct {
	Key          string           `json:"key"`
	Label        string           `json:"label"`
	Name         string           `json:"name"`
	Unit         string           `json:"unit"`
	Core         bool             `json:"core"`
	Present      bool             `json:"present"`
	P75          *float64         `json:"p75"`
	Assessment   Assessment       `json:"assessment"`
	Thresholds   JSONThresholds   `json:"thresholds"`
	Distribution JSONDistribution `json:"distribution"`
}

// JSONQuery echoes the query subject.
type JSONQuery struct {
	Identifier       string `json:"identifier"`
	Scope            string `json:"scope"` // "url" or "origin"
	FormFactor       string `json:"form_factor"`
	CollectionPeriod string `json:"collection_period,omitempty"`
}

// JSONReport is the full --json shape for a single query.
type JSONReport struct {
	Query    JSONQuery    `json:"query"`
	Metrics  []JSONMetric `json:"metrics"`
	ExitCode int          `json:"exit_code"`
}

// ToJSON converts a graded Report into its stable JSON representation. strict
// is threaded through so exit_code matches the process exit code.
func (r *Report) ToJSON(strict bool) JSONReport {
	scope := "url"
	if r.IsOrigin {
		scope = "origin"
	}
	out := JSONReport{
		Query: JSONQuery{
			Identifier:       r.Identifier,
			Scope:            scope,
			FormFactor:       FormFactorLabel(r.FormFactor),
			CollectionPeriod: r.CollectionPeriod,
		},
		Metrics:  make([]JSONMetric, 0, len(r.Metrics)),
		ExitCode: r.ExitCode(strict),
	}
	for _, m := range r.Metrics {
		jm := JSONMetric{
			Key:        m.Def.Key,
			Label:      m.Def.Label,
			Name:       m.Def.Name,
			Unit:       m.Def.Unit,
			Core:       m.Def.Core,
			Present:    m.Present,
			Assessment: m.Assessment,
			Thresholds: JSONThresholds{Good: m.Def.Good, Poor: m.Def.Poor},
			Distribution: JSONDistribution{
				Good:             m.DistGood,
				NeedsImprovement: m.DistNI,
				Poor:             m.DistPoor,
			},
		}
		if m.Present {
			v := m.P75
			jm.P75 = &v
		}
		out.Metrics = append(out.Metrics, jm)
	}
	return out
}

// JSONHistoryMetric is one metric's trend in the --json history output.
type JSONHistoryMetric struct {
	Key       string     `json:"key"`
	Label     string     `json:"label"`
	Name      string     `json:"name"`
	Unit      string     `json:"unit"`
	Core      bool       `json:"core"`
	Sparkline string     `json:"sparkline"`
	P75s      []*float64 `json:"p75s"`
}

// JSONHistoryReport is the full --json shape for a history query.
type JSONHistoryReport struct {
	Query   JSONQuery           `json:"query"`
	Periods []string            `json:"periods"`
	Metrics []JSONHistoryMetric `json:"metrics"`
}

// ToJSON converts a HistoryReport into its stable JSON representation.
func (r *HistoryReport) ToJSON() JSONHistoryReport {
	scope := "url"
	if r.IsOrigin {
		scope = "origin"
	}
	out := JSONHistoryReport{
		Query: JSONQuery{
			Identifier: r.Identifier,
			Scope:      scope,
			FormFactor: FormFactorLabel(r.FormFactor),
		},
		Periods: r.Periods,
		Metrics: make([]JSONHistoryMetric, 0, len(r.Metrics)),
	}
	if out.Periods == nil {
		out.Periods = []string{}
	}
	for _, m := range r.Metrics {
		jm := JSONHistoryMetric{
			Key:       m.Def.Key,
			Label:     m.Def.Label,
			Name:      m.Def.Name,
			Unit:      m.Def.Unit,
			Core:      m.Def.Core,
			Sparkline: m.Sparkline,
			P75s:      make([]*float64, 0, len(m.Series)),
		}
		for _, v := range m.Series {
			if v.Valid {
				val := v.Value
				jm.P75s = append(jm.P75s, &val)
			} else {
				jm.P75s = append(jm.P75s, nil)
			}
		}
		out.Metrics = append(out.Metrics, jm)
	}
	return out
}
