# FlowGuard

**FlowGuard helps backend developers catch duplicate charges and broken webhook
workflows before release, using reproducible CLI tests and business-rule
assertions.**

An API can return `HTTP 200` while its business state is wrong: a retried
payment can double-charge a customer, a duplicated webhook can apply an
effect twice, a delayed event can leave state inconsistent. FlowGuard tests
for *business-logic correctness*, not just response codes.

## Status

Early stage (v0.1). The `idempotency` scenario works end to end: config
validation, allow-listed HTTP replay, business-effect assertions, JSON
reports and exit codes, with unit and end-to-end tests. Webhook-fault
scenarios are planned for v0.2. Expect breaking changes until v1.

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

## Try it

```bash
go run ./examples/mock-server &        # buggy: retries double-charge
./flowguard test --config flowguard.example.yaml
# [FAIL] business_effect_count: expected 1, got 2   (exit code 1)
```

Re-running gives the same result every time, because the example resets the
mock in `setup`. Restart the mock with `-safe` and the run passes (exit
code 0).

## Development

```bash
go test ./...          # unit + end-to-end tests
go test -short ./...   # skip the end-to-end tests that build binaries
```

## Assertions

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
internal/scenarios/ Scenario kinds (idempotency; webhook faults in v0.2)
internal/assertions/ Business-effect assertions
internal/report/    JSON report generation
examples/mock-server/ Local synthetic API for safe, reproducible demos
tests/              End-to-end tests against the built binary
docs/               Design notes (test-state endpoint contract)
```

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
