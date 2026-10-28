# Contributing to FlowGuard

Thanks for helping. Bug reports, documentation fixes and pull requests are
all welcome. For anything larger than a small fix, please open an issue
first so we can agree on the approach before you write code.

Security vulnerabilities are handled privately; see
[SECURITY.md](SECURITY.md) instead of opening an issue.

## Development setup

You need Go 1.25.3 or newer (the version in [go.mod](go.mod)) and git.

```bash
git clone https://github.com/shakerShekhYousef/flowguard.git
cd flowguard
go build ./...
go build -o bin/ ./cmd/flowguard ./examples/mock-server   # optional: local binaries
```

`bin/` is ignored by git. The [quickstart in the README](README.md#try-it-in-two-minutes)
shows how to run the binaries against the bundled mock server.

## Running tests

CI runs these on every push and pull request; run them before you open a
pull request:

```bash
gofmt -l .               # must print nothing
go vet ./...
go test -race ./...      # unit and end-to-end tests
```

Also useful:

```bash
go test -short ./...                      # skip end-to-end tests that build binaries
go test -count=1 ./internal/config/...    # one package, no test cache
go test -race -shuffle=on -count=3 ./...  # catch order-dependent or flaky tests
```

The end-to-end tests in `tests/` build `flowguard` and the mock server
and run them against each other on `127.0.0.1`. Tests only send requests
to `httptest` servers and the mock on `127.0.0.1`; other hostnames, such as
`example.com`, appear only as targets FlowGuard must refuse to contact.

## Coding conventions

- **Formatting and vetting:** code is `gofmt`-formatted and passes
  `go vet`.
- **Match the surrounding code:** follow the naming, comment density and
  structure of the package you are changing. Exported identifiers and
  non-obvious decisions get a comment that says *why*.
- **No new dependencies** without discussion in an issue first. FlowGuard
  depends only on the standard library and `gopkg.in/yaml.v3`.
- **Tests for every behavior change:** add a regression test with the fix
  or feature. Table-driven tests are the norm. Use `httptest` servers,
  never real or third-party hosts.
- **Synthetic data only:** fake secrets in tests use obviously synthetic
  values such as `tok_SYNTH`, so a test can assert that they never appear
  in output.
- **Never leak secrets or bodies:** terminal output, errors and reports
  must not contain request or response bodies, header values or
  credentials. Pass free text through `internal/redact`, and do not echo
  config values that could be secrets in error messages.
- **Exit codes are API:** `0` all assertions passed, `1` an assertion
  failed, `2` a configuration or runtime error. A runtime failure must
  never produce `0`.
- **Fail closed:** validate config at load time with an actionable error,
  rather than guessing or failing halfway through a run. Unknown YAML
  fields stay rejected.
- **Backward compatibility:** valid configs from earlier releases must keep
  producing the same requests, output, reports and exit codes. The golden
  tests in `internal/report` check the report format.

## Pull request guidelines

- Keep each pull request focused on one change; unrelated refactors make
  review harder.
- Describe what changed, why, and how you tested it. Link the issue it
  addresses.
- Update the documentation (`README.md`, `docs/`) when behavior or config
  changes, and add an entry under `## [Unreleased]` in
  [CHANGELOG.md](CHANGELOG.md).
- Make sure CI passes.
- Commit messages start with a short type prefix, as in the existing
  history: `feat:`, `fix:`, `docs:`, `test:` or `chore:`, followed by a
  summary in the imperative mood (`fix: reject observe.request.json`).

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
