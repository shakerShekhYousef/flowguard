// Package report builds and writes the machine-readable run report (FR-06).
//
// Steps record status, timing and error only — never request/response bodies
// or headers. Every free-text field (errors, assertion reasons) passes
// through redact.Text before it is stored, so URLs carry no userinfo or
// query values and credential-like name/value pairs are masked.
package report

import (
	"encoding/json"
	"os"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/redact"
	"github.com/shakerShekhYousef/flowguard/internal/scenarios"
)

type Report struct {
	GeneratedAt time.Time        `json:"generated_at"`
	Passed      bool             `json:"passed"`
	Scenarios   []ScenarioReport `json:"scenarios"`
}

type ScenarioReport struct {
	Name       string            `json:"name"`
	Passed     bool              `json:"passed"`
	Error      string            `json:"error,omitempty"`
	Steps      []StepReport      `json:"steps"`
	Assertions []AssertionReport `json:"assertions"`
}

type StepReport struct {
	Index      int    `json:"index"`
	Status     int    `json:"status"`
	DurationMS int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type AssertionReport struct {
	Rule     string `json:"rule"`
	Expected any    `json:"expected"`
	Actual   any    `json:"actual"`
	Passed   bool   `json:"passed"`
	Reason   string `json:"reason,omitempty"`
	// FailedRequests is present only when a per-request rule failed.
	FailedRequests []RequestFailureReport `json:"failed_requests,omitempty"`
}

type RequestFailureReport struct {
	Index    int `json:"index"`
	Expected int `json:"expected"`
	Actual   int `json:"actual"`
}

func Build(results []scenarios.Result) Report {
	r := Report{GeneratedAt: time.Now().UTC(), Passed: true}

	for _, sr := range results {
		scenarioPassed := sr.Err == nil && sr.Passed
		if !scenarioPassed {
			r.Passed = false
		}

		sRep := ScenarioReport{Name: sr.Name, Passed: scenarioPassed}
		sRep.Error = redact.Error(sr.Err)

		for _, step := range sr.Steps {
			sRep.Steps = append(sRep.Steps, StepReport{
				Index:      step.Index,
				Status:     step.Status,
				DurationMS: step.Duration.Milliseconds(),
				Error:      redact.Error(step.Err),
			})
		}

		for _, a := range sr.Assertions {
			aRep := AssertionReport{
				Rule:     a.Rule,
				Expected: a.Expected,
				Actual:   a.Actual,
				Passed:   a.Passed,
				Reason:   redact.Text(a.Reason),
			}
			for _, f := range a.FailedRequests {
				aRep.FailedRequests = append(aRep.FailedRequests, RequestFailureReport(f))
			}
			sRep.Assertions = append(sRep.Assertions, aRep)
		}

		r.Scenarios = append(r.Scenarios, sRep)
	}

	return r
}

// reportFileMode keeps reports owner-only: they describe target hosts,
// paths and failures, even though secrets are redacted.
const reportFileMode = 0o600

// Write saves r as indented JSON with reportFileMode. An existing file is
// truncated and tightened to reportFileMode too, since os.WriteFile alone
// only applies the mode when it creates the file.
func Write(path string, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, reportFileMode)
	if err != nil {
		return err
	}
	if err := f.Chmod(reportFileMode); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
