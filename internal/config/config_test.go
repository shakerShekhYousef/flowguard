package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flowguard.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("writing temp config: %v", err)
	}
	return path
}

const validConfig = `
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
      json: {customer_id: demo, amount: 100}
    repeat: 2
    assert:
      status: 200
      business_effect_count: 1
`

func TestLoad_Valid(t *testing.T) {
	cfg, err := Load(writeTemp(t, validConfig))
	if err != nil {
		t.Fatalf("expected valid config to load, got error: %v", err)
	}
	if len(cfg.Scenarios) != 1 || cfg.Scenarios[0].Name != "charge-once" {
		t.Fatalf("unexpected scenarios: %+v", cfg.Scenarios)
	}
}

func TestLoad_Invalid(t *testing.T) {
	cases := map[string]string{
		"missing base_url": `
version: 1
target:
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: x
    kind: idempotency
    request: {method: POST, path: /orders}
    repeat: 1
    assert: {status: 200}
`,
		"host not allowed": `
version: 1
target:
  base_url: http://evil.example.com
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: x
    kind: idempotency
    request: {method: POST, path: /orders}
    repeat: 1
    assert: {status: 200}
`,
		"no scenarios": `
version: 1
target:
  base_url: http://127.0.0.1:8080
  allowed_hosts: [127.0.0.1]
scenarios: []
`,
		"unsupported kind": `
version: 1
target:
  base_url: http://127.0.0.1:8080
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: x
    kind: webhook-fault
    request: {method: POST, path: /orders}
    repeat: 1
    assert: {status: 200}
`,
		"malformed yaml": `not: [valid`,
	}

	for name, contents := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(writeTemp(t, contents)); err == nil {
				t.Fatalf("expected an error, got none")
			}
		})
	}
}
