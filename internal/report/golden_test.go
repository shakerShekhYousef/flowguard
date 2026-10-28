package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/scenarios"
)

// idempotencyResults covers every shape a v0.1.1 idempotency report can
// take: a passing scenario, failed assertions with failed_requests, a step
// runtime error carrying credentials, and a setup error with no steps. It
// uses only fields that existed in v0.1.1, so the same function, run against
// the v0.1.1 sources, produced testdata/idempotency_v0.1.1.golden.json.
func idempotencyResults() []scenarios.Result {
	ms := time.Millisecond
	stepErr := errors.New(`Post "http://fguser:pw@127.0.0.1:1/orders?api_key=sk_test_SYNTH": dial tcp 127.0.0.1:1: connect: connection refused`)
	return []scenarios.Result{
		{
			Name:   "safe-create-order",
			Passed: true,
			Steps: []scenarios.StepReport{
				{Index: 0, Status: 201, Duration: 4 * ms},
				{Index: 1, Status: 201, Duration: 2 * ms},
				{Index: 2, Status: 201, Duration: 1 * ms},
			},
			Assertions: assertions.Evaluate(
				assertions.Rule{Status: intPtr(201), BusinessEffectCount: intPtr(1)},
				assertions.Observed{Statuses: []int{201, 201, 201}, BusinessEffectCount: 1},
			),
		},
		{
			Name: "buggy-create-order",
			Steps: []scenarios.StepReport{
				{Index: 0, Status: 201, Duration: 3 * ms},
				{Index: 1, Status: 500, Duration: 2 * ms},
			},
			Assertions: assertions.Evaluate(
				assertions.Rule{Status: intPtr(201), BusinessEffectCount: intPtr(1)},
				assertions.Observed{Statuses: []int{201, 500}, BusinessEffectCount: 2},
			),
		},
		{
			Name:  "unreachable",
			Err:   fmt.Errorf("step 0: %w", stepErr),
			Steps: []scenarios.StepReport{{Index: 0, Duration: 7 * ms, Err: stepErr}},
		},
		{
			Name: "setup-fails",
			Err:  errors.New("setup[0]: POST /_test/reset returned status 404, want 2xx"),
		},
	}
}

var (
	generatedAtField = regexp.MustCompile(`"generated_at": "[^"]*"`)
	durationField    = regexp.MustCompile(`"duration_ms": \d+`)
)

// normalize blanks the fields that legitimately differ between runs.
func normalize(data []byte) []byte {
	data = generatedAtField.ReplaceAll(data, []byte(`"generated_at": "<time>"`))
	return durationField.ReplaceAll(data, []byte(`"duration_ms": 0`))
}

// writeAndRead writes r with Write, so the golden comparison covers the
// exact bytes users get on disk.
func writeAndRead(t *testing.T, r Report) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	if err := Write(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readGolden(t *testing.T, name string) []byte {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return want
}

// TestGolden_IdempotencyReportUnchangedSinceV011 compares byte for byte
// (key order, indentation, null steps, omitted fields) with the report
// v0.1.1 wrote for the same results, ignoring only time and duration.
func TestGolden_IdempotencyReportUnchangedSinceV011(t *testing.T) {
	got := normalize(writeAndRead(t, Build(idempotencyResults())))
	want := normalize(readGolden(t, "idempotency_v0.1.1.golden.json"))

	if string(got) != string(want) {
		t.Fatalf("idempotency report differs from v0.1.1\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestGolden_IdempotencyStepsHaveNoWebhookFields checks key sets directly,
// so the guarantee does not depend on the golden file alone.
func TestGolden_IdempotencyStepsHaveNoWebhookFields(t *testing.T) {
	var doc struct {
		Scenarios []struct {
			Steps []map[string]any `json:"steps"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(writeAndRead(t, Build(idempotencyResults())), &doc); err != nil {
		t.Fatal(err)
	}
	steps := 0
	for _, sc := range doc.Scenarios {
		for _, step := range sc.Steps {
			steps++
			for key := range step {
				switch key {
				case "index", "status", "duration_ms", "error":
				default:
					t.Fatalf("idempotency step has unexpected key %q: %v", key, step)
				}
			}
		}
	}
	if steps != 6 {
		t.Fatalf("checked %d steps, want 6", steps)
	}
}

// webhookResults is a webhook scenario stopped by a delivery runtime error
// after a rejected delivery, plus one that completed with a state mismatch.
func webhookResults() []scenarios.Result {
	ms := time.Millisecond
	dropErr := errors.New(`delivery[1] (event "evt_drop"): Post "http://127.0.0.1:8080/webhooks?token=tok_SYNTH": EOF`)
	return []scenarios.Result{
		{
			Name: "duplicate-delivery",
			Steps: []scenarios.StepReport{
				{Index: 0, EventID: "evt_paid_1", Status: 200, Outcome: "delivered", Duration: 5 * ms},
				{Index: 1, EventID: "evt_paid_1", Status: 200, Outcome: "delivered", Duration: 3 * ms},
			},
			Assertions: append(append(
				assertions.EvaluateDelivery(0, "delivered", nil, "delivered", 200),
				assertions.EvaluateDelivery(1, "delivered", intPtr(200), "delivered", 200)...),
				assertions.EvaluateState(
					map[string]any{"charges": 1, "status": "paid"},
					map[string]any{"charges": json.Number("2"), "status": "paid"})...),
		},
		{
			Name: "dropped-delivery",
			Err:  dropErr,
			Steps: []scenarios.StepReport{
				{Index: 0, EventID: "evt_note", Status: 422, Outcome: "rejected", Duration: 2 * ms},
				{Index: 1, EventID: "evt_drop", Duration: 1 * ms, Err: dropErr},
			},
			Assertions: assertions.EvaluateDelivery(0, "delivered", nil, "rejected", 422),
		},
	}
}

// TestGolden_WebhookReport fixes the webhook report shape: event_id and
// outcome sit next to index and status, and a step that failed at runtime
// has no outcome.
func TestGolden_WebhookReport(t *testing.T) {
	got := normalize(writeAndRead(t, Build(webhookResults())))
	want := normalize(readGolden(t, "webhook.golden.json"))

	if string(got) != string(want) {
		t.Fatalf("webhook report differs from golden\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
