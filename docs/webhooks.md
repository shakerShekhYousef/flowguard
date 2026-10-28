# Webhook testing

> **Status:** v0.2 Milestone 1, unreleased. Available when built from the
> `main` branch; not yet in a tagged release. The schema may still change
> before v0.2.0.

Webhook providers (payment processors, shipping carriers, identity
providers) deliver events at least once. Networks fail, providers retry,
and the same event can arrive twice or after a newer one. A receiver that
answers `200 OK` to every delivery can still charge a customer twice or
move an order back to an earlier state.

A `kind: webhook` scenario checks this directly:

1. FlowGuard plays the **provider**: it sends the events you declare to your
   receiver's endpoint, in the order you list them, including duplicates.
2. It records each delivery's HTTP outcome and checks it against what you
   expect.
3. It reads your receiver's **business state** from a test-state endpoint
   and compares it with the values you assert.

FlowGuard simulates a webhook provider's deliveries. It does not test a
real provider's internal retry scheduler, and it never contacts a real
provider.

## Contents

- [What Milestone 1 supports](#what-milestone-1-supports)
- [Quick example](#quick-example)
- [Configuration reference](#configuration-reference)
- [Header precedence](#header-precedence)
- [Delivery outcomes and expectations](#delivery-outcomes-and-expectations)
- [Observing business state](#observing-business-state)
- [State assertions](#state-assertions)
- [Errors and exit codes](#errors-and-exit-codes)
- [Terminal output](#terminal-output)
- [JSON report](#json-report)
- [More examples](#more-examples)
- [Security](#security)
- [Limitations and planned features](#limitations-and-planned-features)

## What Milestone 1 supports

| Implemented | Notes |
| ----------- | ----- |
| Sequential deliveries | Deliveries run one at a time, strictly in list order. |
| Duplicate events | List the same event more than once in `deliveries`. |
| Expected HTTP outcome | `delivered` (2xx) or `rejected` (any other status), per delivery. |
| Exact HTTP status | Optional `expect.status`, per delivery. |
| Final-state assertions | One `GET` after the last delivery; exact comparison of JSON values at dot-paths. |
| Manual ordering | Declare events in any order you like, for example an older event after a newer one. FlowGuard sends them as listed; there are no reordering presets. |

Not implemented yet: delivery delays, retry policies, timeout expectations,
out-of-order presets, HMAC signing and state polling. See
[Limitations and planned features](#limitations-and-planned-features).
Their config fields are rejected as unknown fields today, so a config can
never appear to use them.

## Quick example

The repository ships `flowguard.webhooks.example.yaml`, which runs against
the bundled mock receiver (`examples/mock-server`):

```yaml
version: 1

target:
  base_url: http://127.0.0.1:8080
  allowed_hosts: [127.0.0.1, localhost]

scenarios:
  - name: duplicate-payment
    kind: webhook
    setup:
      - method: POST
        path: /_test/reset
    endpoint:
      method: POST
      path: /webhooks
    events:
      - id: evt_paid_1
        json:
          id: evt_paid_1
          type: order.paid
          data: {order_id: ord_1, amount: 100}
    deliveries:
      - event: evt_paid_1
        expect: {outcome: delivered, status: 200}
      - event: evt_paid_1   # duplicate delivery of the same event id
        expect: {outcome: delivered, status: 200}
    observe:
      request:
        method: GET
        path: /_test/orders/ord_1
    assert:
      state:
        status: paid
        charges: 1
```

Run it from a checkout (POSIX shell; `&` and `kill %1` are job control):

```bash
go build -o flowguard ./cmd/flowguard
go run ./examples/mock-server &        # buggy by default
./flowguard test --config flowguard.webhooks.example.yaml
kill %1
```

Against the buggy mock, both deliveries succeed but the order is charged
twice, so the run fails with exit code 1:

```
Running: duplicate-payment
  delivery[0] evt_paid_1: 200 delivered
  delivery[1] evt_paid_1: 200 delivered
  [PASS] delivery[0].outcome
  [PASS] delivery[0].status
  [PASS] delivery[1].outcome
  [PASS] delivery[1].status
  [FAIL] state.charges: expected 1, got 2
  [PASS] state.status
```

Start the mock with `go run ./examples/mock-server -safe &` instead and the
same command passes with exit code 0 (`[PASS] state.charges`).

Every delivery check passed in both runs. Only the business state shows the
bug, which is why a webhook test should assert on state, not just on status
codes.

## Configuration reference

A webhook scenario uses these fields. Unknown fields are rejected at every
level, as in all FlowGuard configs.

| Field | Required | Description |
| ----- | -------- | ----------- |
| `name` | yes | Scenario name, shown in output and reports. |
| `kind` | yes | `webhook`. |
| `setup` | no | Requests sent first, in order, to reset test state. A transport error or non-2xx response stops the scenario (exit 2). Same format and rules as for idempotency scenarios; see [docs/test-state.md](test-state.md#fixtures-and-isolation). |
| `endpoint.method` | yes | HTTP method for every delivery, usually `POST`. |
| `endpoint.path` | yes | Receiver path, e.g. `/webhooks`. Joined to `target.base_url`. |
| `endpoint.headers` | no | Default headers for every delivery. |
| `events` | yes | 1 to 100 events. |
| `events[].id` | yes | Unique name used by `deliveries[].event` and shown in output and reports. FlowGuard does **not** insert it into the body; put it in `json` if your receiver reads it. |
| `events[].headers` | no | Headers for this event; override `endpoint.headers`. |
| `events[].json` | yes | The event body, a JSON object, sent exactly as written. |
| `deliveries` | yes | 1 to 100 deliveries, sent in this order. |
| `deliveries[].event` | yes | The `id` of a declared event. Repeat it to deliver a duplicate. |
| `deliveries[].expect.outcome` | no | `delivered` (default) or `rejected`. |
| `deliveries[].expect.status` | no | Exact HTTP status, 100-599. Must agree with the outcome. |
| `observe.request` | with `assert.state` | `method` (must be `GET`), `path` and optional `headers` for the state request. |
| `assert.state` | with `observe` | Map of dot-path to expected scalar value. |

`observe` and `assert.state` must be given together. Both are optional: a
scenario without them checks only delivery outcomes.

Idempotency-only fields are rejected in webhook scenarios, with a hint:
`request` (use `endpoint`, `events` and `deliveries`), `repeat` (list a
delivery again), `assert.status` (use `deliveries[].expect.status`) and
`assert.business_effect_count` (use `observe` and `assert.state`).
`endpoint.json` is rejected too, because each delivery sends its event's
`json`.

### Events and deliveries

Events are declared once and delivered by reference, so a duplicate is
simply a repeated `deliveries` entry:

```yaml
    events:
      - id: evt_created_1
        json: {id: evt_created_1, type: order.created, data: {order_id: ord_1}}
      - id: evt_paid_1
        json: {id: evt_paid_1, type: order.paid, data: {order_id: ord_1, amount: 100}}
    deliveries:
      - event: evt_created_1
      - event: evt_paid_1
      - event: evt_paid_1      # duplicate
```

Each delivery encodes its event's `json` with Go's `encoding/json`, which
sorts object keys, so every delivery of an event sends identical bytes.

## Header precedence

- `endpoint.headers` apply to every delivery.
- `events[].headers` override them for that event. Names are compared
  case-insensitively, and each header is sent once.
- `Content-Type: application/json` is added unless either map sets
  `Content-Type`.
- One map may not name the same header twice in different case
  (`X-Id` and `x-id`): validation rejects it.
- Transport-managed headers cannot be set: `Connection`, `Content-Length`,
  `Host`, `Keep-Alive`, `Proxy-Connection`, `Te`, `Trailer`,
  `Transfer-Encoding`, `Upgrade`.

```yaml
    endpoint:
      method: POST
      path: /webhooks
      headers:
        X-Provider: synthetic
        X-Event-Version: "1"
    events:
      - id: evt_paid_1
        headers:
          X-Event-Version: "2"    # this event is sent with X-Event-Version: 2
        json: {id: evt_paid_1, type: order.paid, data: {order_id: ord_1, amount: 100}}
```

## Delivery outcomes and expectations

FlowGuard classifies each response:

| Response | Outcome |
| -------- | ------- |
| 2xx | `delivered` |
| Any other status (3xx after redirects, 4xx, 5xx) | `rejected` |
| No response (connection refused, reset, timeout, blocked host) | runtime error, not an outcome |

Every delivery gets a `delivery[i].outcome` assertion. Without `expect`, the
expected outcome is `delivered`, so an unexpected rejection never passes
silently. Add `expect.status` to also get a `delivery[i].status` assertion:

```yaml
    deliveries:
      - event: evt_paid_1                              # outcome must be delivered
      - event: evt_paid_1
        expect: {status: 200}                          # delivered, and exactly 200
      - event: evt_malformed
        expect: {outcome: rejected, status: 422}       # receiver must refuse it
```

`expect.status` must agree with the outcome: `outcome: delivered` with
`status: 500` is a config error, and so is `outcome: rejected` with
`status: 200`.

A failed expectation is an assertion failure: it is reported, and the
remaining deliveries and the state check still run.

## Observing business state

After the last delivery, FlowGuard sends one request:

```yaml
    observe:
      request:
        method: GET
        path: /_test/orders/ord_1
        headers: {X-Test-Tenant: tenant_1}   # optional
```

- Only `GET` is allowed, because reading state must not change it.
- The request is sent once, immediately after the last delivery. There is
  no waiting or polling, so the receiver must have finished processing by
  the time it answered the delivery. Receivers that process events
  asynchronously are not supported yet.
- The response must be status `200` with exactly one JSON value. Anything
  else is a runtime error (exit 2). The response body is never printed or
  written to the report.
- The request goes through the same `allowed_hosts`, redirect policy,
  10-second timeout and 1 MiB response limit as every other request.

The receiver must expose this endpoint; FlowGuard cannot see a database.
[docs/test-state.md](test-state.md#webhook-order-state) describes the mock's
endpoint and what a staging environment needs.

## State assertions

`assert.state` maps dot-paths into the observed JSON to expected values:

```yaml
    assert:
      state:
        status: paid               # top-level key
        charges: 1
        customer.id: cus_1         # nested object
        items.0.sku: sku_1         # array index
        refund_id: null            # must be present and null
```

- **Paths** walk object keys and non-negative array indexes. A key that
  itself contains a dot cannot be addressed.
- **Values** must be scalars: string, number, boolean or `null`.
- **Equality** is by JSON type and value. Numbers compare exactly, so `1`,
  `1.0` and `1e0` are equal and large integers keep full precision. The
  string `"1"` never equals the number `1`.
- **Missing paths fail.** A missing key is never treated as null, zero,
  false or an empty string.
- **YAML typing:** an unquoted `2026-10-27` is read by YAML as a timestamp
  and rejected with a hint to quote it. Quote values you mean as strings
  (`"007"`, `"true"`, `"2026-10-27"`).

Each path is its own assertion, `state.<path>`, so one failure does not hide
another:

```
  [FAIL] state.charges: expected 1, got 2
  [FAIL] state.status: expected "paid", got "created"
  [FAIL] state.refund_id: expected "r_1", but path not found: no key "refund_id" in object at the top level
```

Only asserted paths are reported. If any segment of a path looks sensitive
(it contains `token`, `secret`, `password`, `api_key`, `signature`,
`session` or a similar word), both values are shown as `REDACTED`. Other
strings are redacted for credential-like content and truncated to 200
characters; objects and arrays are shown as `<object>` and `<array>`.

## Errors and exit codes

| Situation | Result | Exit code |
| --------- | ------ | --------- |
| All delivery and state assertions pass | pass | 0 |
| A delivery outcome or status differs from `expect` | assertion failure; later deliveries and the state check still run | 1 |
| A state value differs, or a path is missing | assertion failure | 1 |
| Invalid config, including unknown fields | nothing runs | 2 |
| A `setup` request fails | the scenario stops before any delivery | 2 |
| A delivery gets no response (refused, reset, timeout, host not allowed) | the scenario stops: no further deliveries, no state check; earlier results are kept | 2 |
| The state request fails (transport error, non-200, invalid JSON) | delivery results are kept; no state assertions | 2 |

When several scenarios run, the exit code is the most severe result: 2,
then 1, then 0. Every scenario still runs.

## Terminal output

Each scenario prints one line per delivery, then every assertion
(deliveries first, then state), then any runtime error:

```
Running: dropped-connection
  delivery[0] evt_note: 422 rejected
  delivery[1] evt_drop: not delivered (runtime error)
  [FAIL] delivery[0].outcome: expected delivered, got rejected (status 422)
  [ERROR] delivery[1] (event "evt_drop"): Post "http://127.0.0.1:8080/webhooks": EOF
```

Passing assertions print just their rule name (`[PASS] state.charges`).

## JSON report

`--report <file>` writes the same report format as idempotency scenarios,
with owner-only permissions (`0600`). Webhook steps add two fields:

| Field | Description |
| ----- | ----------- |
| `steps[].index` | Delivery index, matching `delivery[i]` in assertion rules. |
| `steps[].event_id` | The delivered event's `id`. |
| `steps[].status` | HTTP status, or `0` if no response was received. |
| `steps[].outcome` | `delivered` or `rejected`. Absent when the delivery failed at runtime. |
| `steps[].duration_ms` | Time for the delivery. |
| `steps[].error` | Redacted runtime error, only on the failed delivery. |

`event_id` and `outcome` are omitted for idempotency steps, so idempotency
reports are byte-for-byte the same as in v0.1.1 apart from timestamps and
durations.

```json
{
  "name": "late-created-event",
  "passed": false,
  "steps": [
    { "index": 0, "event_id": "evt_paid_2", "status": 200, "outcome": "delivered", "duration_ms": 0 },
    { "index": 1, "event_id": "evt_created_1", "status": 200, "outcome": "delivered", "duration_ms": 0 }
  ],
  "assertions": [
    { "rule": "delivery[0].outcome", "expected": "delivered", "actual": "delivered", "passed": true },
    { "rule": "delivery[1].outcome", "expected": "delivered", "actual": "delivered", "passed": true },
    { "rule": "state.last_sequence", "expected": 2, "actual": 1, "passed": false,
      "reason": "state.last_sequence: expected 2, got 1" },
    { "rule": "state.status", "expected": "paid", "actual": "created", "passed": false,
      "reason": "state.status: expected \"paid\", got \"created\"" }
  ]
}
```

A delivery that failed at runtime appears as the last step, with `index`,
`event_id`, `status: 0` and `error`, and the scenario's `error` field holds
the same message. Request bodies, response bodies and headers are never
written to the report.

## More examples

All examples below run against the bundled mock receiver (see
[docs/test-state.md](test-state.md#webhook-order-state) for its rules).
Put them under `scenarios:` with the same `target` as the quick example.

### Expect a rejection

The mock rejects event types it does not support with `422`. This scenario
passes only if the receiver refuses the event:

```yaml
  - name: rejects-unsupported-events
    kind: webhook
    setup:
      - {method: POST, path: /_test/reset}
    endpoint: {method: POST, path: /webhooks}
    events:
      - id: evt_refund_1
        json: {id: evt_refund_1, type: order.refunded, data: {order_id: ord_1}}
    deliveries:
      - event: evt_refund_1
        expect: {outcome: rejected, status: 422}
```

```
Running: rejects-unsupported-events
  delivery[0] evt_refund_1: 422 rejected
  [PASS] delivery[0].outcome
  [PASS] delivery[0].status
```

### Deliver an older event last (manual ordering)

Listing an older event after a newer one simulates a late delivery. This is
plain list ordering, not a built-in fault preset:

```yaml
  - name: late-created-event
    kind: webhook
    setup:
      - {method: POST, path: /_test/reset}
    endpoint: {method: POST, path: /webhooks}
    events:
      - id: evt_paid_2
        json: {id: evt_paid_2, type: order.paid, sequence: 2, data: {order_id: ord_2, amount: 100}}
      - id: evt_created_1
        json: {id: evt_created_1, type: order.created, sequence: 1, data: {order_id: ord_2}}
    deliveries:
      - event: evt_paid_2
      - event: evt_created_1     # arrives after the newer event
    observe:
      request: {method: GET, path: /_test/orders/ord_2}
    assert:
      state:
        status: paid
        last_sequence: 2
```

The buggy mock lets the late event roll the order back (exit 1):

```
  [FAIL] state.last_sequence: expected 2, got 1
  [FAIL] state.status: expected "paid", got "created"
```

With `-safe`, the mock ignores the stale event and both state assertions
pass (exit 0).

## Security

Webhook scenarios follow the same rules as every FlowGuard scenario; see
[Safety](../README.md#safety) in the README.

- Every request (setup, each delivery, the state request and every
  redirect hop) must go to a host in `allowed_hosts`, over `http` or
  `https`. Anything else stops the scenario before the request is sent.
  This is hostname allow-listing, not network-level SSRF protection.
- Use synthetic events only. Do not paste real customer data, real event
  payloads or real secrets into a config.
- Terminal output and reports never include request or response bodies or
  headers. Errors and assertion reasons are redacted for URL credentials,
  query values, credential-like fields and bearer tokens. Redaction is
  pattern-based, so treat reports as internal artifacts anyway.
- Point FlowGuard only at receivers you own in a local or disposable test
  environment. Do not send synthetic events to a production receiver: they
  can trigger real side effects such as fulfilment or emails.

## Limitations and planned features

Milestone 1 limitations:

- One attempt per delivery. A slow receiver is a runtime error after 10
  seconds, not a `timeout` outcome.
- State is read once, immediately after the last delivery; asynchronous
  processing is not supported.
- State paths cannot address keys that contain a dot.
- Deliveries are sequential; there is no concurrent delivery.
- Webhook signatures are not generated, so a receiver that verifies
  signatures must have verification disabled in the test environment.
- A state response over 1 MiB is reported as invalid JSON.

Planned for v0.2 Milestone 2 (not implemented; these fields are rejected
today):

| Planned feature | Planned config |
| --------------- | -------------- |
| Delivery delays | `deliveries[].delay` |
| Retry policies on timeout or error responses | `deliveries[].retry` |
| Per-delivery timeouts and a `timeout` outcome | `deliveries[].timeout`, `expect.outcome: timeout` |
| Attempt-count expectations | `expect.attempts` |
| Waiting for asynchronous state | `observe.wait` |
| Out-of-order and delayed scenario examples | mock `sequence` rules plus delays |
| HMAC-SHA256 request signing | `signing` |

Planned config names may change before they ship.
