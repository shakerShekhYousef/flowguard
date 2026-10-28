# FlowGuard

[![CI](https://github.com/shakerShekhYousef/flowguard/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/shakerShekhYousef/flowguard/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/tag/shakerShekhYousef/flowguard?sort=semver&label=release)](https://github.com/shakerShekhYousef/flowguard/releases)
[![Go version](https://img.shields.io/github/go-mod/go-version/shakerShekhYousef/flowguard)](go.mod)
[![License: MIT](https://img.shields.io/github/license/shakerShekhYousef/flowguard)](LICENSE)

**FlowGuard is a command-line tool that checks whether your API's business
state stays correct when requests are retried and webhooks arrive twice or
out of order.**

## The problem

An API can answer `HTTP 200` while its business state is wrong:

- a client retries a payment request and the customer is charged twice;
- a payment provider delivers the same webhook twice and the order is
  fulfilled twice;
- an older event arrives after a newer one and moves an order back to an
  earlier state.

Every response looks fine, so tests and monitors that only check status
codes see nothing wrong. The bug is only visible in the resulting state.

## How FlowGuard differs from ordinary API tests

| A typical API test | A FlowGuard scenario |
| ------------------ | -------------------- |
| Sends a request once and checks the response. | Replays the same request, or delivers the same webhook event again or out of order. |
| Asserts on status codes and response bodies. | Asserts on **business state** after all requests: how many charges were applied, which status the order ended in. |
| Test logic lives in code. | Scenarios are declarative YAML; results are CI exit codes and a JSON report. |
| Can reach any host the test code names. | Only contacts hosts listed in `allowed_hosts`, including every redirect hop. |

You can write these checks by hand in any test framework. FlowGuard
packages the replay, the state check and the safety rails so that each
scenario is a few lines of config. It needs a test-only endpoint on your
service that reports the state to check (see
[docs/test-state.md](docs/test-state.md)).

## What you can test

- **Idempotency** (`kind: idempotency`): send the same request several
  times with one `Idempotency-Key`, then check that the business effect
  happened exactly once and that every response had the expected status.
- **Webhook receivers** (`kind: webhook`): FlowGuard plays the webhook
  provider. It delivers the events you declare, in the order you list
  them, including duplicates and late events. It checks each delivery's
  outcome (`delivered` for 2xx, `rejected` otherwise) and then the
  receiver's final state, with exact comparison of JSON values. It
  simulates a provider's deliveries; it does not test a real provider's
  own retry scheduler and never contacts one.

## Feature status

| Feature | Status |
| ------- | ------ |
| `idempotency` scenarios: replay a request, assert status and business-effect count | Released in v0.1.0 |
| JSON reports, CI exit codes, host allow-list, redaction | Released in v0.1.0 |
| `webhook` scenarios: sequential, duplicate and manually reordered deliveries, outcome and status expectations, exact final-state assertions | Released in v0.2.0 |
| Automatic retries, delivery delays, timeout simulation, HMAC signing, state polling | **Planned** for v0.3.0; not implemented |

FlowGuard is at an early stage. Expect breaking changes until v1.

## Install

Requires Go 1.25.3 or newer.

```bash
go install github.com/shakerShekhYousef/flowguard/cmd/flowguard@v0.2.0
flowguard version    # flowguard version v0.2.0
```

The binary lands in `$(go env GOBIN)`, or `$(go env GOPATH)/bin` if `GOBIN`
is unset; make sure that directory is on your `PATH`. The module path is
case-sensitive: use `shakerShekhYousef` exactly as written. Use `@latest`
instead of `@v0.2.0` to get the newest release.

## Try it in two minutes

The repository includes a mock API with a deliberate bug and example
configs, so you can see a failing and a passing run without touching any
real service. The commands below use a POSIX shell (macOS, Linux, WSL);
see [Platform support](#platform-support) for Windows.

**1. Build FlowGuard and the mock:**

```bash
git clone https://github.com/shakerShekhYousef/flowguard.git
cd flowguard
go build -o bin/ ./cmd/flowguard ./examples/mock-server
```

**2. Start the buggy mock and deliver a duplicate webhook:**

```bash
bin/mock-server &          # buggy by default: a duplicate event charges again
sleep 1                    # give it a moment to start listening
bin/flowguard test --config flowguard.webhooks.example.yaml
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

The exit code is `1`. Both deliveries got `200`, so every delivery check
passed, but the order was charged twice.

**3. Restart the mock in safe mode and run it again:**

```bash
kill $!                    # stop the buggy mock ($! is the last background job)
bin/mock-server -safe &    # dedupes events by id
sleep 1
bin/flowguard test --config flowguard.webhooks.example.yaml
```

```
Running: duplicate-payment
  delivery[0] evt_paid_1: 200 delivered
  delivery[1] evt_paid_1: 200 delivered
  [PASS] delivery[0].outcome
  [PASS] delivery[0].status
  [PASS] delivery[1].outcome
  [PASS] delivery[1].status
  [PASS] state.charges
  [PASS] state.status
```

The exit code is `0`.

**4. Try the idempotency example against the same mock, then stop it:**

```bash
bin/flowguard test --config flowguard.example.yaml --report report.json
kill $!
```

```
Running: charge-once
  [PASS] status
  [PASS] business_effect_count

report written to report.json
```

Against the buggy mock, the same command prints
`[FAIL] business_effect_count: expected 1, got 2` and exits `1`. Every
example resets the mock in `setup`, so re-running gives the same result.
Each run also prints a `FlowGuard <version>` header line, omitted above.

The mock's safe mode follows illustrative rules (dedupe by event id, ignore
stale `sequence` numbers, treat cancelled as final, charge an order at most
once). They show the kind of invariant FlowGuard checks; they are not
universal requirements for payment systems.

## Write your own scenario

An idempotency scenario:

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

```bash
flowguard test --config flowguard.yaml --report report.json
```

- `base_url` must be just a scheme and host (with optional port), such as
  `http://127.0.0.1:8080`. A path prefix like `/api/v1` is rejected, since
  it would otherwise be ignored. Put the prefix in each request path
  instead (`path: /api/v1/orders`).
- FlowGuard reads the business-effect count from a test-state endpoint on
  the target; see [docs/test-state.md](docs/test-state.md) for the
  contract and for what a staging environment needs.
- `setup` requests run before the scenario so every run starts from a known
  state. FlowGuard never resets a target unless you declare it; see
  [Fixtures and isolation](docs/test-state.md#fixtures-and-isolation), and
  only reset environments you own.

For webhook scenarios (`endpoint`, `events`, `deliveries`, `observe`,
`assert.state`), start from
[`flowguard.webhooks.example.yaml`](flowguard.webhooks.example.yaml) and see
[docs/webhooks.md](docs/webhooks.md).

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

## Exit codes and reports

| Code | Meaning                          |
| ---- | -------------------------------- |
| 0    | All assertions passed            |
| 1    | A business assertion failed      |
| 2    | Configuration or runtime error   |

`--report <file>` writes a JSON report with each step's status and timing
and every assertion's expected and actual value. Reports are created with
owner-only permissions (`0600`) and never contain request or response
bodies or headers.

## Documentation

- [docs/webhooks.md](docs/webhooks.md): webhook scenarios, configuration,
  outcomes, state assertions, reports and limitations.
- [docs/test-state.md](docs/test-state.md): the test-state endpoints
  FlowGuard reads, the mock server's rules, reset strategies and staging
  requirements.
- [CHANGELOG.md](CHANGELOG.md): release history and unreleased changes.
- [CONTRIBUTING.md](CONTRIBUTING.md): development setup, tests and pull
  requests.
- [SECURITY.md](SECURITY.md): reporting vulnerabilities and safe use.

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
  sorted and `<`, `>`, `&` are escaped as `<`, `>`, `&`.
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
| Windows | Builds, but is not tested in CI. Reports cannot be restricted to owner-only `0600` (Windows permissions do not map to Unix modes), so protect the report directory instead. The quickstart uses POSIX shell features (`&`, `$!`, `sleep`): run `bin\mock-server.exe` in a second terminal instead and stop it with Ctrl+C. |

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
only reach the intended targets. To report a vulnerability, see
[SECURITY.md](SECURITY.md).

## Project layout

```
cmd/flowguard/        CLI entry point
internal/config/      YAML parsing and validation
internal/runner/      Allow-listed HTTP execution
internal/scenarios/   Scenario kinds (idempotency, webhook)
internal/assertions/  Status, business-effect and state assertions
internal/report/      JSON report generation
examples/mock-server/ Local synthetic API and webhook receiver for demos
tests/                End-to-end tests against the built binaries
docs/                 User docs (webhooks, test-state endpoints) and design notes
```

## Contributing

Bug reports, documentation fixes and pull requests are welcome; see
[CONTRIBUTING.md](CONTRIBUTING.md). Please report security issues as
described in [SECURITY.md](SECURITY.md), not in a public issue.

## License

MIT — see [LICENSE](LICENSE).
