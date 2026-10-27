package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

// fakeOrders mimics examples/mock-server: when safe is false every request
// applies a new effect, reproducing the double-charge bug.
func fakeOrders(safe bool) http.Handler {
	h, _ := fakeOrdersWithLog(safe, http.StatusNoContent)
	return h
}

// fakeOrdersWithLog also records "METHOD /path" for every request, and
// answers POST /_test/reset with resetStatus.
func fakeOrdersWithLog(safe bool, resetStatus int) (http.Handler, func() []string) {
	var mu sync.Mutex
	effects := map[string]int{}
	var log []string

	mux := http.NewServeMux()
	mux.HandleFunc("POST /_test/reset", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if resetStatus < 300 {
			effects = map[string]int{}
		}
		mu.Unlock()
		w.WriteHeader(resetStatus)
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		mu.Lock()
		if !safe || effects[key] == 0 {
			effects[key]++
		}
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /_test/business-effect-count", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := effects[r.URL.Query().Get("key")]
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]int{"business_effect_count": n})
	})
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		log = append(log, r.Method+" "+r.URL.Path)
		mu.Unlock()
		mux.ServeHTTP(w, r)
	})
	return logged, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), log...)
	}
}

func scenario() config.Scenario {
	one, ok := 1, 200
	return config.Scenario{
		Name: "charge-once",
		Kind: "idempotency",
		Request: config.Request{
			Method: http.MethodPost,
			Path:   "/orders",
			// Lower-case on purpose: lookup must be case-insensitive, and the
			// key needs URL escaping in the effect-count query.
			Headers: map[string]string{"idempotency-key": "order 1&x=2"},
			JSON:    map[string]any{"amount": 100},
		},
		Repeat: 3,
		Assert: config.AssertRule{Status: &ok, BusinessEffectCount: &one},
	}
}

func TestRunIdempotency(t *testing.T) {
	cases := []struct {
		name       string
		safe       bool
		wantPassed bool
	}{
		{"buggy target double-charges", false, false},
		{"idempotent target passes", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(fakeOrders(tc.safe))
			defer srv.Close()

			client := runner.New([]string{"127.0.0.1"}, time.Second)
			res := RunIdempotency(context.Background(), client, srv.URL, scenario())

			if res.Err != nil {
				t.Fatalf("unexpected runtime error: %v", res.Err)
			}
			if len(res.Steps) != 3 {
				t.Fatalf("expected 3 steps, got %d", len(res.Steps))
			}
			if res.Passed != tc.wantPassed {
				t.Fatalf("Passed = %v, want %v (assertions: %+v)", res.Passed, tc.wantPassed, res.Assertions)
			}
		})
	}
}

func TestRunIdempotency_RuntimeErrorIsNotAssertionFailure(t *testing.T) {
	client := runner.New([]string{"127.0.0.1"}, 200*time.Millisecond)
	// Nothing listens on port 1.
	res := RunIdempotency(context.Background(), client, "http://127.0.0.1:1", scenario())

	if res.Err == nil {
		t.Fatal("expected a runtime error")
	}
	if len(res.Assertions) != 0 {
		t.Fatalf("assertions should not run after a runtime error, got %+v", res.Assertions)
	}
}

func withReset(sc config.Scenario) config.Scenario {
	sc.Setup = []config.Request{{Method: http.MethodPost, Path: "/_test/reset"}}
	return sc
}

func effectReason(res Result) string {
	for _, a := range res.Assertions {
		if a.Rule == "business_effect_count" {
			return fmt.Sprintf("%v/%v", a.Expected, a.Actual)
		}
	}
	return ""
}

func TestRunIdempotency_SetupRunsFirst(t *testing.T) {
	h, log := fakeOrdersWithLog(false, http.StatusNoContent)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := runner.New([]string{"127.0.0.1"}, time.Second)
	res := RunIdempotency(context.Background(), client, srv.URL, withReset(scenario()))
	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}

	got := log()
	want := []string{"POST /_test/reset", "POST /orders", "POST /orders", "POST /orders", "GET /_test/business-effect-count"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("request order = %v, want %v", got, want)
	}
	if len(res.Steps) != 3 {
		t.Fatalf("setup must not count as a replay step: got %d steps", len(res.Steps))
	}
}

func TestRunIdempotency_ConsecutiveRunsAreDeterministicWithReset(t *testing.T) {
	srv := httptest.NewServer(fakeOrders(false)) // buggy: shared state across runs
	defer srv.Close()
	client := runner.New([]string{"127.0.0.1"}, time.Second)

	var outcomes []string
	for i := 0; i < 3; i++ {
		res := RunIdempotency(context.Background(), client, srv.URL, withReset(scenario()))
		if res.Err != nil {
			t.Fatalf("run %d: %v", i, res.Err)
		}
		outcomes = append(outcomes, effectReason(res))
	}

	// expected/actual: repeat=3 against the buggy target -> 3 effects, every time.
	for i, o := range outcomes {
		if o != "1/3" {
			t.Fatalf("run %d observed %s, want 1/3 on every run (all: %v)", i, o, outcomes)
		}
	}
}

func TestRunIdempotency_WithoutResetStateLeaksAcrossRuns(t *testing.T) {
	// Documents the problem setup solves: same key, shared target, no reset.
	srv := httptest.NewServer(fakeOrders(false))
	defer srv.Close()
	client := runner.New([]string{"127.0.0.1"}, time.Second)

	first := RunIdempotency(context.Background(), client, srv.URL, scenario())
	second := RunIdempotency(context.Background(), client, srv.URL, scenario())

	if effectReason(first) != "1/3" || effectReason(second) != "1/6" {
		t.Fatalf("expected drift 1/3 then 1/6, got %s then %s", effectReason(first), effectReason(second))
	}
}

func TestRunIdempotency_SetupFailureAbortsBeforeReplay(t *testing.T) {
	h, log := fakeOrdersWithLog(true, http.StatusForbidden)
	srv := httptest.NewServer(h)
	defer srv.Close()

	client := runner.New([]string{"127.0.0.1"}, time.Second)
	sc := withReset(scenario())
	sc.Setup[0].Path = "/_test/reset?token=SYNTH_SECRET"
	res := RunIdempotency(context.Background(), client, srv.URL, sc)

	if res.Err == nil || !strings.Contains(res.Err.Error(), "setup[0]") || !strings.Contains(res.Err.Error(), "403") {
		t.Fatalf("expected a setup[0] 403 error, got %v", res.Err)
	}
	if strings.Contains(res.Err.Error(), "SYNTH_SECRET") {
		t.Fatalf("setup error leaks query value: %v", res.Err)
	}
	if len(res.Steps) != 0 || len(res.Assertions) != 0 {
		t.Fatalf("scenario must not replay or assert after setup fails: %+v", res)
	}
	if got := log(); len(got) != 1 {
		t.Fatalf("only the setup request should have been sent, got %v", got)
	}
}

func TestRunIdempotency_SetupRespectsAllowList(t *testing.T) {
	srv := httptest.NewServer(fakeOrders(true))
	defer srv.Close()

	client := runner.New([]string{"127.0.0.1"}, time.Second)
	sc := scenario()
	sc.Setup = []config.Request{{Method: http.MethodPost, Path: "http://example.com/_test/reset"}}
	res := RunIdempotency(context.Background(), client, srv.URL, sc)

	if !errors.Is(res.Err, runner.ErrDisallowedTarget) {
		t.Fatalf("expected ErrDisallowedTarget from setup, got %v", res.Err)
	}
}

// sequenceOrders answers POST /orders with the given statuses in order
// (repeating the last), and always reports exactly one business effect.
func sequenceOrders(statuses ...int) http.Handler {
	var mu sync.Mutex
	n := 0
	mux := http.NewServeMux()
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		s := statuses[min(n, len(statuses)-1)]
		n++
		mu.Unlock()
		w.WriteHeader(s)
	})
	mux.HandleFunc("GET /_test/business-effect-count", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]int{"business_effect_count": 1})
	})
	return mux
}

func TestRunIdempotency_StatusCheckedForEveryRequest(t *testing.T) {
	cases := []struct {
		name        string
		statuses    []int
		wantPassed  bool
		wantIndexes []int
	}{
		{"500 then 200 fails", []int{500, 200, 200}, false, []int{0}},
		{"200 then 500 fails", []int{200, 200, 500}, false, []int{2}},
		{"flapping fails at each bad index", []int{503, 200, 503}, false, []int{0, 2}},
		{"all 200 passes", []int{200, 200, 200}, true, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(sequenceOrders(tc.statuses...))
			defer srv.Close()

			client := runner.New([]string{"127.0.0.1"}, time.Second)
			res := RunIdempotency(context.Background(), client, srv.URL, scenario()) // repeat 3, expects 200 and 1 effect

			if res.Err != nil {
				t.Fatalf("unexpected runtime error: %v", res.Err)
			}
			if res.Passed != tc.wantPassed {
				t.Fatalf("Passed = %v, want %v (%+v)", res.Passed, tc.wantPassed, res.Assertions)
			}

			var status, effect *assertions.Result
			for i := range res.Assertions {
				switch res.Assertions[i].Rule {
				case "status":
					status = &res.Assertions[i]
				case "business_effect_count":
					effect = &res.Assertions[i]
				}
			}
			if status == nil || effect == nil {
				t.Fatalf("both assertions must be evaluated: %+v", res.Assertions)
			}
			if !effect.Passed {
				t.Fatalf("business_effect_count should still pass independently: %+v", effect)
			}

			var got []int
			for _, f := range status.FailedRequests {
				got = append(got, f.Index)
			}
			if fmt.Sprint(got) != fmt.Sprint(tc.wantIndexes) {
				t.Fatalf("failed request indexes = %v, want %v", got, tc.wantIndexes)
			}
			for _, f := range status.FailedRequests {
				if res.Steps[f.Index].Status != f.Actual {
					t.Fatalf("failure index %d does not match step status %d", f.Index, res.Steps[f.Index].Status)
				}
			}
		})
	}
}
