package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/scenarios"
)

func TestBuild(t *testing.T) {
	r := Build([]scenarios.Result{
		{Name: "ok", Passed: true, Assertions: []assertions.Result{{Rule: "status", Passed: true}}},
		{Name: "broken", Err: errors.New("connection refused")},
	})

	if r.Passed {
		t.Fatal("report should fail when any scenario errored")
	}
	if !r.Scenarios[0].Passed || r.Scenarios[1].Passed {
		t.Fatalf("unexpected per-scenario results: %+v", r.Scenarios)
	}
	if r.Scenarios[1].Error != "connection refused" {
		t.Fatalf("Error = %q", r.Scenarios[1].Error)
	}
}

func TestWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := Write(path, Build([]scenarios.Result{{Name: "ok", Passed: true}})); err != nil {
		t.Fatalf("Write: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if !got.Passed || got.Scenarios[0].Name != "ok" {
		t.Fatalf("unexpected report: %+v", got)
	}
}

func TestBuild_RedactsSecrets(t *testing.T) {
	// Synthetic credentials as they would appear in net/http errors.
	stepErr := errors.New(`Post "http://fguser:***@127.0.0.1:1/orders?api_key=sk_test_SYNTH": dial tcp 127.0.0.1:1: connect: connection refused`)
	r := Build([]scenarios.Result{{
		Name:  "leaky",
		Err:   fmt.Errorf("step 0: %w (Authorization: Bearer tok_SYNTH)", stepErr),
		Steps: []scenarios.StepReport{{Index: 0, Err: stepErr}},
		Assertions: []assertions.Result{{
			Rule:   "status",
			Reason: "status: expected 200, got 401 for token=tok_SYNTH",
		}},
	}})

	path := filepath.Join(t.TempDir(), "report.json")
	if err := Write(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{"sk_test_SYNTH", "fguser", "tok_SYNTH"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("report leaks %q:\n%s", secret, data)
		}
	}
	for _, keep := range []string{"127.0.0.1:1/orders", "connection refused", "expected 200, got 401"} {
		if !strings.Contains(string(data), keep) {
			t.Fatalf("report lost diagnostic context %q:\n%s", keep, data)
		}
	}
}

func TestBuild_StatusFailureDetails(t *testing.T) {
	failing := assertions.Evaluate(
		assertions.Rule{Status: intPtr(200)},
		assertions.Observed{Statuses: []int{500, 200}},
	)
	passing := assertions.Evaluate(
		assertions.Rule{Status: intPtr(200)},
		assertions.Observed{Statuses: []int{200, 200}},
	)
	r := Build([]scenarios.Result{
		{Name: "flaky", Passed: false, Assertions: failing},
		{Name: "steady", Passed: true, Assertions: passing},
	})

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Scenarios []struct {
			Assertions []map[string]any `json:"assertions"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}

	fail := doc.Scenarios[0].Assertions[0]
	if fail["expected"] != float64(200) || fail["actual"] != float64(500) {
		t.Fatalf("expected/actual should stay scalar ints, got %v / %v", fail["expected"], fail["actual"])
	}
	fr, ok := fail["failed_requests"].([]any)
	if !ok || len(fr) != 1 {
		t.Fatalf("failed_requests missing or wrong: %s", data)
	}
	if got := fr[0].(map[string]any); got["index"] != float64(0) || got["expected"] != float64(200) || got["actual"] != float64(500) {
		t.Fatalf("failed_requests[0] = %v", got)
	}

	pass := doc.Scenarios[1].Assertions[0]
	if _, present := pass["failed_requests"]; present {
		t.Fatalf("failed_requests must be omitted when the assertion passed: %s", data)
	}
	if pass["actual"] != float64(200) {
		t.Fatalf("passing actual = %v, want 200", pass["actual"])
	}
}

func intPtr(v int) *int { return &v }
