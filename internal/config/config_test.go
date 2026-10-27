package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestLoad_Setup(t *testing.T) {
	cfg, err := Load(writeTemp(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scenarios[0].Setup) != 0 {
		t.Fatalf("setup should be empty when omitted, got %+v", cfg.Scenarios[0].Setup)
	}

	withSetup := strings.Replace(validConfig, "    request:\n", "    setup:\n      - {method: POST, path: /_test/reset}\n    request:\n", 1)
	cfg, err = Load(writeTemp(t, withSetup))
	if err != nil {
		t.Fatalf("config with setup should load: %v", err)
	}
	if got := cfg.Scenarios[0].Setup; len(got) != 1 || got[0].Method != "POST" || got[0].Path != "/_test/reset" {
		t.Fatalf("unexpected setup: %+v", got)
	}
}

func TestRequestHeader_CaseInsensitive(t *testing.T) {
	r := Request{Headers: map[string]string{"idempotency-key": "abc"}}
	if got := r.Header("Idempotency-Key"); got != "abc" {
		t.Fatalf("Header() = %q, want %q", got, "abc")
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
		"setup without path": `
version: 1
target:
  base_url: http://127.0.0.1:8080
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: x
    kind: idempotency
    setup: [{method: POST}]
    request: {method: POST, path: /orders, headers: {Idempotency-Key: k}}
    repeat: 1
    assert: {status: 200}
`,
		"setup without method": `
version: 1
target:
  base_url: http://127.0.0.1:8080
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: x
    kind: idempotency
    setup: [{path: /_test/reset}]
    request: {method: POST, path: /orders, headers: {Idempotency-Key: k}}
    repeat: 1
    assert: {status: 200}
`,
		"non-http scheme": `
version: 1
target:
  base_url: ftp://127.0.0.1:8080
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: x
    kind: idempotency
    request: {method: POST, path: /orders, headers: {Idempotency-Key: k}}
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
		"idempotency without key": `
version: 1
target:
  base_url: http://127.0.0.1:8080
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: x
    kind: idempotency
    request: {method: POST, path: /orders}
    repeat: 2
    assert: {business_effect_count: 1}
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

func TestLoad_RejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name    string
		from    string // text in validConfig to replace
		to      string
		wantErr []string // substrings the error must contain
	}{
		{
			name:    "misspelled assertion field",
			from:    "business_effect_count: 1",
			to:      "business_effect_cnt: 1",
			wantErr: []string{`unknown field "business_effect_cnt" in assert`, "business_effect_count", "line 18"},
		},
		{
			name:    "misspelled setup key on scenario",
			from:    "    request:\n",
			to:      "    setp:\n      - {method: POST, path: /_test/reset}\n    request:\n",
			wantErr: []string{`unknown field "setp" in scenario`, "setup"},
		},
		{
			name:    "misspelled field inside setup entry",
			from:    "    request:\n",
			to:      "    setup:\n      - {methd: POST, path: /_test/reset}\n    request:\n",
			wantErr: []string{`unknown field "methd" in request/setup entry`, "method"},
		},
		{
			name:    "unknown nested request field",
			from:    "      path: /orders\n",
			to:      "      path: /orders\n      body: {amount: 100}\n",
			wantErr: []string{`unknown field "body" in request/setup entry`, "json"},
		},
		{
			name:    "unknown target field",
			from:    "  allowed_hosts:",
			to:      "  allowed_host: [x]\n  allowed_hosts:",
			wantErr: []string{`unknown field "allowed_host" in target`, "allowed_hosts"},
		},
		{
			name:    "unknown top-level field",
			from:    "version: 1\n",
			to:      "version: 1\ntargets: {}\n",
			wantErr: []string{`unknown field "targets" in top level`, "scenarios"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(validConfig, tc.from) {
				t.Fatalf("test bug: %q not found in validConfig", tc.from)
			}
			_, err := Load(writeTemp(t, strings.Replace(validConfig, tc.from, tc.to, 1)))
			if err == nil {
				t.Fatal("expected an error for an unknown field, got none")
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestLoad_ReportsEveryUnknownField(t *testing.T) {
	cfg := strings.Replace(validConfig, "business_effect_count: 1", "business_effect_cnt: 1\n      stauts: 200", 1)
	_, err := Load(writeTemp(t, cfg))
	if err == nil || !strings.Contains(err.Error(), `"business_effect_cnt"`) || !strings.Contains(err.Error(), `"stauts"`) {
		t.Fatalf("expected both unknown fields to be reported, got %v", err)
	}
}

func TestLoad_FreeFormMapsStayOpen(t *testing.T) {
	// headers and json are user data, not schema: any key must be accepted.
	cfg := strings.Replace(validConfig,
		"      json: {customer_id: demo, amount: 100}",
		"      json: {customer_id: demo, amount: 100, any_new_key: {nested: true}}\n", 1)
	cfg = strings.Replace(cfg, "        Idempotency-Key: test-order-001", "        Idempotency-Key: test-order-001\n        X-Anything-Custom: v", 1)

	got, err := Load(writeTemp(t, cfg))
	if err != nil {
		t.Fatalf("free-form headers/json keys should be accepted: %v", err)
	}
	if got.Scenarios[0].Request.JSON["any_new_key"] == nil || got.Scenarios[0].Request.Header("X-Anything-Custom") != "v" {
		t.Fatalf("free-form values not decoded: %+v", got.Scenarios[0].Request)
	}
}

func TestLoad_ShippedExampleIsValid(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "flowguard.example.yaml"))
	if err != nil {
		t.Fatalf("flowguard.example.yaml must stay valid under strict decoding: %v", err)
	}
	if len(cfg.Scenarios) != 1 || len(cfg.Scenarios[0].Setup) != 1 || cfg.Scenarios[0].Assert.BusinessEffectCount == nil {
		t.Fatalf("example decoded unexpectedly: %+v", cfg.Scenarios)
	}
}

func TestLoad_Empty(t *testing.T) {
	for name, contents := range map[string]string{"empty": "", "comment only": "# nothing\n"} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeTemp(t, contents))
			if err == nil || !strings.Contains(err.Error(), "config is empty") {
				t.Fatalf("expected a 'config is empty' error, got %v", err)
			}
		})
	}
}

func TestLoad_BaseURLPath(t *testing.T) {
	withBase := func(base string) string {
		return strings.Replace(validConfig, "base_url: http://127.0.0.1:8080", "base_url: "+base, 1)
	}

	for _, ok := range []string{"http://127.0.0.1:8080", "http://127.0.0.1:8080/", "https://localhost"} {
		t.Run("accepts "+ok, func(t *testing.T) {
			if _, err := Load(writeTemp(t, withBase(ok))); err != nil {
				t.Fatalf("base_url %q should be accepted: %v", ok, err)
			}
		})
	}

	for _, bad := range []string{
		"http://127.0.0.1:8080/api/v1",
		"http://127.0.0.1:8080/api/v1/",
		"http://127.0.0.1:8080/api",
		"http://127.0.0.1:8080//",
	} {
		t.Run("rejects "+bad, func(t *testing.T) {
			_, err := Load(writeTemp(t, withBase(bad)))
			if err == nil {
				t.Fatalf("base_url %q must be rejected: its path would be silently dropped", bad)
			}
			for _, want := range []string{"base_url path", "not supported", "http://127.0.0.1:8080", "put the prefix in each request path"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestLoad_PrefixInRequestPathIsAllowed(t *testing.T) {
	// The supported way to target a prefixed API.
	cfg := strings.Replace(validConfig, "      path: /orders", "      path: /api/v1/orders", 1)
	got, err := Load(writeTemp(t, cfg))
	if err != nil {
		t.Fatalf("a prefix in request.path should be accepted: %v", err)
	}
	if got.Scenarios[0].Request.Path != "/api/v1/orders" {
		t.Fatalf("path = %q", got.Scenarios[0].Request.Path)
	}
}
