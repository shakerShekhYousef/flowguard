// Package assertions evaluates a scenario's declared rules against observed
// results (FR-05): HTTP status and business-effect counts today, richer
// JSON-value checks later.
package assertions

import (
	"fmt"
	"strings"
)

type Result struct {
	Rule     string
	Expected any
	Actual   any
	Passed   bool
	Reason   string
	// FailedRequests lists every request that broke a per-request rule
	// (today: status). Empty when the rule passed or is not per-request.
	FailedRequests []RequestFailure
}

// RequestFailure identifies one replayed request that did not match.
type RequestFailure struct {
	Index    int
	Expected int
	Actual   int
}

// Observed carries what a scenario actually saw, independent of how it was
// obtained (idempotency replay today, webhook replay later).
type Observed struct {
	// Statuses holds the HTTP status of every replayed request, in order.
	Statuses            []int
	BusinessEffectCount int
}

type Rule struct {
	Status              *int
	BusinessEffectCount *int
}

func Evaluate(rule Rule, observed Observed) []Result {
	var results []Result

	if rule.Status != nil {
		results = append(results, evaluateStatus(*rule.Status, observed.Statuses))
	}

	if rule.BusinessEffectCount != nil {
		passed := observed.BusinessEffectCount == *rule.BusinessEffectCount
		results = append(results, Result{
			Rule:     "business_effect_count",
			Expected: *rule.BusinessEffectCount,
			Actual:   observed.BusinessEffectCount,
			Passed:   passed,
			Reason:   reasonIfFailed(passed, "business_effect_count", *rule.BusinessEffectCount, observed.BusinessEffectCount),
		})
	}

	return results
}

// evaluateStatus requires every replayed request to return expected. A
// retry that fails before a later one succeeds is still a failure: clients
// see that error response.
func evaluateStatus(expected int, statuses []int) Result {
	r := Result{Rule: "status", Expected: expected}

	if len(statuses) == 0 {
		r.Reason = "status: no responses were recorded"
		return r
	}

	for i, s := range statuses {
		if s != expected {
			r.FailedRequests = append(r.FailedRequests, RequestFailure{Index: i, Expected: expected, Actual: s})
		}
	}

	if len(r.FailedRequests) == 0 {
		r.Passed = true
		r.Actual = expected
		return r
	}

	// Actual stays a single int (the first mismatch) for report consumers
	// that predate FailedRequests.
	r.Actual = r.FailedRequests[0].Actual
	parts := make([]string, len(r.FailedRequests))
	for i, f := range r.FailedRequests {
		parts[i] = fmt.Sprintf("request[%d] got %d", f.Index, f.Actual)
	}
	r.Reason = fmt.Sprintf("status: expected %d for all %d requests, %s", expected, len(statuses), strings.Join(parts, ", "))
	return r
}

func reasonIfFailed(passed bool, name string, expected, actual int) string {
	if passed {
		return ""
	}
	return fmt.Sprintf("%s: expected %d, got %d", name, expected, actual)
}

func AllPassed(results []Result) bool {
	for _, r := range results {
		if !r.Passed {
			return false
		}
	}
	return true
}
