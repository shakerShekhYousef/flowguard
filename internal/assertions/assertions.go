// Package assertions evaluates a scenario's declared rules against observed
// results (FR-05): HTTP status and business-effect counts today, richer
// JSON-value checks later.
package assertions

import "fmt"

type Result struct {
	Rule     string
	Expected any
	Actual   any
	Passed   bool
	Reason   string
}

// Observed carries what a scenario actually saw, independent of how it was
// obtained (idempotency replay today, webhook replay later).
type Observed struct {
	LastStatus          int
	BusinessEffectCount int
}

type Rule struct {
	Status              *int
	BusinessEffectCount *int
}

func Evaluate(rule Rule, observed Observed) []Result {
	var results []Result

	if rule.Status != nil {
		passed := observed.LastStatus == *rule.Status
		results = append(results, Result{
			Rule:     "status",
			Expected: *rule.Status,
			Actual:   observed.LastStatus,
			Passed:   passed,
			Reason:   reasonIfFailed(passed, "status", *rule.Status, observed.LastStatus),
		})
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
