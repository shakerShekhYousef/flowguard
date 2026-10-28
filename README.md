# FlowGuard

**FlowGuard helps backend developers catch duplicate charges and broken webhook
workflows before release, using reproducible CLI tests and business-rule
assertions.**

An API can return `HTTP 200` while its business state is wrong: a retried
payment can double-charge a customer, a duplicated webhook can apply an
effect twice, a delayed event can leave state inconsistent. FlowGuard tests
for *business-logic correctness*, not just response codes.

## Status

Early stage. Expect breaking changes until v1.

| Feature | Status |
| ------- | ------ |
| `idempotency` scenarios: replay a request, assert status and business-effect count | Released in v0.1.0 |
| JSON reports, CI exit codes, host allow-list, redaction | Released in v0.1.0 |
| `webhook` scenarios: sequential, duplicate and manually reordered deliveries, outcome and status expectations, exact final-state assertions | v0.2.0 (Milestone 1), on `main` |
| Automatic retries, delivery delays, timeout simulation, HMAC signing, state polling | Planned for v0.3.0 (Milestone 2); not implemented |

> **Release status:** v0.2.0 is prepared on `main`. Until the `v0.2.0` tag
> is published, the latest release is v0.1.1, which has no webhook support,
> and `go install …@latest` installs it. To use webhook scenarios before
> then, build from a checkout of `main`.

## Documentation

- [docs/webhooks.md](docs/webhooks.md): webhook scenarios, configuration,
  outcomes, state assertions, reports and limitations.
- [docs/test-state.md](docs/test-state.md): the test-state endpoints
  FlowGuard reads, the mock server's rules, reset strategies and staging
  requirements.
- [CHANGELOG.md](CHANGELOG.md): release history and unreleased changes.

## How it works

Define a scenario in YAML:

```yaml
version: 1

target:
  base_url: http://127.0.0.1:8080
  allowed_hosts: [127.0.0.1, localhost]

scenarios:
  - name: charge-once
    kind: idempotency
    setup:                      # optional: reset test state first
      - method: POST
        path: /_test/reset
    request:
      method: POST
      path: /orders
      headers:
        Idempotency-Key: test-order-001
      json: { customer_id: demo, amount: 100 }
    repeat: 2
    assert:
      status: 200
      business_effect_count: 1
```

Run it:

```bash
flowguard test --config flowguard.yaml --report report.json
```

`base_url` must be just a scheme and host (with optional port), such as
`http://127.0.0.1:8080`. A path prefix like `/api/v1` is rejected, since it
would otherwise be ignored and the scenario would test a different endpoint.
Put the prefix in each request path instead (`path: /api/v1/orders`).

FlowGuard learns the business-effect count from a test-state endpoint on the
target; see [docs/test-state.md](docs/test-state.md) for the contract.

`setup` requests run before the scenario so every run starts from a known
state. FlowGuard never resets a target unless you declare it; see
[Fixtures and isolation](docs/test-state.md#fixtures-and-isolation) for how
to manage fixtures, and only reset environments you own.

FlowGuard replays the request against an explicitly allow-listed target and
checks whether the underlying business effect (e.g. an order being created)
happened exactly once — not just whether the HTTP response looked fine.

## Install

Requires Go 1.25.3 or newer.

```bash
go install github.com/shakerShekhYousef/flowguard/cmd/flowguard@latest
flowguard version
```

The binary lands in `$(go env GOBIN)`, or `$(go env GOPATH)/bin` if `GOBIN`
is unset; make sure that directory is on your `PATH`. The module path is
case-sensitive: use `shakerShekhYousef` exactly as written. To pin a release,
replace `@latest` with a tag such as `@v0.1.0`.

Or build from a checkout:

```bash
git clone https://github.com/shakerShekhYousef/flowguard.git
cd flowguard
go build -o flowguard ./cmd/flowguard
./flowguard version
```

## Idempotency quickstart

Run from a checkout, after building `./flowguard` as shown above. The
commands use a POSIX shell (`&` starts the mock in the background and
`kill %1` stops it); on Windows, run the mock in a second terminal instead.

```bash
go run ./examples/mock-server &        # buggy: retries double-charge
./flowguard test --config flowguard.example.yaml
# [FAIL] business_effect_count: expected 1, got 2   (exit code 1)
kill %1
```

Re-running gives the same result every time, because the example resets the
mock in `setup`. Start the mock with `go run ./examples/mock-server -safe &`
instead and the run passes (exit code 0).

## Webhook quickstart

> Webhook scenarios need v0.2.0 or a build of `main`. A v0.1.1 binary
> rejects the webhook example with `unknown field` config errors (exit
> code 2). Until the `v0.2.0` tag is published, build `./flowguard` from a
> checkout as shown above.

Payment providers retry webhooks, so receivers see the same event more than
once. `flowguard.webhooks.example.yaml` plays the provider: it delivers one
`order.paid` event twice, then reads the order's state from the receiver.
The same POSIX shell notes apply as above.

Start the bundled mock receiver (buggy by default) and run the example:

```bash
go run ./examples/mock-server &        # buggy: a duplicate event charges again
./flowguard test --config flowguard.webhooks.example.yaml
```

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

The exit code is 1. Stop the mock (`kill %1`), restart it in safe mode and
run the same command again:

```bash
go run ./examples/mock-server -safe &  # dedupes events by id
./flowguard test --config flowguard.webhooks.example.yaml
```

```
  ...
  [PASS] state.charges
  [PASS] state.status
```

The exit code is 0. Stop the mock with `kill %1` when you are done.

**Why HTTP 200 is not enough:** the buggy receiver answers both deliveries
with 200, so every delivery check passes; a monitor watching status codes
would see nothing wrong. The bug is only visible in business state, the
customer charged twice, which is why the scenario asserts on
`GET /_test/orders/ord_1` after the deliveries.

The mock's safe mode follows illustrative rules (dedupe by event id, ignore
stale `sequence` numbers, treat cancelled as final, charge an order at most
once). They show the kind of invariant FlowGuard checks; they are not
universal requirements for payment systems. FlowGuard simulates a webhook
provider's deliveries; it does not test a real provider's internal retry
scheduler.

See [docs/webhooks.md](docs/webhooks.md) for the full configuration
reference and more examples.

## Development

```bash
go test ./...          # unit + end-to-end tests
go test -short ./...   # skip the end-to-end tests that build binaries
```

## Assertions

Idempotency scenarios support these rules. Webhook scenarios use per-delivery
expectations and `assert.state` instead; see
[docs/webhooks.md](docs/webhooks.md#delivery-outcomes-and-expectations).

| Rule | Passes when |
| ---- | ----------- |
| `status` | **Every** replayed request returns this HTTP status. A retry that errors before a later one succeeds (e.g. `500` then `200`) fails, because a client saw that error. |
| `business_effect_count` | The target reports exactly this many business effects for the scenario's `Idempotency-Key` after all replays. |

Each rule is evaluated independently, so a status failure does not hide the
business-effect result (and vice versa). Setup requests are not asserted.

When `status` fails, the terminal names each failing request by its
zero-based index (matching `steps[].index` in the report):

```
[FAIL] status: expected 200 for all 3 requests, request[0] got 500, request[2] got 503
```

and the JSON report adds `failed_requests`; `actual` is the first unexpected
status:

```json
{
  "rule": "status", "expected": 200, "actual": 500, "passed": false,
  "failed_requests": [
    { "index": 0, "expected": 200, "actual": 500 },
    { "index": 2, "expected": 200, "actual": 503 }
  ]
}
```

`failed_requests` is omitted when the rule passes, so passing reports are
unchanged.

## Exit codes

| Code | Meaning                          |
| ---- | -------------------------------- |
| 0    | All assertions passed            |
| 1    | A business assertion failed      |
| 2    | Configuration or runtime error   |

## Project layout

```
cmd/flowguard/      CLI entry point
internal/config/    YAML parsing and validation
internal/runner/    Allow-listed HTTP execution
internal/scenarios/ Scenario kinds (idempotency, webhook)
internal/assertions/ Business-effect assertions
internal/report/    JSON report generation
examples/mock-server/ Local synthetic API and webhook receiver for demos
tests/              End-to-end tests against the built binary
docs/               User docs (webhooks, test-state endpoints) and design notes
```

## Known limitations

- Business effects are read from test-only endpoints that your service must
  expose in a test environment; see [docs/test-state.md](docs/test-state.md).
- State is read once, right after the last request or delivery; services
  that process asynchronously are not supported yet.
- Webhook deliveries are sent once each, sequentially, without delays,
  retries or signatures; those are planned for v0.3.0. A receiver that
  verifies webhook signatures needs verification disabled in the test
  environment.
- Webhook event bodies are re-encoded with Go's `encoding/json`: keys are
  sorted and `<`, `>`, `&` are escaped as `\u003c`, `\u003e`, `\u0026`.
  The JSON values are identical, but the bytes differ from the YAML text.
- Idempotency scenarios are validated exactly as in v0.1.1. Their header
  names and values are not syntax-checked at load time, so a header that
  net/http refuses (for example one containing a newline) is reported when
  the request is sent, as a runtime error (exit code 2), after any `setup`
  requests have run. Webhook scenarios check all their headers at load
  time.
- A query string in `base_url` is ignored rather than rejected.
- Redaction is pattern-based. It covers URL credentials, query values,
  `name=value` pairs with sensitive names and `Bearer`/`Basic` tokens, but
  cannot recognise every secret: a bare token such as `sk_live_…` in an
  error or an asserted state value is shown as is. Scenario names and rule
  names are never redacted. Keep real secrets out of configs and treat
  reports as internal artifacts.

## Platform support

| Platform | Status |
| -------- | ------ |
| Linux | Supported; CI runs gofmt, `go vet` and `go test -race` on Ubuntu. |
| macOS | Supported; tested locally by the maintainer, not in CI. |
| Windows | Builds, but is not tested in CI. Reports cannot be restricted to owner-only `0600` (Windows permissions do not map to Unix modes), so protect the report directory instead. The quickstarts use POSIX job control (`&`, `kill %1`); run the mock in a second terminal instead. |

## Safety

FlowGuard only sends requests to hosts explicitly listed in `allowed_hosts`
and fails closed otherwise. It is built for local mocks and authorized
staging environments — never point it at a production or third-party target
without explicit permission.

What the allow-list enforces:

- Every URL FlowGuard contacts — the configured `base_url`, the test-state
  endpoint, and **every redirect hop** — must use `http` or `https` and have
  a hostname in `allowed_hosts`. A redirect anywhere else stops the scenario
  with an error before the redirected request is sent. Redirects within
  allowed hosts are followed (up to 10).

What it does **not** do:

- It matches **hostnames**, not network addresses. It does not check which
  IP an allowed name resolves to, so it is not complete SSRF protection: an
  allowed hostname that resolves (or is re-pointed via DNS) to an internal
  address will still be contacted.
- Proxy settings from the environment (`HTTP_PROXY`, `HTTPS_PROXY`) are
  honored; the allow-list applies to the destination URL, not the proxy.

For untrusted configs or shared CI runners, rely on network-level controls
as well — e.g. run FlowGuard in a container or network namespace that can
only reach the intended targets.

## License

MIT — see [LICENSE](LICENSE).
