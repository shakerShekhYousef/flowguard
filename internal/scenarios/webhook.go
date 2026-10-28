package scenarios

import (
	"context"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

// RunWebhook runs a webhook scenario: FlowGuard acts as the provider and
// delivers sc's events to the receiver under test, then reads its final
// business state. Order: setup, every delivery (each checked against its
// expectation), state observation, state assertions.
//
// A setup, delivery or observation runtime error stops the scenario and is
// returned in Result.Err; results gathered before it are kept, and state is
// never observed after a failed delivery. Assertion failures never stop
// execution.
func RunWebhook(ctx context.Context, client *runner.Client, baseURL string, sc config.Scenario) Result {
	result := Result{Name: sc.Name}

	if err := runSetup(ctx, client, baseURL, sc.Setup); err != nil {
		result.Err = err
		return result
	}

	deliveries, checks, err := runDeliveries(ctx, client, baseURL, sc)
	for _, d := range deliveries {
		result.Steps = append(result.Steps, StepReport{
			Index:    d.Index,
			Status:   d.Status,
			Duration: d.Duration,
			Err:      d.Err,
			EventID:  d.EventID,
			Outcome:  d.Outcome,
		})
	}
	result.Assertions = checks
	if err != nil {
		result.Err = err
		return result
	}

	stateChecks, err := checkState(ctx, client, baseURL, sc)
	if err != nil {
		result.Err = err
		return result
	}
	result.Assertions = append(result.Assertions, stateChecks...)
	result.Passed = assertions.AllPassed(result.Assertions)
	return result
}
