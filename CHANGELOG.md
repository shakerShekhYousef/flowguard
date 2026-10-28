# Changelog

All notable changes to FlowGuard are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Until v1.0.0, minor versions may
include breaking changes.

## [Unreleased]

## [0.2.0] - 2026-10-28

Webhook testing, Milestone 1. FlowGuard can now play a webhook provider and
check a receiver's business state after duplicate or reordered deliveries.
Automatic retries, delivery delays, timeout simulation, HMAC signing and
state polling (Milestone 2) are **not** part of this release; they are
planned for v0.3.0.

### Added

- `webhook` scenario kind: FlowGuard simulates a webhook provider. Declare
  `endpoint`, `events` (with optional per-event `headers`) and `deliveries`;
  the events are sent one at a time in list order, and repeating an event in
  `deliveries` delivers a duplicate. Listing an older event after a newer
  one simulates a late delivery. Event JSON is encoded once per delivery
  with `encoding/json` (keys sorted, `<>&` escaped) and every delivery of an
  event sends the same bytes. See `docs/webhooks.md`.
- Delivery expectations: every delivery is checked against
  `expect.outcome` (`delivered` for 2xx, `rejected` otherwise; defaults to
  `delivered`) and an optional exact `expect.status`, as the
  `delivery[i].outcome` and `delivery[i].status` assertions.
- Business-state assertions: `observe.request` reads JSON state with one
  `GET` after the last delivery, and `assert.state` compares dot-paths
  (`status`, `customer.id`, `items.0.sku`) by JSON type and exact value as
  `state.<path>` assertions. A missing path fails.
- Exact numbers in `assert.state`: expected numbers are read from the YAML
  text, so large integers, long decimals and scientific notation are
  compared exactly and never rounded to float64 (`0.10000000000000001` does
  not equal `0.1`). Exponents beyond ±1000 or numbers over 2000 digits are
  rejected at load time. Unquoted literals that YAML would otherwise read as
  strings, such as `1e400`, are numbers.
- Sensitive state paths (`api_token`, `password`, ...) show both values as
  `REDACTED`, with a readable message:
  `state.api_token (sensitive path, values redacted): expected REDACTED, got REDACTED`.
- Webhook terminal output: one line per delivery
  (`delivery[0] evt_paid_1: 200 delivered`).
- Webhook JSON reports: steps gain `event_id` and `outcome`, both omitted
  for idempotency steps.
- `examples/mock-server` webhook receiver: `POST /webhooks` for synthetic
  `order.created`, `order.paid` and `order.cancelled` events,
  `GET /_test/orders/{id}` for order state, and a reset that clears it.
  Buggy by default (duplicates and stale events apply); `-safe` follows
  illustrative dedupe and ordering rules.
- `flowguard.webhooks.example.yaml` duplicate-payment quickstart, with
  end-to-end tests against the built binaries (buggy exits 1, safe exits 0).
- Documentation: `docs/webhooks.md`, webhook sections in
  `docs/test-state.md`, platform notes in the README.

### Changed

- Terminal output prints a scenario's assertions before its runtime error,
  so results gathered before the error stay visible. Idempotency output is
  unchanged, because idempotency scenarios never have both.
- Config error wording, for configs that were already invalid (exit code 2
  as before):
  - unknown fields in a request entry are reported as
    `unknown field "x" in request entry (request, setup, endpoint or observe.request)`
    instead of `... in request/setup entry`;
  - the "valid fields" lists for scenarios and `assert` include the webhook
    fields, and the unsupported-kind error lists `idempotency, webhook`.

### Config validation

- Webhook scenarios reject idempotency-only fields (`request`, `repeat`,
  `assert.status`, `assert.business_effect_count`) and vice versa, with a
  hint to the webhook equivalent.
- `observe.request.method` must be `GET`, `observe.request.json` is
  rejected (the state request has no body), and `observe` and
  `assert.state` must be given together.
- At most 100 events and 100 deliveries per scenario; event ids must be
  unique and every delivery must name a declared event.
- `expect.status` must be 100-599 and agree with `expect.outcome`.
- Every header map in a webhook scenario (`setup`, `endpoint`, `events`,
  `observe.request`) is checked at load time: names must be valid HTTP
  header names, values may not contain CR, LF or other control characters,
  the same header may not appear twice in different case, and
  transport-managed headers such as `Host` or `Content-Length` cannot be
  set. Nothing, not even `setup`, is sent for a config that fails these
  checks, and errors never print header values.
- `assert.state` values must be scalars; an unquoted date gets a hint to
  quote it.

### Compatibility

- Valid v0.1 configs behave exactly as in v0.1.1: same requests, terminal
  output, JSON reports and exit codes. A golden test checks the report
  against v0.1.1 output. Idempotency scenarios keep v0.1.1 validation; the
  new header checks apply to webhook scenarios only.

### Known limitations

- Deliveries are not signed and are sent once each, without delays or
  retries (planned for v0.3.0). State is read once, without polling.
- Event JSON is re-encoded (sorted keys, escaped `<>&`), so receivers that
  compare raw bytes may see different bytes from the YAML text.
- Redaction is pattern-based: a bare secret in an asserted state value at a
  non-sensitive path, or in a scenario name, is shown as is.
- Windows builds but is not tested in CI, and reports there are not
  restricted to `0600`.

## [0.1.1] - 2026-10-27

### Fixed

- `flowguard version` and the run header now report the real module version
  (e.g. `v0.1.1`) from Go build info. v0.1.0 binaries print `0.1.0-dev`
  because the version was hard-coded; the code in v0.1.0 is otherwise
  unaffected.

### Added

- `CHANGELOG.md`.

## [0.1.0] - 2026-10-27

First public release. FlowGuard catches business-logic failures that HTTP
status codes hide, starting with duplicate effects from retried requests.

### Added

- `flowguard test --config <file> [--report <file>]` CLI with CI-friendly exit
  codes: `0` all assertions passed, `1` an assertion failed, `2` config or
  runtime error.
- `idempotency` scenario: replays a request `repeat` times with the same
  `Idempotency-Key`, then checks how many business effects the target applied.
- Assertions:
  - `status`: every replayed request must return the expected status;
    failures name each request index.
  - `business_effect_count`: the target must report exactly this many
    effects for the key.
- Optional per-scenario `setup` requests to reset test state before a run.
  Nothing is reset unless declared; a failed setup aborts with exit code 2.
- JSON report (`--report`) with per-step status and timing, assertion results
  and `failed_requests` details.
- `examples/mock-server`: a local orders API that reproduces the
  double-charge bug by default, with `-safe` for the fixed behavior.
- GitHub Actions CI: gofmt check, `go vet`, `go test -race`.

### Security

- Requests go only to hosts in `allowed_hosts`, using `http` or `https`,
  including every redirect hop. This is hostname allow-listing, not
  network-level SSRF protection; see the README.
- Terminal output and reports redact URL credentials, query values,
  credential-like fields and bearer tokens. Request and response bodies are
  never output.
- Reports are written with owner-only permissions (`0600`).

### Config validation

- Unknown YAML fields are rejected at every level, naming the field and the
  valid alternatives, so a typo cannot silently drop an assertion.
- `base_url` must be scheme and host only; a path prefix such as `/api/v1` is
  rejected because it would otherwise be ignored.

### Known limitations

- `flowguard version` prints `0.1.0-dev` (fixed in 0.1.1).
- Business effects are read from a test-only endpoint
  (`GET /_test/business-effect-count`); see `docs/test-state.md`.
- A query string in `base_url` is ignored rather than rejected.
- Webhook-fault scenarios are planned for v0.2.

[Unreleased]: https://github.com/shakerShekhYousef/flowguard/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/shakerShekhYousef/flowguard/compare/v0.1.1...v0.2.0
[0.1.1]: https://github.com/shakerShekhYousef/flowguard/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/shakerShekhYousef/flowguard/releases/tag/v0.1.0
