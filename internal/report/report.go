// Package report builds and writes the machine-readable run report (FR-06).
//
// Steps only record status/timing/error today, not raw request or response
// bodies, so there is nothing sensitive to redact yet. If a future version
// adds body/header capture for richer reproduction evidence, it must redact
// fields matching names like authorization/token/secret/password/api-key
// before they reach this package.
package report

import (
	"encoding/json"
	"os"
	"time"

	"github.com/shakeryoussef/flowguard/internal/scenarios"
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
}

func Build(results []scenarios.Result) Report {
	r := Report{GeneratedAt: time.Now().UTC(), Passed: true}

	for _, sr := range results {
		scenarioPassed := sr.Err == nil && sr.Passed
		if !scenarioPassed {
			r.Passed = false
		}

		sRep := ScenarioReport{Name: sr.Name, Passed: scenarioPassed}
		if sr.Err != nil {
			sRep.Error = sr.Err.Error()
		}

		for _, step := range sr.Steps {
			stepErr := ""
			if step.Err != nil {
				stepErr = step.Err.Error()
			}
			sRep.Steps = append(sRep.Steps, StepReport{
				Index:      step.Index,
				Status:     step.Status,
				DurationMS: step.Duration.Milliseconds(),
				Error:      stepErr,
			})
		}

		for _, a := range sr.Assertions {
			sRep.Assertions = append(sRep.Assertions, AssertionReport{
				Rule:     a.Rule,
				Expected: a.Expected,
				Actual:   a.Actual,
				Passed:   a.Passed,
				Reason:   a.Reason,
			})
		}

		r.Scenarios = append(r.Scenarios, sRep)
	}

	return r
}

func Write(path string, r Report) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
