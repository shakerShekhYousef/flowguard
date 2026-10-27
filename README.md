# FlowGuard

**FlowGuard helps backend developers catch duplicate charges and broken webhook
workflows before release, using reproducible CLI tests and business-rule
assertions.**

An API can return `HTTP 200` while its business state is wrong: a retried
payment can double-charge a customer, a duplicated webhook can apply an
effect twice, a delayed event can leave state inconsistent. FlowGuard tests
for *business-logic correctness*, not just response codes.

## Status

Early, working-plan stage (v0.1). The CLI, config format and example mock
server below are functional; the HTTP runner, idempotency replay engine and
reporting are under active development. Expect breaking changes until v1.

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
flowguard test --config flowguard.yaml
```

FlowGuard replays the request against an explicitly allow-listed target and
checks whether the underlying business effect (e.g. an order being created)
happened exactly once — not just whether the HTTP response looked fine.

## Install (development)

```bash
go build -o flowguard ./cmd/flowguard
./flowguard version
```

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
internal/runner/    HTTP execution (in progress)
internal/scenarios/ Idempotency and webhook-fault scenarios (in progress)
internal/assertions/Business-effect assertions (in progress)
internal/report/    JSON report generation (in progress)
examples/mock-server/ Local synthetic API for safe, reproducible demos
```

## Safety

FlowGuard only sends requests to hosts explicitly listed in `allowed_hosts`
and fails closed otherwise. It is built for local mocks and authorized
staging environments — never point it at a production or third-party target
without explicit permission.

## License

MIT — see [LICENSE](LICENSE).
