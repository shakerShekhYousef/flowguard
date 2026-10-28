# Changelog

All notable changes to FlowGuard are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Until v1.0.0, minor versions may
include breaking changes.

## [Unreleased]

Planned for v0.2.0. Not released yet: build from `main` to try it.
v0.2 Milestone 2 (delays, retries, timeouts, state polling, signing) is
still to come and may change the schema below before release.

### Added

- `webhook` scenario kind: FlowGuard simulates a webhook provider. Declare
  `endpoint`, `events` (with optional per-event `headers`) and `deliveries`;
  the events are sent one at a time in list order, and repeating an event in
  `deliveries` delivers a duplicate. Event JSON is encoded once per delivery
  and sent byte for byte. See `docs/webhooks.md`.
- Delivery expectations: every delivery is checked against
  `expect.outcome` (`delivered` for 2xx, `rejected` otherwise; defaults to
  `delivered`) and an optional exact `expect.status`, as the
  `delivery[i].outcome` and `delivery[i].status` assertions.
- Business-state assertions: `observe.request` reads JSON state with one
  `GET` after the last delivery, and `assert.state` compares dot-paths
  (`status`, `customer.id`, `items.0.sku`) by exact JSON type and value as
  `state.<path>` assertions. A missing path fails; values at sensitive paths
  are shown as `REDACTED`.
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
  `docs/test-state.md` and the README.

### Changed

- Terminal output prints a scenario's assertions before its runtime error,
  so results gathered before the error stay visible. Idempotency output is
  unchanged, because idempotency scenarios never have both.

### Config validation

- Webhook scenarios reject idempotency-only fields (`request`, `repeat`,
  `assert.status`, `assert.business_effect_count`) and vice versa, with a
  hint to the webhook equivalent.
- `observe.request.method` must be `GET`; `observe` and `assert.state` must
  be given together.
- At most 100 events and 100 deliveries per scenario; event ids must be
  unique and every delivery must name a declared event.
- `expect.status` must be 100-599 and agree with `expect.outcome`.
- Webhook headers may not repeat a name in different case or set
  transport-managed headers such as `Host` or `Content-Length`.
- `assert.state` values must be scalars; an unquoted date gets a hint to
  quote it.

### Compatibility

- v0.1 configs, idempotency terminal output, JSON reports and exit codes are
  unchanged; a golden test checks the report against v0.1.1 output.

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

[Unreleased]: https://github.com/shakerShekhYousef/flowguard/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/shakerShekhYousef/flowguard/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/shakerShekhYousef/flowguard/releases/tag/v0.1.0
