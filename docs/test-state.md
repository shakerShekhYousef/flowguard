# Test-state endpoint (v0.1)

An HTTP `200` says nothing about whether a retried request charged a customer
twice. To check the *business effect*, FlowGuard asks the target how many
times the effect actually happened.

## Convention

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

## Reference implementation

`examples/mock-server` implements the endpoint, plus `POST /_test/reset` to
clear its state between runs:

```bash
go run ./examples/mock-server          # buggy: retries double-charge
go run ./examples/mock-server -safe    # fixed: retries are idempotent
```

Effects are counted per key and persist for the server's lifetime. The
shipped `flowguard.example.yaml` therefore declares a `setup` step that calls
`/_test/reset` before each run (see below).

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

## Limitations

This is a stand-in. Real services rarely expose such an endpoint, and adding
one only for tests is not always acceptable. How FlowGuard should observe
business effects on real targets (a hook like this, a database query, or
inspecting downstream calls) is an open product decision, still to be made.
