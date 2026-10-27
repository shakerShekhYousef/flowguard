// Package scenarios implements scenario kinds declared in flowguard.yaml.
// v0.1 ships "idempotency" (FR-03, FR-04); webhook-fault kinds arrive in
// v0.2 (FR-08).
package scenarios

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

type StepReport struct {
	Index    int
	Status   int
	Duration time.Duration
	Err      error
}

type Result struct {
	Name       string
	Steps      []StepReport
	Assertions []assertions.Result
	Passed     bool
	Err        error // set on infrastructure failure, distinct from assertion failure
}

// RunIdempotency sends sc.Request sc.Repeat times using the same
// Idempotency-Key, then asks the target's test-state endpoint how many times
// the business effect actually applied. A correct implementation applies it
// once no matter how many times the request is replayed.
//
// Convention (v0.1, example mock server only — see docs/test-state.md):
// GET {base_url}/_test/business-effect-count?key=<Idempotency-Key> ->
// {"business_effect_count": N}. This is a stand-in until real targets can
// expose their own test-state hook (see open product decision in the plan).
func RunIdempotency(ctx context.Context, client *runner.Client, baseURL string, sc config.Scenario) Result {
	result := Result{Name: sc.Name}

	if err := runSetup(ctx, client, baseURL, sc.Setup); err != nil {
		result.Err = err
		return result
	}

	var statuses []int
	for i := 0; i < sc.Repeat; i++ {
		sr := client.Do(ctx, baseURL, sc.Request)
		result.Steps = append(result.Steps, StepReport{Index: i, Status: sr.Status, Duration: sr.Duration, Err: sr.Err})
		if sr.Err != nil {
			result.Err = fmt.Errorf("step %d: %w", i, sr.Err)
			return result
		}
		statuses = append(statuses, sr.Status)
	}

	key := sc.Request.Header(config.IdempotencyKeyHeader)
	count, err := queryBusinessEffectCount(ctx, client, baseURL, key)
	if err != nil {
		result.Err = fmt.Errorf("querying business effect count: %w", err)
		return result
	}

	rule := assertions.Rule{Status: sc.Assert.Status, BusinessEffectCount: sc.Assert.BusinessEffectCount}
	observed := assertions.Observed{Statuses: statuses, BusinessEffectCount: count}
	result.Assertions = assertions.Evaluate(rule, observed)
	result.Passed = assertions.AllPassed(result.Assertions)

	return result
}

func queryBusinessEffectCount(ctx context.Context, client *runner.Client, baseURL, key string) (int, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return 0, fmt.Errorf("parsing base url: %w", err)
	}
	u := base.ResolveReference(&url.URL{Path: "/_test/business-effect-count"})
	u.RawQuery = url.Values{"key": {key}}.Encode()

	var out struct {
		BusinessEffectCount int `json:"business_effect_count"`
	}
	if err := client.GetJSON(ctx, u.String(), &out); err != nil {
		return 0, err
	}
	return out.BusinessEffectCount, nil
}
