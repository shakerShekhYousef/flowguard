package scenarios

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

// DeliveryResult records one webhook delivery.
type DeliveryResult struct {
	Index    int
	EventID  string
	Status   int
	Outcome  string // config.OutcomeDelivered or OutcomeRejected; empty on a runtime error
	Duration time.Duration
	Err      error
}

// runDeliveries sends sc.Deliveries strictly in declared order, each with
// its event's JSON encoded once and sent byte-for-byte via DoBody. Every
// completed delivery is checked against its expectation; a failed check is
// recorded and the remaining deliveries still run. A transport, policy or
// encoding error is a runtime error: it is returned and later deliveries
// are not sent.
func runDeliveries(ctx context.Context, client *runner.Client, baseURL string, sc config.Scenario) ([]DeliveryResult, []assertions.Result, error) {
	if sc.Endpoint == nil {
		return nil, nil, fmt.Errorf("webhook scenario has no endpoint")
	}
	events := make(map[string]config.Event, len(sc.Events))
	for _, e := range sc.Events {
		events[e.ID] = e
	}

	var results []DeliveryResult
	var checks []assertions.Result
	for i, d := range sc.Deliveries {
		res := DeliveryResult{Index: i, EventID: d.Event}

		event, ok := events[d.Event]
		if !ok {
			res.Err = fmt.Errorf("delivery[%d]: event %q is not declared", i, d.Event)
			return append(results, res), checks, res.Err
		}
		body, err := json.Marshal(event.JSON)
		if err != nil {
			res.Err = fmt.Errorf("delivery[%d] (event %q): encoding json: %w", i, d.Event, err)
			return append(results, res), checks, res.Err
		}

		req := config.Request{
			Method:  sc.Endpoint.Method,
			Path:    sc.Endpoint.Path,
			Headers: mergeHeaders(sc.Endpoint.Headers, event.Headers),
		}
		sr := client.DoBody(ctx, baseURL, req, body)
		res.Status, res.Duration = sr.Status, sr.Duration
		if sr.Err != nil {
			res.Err = fmt.Errorf("delivery[%d] (event %q): %w", i, d.Event, sr.Err)
			return append(results, res), checks, res.Err
		}

		res.Outcome = classifyOutcome(sr.Status)
		results = append(results, res)

		var wantStatus *int
		if d.Expect != nil {
			wantStatus = d.Expect.Status
		}
		checks = append(checks, assertions.EvaluateDelivery(i, d.ExpectedOutcome(), wantStatus, res.Outcome, res.Status)...)
	}
	return results, checks, nil
}

func classifyOutcome(status int) string {
	if status >= 200 && status <= 299 {
		return config.OutcomeDelivered
	}
	return config.OutcomeRejected
}

// mergeHeaders returns a new map with defaults overridden by overrides,
// keyed by canonical header name so no header is sent twice in different
// case. Neither input is modified.
func mergeHeaders(defaults, overrides map[string]string) map[string]string {
	merged := make(map[string]string, len(defaults)+len(overrides))
	for k, v := range defaults {
		merged[http.CanonicalHeaderKey(k)] = v
	}
	for k, v := range overrides {
		merged[http.CanonicalHeaderKey(k)] = v
	}
	return merged
}
