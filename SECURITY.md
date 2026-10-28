# Security policy

## Supported versions

FlowGuard is pre-1.0. Security fixes are made on `main` and released in a
new version; only the latest release is supported.

| Version | Supported |
| ------- | --------- |
| 0.2.x (latest) | Yes |
| < 0.2 | No: upgrade to the latest release |

## Reporting a vulnerability

Please **do not report vulnerabilities in public issues, pull requests or
discussions.**

1. If the repository's **Security** tab offers **Report a vulnerability**
   (GitHub private vulnerability reporting), use it. The report is visible
   only to you and the maintainer.
2. If that option is not available, open a public issue titled
   "Security contact request" that contains **no details** of the problem,
   and the maintainer will arrange a private channel with you.

Helpful details for the private report:

- the FlowGuard version (`flowguard version`), Go version and OS;
- what an attacker can achieve, and under which assumptions (for example,
  "an attacker who controls the target's responses can ...");
- a minimal config and steps to reproduce, using synthetic data and a
  local target such as `examples/mock-server`;
- whether you have shared the issue with anyone else.

This is a single-maintainer project, so responses are best effort. You
will be told when the report is confirmed and when a fix is released, and
you will be credited in the release notes unless you prefer not to be.

## What counts as a vulnerability

In scope, for example:

- requests sent to a host that is not in `allowed_hosts`, including through
  redirects, absolute request paths or URL parsing;
- secrets, request or response bodies, or header values appearing in
  terminal output, error messages or JSON reports;
- report files written with permissions wider than owner-only on Linux or
  macOS;
- header or request injection from config values;
- crashes, hangs or unbounded resource use caused by a target's responses.

Out of scope:

- limitations the documentation already states, such as the allow-list
  matching hostnames rather than IP addresses (DNS-based SSRF),
  environment proxy settings being honored, pattern-based redaction
  missing an unrecognized secret format, or `0600` not being enforced on
  Windows;
- the deliberate bugs in `examples/mock-server`, which is a demo, not a
  service to deploy;
- problems that require an attacker who can already edit your FlowGuard
  config or run commands on your machine.

## Use FlowGuard only on systems you control

FlowGuard sends real requests and synthetic webhook events, and scenarios
are designed to trigger duplicate and out-of-order processing. Run it only
against:

- local mocks, or
- test and staging environments that you own or are explicitly authorized
  to test.

Do not point it at production systems or third-party services (including
real webhook providers or payment APIs) without written permission: a
scenario can create real side effects such as charges, orders or emails.
Use synthetic data and test credentials in configs, never real secrets or
customer data, and treat reports as internal artifacts. See
[Safety](README.md#safety) in the README for what the host allow-list does
and does not protect against.
