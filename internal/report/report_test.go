package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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

func TestWrite_OwnerOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	r := Build([]scenarios.Result{{Name: "ok", Passed: true}})

	t.Run("new file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "report.json")
		if err := Write(path, r); err != nil {
			t.Fatal(err)
		}
		assertMode(t, path, 0o600)
	})

	t.Run("existing world-readable file is tightened", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "report.json")
		// Longer than the new report, so incomplete truncation would show.
		if err := os.WriteFile(path, []byte(strings.Repeat("stale ", 500)), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o644); err != nil { // defeat umask
			t.Fatal(err)
		}

		if err := Write(path, r); err != nil {
			t.Fatal(err)
		}
		assertMode(t, path, 0o600)

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got Report
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("old contents were not fully truncated: %v\n%s", err, data)
		}
	})
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode = %#o, want %#o", got, want)
	}
}

func TestBuild_WebhookSteps(t *testing.T) {
	r := Build([]scenarios.Result{{
		Name: "wh",
		Err:  errors.New("delivery[2] (event \"evt_c\"): EOF"),
		Steps: []scenarios.StepReport{
			{Index: 0, EventID: "evt_a", Status: 200, Outcome: "delivered"},
			{Index: 1, EventID: "evt_b", Status: 409, Outcome: "rejected"},
			{Index: 2, EventID: "evt_c", Err: errors.New("delivery[2] (event \"evt_c\"): EOF")},
		},
	}})

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Scenarios []struct {
			Steps []map[string]any `json:"steps"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	steps := doc.Scenarios[0].Steps

	for i, want := range []struct{ id, outcome string }{{"evt_a", "delivered"}, {"evt_b", "rejected"}, {"evt_c", ""}} {
		if steps[i]["index"] != float64(i) || steps[i]["event_id"] != want.id {
			t.Fatalf("steps[%d] = %v, want index %d event_id %s", i, steps[i], i, want.id)
		}
		outcome, present := steps[i]["outcome"]
		if want.outcome == "" && present {
			t.Fatalf("a delivery that failed at runtime has no outcome: %v", steps[i])
		}
		if want.outcome != "" && outcome != want.outcome {
			t.Fatalf("steps[%d].outcome = %v, want %s", i, outcome, want.outcome)
		}
		if _, hasErr := steps[i]["error"]; hasErr != (i == 2) {
			t.Fatalf("steps[%d] error presence = %v: %v", i, hasErr, steps[i])
		}
	}
}

func TestBuild_WebhookRedaction(t *testing.T) {
	r := Build([]scenarios.Result{{
		Name: "wh",
		Steps: []scenarios.StepReport{
			{Index: 0, EventID: "evt token=tok_EVT_SYNTH", Status: 200, Outcome: "delivered"},
		},
		Assertions: assertions.EvaluateState(
			map[string]any{"api_token": "tok_EXPECT_SYNTH", "note": "see https://u:pw_URL_SYNTH@x.test/?k=q_SYNTH"},
			map[string]any{"api_token": "tok_ACTUAL_SYNTH", "note": "other"}),
	}})

	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"tok_EVT_SYNTH", "tok_EXPECT_SYNTH", "tok_ACTUAL_SYNTH", "pw_URL_SYNTH", "q_SYNTH"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("report leaks %q:\n%s", secret, data)
		}
	}
	if !strings.Contains(string(data), `"rule":"state.api_token","expected":"REDACTED","actual":"REDACTED"`) {
		t.Fatalf("sensitive state path should report REDACTED values:\n%s", data)
	}
}
