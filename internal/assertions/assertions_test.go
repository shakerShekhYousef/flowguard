package assertions

import "testing"

func intPtr(v int) *int { return &v }

func TestEvaluate(t *testing.T) {
	rule := Rule{Status: intPtr(200), BusinessEffectCount: intPtr(1)}

	results := Evaluate(rule, Observed{Statuses: []int{200, 200}, BusinessEffectCount: 1})
	if len(results) != 2 || !AllPassed(results) {
		t.Fatalf("expected two passing results, got %+v", results)
	}

	results = Evaluate(rule, Observed{Statuses: []int{200, 200}, BusinessEffectCount: 2})
	if AllPassed(results) {
		t.Fatalf("expected a failure for a doubled effect, got %+v", results)
	}
	if got, want := results[1].Reason, "business_effect_count: expected 1, got 2"; got != want {
		t.Fatalf("Reason = %q, want %q", got, want)
	}
}

func TestEvaluate_OnlyDeclaredRules(t *testing.T) {
	results := Evaluate(Rule{Status: intPtr(201)}, Observed{Statuses: []int{201}, BusinessEffectCount: 5})
	if len(results) != 1 || results[0].Rule != "status" || !results[0].Passed {
		t.Fatalf("expected only a passing status result, got %+v", results)
	}
}

func TestEvaluateStatus_EveryRequest(t *testing.T) {
	cases := []struct {
		name       string
		statuses   []int
		wantPassed bool
		wantActual int
		wantFailed []RequestFailure
		wantReason string
	}{
		{
			name: "all match", statuses: []int{200, 200, 200},
			wantPassed: true, wantActual: 200,
		},
		{
			name: "first fails, last succeeds", statuses: []int{500, 200},
			wantActual: 500,
			wantFailed: []RequestFailure{{Index: 0, Expected: 200, Actual: 500}},
			wantReason: "status: expected 200 for all 2 requests, request[0] got 500",
		},
		{
			name: "last fails", statuses: []int{200, 409},
			wantActual: 409,
			wantFailed: []RequestFailure{{Index: 1, Expected: 200, Actual: 409}},
			wantReason: "status: expected 200 for all 2 requests, request[1] got 409",
		},
		{
			name: "middle of three fails", statuses: []int{200, 502, 200},
			wantActual: 502,
			wantFailed: []RequestFailure{{Index: 1, Expected: 200, Actual: 502}},
		},
		{
			name: "several fail", statuses: []int{500, 200, 503},
			wantActual: 500,
			wantFailed: []RequestFailure{{Index: 0, Expected: 200, Actual: 500}, {Index: 2, Expected: 200, Actual: 503}},
			wantReason: "status: expected 200 for all 3 requests, request[0] got 500, request[2] got 503",
		},
		{
			name: "no responses", statuses: nil,
			wantReason: "status: no responses were recorded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := Evaluate(Rule{Status: intPtr(200)}, Observed{Statuses: tc.statuses})[0]

			if r.Passed != tc.wantPassed {
				t.Fatalf("Passed = %v, want %v (%+v)", r.Passed, tc.wantPassed, r)
			}
			if tc.wantActual != 0 && r.Actual != tc.wantActual {
				t.Errorf("Actual = %v, want %d", r.Actual, tc.wantActual)
			}
			if r.Expected != 200 {
				t.Errorf("Expected = %v, want 200", r.Expected)
			}
			if len(r.FailedRequests) != len(tc.wantFailed) {
				t.Fatalf("FailedRequests = %+v, want %+v", r.FailedRequests, tc.wantFailed)
			}
			for i := range tc.wantFailed {
				if r.FailedRequests[i] != tc.wantFailed[i] {
					t.Errorf("FailedRequests[%d] = %+v, want %+v", i, r.FailedRequests[i], tc.wantFailed[i])
				}
			}
			if tc.wantReason != "" && r.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", r.Reason, tc.wantReason)
			}
			if tc.wantPassed && r.Reason != "" {
				t.Errorf("passing result should have no reason, got %q", r.Reason)
			}
		})
	}
}

func TestEvaluate_StatusFailureDoesNotHideEffectCount(t *testing.T) {
	results := Evaluate(
		Rule{Status: intPtr(200), BusinessEffectCount: intPtr(1)},
		Observed{Statuses: []int{500, 200}, BusinessEffectCount: 1},
	)
	if len(results) != 2 || results[0].Passed || !results[1].Passed {
		t.Fatalf("want status FAIL and business_effect_count PASS, got %+v", results)
	}
	if len(results[1].FailedRequests) != 0 {
		t.Fatalf("business_effect_count is not per-request: %+v", results[1])
	}
}
