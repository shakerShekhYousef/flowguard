package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

// shop is a synthetic webhook receiver with one order. With dedupe off it
// reproduces the duplicate-delivery bug (each order.paid charges again).
type shop struct {
	mu        sync.Mutex
	dedupe    bool
	charges   int
	status    string
	processed map[string]bool
	log       []string

	resetStatus int // 0 means 204
	stateStatus int // 0 means 200
	stateBody   string
}

func newShop(dedupe bool) *shop {
	return &shop{dedupe: dedupe, status: "pending", processed: map[string]bool{}}
}

func (s *shop) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /_test/reset", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.record("reset")
		if s.resetStatus != 0 {
			w.WriteHeader(s.resetStatus)
			return
		}
		s.charges, s.status, s.processed = 0, "pending", map[string]bool{}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /webhooks", func(w http.ResponseWriter, r *http.Request) {
		var evt struct{ ID, Type string }
		_ = json.NewDecoder(r.Body).Decode(&evt)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.record("deliver " + evt.ID)
		if r.Header.Get("X-Hang-Up") != "" {
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
			return
		}
		if r.Header.Get("X-Reject") != "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		if s.dedupe && s.processed[evt.ID] {
			w.WriteHeader(http.StatusOK)
			return
		}
		s.processed[evt.ID] = true
		if evt.Type == "order.paid" {
			s.charges++
			s.status = "paid"
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /_test/orders/ord_1", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.record("observe")
		if s.stateStatus != 0 {
			w.WriteHeader(s.stateStatus)
		}
		if s.stateBody != "" {
			_, _ = w.Write([]byte(s.stateBody))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ord_1", "status": s.status, "charges": s.charges})
	})
	return mux
}

func (s *shop) record(entry string) { s.log = append(s.log, entry) }

func (s *shop) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.log...)
}

func serveShop(t *testing.T, s *shop) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(s.handler())
	t.Cleanup(srv.Close)
	return srv
}

// duplicateScenario is the Milestone 1 example: order.paid delivered twice,
// expecting exactly one charge.
func duplicateScenario() config.Scenario {
	return config.Scenario{
		Name:     "duplicate-delivery",
		Kind:     "webhook",
		Setup:    []config.Request{{Method: http.MethodPost, Path: "/_test/reset"}},
		Endpoint: &config.Request{Method: http.MethodPost, Path: "/webhooks"},
		Events: []config.Event{
			{ID: "evt_paid_1", JSON: map[string]any{"id": "evt_paid_1", "type": "order.paid", "data": map[string]any{"order_id": "ord_1", "amount": 100}}},
		},
		Deliveries: []config.Delivery{{Event: "evt_paid_1"}, {Event: "evt_paid_1"}},
		Observe:    &config.Observe{Request: config.Request{Method: http.MethodGet, Path: "/_test/orders/ord_1"}},
		Assert:     config.AssertRule{State: map[string]any{"status": "paid", "charges": 1}},
	}
}

func runWebhook(t *testing.T, srv *httptest.Server, sc config.Scenario) Result {
	t.Helper()
	return RunWebhook(context.Background(), client(), srv.URL, sc)
}

func rules(rs []assertions.Result) map[string]assertions.Result {
	m := map[string]assertions.Result{}
	for _, r := range rs {
		m[r.Rule] = r
	}
	return m
}

func TestRunWebhook_SuccessfulScenario(t *testing.T) {
	s := newShop(true)
	res := runWebhook(t, serveShop(t, s), duplicateScenario())

	if res.Err != nil || !res.Passed {
		t.Fatalf("want a passing scenario, got err=%v passed=%v %+v", res.Err, res.Passed, res.Assertions)
	}
	if res.Name != "duplicate-delivery" {
		t.Fatalf("Name = %q", res.Name)
	}
	var got []string
	for _, a := range res.Assertions {
		got = append(got, a.Rule)
	}
	want := "delivery[0].outcome,delivery[1].outcome,state.charges,state.status"
	if strings.Join(got, ",") != want {
		t.Fatalf("assertions = %v, want %s (deliveries first, then state)", got, want)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("want 2 steps, got %+v", res.Steps)
	}
	for i, st := range res.Steps {
		if st.Index != i || st.EventID != "evt_paid_1" || st.Status != http.StatusOK || st.Outcome != "delivered" || st.Duration <= 0 || st.Err != nil {
			t.Fatalf("step %d = %+v", i, st)
		}
	}
}

func TestRunWebhook_ExecutionOrder(t *testing.T) {
	s := newShop(true)
	runWebhook(t, serveShop(t, s), duplicateScenario())

	if got := strings.Join(s.calls(), " | "); got != "reset | deliver evt_paid_1 | deliver evt_paid_1 | observe" {
		t.Fatalf("calls = %s", got)
	}
}

func TestRunWebhook_DuplicateDeliveryBugFailsState(t *testing.T) {
	s := newShop(false) // no dedupe: charges twice
	res := runWebhook(t, serveShop(t, s), duplicateScenario())

	if res.Err != nil {
		t.Fatalf("a state mismatch is not a runtime error: %v", res.Err)
	}
	if res.Passed {
		t.Fatal("the duplicate-charge bug must fail the scenario")
	}
	r := rules(res.Assertions)
	if r["state.charges"].Passed || r["state.charges"].Reason != "state.charges: expected 1, got 2" {
		t.Fatalf("state.charges = %+v", r["state.charges"])
	}
	if !r["state.status"].Passed || !r["delivery[0].outcome"].Passed || !r["delivery[1].outcome"].Passed {
		t.Fatalf("only state.charges should fail: %+v", res.Assertions)
	}
}

func TestRunWebhook_DeliveryAssertionFailureContinues(t *testing.T) {
	s := newShop(true)
	sc := duplicateScenario()
	sc.Events = append(sc.Events, config.Event{ID: "evt_bad", Headers: map[string]string{"X-Reject": "1"}, JSON: map[string]any{"id": "evt_bad", "type": "order.note"}})
	sc.Deliveries = []config.Delivery{{Event: "evt_bad"}, {Event: "evt_paid_1"}}

	res := runWebhook(t, serveShop(t, s), sc)

	if res.Err != nil {
		t.Fatalf("an unexpected rejection is an assertion failure, not a runtime error: %v", res.Err)
	}
	if res.Passed {
		t.Fatal("scenario must fail")
	}
	r := rules(res.Assertions)
	if r["delivery[0].outcome"].Reason != "delivery[0].outcome: expected delivered, got rejected (status 422)" {
		t.Fatalf("delivery[0] = %+v", r["delivery[0].outcome"])
	}
	if !r["delivery[1].outcome"].Passed || !r["state.charges"].Passed || !r["state.status"].Passed {
		t.Fatalf("later delivery and state must still run and pass: %+v", res.Assertions)
	}
	if got := strings.Join(s.calls(), " | "); got != "reset | deliver evt_bad | deliver evt_paid_1 | observe" {
		t.Fatalf("calls = %s", got)
	}
}

func TestRunWebhook_SetupFailureStops(t *testing.T) {
	s := newShop(true)
	s.resetStatus = http.StatusForbidden
	res := runWebhook(t, serveShop(t, s), duplicateScenario())

	if res.Err == nil || !strings.Contains(res.Err.Error(), "setup[0]") || !strings.Contains(res.Err.Error(), "403") {
		t.Fatalf("want a setup[0] 403 runtime error, got %v", res.Err)
	}
	if len(res.Steps) != 0 || len(res.Assertions) != 0 || res.Passed {
		t.Fatalf("nothing may run after setup fails: %+v", res)
	}
	if got := strings.Join(s.calls(), " | "); got != "reset" {
		t.Fatalf("calls = %s, want only the reset", got)
	}
}

func TestRunWebhook_DeliveryRuntimeErrorSkipsObservation(t *testing.T) {
	s := newShop(true)
	sc := duplicateScenario()
	sc.Events = append(sc.Events,
		config.Event{ID: "evt_bad", Headers: map[string]string{"X-Reject": "1"}, JSON: map[string]any{"id": "evt_bad"}},
		config.Event{ID: "evt_drop", Headers: map[string]string{"X-Hang-Up": "1"}, JSON: map[string]any{"id": "evt_drop"}},
	)
	sc.Deliveries = []config.Delivery{{Event: "evt_bad"}, {Event: "evt_drop"}, {Event: "evt_paid_1"}}

	res := runWebhook(t, serveShop(t, s), sc)

	if res.Err == nil || !strings.Contains(res.Err.Error(), `delivery[1] (event "evt_drop")`) {
		t.Fatalf("want a runtime error for delivery[1], got %v", res.Err)
	}
	if got := strings.Join(s.calls(), " | "); got != "reset | deliver evt_bad | deliver evt_drop" {
		t.Fatalf("calls = %s: no later delivery and no observation allowed", got)
	}
	if len(res.Steps) != 2 || res.Steps[1].Err == nil || res.Steps[1].EventID != "evt_drop" {
		t.Fatalf("steps = %+v", res.Steps)
	}
	r := rules(res.Assertions)
	if len(res.Assertions) != 1 || r["delivery[0].outcome"].Passed {
		t.Fatalf("the earlier delivery assertion must be kept, and no state assertions added: %+v", res.Assertions)
	}
	if res.Passed {
		t.Fatal("a runtime error is never a pass")
	}
}

func TestRunWebhook_ObservationRuntimeErrors(t *testing.T) {
	cases := map[string]func(*shop){
		"non-200":      func(s *shop) { s.stateStatus = http.StatusNotFound; s.stateBody = `{"error":"no such order"}` },
		"invalid JSON": func(s *shop) { s.stateBody = `not json` },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			s := newShop(true)
			setup(s)
			res := runWebhook(t, serveShop(t, s), duplicateScenario())

			if res.Err == nil || !strings.HasPrefix(res.Err.Error(), "observe:") {
				t.Fatalf("want an observe runtime error, got %v", res.Err)
			}
			if len(res.Assertions) != 2 || !assertions.AllPassed(res.Assertions) {
				t.Fatalf("delivery assertions must be kept, no state assertions: %+v", res.Assertions)
			}
			if res.Passed {
				t.Fatal("a runtime error is never a pass")
			}
		})
	}

	t.Run("disallowed observe target", func(t *testing.T) {
		s := newShop(true)
		sc := duplicateScenario()
		sc.Observe.Request.Path = "http://example.com/_test/orders/ord_1"
		res := runWebhook(t, serveShop(t, s), sc)
		if !errors.Is(res.Err, runner.ErrDisallowedTarget) {
			t.Fatalf("want ErrDisallowedTarget, got %v", res.Err)
		}
	})
}

func TestRunWebhook_WithoutObserve(t *testing.T) {
	s := newShop(true)
	sc := duplicateScenario()
	sc.Observe, sc.Assert.State = nil, nil

	res := runWebhook(t, serveShop(t, s), sc)

	if res.Err != nil || !res.Passed || len(res.Assertions) != 2 {
		t.Fatalf("delivery-only scenario should pass with 2 checks: %+v", res)
	}
	for _, c := range s.calls() {
		if c == "observe" {
			t.Fatal("state must not be read without observe")
		}
	}
}
