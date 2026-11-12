package crux

import "testing"

func TestBreachesRulesAndBudgets(t *testing.T) {
	r := &Report{Identifier: "https://x.example"}
	for _, d := range MetricDefs {
		mr := MetricReport{Def: d, Present: true}
		switch d.Label {
		case "LCP":
			mr.P75 = 4500 // poor, and over a 3000 budget: listed once as poor
		case "INP":
			mr.P75 = 300 // needs improvement
		case "CLS":
			mr.P75 = 0.05
		case "FCP":
			mr.P75 = 2000 // over a 1500 budget
		case "TTFB":
			mr.Present = false // budgeted but missing: no breach
		}
		mr.Assessment = Grade(mr.P75, d.Good, d.Poor)
		r.Metrics = append(r.Metrics, mr)
	}
	budgets, err := ParseBudgets("lcp=3000,fcp=1500,ttfb=100")
	if err != nil {
		t.Fatal(err)
	}
	got := r.Breaches(false, budgets)
	if len(got) != 2 || got[0].Metric != "LCP" || got[0].Rule != "poor" || got[1].Metric != "FCP" || got[1].Rule != "budget" || got[1].Limit != 1500 {
		t.Errorf("non-strict: %+v", got)
	}
	strict := r.Breaches(true, nil)
	if len(strict) != 2 || strict[1].Metric != "INP" || strict[1].Rule != "needs-improvement" {
		t.Errorf("strict: %+v", strict)
	}
	if d := got[0].Describe(); d != "LCP p75 4500 ms (poor: > 4000 ms)" {
		t.Errorf("describe: %q", d)
	}
	a := BuildAlert(2, got, []string{"https://y.example"}, nil)
	if a.Status != "breach" || a.Text == "" || len(a.Errors) != 0 {
		t.Errorf("alert: %+v", a)
	}
}
