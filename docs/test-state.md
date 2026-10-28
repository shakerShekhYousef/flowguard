# Test-state endpoints

An HTTP `200` says nothing about whether a retried request charged a customer
twice. To check the *business effect*, FlowGuard asks the target what
actually happened, through a test-only endpoint:

- `idempotency` scenarios read a count from a fixed convention,
  [`GET /_test/business-effect-count`](#idempotency-business-effect-count).
- `webhook` scenarios (v0.2.0) read any JSON state from a path you
  declare in `observe.request`, such as the mock's
  [`GET /_test/orders/{id}`](#webhook-order-state).

## Why HTTP 200 is not enough

A receiver that charges twice for a duplicated webhook still answers `200`
to both deliveries; so does an API that applies a retried payment twice.
Status codes describe the HTTP exchange, not the business outcome. Only the
resulting state (how many charges, which status the order ended in) shows
whether the business rule held. FlowGuard therefore asserts on state as
well as on status codes.

## Idempotency: business-effect count

After replaying an `idempotency` scenario, FlowGuard sends:

```
GET {base_url}/_test/business-effect-count?key=<Idempotency-Key>
```

The target must answer `200` with:

```json
{ "business_effect_count": 1 }
```

`key` is the value of the scenario's `Idempotency-Key` request header (matched
case-insensitively, URL-encoded in the query). The header is required for
`idempotency` scenarios; config validation rejects a scenario without it.

A correct, idempotent target reports `1` no matter how many times the request
was replayed. Any other value fails the `business_effect_count` assertion.

The same allow-list applies to this request as to the scenario requests.

### Reference implementation

`examples/mock-server` implements the endpoint, plus `POST /_test/reset` to
clear its state between runs:

```bash
go run ./examples/mock-server          # buggy: retries double-charge
go run ./examples/mock-server -safe    # fixed: retries are idempotent
```

Effects are counted per key and persist for the server's lifetime. The
shipped `flowguard.example.yaml` therefore declares a `setup` step that calls
`/_test/reset` before each run (see below).

## Webhook order state

A webhook scenario reads state with the request you declare:

```yaml
    observe:
      request:
        method: GET
        path: /_test/orders/ord_1
    assert:
      state:
        status: paid
        charges: 1
```

FlowGuard requires no particular path or response shape, only:

- method `GET` (reading state must not change it);
- status `200`, with exactly one JSON value as the body (at most 1 MiB);
- the values you assert, at the dot-paths you assert them.

A non-200 status, invalid JSON or a transport error is a runtime error
(exit code 2). A wrong value or missing path is an assertion failure (exit
code 1). See [docs/webhooks.md](webhooks.md#state-assertions) for the
comparison rules.

### The mock's order endpoint

`examples/mock-server` exposes `GET /_test/orders/{id}`:

```json
{
  "id": "ord_1",
  "status": "paid",
  "charges": 1,
  "processed_events": ["evt_paid_1"],
  "last_sequence": 0
}
```

| Field | Meaning |
| ----- | ------- |
| `id` | Order id from `data.order_id`. |
| `status` | `created`, `paid` or `cancelled`. |
| `charges` | Number of times a payment was applied. |
| `processed_events` | Ids of the events applied to this order, in order. |
| `last_sequence` | Highest applied `sequence` (`0` if none had one). In buggy mode, the last one applied. |

An order that no event has named yet returns `404` with
`{"error": "order not found"}`; FlowGuard reports that as a runtime error.

The mock receives events on `POST /webhooks`:

```json
{ "id": "evt_paid_1", "type": "order.paid", "sequence": 2,
  "data": { "order_id": "ord_1", "amount": 100 } }
```

- `type` is `order.created`, `order.paid` or `order.cancelled`.
- `order.paid` needs a positive integer `data.amount`.
- `sequence` is optional; if present it must be a positive integer.
- Ids are 1-128 characters from `A-Z a-z 0-9 _ . : -`.
- Valid events get `200` with `{"received": true, "applied": true}`, or
  `"applied": false` and a `reason` when safe mode ignores them. Malformed
  JSON gets `400`, a body over 1 MiB `413`, and a missing or invalid field
  or unsupported type `422`. Rejected events change nothing.
- The first valid event naming an order creates it, whatever its type.

### The mock's business rules

The mock's rules are **illustrative**: they show the kind of invariant
FlowGuard can check, not universal requirements for payment systems. Your
receiver's rules may differ; assert on whatever your business requires.

| Situation | Buggy (default) | `-safe` |
| --------- | --------------- | ------- |
| Same event id delivered again | Applied again (charges twice) | Ignored: `duplicate` |
| `sequence` at or below the last applied one | Applied; state moves backwards | Ignored: `stale` |
| Any event after cancellation | Applied (a payment charges and reopens the order) | Ignored: `order cancelled` |
| A second payment event (new id) for a paid order | Charges again | Ignored: `already paid` |
| Late `order.created` for an existing order | Status goes back to `created` | Status unchanged |

Safe mode checks these rules in the order listed. Ignored events still get
`200`, as a real receiver would answer, so the provider does not retry.

## Reset and setup

`POST /_test/reset` on the mock returns `204` and clears everything:
idempotency counts, orders, processed event ids and sequence numbers. It is
safe to call repeatedly. Both shipped examples call it in `setup`, so every
run starts from empty state and gives the same result.

## Fixtures and isolation

An idempotency scenario replays a request with a fixed `Idempotency-Key`. If
the target still remembers that key from an earlier run, the observed count
reflects *both* runs: against the buggy mock, a second run reports `got 4`
instead of `got 2`, and against a fixed target a stale key can hide a bug by
returning a cached response. Each run must start from a known state.

FlowGuard **never resets anything on its own.** It only sends the requests
your config declares. You choose one of these strategies:

### 1. Explicit `setup` requests (recommended for disposable targets)

```yaml
scenarios:
  - name: charge-once
    kind: idempotency
    setup:
      - method: POST
        path: /_test/reset
    request: ...
```

- `setup` is optional; omit it and behavior is unchanged.
- Setup requests run in order, once per scenario, before the replay.
- They go through the same `allowed_hosts` and redirect policy as every
  other request, and errors are redacted the same way.
- Any transport error or non-2xx status aborts the scenario with a runtime
  error (exit code 2) before any replay or assertion, so a failed reset can
  never produce a misleading pass or fail.
- Setup requests are not counted as replay steps in the report.

Only point `setup` at hooks **you own** in an environment that exists for
testing (a local mock, a per-branch preview, a dedicated test tenant). Do
not wire it to a reset endpoint on shared staging, and never on production:
FlowGuard will call whatever you declare.

### 2. Fresh fixture per run

Start a new target process or container for each run (this is what
FlowGuard's own end-to-end tests do with `examples/mock-server`). No reset
hook is needed, because no state survives between runs.

### 3. Unique keys per run

If the target cannot be reset, generate a unique `Idempotency-Key` per run
(e.g. render the YAML in CI with the build ID). Old keys are left behind
but never observed. Clean them up with your environment's own retention
policy; FlowGuard will not delete them.

## Requirements for staging environments

To use FlowGuard against your own service in a test or staging
environment, the service needs:

- **A read-only state endpoint** for each thing you assert on, answering
  `200` with JSON. It should be available only in that environment, for
  example behind a test-only route, a feature flag or an internal network.
- **A way to start from known state:** a reset hook you own, a fresh
  environment per run, or unique ids per run (see the strategies above).
- **Synchronous processing for the asserted effect**, or a test mode that
  finishes processing before answering. FlowGuard reads state once, right
  after the last delivery, and does not poll yet.
- **Webhook signature checks disabled or set to a test secret** that you
  control. FlowGuard does not sign deliveries yet.
- **An `allowed_hosts` entry** for the environment's hostname. Do not list
  production or third-party hosts.

## Limitations

This is a stand-in. Real services rarely expose such an endpoint, and adding
one only for tests is not always acceptable. How FlowGuard should observe
business effects on real targets (a hook like this, a database query, or
inspecting downstream calls) is an open product decision, still to be made.
