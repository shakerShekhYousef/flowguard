// Package scenarios implements scenario kinds declared in flowguard.yaml.
// v0.1 ships "idempotency" (FR-03, FR-04); webhook-fault kinds arrive in
// v0.2 (FR-08).
package scenarios

import (
	"context"
	"fmt"
	"time"

	"github.com/shakeryoussef/flowguard/internal/assertions"
	"github.com/shakeryoussef/flowguard/internal/config"
	"github.com/shakeryoussef/flowguard/internal/runner"
)

// EffectCountHeader names the request header whose value is used as the key
// when querying a target's business-effect test endpoint.
const EffectCountHeader = "Idempotency-Key"

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

	var lastStatus int
	for i := 0; i < sc.Repeat; i++ {
		sr := client.Do(ctx, baseURL, sc.Request)
		result.Steps = append(result.Steps, StepReport{Index: i, Status: sr.Status, Duration: sr.Duration, Err: sr.Err})
		if sr.Err != nil {
			result.Err = fmt.Errorf("step %d: %w", i, sr.Err)
			return result
		}
		lastStatus = sr.Status
	}

	key := sc.Request.Headers[EffectCountHeader]
	count, err := queryBusinessEffectCount(ctx, client, baseURL, key)
	if err != nil {
		result.Err = fmt.Errorf("querying business effect count: %w", err)
		return result
	}

	rule := assertions.Rule{Status: sc.Assert.Status, BusinessEffectCount: sc.Assert.BusinessEffectCount}
	observed := assertions.Observed{LastStatus: lastStatus, BusinessEffectCount: count}
	result.Assertions = assertions.Evaluate(rule, observed)
	result.Passed = assertions.AllPassed(result.Assertions)

	return result
}

func queryBusinessEffectCount(ctx context.Context, client *runner.Client, baseURL, key string) (int, error) {
	u := baseURL + "/_test/business-effect-count?key=" + key

	var out struct {
		BusinessEffectCount int `json:"business_effect_count"`
	}
	if err := client.GetJSON(ctx, u, &out); err != nil {
		return 0, err
	}
	return out.BusinessEffectCount, nil
}
