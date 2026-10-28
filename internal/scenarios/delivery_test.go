package scenarios

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

// received is one request as a webhookReceiver saw it.
type received struct {
	Method string
	Path   string
	Body   []byte
	Header http.Header
}

// webhookReceiver records every request. It answers with the status in the
// X-Respond-Status request header (default 200), and drops the connection
// without answering when X-Hang-Up is set.
func webhookReceiver(t *testing.T) (*httptest.Server, func() []received) {
	t.Helper()
	var mu sync.Mutex
	var seen []received
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, received{Method: r.Method, Path: r.URL.Path, Body: b, Header: r.Header.Clone()})
		mu.Unlock()

		if r.Header.Get("X-Hang-Up") != "" {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		status := http.StatusOK
		if s := r.Header.Get("X-Respond-Status"); s != "" {
			status, _ = strconv.Atoi(s)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []received {
		mu.Lock()
		defer mu.Unlock()
		return append([]received(nil), seen...)
	}
}

// webhookScenario builds a fresh scenario on every call, so tests can
// compare a used scenario against an untouched copy.
func webhookScenario() config.Scenario {
	return config.Scenario{
		Name: "webhooks",
		Kind: "webhook",
		Endpoint: &config.Request{
			Method:  http.MethodPost,
			Path:    "/webhooks",
			Headers: map[string]string{"X-Source": "endpoint", "x-shared": "endpoint"},
		},
		Events: []config.Event{
			{ID: "evt_a", JSON: map[string]any{"id": "evt_a", "type": "order.created", "data": map[string]any{"order_id": "ord_1"}}},
			{ID: "evt_b", JSON: map[string]any{"id": "evt_b", "type": "order.paid", "data": map[string]any{"order_id": "ord_1", "amount": 100}}, Headers: map[string]string{"X-SHARED": "event"}},
			{ID: "evt_c", JSON: map[string]any{"id": "evt_c", "type": "order.cancelled"}},
		},
		Deliveries: []config.Delivery{{Event: "evt_a"}, {Event: "evt_b"}, {Event: "evt_c"}},
	}
}

func deliver(t *testing.T, srv *httptest.Server, sc config.Scenario) ([]DeliveryResult, []assertions.Result, error) {
	t.Helper()
	return runDeliveries(context.Background(), runner.New([]string{"127.0.0.1"}, time.Second), srv.URL, sc)
}

func eventIDs(t *testing.T, reqs []received) []string {
	t.Helper()
	var ids []string
	for _, r := range reqs {
		var body struct{ ID string }
		if err := json.Unmarshal(r.Body, &body); err != nil {
			t.Fatalf("receiver got non-JSON body %q: %v", r.Body, err)
		}
		ids = append(ids, body.ID)
	}
	return ids
}

func TestRunDeliveries_DeclaredOrder(t *testing.T) {
	srv, seen := webhookReceiver(t)
	sc := webhookScenario()
	sc.Deliveries = []config.Delivery{{Event: "evt_c"}, {Event: "evt_a"}, {Event: "evt_b"}}

	results, checks, err := deliver(t, srv, sc)

	if err != nil {
		t.Fatalf("unexpected runtime error: %v", err)
	}
	if got := strings.Join(eventIDs(t, seen()), ","); got != "evt_c,evt_a,evt_b" {
		t.Fatalf("receiver order = %s, want evt_c,evt_a,evt_b", got)
	}
	for i, r := range results {
		if r.Index != i || r.EventID != sc.Deliveries[i].Event {
			t.Fatalf("results[%d] = %+v, want index %d event %s", i, r, i, sc.Deliveries[i].Event)
		}
	}
	if !assertions.AllPassed(checks) || len(checks) != 3 {
		t.Fatalf("want 3 passing outcome checks, got %+v", checks)
	}
	for _, req := range seen() {
		if req.Method != http.MethodPost || req.Path != "/webhooks" {
			t.Fatalf("sent %s %s, want POST /webhooks", req.Method, req.Path)
		}
	}
}

func TestRunDeliveries_DuplicateDeliverySendsIdenticalBytes(t *testing.T) {
	srv, seen := webhookReceiver(t)
	sc := webhookScenario()
	sc.Deliveries = []config.Delivery{{Event: "evt_b"}, {Event: "evt_a"}, {Event: "evt_b"}, {Event: "evt_b"}}

	results, _, err := deliver(t, srv, sc)

	if err != nil || len(results) != 4 {
		t.Fatalf("want 4 deliveries, got %d (err %v)", len(results), err)
	}
	reqs := seen()
	want, _ := json.Marshal(sc.Events[1].JSON)
	for _, i := range []int{0, 2, 3} {
		if !bytes.Equal(reqs[i].Body, want) {
			t.Fatalf("delivery %d body = %q, want %q", i, reqs[i].Body, want)
		}
		if !reflect.DeepEqual(reqs[i].Header, reqs[0].Header) {
			t.Fatalf("repeated delivery %d has different headers:\n%v\n%v", i, reqs[i].Header, reqs[0].Header)
		}
	}
	if results[0].EventID != "evt_b" || results[2].EventID != "evt_b" || results[3].EventID != "evt_b" {
		t.Fatalf("results = %+v", results)
	}
}

func TestRunDeliveries_HeaderPrecedence(t *testing.T) {
	srv, seen := webhookReceiver(t)
	sc := webhookScenario()
	sc.Deliveries = []config.Delivery{{Event: "evt_a"}, {Event: "evt_b"}}

	if _, _, err := deliver(t, srv, sc); err != nil {
		t.Fatal(err)
	}
	reqs := seen()

	// evt_a has no headers: endpoint defaults apply.
	if v := reqs[0].Header.Values("X-Shared"); len(v) != 1 || v[0] != "endpoint" {
		t.Fatalf("evt_a X-Shared = %v, want [endpoint]", v)
	}
	// evt_b's X-SHARED overrides endpoint's x-shared, sent once.
	if v := reqs[1].Header.Values("X-Shared"); len(v) != 1 || v[0] != "event" {
		t.Fatalf("evt_b X-Shared = %v, want exactly [event]", v)
	}
	for i, r := range reqs {
		if r.Header.Get("X-Source") != "endpoint" {
			t.Fatalf("request %d lost endpoint default X-Source", i)
		}
		for name, values := range r.Header {
			if len(values) != 1 {
				t.Fatalf("request %d sent header %s %d times: %v", i, name, len(values), values)
			}
		}
	}
}

func TestRunDeliveries_ContentType(t *testing.T) {
	cases := []struct {
		name       string
		endpoint   map[string]string
		event      map[string]string
		wantHeader string
	}{
		{"default", nil, nil, "application/json"},
		{"endpoint sets it", map[string]string{"content-type": "application/cloudevents+json"}, nil, "application/cloudevents+json"},
		{"event overrides endpoint", map[string]string{"Content-Type": "application/cloudevents+json"}, map[string]string{"CONTENT-TYPE": "application/vnd.test+json"}, "application/vnd.test+json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, seen := webhookReceiver(t)
			sc := webhookScenario()
			sc.Endpoint.Headers = tc.endpoint
			sc.Events[0].Headers = tc.event
			sc.Deliveries = []config.Delivery{{Event: "evt_a"}}

			if _, _, err := deliver(t, srv, sc); err != nil {
				t.Fatal(err)
			}
			if v := seen()[0].Header.Values("Content-Type"); len(v) != 1 || v[0] != tc.wantHeader {
				t.Fatalf("Content-Type = %v, want exactly [%s]", v, tc.wantHeader)
			}
		})
	}
}

func TestRunDeliveries_OutcomesAndExpectations(t *testing.T) {
	status := func(v int) *int { return &v }
	cases := []struct {
		name        string
		respond     string // receiver status for the single delivery
		expect      *config.Expect
		wantOutcome string
		wantPassed  bool
		wantRules   []string
		wantReason  string
	}{
		{"200 delivered, default expectation", "200", nil, "delivered", true, []string{"delivery[0].outcome"}, ""},
		{"204 delivered, default expectation", "204", nil, "delivered", true, []string{"delivery[0].outcome"}, ""},
		{"500 rejected fails default expectation", "500", nil, "rejected", false, []string{"delivery[0].outcome"}, "delivery[0].outcome: expected delivered, got rejected (status 500)"},
		{"409 rejected fails default expectation", "409", nil, "rejected", false, []string{"delivery[0].outcome"}, "delivery[0].outcome: expected delivered, got rejected (status 409)"},
		{"304 is rejected (only 2xx is delivered)", "304", nil, "rejected", false, []string{"delivery[0].outcome"}, "delivery[0].outcome: expected delivered, got rejected (status 304)"},
		{"302 without Location is rejected", "302", &config.Expect{Outcome: "rejected", Status: status(302)}, "rejected", true, []string{"delivery[0].outcome", "delivery[0].status"}, ""},
		{"299 is delivered", "299", nil, "delivered", true, []string{"delivery[0].outcome"}, ""},
		{"expected rejection passes", "503", &config.Expect{Outcome: "rejected"}, "rejected", true, []string{"delivery[0].outcome"}, ""},
		{"exact status passes", "202", &config.Expect{Status: status(202)}, "delivered", true, []string{"delivery[0].outcome", "delivery[0].status"}, ""},
		{"exact status mismatch fails", "200", &config.Expect{Status: status(202)}, "delivered", false, []string{"delivery[0].outcome", "delivery[0].status"}, "delivery[0].status: expected 202, got 200"},
		{"rejected with exact status passes", "422", &config.Expect{Outcome: "rejected", Status: status(422)}, "rejected", true, []string{"delivery[0].outcome", "delivery[0].status"}, ""},
		{"delivered but rejection expected fails", "200", &config.Expect{Outcome: "rejected"}, "delivered", false, []string{"delivery[0].outcome"}, "delivery[0].outcome: expected rejected, got delivered (status 200)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := webhookReceiver(t)
			sc := webhookScenario()
			sc.Events[0].Headers = map[string]string{"X-Respond-Status": tc.respond}
			sc.Deliveries = []config.Delivery{{Event: "evt_a", Expect: tc.expect}}

			results, checks, err := deliver(t, srv, sc)

			if err != nil {
				t.Fatalf("a non-2xx response is an outcome, not a runtime error: %v", err)
			}
			if results[0].Outcome != tc.wantOutcome || strconv.Itoa(results[0].Status) != tc.respond {
				t.Fatalf("result = %+v, want outcome %s status %s", results[0], tc.wantOutcome, tc.respond)
			}
			var rules []string
			var reasons []string
			for _, c := range checks {
				rules = append(rules, c.Rule)
				if c.Reason != "" {
					reasons = append(reasons, c.Reason)
				}
			}
			if strings.Join(rules, ",") != strings.Join(tc.wantRules, ",") {
				t.Fatalf("rules = %v, want %v", rules, tc.wantRules)
			}
			if assertions.AllPassed(checks) != tc.wantPassed {
				t.Fatalf("AllPassed = %v, want %v (%+v)", !tc.wantPassed, tc.wantPassed, checks)
			}
			if tc.wantReason != "" && (len(reasons) != 1 || reasons[0] != tc.wantReason) {
				t.Fatalf("reasons = %q, want [%q]", reasons, tc.wantReason)
			}
		})
	}
}

func TestRunDeliveries_AssertionFailureContinues(t *testing.T) {
	srv, seen := webhookReceiver(t)
	sc := webhookScenario()
	sc.Events[0].Headers = map[string]string{"X-Respond-Status": "500"}

	results, checks, err := deliver(t, srv, sc)

	if err != nil {
		t.Fatalf("an assertion failure is not a runtime error: %v", err)
	}
	if len(seen()) != 3 || len(results) != 3 {
		t.Fatalf("all 3 deliveries must still run, sent %d", len(seen()))
	}
	if checks[0].Passed || !checks[1].Passed || !checks[2].Passed {
		t.Fatalf("want only delivery[0] to fail, got %+v", checks)
	}
}

func TestRunDeliveries_RuntimeErrorStops(t *testing.T) {
	t.Run("connection dropped", func(t *testing.T) {
		srv, seen := webhookReceiver(t)
		sc := webhookScenario()
		sc.Events[1].Headers = map[string]string{"X-Hang-Up": "1"}

		results, checks, err := deliver(t, srv, sc)

		if err == nil || !strings.Contains(err.Error(), `delivery[1] (event "evt_b")`) {
			t.Fatalf("want a runtime error naming delivery[1], got %v", err)
		}
		if got := eventIDs(t, seen()); strings.Join(got, ",") != "evt_a,evt_b" {
			t.Fatalf("evt_c must not be sent after a runtime error; receiver saw %v", got)
		}
		if len(results) != 2 || results[1].Err == nil || results[1].Outcome != "" {
			t.Fatalf("results = %+v, want 2 with the second failed and no outcome", results)
		}
		if len(checks) != 1 {
			t.Fatalf("only completed deliveries are checked, got %+v", checks)
		}
	})

	t.Run("disallowed target", func(t *testing.T) {
		srv, seen := webhookReceiver(t)
		sc := webhookScenario()
		sc.Endpoint.Path = "http://example.com/webhooks"

		results, _, err := deliver(t, srv, sc)

		if !errors.Is(err, runner.ErrDisallowedTarget) {
			t.Fatalf("want ErrDisallowedTarget, got %v", err)
		}
		if len(results) != 1 || len(seen()) != 0 {
			t.Fatalf("nothing may be sent: results=%d received=%d", len(results), len(seen()))
		}
	})

	t.Run("unencodable event", func(t *testing.T) {
		srv, seen := webhookReceiver(t)
		sc := webhookScenario()
		sc.Events[0].JSON = map[string]any{"amount": math.NaN()}

		_, _, err := deliver(t, srv, sc)

		if err == nil || !strings.Contains(err.Error(), "encoding json") {
			t.Fatalf("want an encoding error, got %v", err)
		}
		if len(seen()) != 0 {
			t.Fatal("nothing may be sent after an encoding error")
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		srv, seen := webhookReceiver(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, _, err := runDeliveries(ctx, runner.New([]string{"127.0.0.1"}, time.Second), srv.URL, webhookScenario())

		if !errors.Is(err, context.Canceled) || len(seen()) != 0 {
			t.Fatalf("want context.Canceled with nothing sent, got %v (%d sent)", err, len(seen()))
		}
	})
}

func TestRunDeliveries_DoesNotMutateScenario(t *testing.T) {
	srv, _ := webhookReceiver(t)
	sc := webhookScenario()

	if _, _, err := deliver(t, srv, sc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sc, webhookScenario()) {
		t.Fatalf("scenario was modified:\n got %+v\nwant %+v", sc, webhookScenario())
	}
}

func TestRunDeliveries_RecordsDuration(t *testing.T) {
	srv, _ := webhookReceiver(t)
	results, _, err := deliver(t, srv, webhookScenario())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Duration <= 0 {
			t.Fatalf("delivery %d has no duration: %+v", r.Index, r)
		}
	}
}

func TestMergeHeaders(t *testing.T) {
	defaults := map[string]string{"x-a": "1", "X-B": "default", "content-type": "application/json"}
	overrides := map[string]string{"X-b": "override", "x-c": "3"}
	defaultsCopy := map[string]string{"x-a": "1", "X-B": "default", "content-type": "application/json"}
	overridesCopy := map[string]string{"X-b": "override", "x-c": "3"}

	got := mergeHeaders(defaults, overrides)

	want := map[string]string{"X-A": "1", "X-B": "override", "Content-Type": "application/json", "X-C": "3"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeHeaders = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(defaults, defaultsCopy) || !reflect.DeepEqual(overrides, overridesCopy) {
		t.Fatal("inputs were modified")
	}
	if len(mergeHeaders(nil, nil)) != 0 {
		t.Fatal("nil inputs should give an empty map")
	}
}
