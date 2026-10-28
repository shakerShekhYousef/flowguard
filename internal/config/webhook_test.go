package config

import (
	"fmt"
	"strings"
	"testing"
)

const webhookHeader = `
version: 1
target:
  base_url: http://127.0.0.1:8080
  allowed_hosts: [127.0.0.1]
scenarios:
`

// validWebhookScenario is the Milestone 1 duplicate-delivery example.
const validWebhookScenario = `  - name: duplicate-delivery
    kind: webhook
    setup:
      - {method: POST, path: /_test/reset}
    endpoint:
      method: POST
      path: /webhooks
      headers: {X-Source: flowguard}
    events:
      - id: evt_paid_1
        headers: {X-Event-Type: order.paid}
        json: {id: evt_paid_1, type: order.paid, data: {order_id: ord_1, amount: 100}}
    deliveries:
      - event: evt_paid_1
      - event: evt_paid_1
        expect: {outcome: delivered, status: 200}
    observe:
      request: {method: GET, path: /_test/orders/ord_1}
    assert:
      state:
        status: paid
        charges: 1
`

// webhookWith returns a config whose single webhook scenario has each
// "from" text replaced by its "to" text. It fails the test if a "from" is
// missing, so a typo in a test cannot pass by accident.
func webhookWith(t *testing.T, pairs ...string) string {
	t.Helper()
	sc := validWebhookScenario
	for i := 0; i+1 < len(pairs); i += 2 {
		if !strings.Contains(sc, pairs[i]) {
			t.Fatalf("test bug: %q not found in validWebhookScenario", pairs[i])
		}
		sc = strings.Replace(sc, pairs[i], pairs[i+1], 1)
	}
	return webhookHeader + sc
}

func TestLoad_WebhookValid(t *testing.T) {
	cfg, err := Load(writeTemp(t, webhookHeader+validWebhookScenario))
	if err != nil {
		t.Fatalf("valid webhook config should load: %v", err)
	}
	sc := cfg.Scenarios[0]

	if sc.Endpoint == nil || sc.Endpoint.Method != "POST" || sc.Endpoint.Path != "/webhooks" || sc.Endpoint.Header("x-source") != "flowguard" {
		t.Fatalf("endpoint = %+v", sc.Endpoint)
	}
	if len(sc.Events) != 1 || sc.Events[0].ID != "evt_paid_1" || sc.Events[0].JSON["type"] != "order.paid" || sc.Events[0].Headers["X-Event-Type"] != "order.paid" {
		t.Fatalf("events = %+v", sc.Events)
	}
	if len(sc.Deliveries) != 2 || sc.Deliveries[0].Event != "evt_paid_1" {
		t.Fatalf("deliveries = %+v", sc.Deliveries)
	}
	if sc.Deliveries[0].Expect != nil {
		t.Fatalf("omitted expect should decode as nil, got %+v", sc.Deliveries[0].Expect)
	}
	if got := sc.Deliveries[1].Expect; got.Outcome != "delivered" || got.Status == nil || *got.Status != 200 {
		t.Fatalf("expect = %+v", got)
	}
	if sc.Observe == nil || sc.Observe.Request.Path != "/_test/orders/ord_1" {
		t.Fatalf("observe = %+v", sc.Observe)
	}
	if sc.Assert.State["status"] != "paid" || sc.Assert.State["charges"] != 1 {
		t.Fatalf("assert.state = %+v", sc.Assert.State)
	}
	if len(sc.Setup) != 1 {
		t.Fatalf("setup = %+v", sc.Setup)
	}
}

func TestDelivery_ExpectedOutcomeDefaultsToDelivered(t *testing.T) {
	cases := []struct {
		d    Delivery
		want string
	}{
		{Delivery{}, OutcomeDelivered},
		{Delivery{Expect: &Expect{}}, OutcomeDelivered},
		{Delivery{Expect: &Expect{Status: intPtr(204)}}, OutcomeDelivered},
		{Delivery{Expect: &Expect{Outcome: OutcomeRejected}}, OutcomeRejected},
	}
	for _, tc := range cases {
		if got := tc.d.ExpectedOutcome(); got != tc.want {
			t.Errorf("ExpectedOutcome(%+v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestLoad_WebhookValidVariants(t *testing.T) {
	cases := map[string][]string{
		"no observe or state (deliveries only)": {
			"    observe:\n      request: {method: GET, path: /_test/orders/ord_1}\n", "",
			"    assert:\n      state:\n        status: paid\n        charges: 1\n", "",
		},
		"rejected with a 5xx status": {
			"expect: {outcome: delivered, status: 200}", "expect: {outcome: rejected, status: 503}",
		},
		"status only (defaults to delivered)": {
			"expect: {outcome: delivered, status: 200}", "expect: {status: 204}",
		},
		"scalar state values": {
			"        charges: 1\n", "        charges: 1\n        refunded: false\n        note: null\n        total: 99.5\n        created: \"2026-10-27\"\n        items.0.sku: abc\n",
		},
		"setup omitted": {
			"    setup:\n      - {method: POST, path: /_test/reset}\n", "",
		},
	}
	for name, pairs := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, webhookWith(t, pairs...))); err != nil {
				t.Fatalf("should load: %v", err)
			}
		})
	}
}

func TestLoad_MixedKindsInOneFile(t *testing.T) {
	idem := validConfig[strings.Index(validConfig, "  - name: charge-once"):]
	if _, err := Load(writeTemp(t, webhookHeader+validWebhookScenario+idem)); err != nil {
		t.Fatalf("idempotency and webhook scenarios should coexist: %v", err)
	}
}

func TestLoad_WebhookRejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"event field", "        json: {id: evt_paid_1", "        jsn: {id: evt_paid_1", `unknown field "jsn" in event`},
		{"delivery field", "      - event: evt_paid_1\n      - event", "      - evnt: evt_paid_1\n      - event", `unknown field "evnt" in delivery`},
		{"expect field", "expect: {outcome: delivered, status: 200}", "expect: {outcom: delivered}", `unknown field "outcom" in expect`},
		{"observe field", "      request: {method: GET", "      reqest: {method: GET", `unknown field "reqest" in observe`},
		{"endpoint field", "      path: /webhooks", "      pth: /webhooks", `unknown field "pth" in request entry`},
		{"assert field", "      state:\n", "      stat:\n", `unknown field "stat" in assert`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, webhookWith(t, tc.from, tc.to)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// Milestone 2 fields must stay unknown until they are implemented, so a
// config cannot declare retries or delays that FlowGuard would skip.
func TestLoad_WebhookRejectsMilestone2Fields(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"delay", "      - event: evt_paid_1\n      - event", "      - event: evt_paid_1\n        delay: 300ms\n      - event", `unknown field "delay" in delivery`},
		{"timeout", "      - event: evt_paid_1\n      - event", "      - event: evt_paid_1\n        timeout: 100ms\n      - event", `unknown field "timeout" in delivery`},
		{"retry", "      - event: evt_paid_1\n      - event", "      - event: evt_paid_1\n        retry: {max_attempts: 3}\n      - event", `unknown field "retry" in delivery`},
		{"expect.attempts", "expect: {outcome: delivered, status: 200}", "expect: {attempts: 3}", `unknown field "attempts" in expect`},
		{"observe.wait", "      request: {method: GET, path: /_test/orders/ord_1}\n", "      request: {method: GET, path: /_test/orders/ord_1}\n      wait: {timeout: 2s}\n", `unknown field "wait" in observe`},
		{"signing", "    events:\n", "    signing: {algorithm: hmac-sha256}\n    events:\n", `unknown field "signing" in scenario`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, webhookWith(t, tc.from, tc.to)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoad_WebhookValidation(t *testing.T) {
	endpoint := "    endpoint:\n      method: POST\n      path: /webhooks\n      headers: {X-Source: flowguard}\n"
	events := "    events:\n      - id: evt_paid_1\n        headers: {X-Event-Type: order.paid}\n        json: {id: evt_paid_1, type: order.paid, data: {order_id: ord_1, amount: 100}}\n"
	deliveries := "    deliveries:\n      - event: evt_paid_1\n      - event: evt_paid_1\n        expect: {outcome: delivered, status: 200}\n"
	observe := "    observe:\n      request: {method: GET, path: /_test/orders/ord_1}\n"
	state := "    assert:\n      state:\n        status: paid\n        charges: 1\n"

	cases := []struct {
		name  string
		pairs []string
		want  string
	}{
		// endpoint
		{"endpoint missing", []string{endpoint, ""}, "endpoint is required"},
		{"endpoint method missing", []string{"      method: POST\n      path: /webhooks", "      path: /webhooks"}, "endpoint.method is required"},
		{"endpoint path missing", []string{"      path: /webhooks\n", ""}, "endpoint.path is required"},
		{"endpoint json", []string{"      path: /webhooks\n", "      path: /webhooks\n      json: {a: 1}\n"}, "endpoint.json is not allowed"},
		// events
		{"no events", []string{events, ""}, "events: at least one event is required"},
		{"empty event list", []string{events, "    events: []\n"}, "events: at least one event is required"},
		{"event id missing", []string{"      - id: evt_paid_1\n        headers", "      - headers"}, "events[0].id is required"},
		{"event json missing", []string{"        json: {id: evt_paid_1, type: order.paid, data: {order_id: ord_1, amount: 100}}\n", ""}, "events[0].json is required"},
		{"duplicate event id", []string{events, events + "      - {id: evt_paid_1, json: {x: 1}}\n"}, `events[1]: duplicate event id "evt_paid_1"`},
		// deliveries
		{"no deliveries", []string{deliveries, ""}, "deliveries: at least one delivery is required"},
		{"delivery event missing", []string{"      - event: evt_paid_1\n      - event", "      - expect: {status: 200}\n      - event"}, "deliveries[0].event is required"},
		{"unknown event reference", []string{"      - event: evt_paid_1\n      - event", "      - event: evt_typo\n      - event"}, `deliveries[0].event "evt_typo" does not match any declared event id`},
		// expect
		{"outcome timeout not yet supported", []string{"outcome: delivered, status: 200", "outcome: timeout"}, `deliveries[1].expect: outcome "timeout" is not supported (supported: delivered, rejected)`},
		{"unknown outcome", []string{"outcome: delivered, status: 200", "outcome: ok"}, `outcome "ok" is not supported`},
		{"status out of range", []string{"status: 200", "status: 99"}, "status 99 is not a valid HTTP status"},
		{"status above range", []string{"status: 200", "status: 600"}, "status 600 is not a valid HTTP status"},
		{"delivered with 5xx", []string{"status: 200", "status: 500"}, `status 500 contradicts outcome "delivered"`},
		{"default outcome with 4xx", []string{"outcome: delivered, status: 200", "status: 409"}, `status 409 contradicts outcome "delivered"`},
		{"rejected with 2xx", []string{"outcome: delivered", "outcome: rejected"}, `status 200 contradicts outcome "rejected"`},
		// observe / assert.state
		{"observe without state", []string{state, ""}, "observe and assert.state must be given together"},
		{"state without observe", []string{observe, ""}, "observe and assert.state must be given together"},
		{"observe method missing", []string{"{method: GET, path: /_test/orders/ord_1}", "{path: /_test/orders/ord_1}"}, "observe.request.method is required"},
		{"observe path missing", []string{"{method: GET, path: /_test/orders/ord_1}", "{method: GET}"}, "observe.request.path is required"},
		{"empty state", []string{"      state:\n        status: paid\n        charges: 1\n", "      state: {}\n"}, "assert.state: at least one path is required"},
		{"empty path segment", []string{"        status: paid", "        order..status: paid"}, `path "order..status" has an empty segment`},
		{"trailing dot", []string{"        status: paid", "        status.: paid"}, `path "status." has an empty segment`},
		{"map value", []string{"        status: paid", "        status: {code: paid}"}, "assert.state.status: expected a scalar"},
		{"list value", []string{"        charges: 1", "        charges: [1, 2]"}, "assert.state.charges: expected a scalar"},
		{"unquoted timestamp", []string{"        status: paid", "        created: 2026-10-27"}, "assert.state.created: YAML read this value as a timestamp; quote it"},
		// cross-kind fields
		{"request in webhook", []string{"    kind: webhook\n", "    kind: webhook\n    request: {method: POST, path: /orders}\n"}, "request is only valid for idempotency scenarios"},
		{"repeat in webhook", []string{"    kind: webhook\n", "    kind: webhook\n    repeat: 2\n"}, "repeat is only valid for idempotency scenarios"},
		{"assert.status in webhook", []string{"      state:\n", "      status: 200\n      state:\n"}, "assert.status is only valid for idempotency scenarios"},
		{"business_effect_count in webhook", []string{"      state:\n", "      business_effect_count: 1\n      state:\n"}, "assert.business_effect_count is only valid for idempotency scenarios"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, webhookWith(t, tc.pairs...)))
			if err == nil {
				t.Fatalf("expected an error containing %q, got none", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "scenarios[0]") {
				t.Fatalf("error should locate the scenario: %v", err)
			}
		})
	}
}

func TestLoad_WebhookLimits(t *testing.T) {
	var events, deliveries strings.Builder
	events.WriteString("    events:\n")
	deliveries.WriteString("    deliveries:\n")
	for i := 0; i <= maxEvents; i++ {
		fmt.Fprintf(&events, "      - {id: e%d, json: {n: %d}}\n", i, i)
	}
	for i := 0; i <= maxDeliveries; i++ {
		deliveries.WriteString("      - event: evt_paid_1\n")
	}

	t.Run("too many events", func(t *testing.T) {
		// Prepends maxEvents+1 events to the existing evt_paid_1.
		_, err := Load(writeTemp(t, webhookWith(t, "    events:\n", events.String())))
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d events", maxEvents)) {
			t.Fatalf("want events limit error, got %v", err)
		}
	})

	t.Run("too many deliveries", func(t *testing.T) {
		cfg := webhookWith(t, "    deliveries:\n      - event: evt_paid_1\n      - event: evt_paid_1\n        expect: {outcome: delivered, status: 200}\n", deliveries.String())
		_, err := Load(writeTemp(t, cfg))
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("at most %d deliveries", maxDeliveries)) {
			t.Fatalf("want deliveries limit error, got %v", err)
		}
	})
}

func TestLoad_IdempotencyRejectsWebhookFields(t *testing.T) {
	cases := []struct {
		name, add, want string
	}{
		{"endpoint", "    endpoint: {method: POST, path: /webhooks}\n", "endpoint is only valid for webhook scenarios"},
		{"events", "    events: [{id: e1, json: {a: 1}}]\n", "events is only valid for webhook scenarios"},
		{"deliveries", "    deliveries: [{event: e1}]\n", "deliveries is only valid for webhook scenarios"},
		{"observe", "    observe: {request: {method: GET, path: /x}}\n", "observe is only valid for webhook scenarios"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := strings.Replace(validConfig, "    repeat: 2\n", "    repeat: 2\n"+tc.add, 1)
			_, err := Load(writeTemp(t, cfg))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}

	t.Run("assert.state", func(t *testing.T) {
		cfg := strings.Replace(validConfig, "      business_effect_count: 1\n", "      business_effect_count: 1\n      state: {status: paid}\n", 1)
		_, err := Load(writeTemp(t, cfg))
		if err == nil || !strings.Contains(err.Error(), "assert.state is only valid for webhook scenarios") {
			t.Fatalf("want assert.state error, got %v", err)
		}
	})
}

func TestLoad_UnsupportedKindListsKinds(t *testing.T) {
	cfg := strings.Replace(validConfig, "kind: idempotency", "kind: webhook-fault", 1)
	_, err := Load(writeTemp(t, cfg))
	if err == nil || !strings.Contains(err.Error(), `kind "webhook-fault" is not supported (supported: idempotency, webhook)`) {
		t.Fatalf("want kind error listing both kinds, got %v", err)
	}
}

func intPtr(v int) *int { return &v }

func TestLoad_WebhookHeaderRules(t *testing.T) {
	cases := []struct {
		name  string
		pairs []string
		want  string
	}{
		{"endpoint case duplicate", []string{"headers: {X-Source: flowguard}", "headers: {X-Source: a, x-source: b}"}, `endpoint.headers: "X-Source" and "x-source" are the same header`},
		{"event case duplicate", []string{"headers: {X-Event-Type: order.paid}", "headers: {X-Event-Type: a, X-EVENT-TYPE: b}"}, `events[0].headers: "X-EVENT-TYPE" and "X-Event-Type" are the same header`},
		{"endpoint Host", []string{"headers: {X-Source: flowguard}", "headers: {host: evil.example}"}, `endpoint.headers: "host" is managed by the HTTP transport`},
		{"event Content-Length", []string{"headers: {X-Event-Type: order.paid}", "headers: {Content-Length: \"5\"}"}, `events[0].headers: "Content-Length" is managed by the HTTP transport`},
		{"event Transfer-Encoding", []string{"headers: {X-Event-Type: order.paid}", "headers: {transfer-encoding: chunked}"}, `"transfer-encoding" is managed by the HTTP transport`},
		{"endpoint Connection", []string{"headers: {X-Source: flowguard}", "headers: {Connection: close}"}, `"Connection" is managed by the HTTP transport`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, webhookWith(t, tc.pairs...)))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}

	t.Run("same header across endpoint and event is allowed (event overrides)", func(t *testing.T) {
		cfg := webhookWith(t, "headers: {X-Event-Type: order.paid}", "headers: {x-source: event, Content-Type: application/cloudevents+json}")
		if _, err := Load(writeTemp(t, cfg)); err != nil {
			t.Fatalf("cross-map overrides must be allowed: %v", err)
		}
	})

	t.Run("idempotency headers are unaffected", func(t *testing.T) {
		cfg := strings.Replace(validConfig, "        Idempotency-Key: test-order-001", "        Idempotency-Key: test-order-001\n        Host: example.com", 1)
		if _, err := Load(writeTemp(t, cfg)); err != nil {
			t.Fatalf("v0.1.1 idempotency configs must keep loading: %v", err)
		}
	})
}

func TestLoad_WebhookStateRejectsNonFiniteNumbers(t *testing.T) {
	for _, v := range []string{".nan", ".inf", "-.inf"} {
		t.Run(v, func(t *testing.T) {
			_, err := Load(writeTemp(t, webhookWith(t, "        charges: 1", "        charges: "+v)))
			if err == nil || !strings.Contains(err.Error(), "cannot match any JSON value") {
				t.Fatalf("want non-finite rejection, got %v", err)
			}
		})
	}
}

func TestLoad_ObserveMethodMustBeGET(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH", "HEAD", "get"} {
		t.Run(method, func(t *testing.T) {
			_, err := Load(writeTemp(t, webhookWith(t, "{method: GET, path: /_test/orders/ord_1}", "{method: "+method+", path: /_test/orders/ord_1}")))
			want := `observe.request.method "` + method + `" is not supported: state is read with GET only`
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("want %q, got %v", want, err)
			}
		})
	}
}
