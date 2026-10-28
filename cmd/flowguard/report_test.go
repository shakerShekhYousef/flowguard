package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

// jsonReport mirrors the report file loosely, so tests see exactly which
// keys were written.
type jsonReport struct {
	Passed    bool `json:"passed"`
	Scenarios []struct {
		Name       string           `json:"name"`
		Passed     bool             `json:"passed"`
		Error      string           `json:"error"`
		Steps      []map[string]any `json:"steps"`
		Assertions []map[string]any `json:"assertions"`
	} `json:"scenarios"`
}

// runWithReport runs the CLI with --report and returns the exit code,
// terminal output, raw report and decoded report. Every report must be
// owner-only.
func runWithReport(t *testing.T, cfg string) (int, string, string, jsonReport) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.json")
	code, out := runCLI(t, cfg, "--report", path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no report written: %v\n%s", err, out)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("report mode = %v (%v), want 0600", info.Mode().Perm(), err)
		}
	}
	var rep jsonReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("report is not valid JSON: %v\n%s", err, data)
	}
	return code, out, string(data), rep
}

// assertionByRule indexes a scenario's assertions by rule name.
func assertionByRule(t *testing.T, list []map[string]any) map[string]map[string]any {
	t.Helper()
	m := map[string]map[string]any{}
	for _, a := range list {
		m[a["rule"].(string)] = a
	}
	return m
}

func wantStep(t *testing.T, step map[string]any, index int, eventID string, status int, outcome string) {
	t.Helper()
	if step["index"] != float64(index) || step["event_id"] != eventID || step["status"] != float64(status) || step["outcome"] != outcome {
		t.Fatalf("step = %v, want index %d event_id %s status %d outcome %s", step, index, eventID, status, outcome)
	}
	if _, ok := step["duration_ms"]; !ok {
		t.Fatalf("step has no duration_ms: %v", step)
	}
	if _, ok := step["error"]; ok {
		t.Fatalf("a completed delivery has no error: %v", step)
	}
}

func wantAssertion(t *testing.T, a map[string]any, passed bool, expected, actual any, reason string) {
	t.Helper()
	if a == nil {
		t.Fatal("assertion missing")
	}
	if a["passed"] != passed || a["expected"] != expected || a["actual"] != actual {
		t.Fatalf("assertion = %v, want passed=%v expected=%v actual=%v", a, passed, expected, actual)
	}
	if got, _ := a["reason"].(string); got != reason {
		t.Fatalf("%s reason = %q, want %q", a["rule"], got, reason)
	}
	if _, ok := a["failed_requests"]; ok {
		t.Fatalf("webhook assertions have no failed_requests: %v", a)
	}
}

func TestWebhookReport_Success(t *testing.T) {
	code, out, _, rep := runWithReport(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), twoPaid, "GET"))

	if code != exitOK || !rep.Passed || !rep.Scenarios[0].Passed || rep.Scenarios[0].Error != "" {
		t.Fatalf("exit %d, report %+v\n%s", code, rep, out)
	}
	sc := rep.Scenarios[0]
	if len(sc.Steps) != 2 {
		t.Fatalf("want 2 steps, got %v", sc.Steps)
	}
	wantStep(t, sc.Steps[0], 0, "evt_paid_1", 200, "delivered")
	wantStep(t, sc.Steps[1], 1, "evt_paid_1", 200, "delivered")
	got := assertionByRule(t, sc.Assertions)
	if len(got) != 4 {
		t.Fatalf("want 4 assertions, got %v", sc.Assertions)
	}
	wantAssertion(t, got["delivery[0].outcome"], true, "delivered", "delivered", "")
	wantAssertion(t, got["delivery[1].outcome"], true, "delivered", "delivered", "")
	wantAssertion(t, got["state.charges"], true, float64(1), float64(1), "")
	wantAssertion(t, got["state.status"], true, "paid", "paid", "")
}

func TestWebhookReport_DuplicateDeliveryFails(t *testing.T) {
	code, _, _, rep := runWithReport(t, fmt.Sprintf(webhookConfig, receiver(t, false, http.StatusOK), twoPaid, "GET"))

	if code != exitAssertionFail || rep.Passed || rep.Scenarios[0].Passed {
		t.Fatalf("exit %d, report %+v", code, rep)
	}
	got := assertionByRule(t, rep.Scenarios[0].Assertions)
	wantAssertion(t, got["state.charges"], false, float64(1), float64(2), "state.charges: expected 1, got 2")
	wantAssertion(t, got["state.status"], true, "paid", "paid", "")
}

func TestWebhookReport_DeliveryAssertionFailures(t *testing.T) {
	deliveries := "      - event: evt_note\n      - event: evt_paid_1\n        expect: {status: 202}"
	code, _, _, rep := runWithReport(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), deliveries, "GET"))

	if code != exitAssertionFail || rep.Passed || rep.Scenarios[0].Passed || rep.Scenarios[0].Error != "" {
		t.Fatalf("exit %d, report %+v", code, rep)
	}
	sc := rep.Scenarios[0]
	wantStep(t, sc.Steps[0], 0, "evt_note", 422, "rejected")
	wantStep(t, sc.Steps[1], 1, "evt_paid_1", 200, "delivered")
	got := assertionByRule(t, sc.Assertions)
	wantAssertion(t, got["delivery[0].outcome"], false, "delivered", "rejected", "delivery[0].outcome: expected delivered, got rejected (status 422)")
	wantAssertion(t, got["delivery[1].outcome"], true, "delivered", "delivered", "")
	wantAssertion(t, got["delivery[1].status"], false, float64(202), float64(200), "delivery[1].status: expected 202, got 200")
	wantAssertion(t, got["state.charges"], true, float64(1), float64(1), "")
}

func TestWebhookReport_StateAssertionFailure(t *testing.T) {
	cfg := strings.Replace(fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), twoPaid, "GET"),
		"state: {status: paid, charges: 1}", "state: {status: refunded, charges: 1, refund_id: r_1}", 1)
	code, _, _, rep := runWithReport(t, cfg)

	if code != exitAssertionFail || rep.Scenarios[0].Passed {
		t.Fatalf("exit %d, report %+v", code, rep)
	}
	got := assertionByRule(t, rep.Scenarios[0].Assertions)
	wantAssertion(t, got["state.status"], false, "refunded", "paid", `state.status: expected "refunded", got "paid"`)
	wantAssertion(t, got["state.refund_id"], false, "r_1", nil, `state.refund_id: expected "r_1", but path not found: no key "refund_id" in object at the top level`)
	wantAssertion(t, got["state.charges"], true, float64(1), float64(1), "")
}

func TestWebhookReport_DeliveryRuntimeError(t *testing.T) {
	url, observed := droppingReceiver(t)
	code, _, raw, rep := runWithReport(t, fmt.Sprintf(droppedConfig, url))

	if code != exitRuntimeError || rep.Passed || rep.Scenarios[0].Passed || observed.Load() {
		t.Fatalf("exit %d, observed %v, report %s", code, observed.Load(), raw)
	}
	sc := rep.Scenarios[0]
	if !strings.HasPrefix(sc.Error, `delivery[1] (event "evt_drop"): `) {
		t.Fatalf("scenario error = %q", sc.Error)
	}
	if len(sc.Steps) != 2 {
		t.Fatalf("want the completed and the failed delivery only, got %v", sc.Steps)
	}
	wantStep(t, sc.Steps[0], 0, "evt_note", 422, "rejected")
	failed := sc.Steps[1]
	if failed["index"] != float64(1) || failed["event_id"] != "evt_drop" || failed["error"] != sc.Error {
		t.Fatalf("failed step = %v", failed)
	}
	if _, ok := failed["outcome"]; ok {
		t.Fatalf("a delivery that failed at runtime has no outcome: %v", failed)
	}
	if len(sc.Assertions) != 1 {
		t.Fatalf("want only the assertion evaluated before the error, got %v", sc.Assertions)
	}
	wantAssertion(t, sc.Assertions[0], false, "delivered", "rejected", "delivery[0].outcome: expected delivered, got rejected (status 422)")
	if strings.Contains(raw, "evt_never") || strings.Contains(raw, "state.") {
		t.Fatalf("nothing after the runtime error may be reported:\n%s", raw)
	}
}

func TestWebhookReport_ObservationRuntimeError(t *testing.T) {
	code, _, raw, rep := runWithReport(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusInternalServerError), twoPaid, "GET"))

	if code != exitRuntimeError || rep.Passed || rep.Scenarios[0].Passed {
		t.Fatalf("exit %d, report %s", code, raw)
	}
	sc := rep.Scenarios[0]
	if !strings.HasPrefix(sc.Error, "observe: GET /_test/orders/ord_1 returned status 500, want 200") {
		t.Fatalf("scenario error = %q", sc.Error)
	}
	wantStep(t, sc.Steps[0], 0, "evt_paid_1", 200, "delivered")
	wantStep(t, sc.Steps[1], 1, "evt_paid_1", 200, "delivered")
	got := assertionByRule(t, sc.Assertions)
	if len(got) != 2 || got["delivery[0].outcome"] == nil || got["delivery[1].outcome"] == nil {
		t.Fatalf("want the delivery assertions and no state assertions, got %v", sc.Assertions)
	}
}

func TestWebhookReport_EventIDMapping(t *testing.T) {
	deliveries := "      - event: evt_note\n      - event: evt_paid_1\n      - event: evt_note\n      - event: evt_paid_1"
	_, _, _, rep := runWithReport(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), deliveries, "GET"))

	sc := rep.Scenarios[0]
	wantIDs := []string{"evt_note", "evt_paid_1", "evt_note", "evt_paid_1"}
	if len(sc.Steps) != len(wantIDs) {
		t.Fatalf("want %d steps, got %v", len(wantIDs), sc.Steps)
	}
	for i, id := range wantIDs {
		if sc.Steps[i]["index"] != float64(i) || sc.Steps[i]["event_id"] != id {
			t.Fatalf("steps[%d] = %v, want index %d event_id %s", i, sc.Steps[i], i, id)
		}
	}
	// Every delivery[i] assertion describes steps[i]: its actual outcome is
	// that step's outcome.
	for _, a := range sc.Assertions {
		var i int
		if n, _ := fmt.Sscanf(a["rule"].(string), "delivery[%d].outcome", &i); n == 1 {
			if a["actual"] != sc.Steps[i]["outcome"] {
				t.Fatalf("%s actual %v does not match steps[%d] %v", a["rule"], a["actual"], i, sc.Steps[i])
			}
		}
	}
}

func TestWebhookReport_Redaction(t *testing.T) {
	// The receiver echoes secrets in every response body; deliveries carry
	// secrets in headers and event JSON.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /webhooks", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"echo":"resp_body_SYNTH"}`))
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"api_token":"tok_STATE_SYNTH","note":"ok","extra":"unasserted_SYNTH"}`))
	})
	mux.HandleFunc("GET /broken", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"password":"pw_BODY_SYNTH"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := fmt.Sprintf(`version: 1
target: {base_url: %s, allowed_hosts: [127.0.0.1]}
scenarios:
  - name: secret-state
    kind: webhook
    endpoint: {method: POST, path: /webhooks, headers: {Authorization: "Bearer tok_AUTH_SYNTH"}}
    events:
      - id: evt_1
        headers: {X-Webhook-Signature: sig_SYNTH}
        json: {id: evt_1, card: "4111111111111111"}
    deliveries: [{event: evt_1}]
    observe: {request: {method: GET, path: /state}}
    assert: {state: {api_token: tok_EXPECT_SYNTH, note: ok}}
  - name: secret-query
    kind: webhook
    endpoint: {method: POST, path: /webhooks}
    events: [{id: evt_2, json: {id: evt_2}}]
    deliveries: [{event: evt_2}]
    observe: {request: {method: GET, path: "/broken?token=tok_QUERY_SYNTH"}}
    assert: {state: {ok: true}}
`, srv.URL)
	code, out, raw, rep := runWithReport(t, cfg)

	if code != exitRuntimeError {
		t.Fatalf("exit = %d, want 2\n%s", code, out)
	}
	for _, secret := range []string{
		"tok_AUTH_SYNTH", "sig_SYNTH", "4111111111111111", "resp_body_SYNTH",
		"tok_STATE_SYNTH", "tok_EXPECT_SYNTH", "unasserted_SYNTH", "tok_QUERY_SYNTH", "pw_BODY_SYNTH",
	} {
		if strings.Contains(raw, secret) || strings.Contains(out, secret) {
			t.Fatalf("%q leaked\n--- report ---\n%s\n--- output ---\n%s", secret, raw, out)
		}
	}
	got := assertionByRule(t, rep.Scenarios[0].Assertions)
	// The reason is checked for leaks above, not for exact text: redact.Text
	// also masks the word after "api_token:" in it.
	tokenRule := got["state.api_token"]
	if tokenRule["passed"] != false || tokenRule["expected"] != "REDACTED" || tokenRule["actual"] != "REDACTED" {
		t.Fatalf("state.api_token = %v, want a failure with REDACTED values", tokenRule)
	}
	wantAssertion(t, got["state.note"], true, "ok", "ok", "")
	if !strings.Contains(rep.Scenarios[1].Error, "token=REDACTED") {
		t.Fatalf("observe error should keep the redacted query name: %q", rep.Scenarios[1].Error)
	}
}

func TestReport_IdempotencyHasNoWebhookFields(t *testing.T) {
	// A target that charges on every replay, so assertions fail and steps
	// carry real statuses.
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusCreated)
	})
	mux.HandleFunc("GET /_test/business-effect-count", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int32{"business_effect_count": calls.Load()})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := fmt.Sprintf(`version: 1
target: {base_url: %s, allowed_hosts: [127.0.0.1]}
scenarios:
  - name: create-order
    kind: idempotency
    request: {method: POST, path: /orders, headers: {Idempotency-Key: k1}, json: {amount: 1}}
    repeat: 2
    assert: {status: 201, business_effect_count: 1}
`, srv.URL)
	code, _, raw, rep := runWithReport(t, cfg)

	if code != exitAssertionFail || len(rep.Scenarios[0].Steps) != 2 {
		t.Fatalf("exit %d, report %s", code, raw)
	}
	for _, step := range rep.Scenarios[0].Steps {
		for key := range step {
			if key != "index" && key != "status" && key != "duration_ms" {
				t.Fatalf("idempotency step has unexpected key %q:\n%s", key, raw)
			}
		}
	}
	if strings.Contains(raw, "event_id") || strings.Contains(raw, "outcome") {
		t.Fatalf("idempotency report contains webhook fields:\n%s", raw)
	}
}

func TestReport_ExistingFileTightenedTo0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not enforced on Windows")
	}
	path := filepath.Join(t.TempDir(), "report.json")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // defeat umask
		t.Fatal(err)
	}

	runCLI(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), twoPaid, "GET"), "--report", path)

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("report mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
}
