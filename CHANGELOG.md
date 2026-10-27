# Changelog

All notable changes to FlowGuard are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). Until v1.0.0, minor versions may
include breaking changes.

## [0.1.1] - Unreleased

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

[0.1.1]: https://github.com/shakerShekhYousef/flowguard/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/shakerShekhYousef/flowguard/releases/tag/v0.1.0
