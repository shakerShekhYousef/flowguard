package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type mock struct {
	t   *testing.T
	mux *http.ServeMux
}

func newMock(t *testing.T, safe bool) *mock {
	return &mock{t: t, mux: newMux(newStore(safe))}
}

// do sends one request and returns the status and decoded JSON body (nil
// for an empty body).
func (m *mock) do(method, path, body string, headers ...string) (int, map[string]any) {
	m.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	m.mux.ServeHTTP(rec, req)

	var doc map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
			m.t.Fatalf("%s %s: response is not JSON: %v\n%s", method, path, err, rec.Body)
		}
	}
	return rec.Code, doc
}

func (m *mock) send(event string) map[string]any {
	m.t.Helper()
	status, body := m.do(http.MethodPost, "/webhooks", event)
	if status != http.StatusOK {
		m.t.Fatalf("POST /webhooks %s = %d %v, want 200", event, status, body)
	}
	return body
}

func evt(id, typ, orderID string, extra ...string) string {
	data := fmt.Sprintf(`"order_id":%q`, orderID)
	if typ == typePaid {
		data += `,"amount":100`
	}
	top := ""
	for _, e := range extra {
		top += "," + e
	}
	return fmt.Sprintf(`{"id":%q,"type":%q%s,"data":{%s}}`, id, typ, top, data)
}

func seq(n int) string { return fmt.Sprintf(`"sequence":%d`, n) }

// state returns the order as JSON-decoded values: numbers are float64.
func (m *mock) state(id string) map[string]any {
	m.t.Helper()
	status, body := m.do(http.MethodGet, "/_test/orders/"+id, "")
	if status != http.StatusOK {
		m.t.Fatalf("GET /_test/orders/%s = %d %v, want 200", id, status, body)
	}
	return body
}

func wantOrder(t *testing.T, got map[string]any, status string, charges int, events ...string) {
	t.Helper()
	processed := []any{}
	for _, e := range events {
		processed = append(processed, e)
	}
	if got["status"] != status || got["charges"] != float64(charges) || !reflect.DeepEqual(got["processed_events"], processed) {
		t.Fatalf("order = %v, want status %s, charges %d, processed_events %v", got, status, charges, events)
	}
}

func bothModes(t *testing.T, f func(t *testing.T, m *mock)) {
	for _, safe := range []bool{false, true} {
		t.Run(fmt.Sprintf("safe=%v", safe), func(t *testing.T) { f(t, newMock(t, safe)) })
	}
}

func TestWebhook_ValidDelivery(t *testing.T) {
	bothModes(t, func(t *testing.T, m *mock) {
		body := m.send(`{"id":"evt_paid_1","type":"order.paid","data":{"order_id":"ord_1","amount":100}}`)
		if !reflect.DeepEqual(body, map[string]any{"received": true, "applied": true}) {
			t.Fatalf("response = %v", body)
		}
		got := m.state("ord_1")
		if !reflect.DeepEqual(got, map[string]any{
			"id": "ord_1", "status": "paid", "charges": float64(1),
			"processed_events": []any{"evt_paid_1"}, "last_sequence": float64(0),
		}) {
			t.Fatalf("state = %v", got)
		}
	})
}

func TestWebhook_DuplicatePaymentBuggyChargesTwice(t *testing.T) {
	m := newMock(t, false)
	m.send(evt("evt_paid_1", typePaid, "ord_1"))
	body := m.send(evt("evt_paid_1", typePaid, "ord_1"))

	if body["applied"] != true {
		t.Fatalf("buggy mode applies duplicates, got %v", body)
	}
	wantOrder(t, m.state("ord_1"), "paid", 2, "evt_paid_1", "evt_paid_1")
}

func TestWebhook_DuplicatePaymentSafeChargesOnce(t *testing.T) {
	m := newMock(t, true)
	m.send(evt("evt_paid_1", typePaid, "ord_1"))
	for i := 0; i < 3; i++ {
		body := m.send(evt("evt_paid_1", typePaid, "ord_1"))
		if body["applied"] != false || body["reason"] != "duplicate" {
			t.Fatalf("duplicate response = %v", body)
		}
	}
	wantOrder(t, m.state("ord_1"), "paid", 1, "evt_paid_1")
}

func TestWebhook_OrderLifecycle(t *testing.T) {
	bothModes(t, func(t *testing.T, m *mock) {
		m.send(evt("evt_c", typeCreated, "ord_1", seq(1)))
		wantOrder(t, m.state("ord_1"), "created", 0, "evt_c")

		m.send(evt("evt_p", typePaid, "ord_1", seq(2)))
		wantOrder(t, m.state("ord_1"), "paid", 1, "evt_c", "evt_p")

		m.send(evt("evt_x", typeCancelled, "ord_1", seq(3)))
		got := m.state("ord_1")
		wantOrder(t, got, "cancelled", 1, "evt_c", "evt_p", "evt_x")
		if got["last_sequence"] != float64(3) {
			t.Fatalf("last_sequence = %v, want 3", got["last_sequence"])
		}
	})
}

func TestWebhook_CancelWithoutPayment(t *testing.T) {
	bothModes(t, func(t *testing.T, m *mock) {
		m.send(evt("evt_c", typeCreated, "ord_1"))
		m.send(evt("evt_x", typeCancelled, "ord_1"))
		wantOrder(t, m.state("ord_1"), "cancelled", 0, "evt_c", "evt_x")
	})
}

func TestWebhook_OrdersAreIndependent(t *testing.T) {
	bothModes(t, func(t *testing.T, m *mock) {
		m.send(evt("evt_p1", typePaid, "ord_1"))
		m.send(evt("evt_x2", typeCancelled, "ord_2"))
		wantOrder(t, m.state("ord_1"), "paid", 1, "evt_p1")
		wantOrder(t, m.state("ord_2"), "cancelled", 0, "evt_x2")
	})
}

func TestWebhook_OutOfOrder(t *testing.T) {
	cases := []struct {
		name   string
		events []string
		buggy  func(t *testing.T, got map[string]any)
		safe   func(t *testing.T, got map[string]any)
	}{
		{
			name:   "stale sequence after newer state",
			events: []string{evt("evt_p", typePaid, "ord_1", seq(2)), evt("evt_c", typeCreated, "ord_1", seq(1))},
			buggy: func(t *testing.T, got map[string]any) {
				wantOrder(t, got, "created", 1, "evt_p", "evt_c")
				if got["last_sequence"] != float64(1) {
					t.Fatalf("buggy last_sequence = %v, want the stale 1", got["last_sequence"])
				}
			},
			safe: func(t *testing.T, got map[string]any) {
				wantOrder(t, got, "paid", 1, "evt_p")
				if got["last_sequence"] != float64(2) {
					t.Fatalf("safe last_sequence = %v, want 2", got["last_sequence"])
				}
			},
		},
		{
			name:   "late created without sequence",
			events: []string{evt("evt_p", typePaid, "ord_1"), evt("evt_c", typeCreated, "ord_1")},
			buggy:  func(t *testing.T, got map[string]any) { wantOrder(t, got, "created", 1, "evt_p", "evt_c") },
			safe:   func(t *testing.T, got map[string]any) { wantOrder(t, got, "paid", 1, "evt_p", "evt_c") },
		},
		{
			name:   "payment after cancellation",
			events: []string{evt("evt_x", typeCancelled, "ord_1"), evt("evt_p", typePaid, "ord_1")},
			buggy:  func(t *testing.T, got map[string]any) { wantOrder(t, got, "paid", 1, "evt_x", "evt_p") },
			safe:   func(t *testing.T, got map[string]any) { wantOrder(t, got, "cancelled", 0, "evt_x") },
		},
		{
			name:   "second payment event for a paid order",
			events: []string{evt("evt_p1", typePaid, "ord_1"), evt("evt_p2", typePaid, "ord_1")},
			buggy:  func(t *testing.T, got map[string]any) { wantOrder(t, got, "paid", 2, "evt_p1", "evt_p2") },
			safe:   func(t *testing.T, got map[string]any) { wantOrder(t, got, "paid", 1, "evt_p1") },
		},
		{
			name:   "equal sequence is stale",
			events: []string{evt("evt_p", typePaid, "ord_1", seq(5)), evt("evt_x", typeCancelled, "ord_1", seq(5))},
			buggy:  func(t *testing.T, got map[string]any) { wantOrder(t, got, "cancelled", 1, "evt_p", "evt_x") },
			safe:   func(t *testing.T, got map[string]any) { wantOrder(t, got, "paid", 1, "evt_p") },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, safe := range []bool{false, true} {
				m := newMock(t, safe)
				for _, e := range tc.events {
					m.send(e)
				}
				if safe {
					tc.safe(t, m.state("ord_1"))
				} else {
					tc.buggy(t, m.state("ord_1"))
				}
			}
		})
	}
}

func TestWebhook_SafeIgnoreReasons(t *testing.T) {
	m := newMock(t, true)
	m.send(evt("evt_p", typePaid, "ord_1", seq(2)))
	for _, tc := range []struct{ event, reason string }{
		{evt("evt_p", typePaid, "ord_1", seq(2)), "duplicate"},
		{evt("evt_old", typeCreated, "ord_1", seq(1)), "stale"},
		{evt("evt_p2", typePaid, "ord_1", seq(3)), "already paid"},
		{evt("evt_x", typeCancelled, "ord_1", seq(4)), ""},
		{evt("evt_p3", typePaid, "ord_1", seq(5)), "order cancelled"},
	} {
		body := m.send(tc.event)
		if tc.reason == "" {
			if body["applied"] != true {
				t.Fatalf("%s: want applied, got %v", tc.event, body)
			}
			continue
		}
		if body["applied"] != false || body["reason"] != tc.reason {
			t.Fatalf("%s: want ignored as %q, got %v", tc.event, tc.reason, body)
		}
	}
	// A stale event is remembered, so its redelivery is a duplicate.
	if body := m.send(evt("evt_old", typeCreated, "ord_1", seq(1))); body["reason"] != "duplicate" {
		t.Fatalf("redelivered stale event = %v, want duplicate", body)
	}
}

func TestWebhook_MissingOrder(t *testing.T) {
	m := newMock(t, true)
	status, body := m.do(http.MethodGet, "/_test/orders/ord_missing", "")
	if status != http.StatusNotFound || body["error"] != "order not found" {
		t.Fatalf("got %d %v, want 404 order not found", status, body)
	}
	// No id at all never reaches the handler; the router answers 404.
	rec := httptest.NewRecorder()
	m.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_test/orders/", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("empty order id = %d, want 404", rec.Code)
	}
}

func TestWebhook_InvalidInput(t *testing.T) {
	big := `{"id":"evt_1","type":"order.created","data":{"order_id":"ord_1"},"pad":"` + strings.Repeat("x", maxEventBytes) + `"}`
	cases := []struct {
		name, body string
		status     int
		want       string
	}{
		{"empty body", ``, 400, "invalid JSON"},
		{"not JSON", `id=evt_1`, 400, "invalid JSON"},
		{"truncated", `{"id":"evt_1",`, 400, "invalid JSON"},
		{"array", `[]`, 400, "invalid JSON"},
		{"trailing data", evt("evt_1", typeCreated, "ord_1") + `{}`, 400, "unexpected data"},
		{"amount wrong type", `{"id":"e","type":"order.paid","data":{"order_id":"o","amount":"100"}}`, 400, "invalid JSON"},
		{"fractional amount", `{"id":"e","type":"order.paid","data":{"order_id":"o","amount":1.5}}`, 400, "invalid JSON"},
		{"too large", big, 413, "larger than"},
		{"missing id", `{"type":"order.created","data":{"order_id":"o"}}`, 422, "id is required"},
		{"id with slash", evt("evt/1", typeCreated, "ord_1"), 422, "id is required"},
		{"missing type", `{"id":"e","data":{"order_id":"o"}}`, 422, "type is required"},
		{"unsupported type", evt("evt_1", "order.refunded", "ord_1"), 422, `unsupported event type "order.refunded"`},
		{"type wrong case", evt("evt_1", "Order.Paid", "ord_1"), 422, "unsupported event type"},
		{"missing data", `{"id":"e","type":"order.created"}`, 422, "data is required"},
		{"missing order id", `{"id":"e","type":"order.created","data":{}}`, 422, "data.order_id is required"},
		{"order id with space", evt("evt_1", typeCreated, "ord 1"), 422, "data.order_id is required"},
		{"paid without amount", `{"id":"e","type":"order.paid","data":{"order_id":"o"}}`, 422, "data.amount is required"},
		{"paid zero amount", `{"id":"e","type":"order.paid","data":{"order_id":"o","amount":0}}`, 422, "data.amount is required"},
		{"zero sequence", evt("evt_1", typeCreated, "ord_1", seq(0)), 422, "sequence must be a positive integer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bothModes(t, func(t *testing.T, m *mock) {
				status, body := m.do(http.MethodPost, "/webhooks", tc.body)
				msg, _ := body["error"].(string)
				if status != tc.status || !strings.Contains(msg, tc.want) {
					t.Fatalf("got %d %v, want %d with %q", status, body, tc.status, tc.want)
				}
				if status, _ := m.do(http.MethodGet, "/_test/orders/ord_1", ""); status != http.StatusNotFound {
					t.Fatal("a rejected event must not create an order")
				}
				if status, _ := m.do(http.MethodGet, "/_test/orders/o", ""); status != http.StatusNotFound {
					t.Fatal("a rejected event must not create an order")
				}
			})
		})
	}

	t.Run("rejected event is not remembered", func(t *testing.T) {
		m := newMock(t, true)
		m.do(http.MethodPost, "/webhooks", `{"id":"evt_1","type":"order.paid","data":{"order_id":"ord_1"}}`)
		if body := m.send(evt("evt_1", typePaid, "ord_1")); body["applied"] != true {
			t.Fatalf("a corrected redelivery must apply, got %v", body)
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		m := newMock(t, true)
		req := httptest.NewRequest(http.MethodGet, "/webhooks", nil)
		rec := httptest.NewRecorder()
		m.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("GET /webhooks = %d, want 405", rec.Code)
		}
	})
}

func TestReset_ClearsAllState(t *testing.T) {
	bothModes(t, func(t *testing.T, m *mock) {
		m.do(http.MethodPost, "/orders", `{}`, "Idempotency-Key", "k1")
		m.send(evt("evt_p", typePaid, "ord_1", seq(7)))

		for i := 0; i < 2; i++ { // a reset is repeatable
			if status, _ := m.do(http.MethodPost, "/_test/reset", ""); status != http.StatusNoContent {
				t.Fatalf("reset = %d, want 204", status)
			}
		}

		if status, _ := m.do(http.MethodGet, "/_test/orders/ord_1", ""); status != http.StatusNotFound {
			t.Fatal("orders survived the reset")
		}
		if _, body := m.do(http.MethodGet, "/_test/business-effect-count?key=k1", ""); body["business_effect_count"] != float64(0) {
			t.Fatalf("idempotency effects survived the reset: %v", body)
		}
		// Processed ids and sequences are gone too: the same event, at a
		// lower sequence, applies again as if new.
		if body := m.send(evt("evt_p", typePaid, "ord_1", seq(1))); body["applied"] != true {
			t.Fatalf("event after reset = %v, want applied", body)
		}
		got := m.state("ord_1")
		wantOrder(t, got, "paid", 1, "evt_p")
		if got["last_sequence"] != float64(1) {
			t.Fatalf("last_sequence = %v, want 1", got["last_sequence"])
		}
	})
}

func TestWebhook_Concurrent(t *testing.T) {
	const orders, dupes = 10, 20
	bothModes(t, func(t *testing.T, m *mock) {
		var wg sync.WaitGroup
		for o := 0; o < orders; o++ {
			for d := 0; d < dupes; d++ {
				wg.Add(2)
				go func() {
					defer wg.Done()
					m.do(http.MethodPost, "/webhooks", evt(fmt.Sprintf("evt_p%d", o), typePaid, fmt.Sprintf("ord_%d", o)))
				}()
				go func() {
					defer wg.Done()
					m.do(http.MethodGet, fmt.Sprintf("/_test/orders/ord_%d", o), "")
				}()
			}
		}
		wg.Wait()

		safe := strings.HasSuffix(t.Name(), "safe=true")
		for o := 0; o < orders; o++ {
			got := m.state(fmt.Sprintf("ord_%d", o))
			want := dupes
			if safe {
				want = 1
			}
			if got["charges"] != float64(want) || len(got["processed_events"].([]any)) != want {
				t.Fatalf("ord_%d = %v, want %d charges and processed events", o, got, want)
			}
		}
	})

	t.Run("reset during traffic", func(t *testing.T) {
		m := newMock(t, true)
		var wg sync.WaitGroup
		for i := 0; i < 50; i++ {
			wg.Add(2)
			go func() {
				defer wg.Done()
				m.do(http.MethodPost, "/webhooks", evt(fmt.Sprintf("evt_%d", i), typePaid, "ord_1"))
			}()
			go func() { defer wg.Done(); m.do(http.MethodPost, "/_test/reset", "") }()
		}
		wg.Wait()
		if status, _ := m.do(http.MethodPost, "/_test/reset", ""); status != http.StatusNoContent {
			t.Fatal("reset failed")
		}
		if status, _ := m.do(http.MethodGet, "/_test/orders/ord_1", ""); status != http.StatusNotFound {
			t.Fatal("final reset left state behind")
		}
	})
}

func TestIdempotencyEndpointsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		safe      bool
		wantCount float64
		replayed  bool
	}{{false, 2, false}, {true, 1, true}} {
		t.Run(fmt.Sprintf("safe=%v", tc.safe), func(t *testing.T) {
			m := newMock(t, tc.safe)
			status, first := m.do(http.MethodPost, "/orders", `{"customer_id":"demo","amount":100}`, "Idempotency-Key", "k1")
			if status != 200 || !reflect.DeepEqual(first, map[string]any{"order_id": "k1", "status": "ok", "replayed": false}) {
				t.Fatalf("first POST /orders = %d %v", status, first)
			}
			_, second := m.do(http.MethodPost, "/orders", `{"customer_id":"demo","amount":100}`, "Idempotency-Key", "k1")
			if second["replayed"] != tc.replayed {
				t.Fatalf("second POST /orders = %v", second)
			}
			_, count := m.do(http.MethodGet, "/_test/business-effect-count?key=k1", "")
			if !reflect.DeepEqual(count, map[string]any{"business_effect_count": tc.wantCount}) {
				t.Fatalf("count = %v, want %v", count, tc.wantCount)
			}
			// Webhook traffic does not touch idempotency effects.
			m.send(evt("evt_p", typePaid, "k1"))
			if _, count := m.do(http.MethodGet, "/_test/business-effect-count?key=k1", ""); count["business_effect_count"] != tc.wantCount {
				t.Fatalf("webhook changed the idempotency count: %v", count)
			}
		})
	}
}
