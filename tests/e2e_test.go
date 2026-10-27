// Package tests runs the built flowguard binary against the example mock
// server, checking the documented exit codes end to end.
package tests

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func build(t *testing.T, dir, pkg string) string {
	t.Helper()
	bin := filepath.Join(dir, filepath.Base(pkg))
	cmd := exec.Command("go", "build", "-o", bin, pkg)
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building %s: %v\n%s", pkg, err, out)
	}
	return bin
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func startMock(t *testing.T, bin string, safe bool) string {
	t.Helper()
	addr := freeAddr(t)
	cmd := exec.Command(bin, "-addr", addr, fmt.Sprintf("-safe=%v", safe))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/_test/business-effect-count?key=probe")
		if err == nil {
			resp.Body.Close()
			return "http://" + addr
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("mock server did not start on %s", addr)
	return ""
}

func writeConfig(t *testing.T, dir, baseURL string) string {
	t.Helper()
	path := filepath.Join(dir, "flowguard.yaml")
	cfg := fmt.Sprintf(`version: 1
target:
  base_url: %s
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: charge-once
    kind: idempotency
    request:
      method: POST
      path: /orders
      headers: {Idempotency-Key: e2e-order-001}
      json: {customer_id: demo, amount: 100}
    repeat: 2
    assert:
      status: 200
      business_effect_count: 1
`, baseURL)
	if err := os.WriteFile(path, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func exitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

func TestEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries; skipped in -short mode")
	}

	dir := t.TempDir()
	flowguard := build(t, dir, "./cmd/flowguard")
	mock := build(t, dir, "./examples/mock-server")

	cases := []struct {
		name     string
		safe     bool
		wantCode int
		wantOut  string
	}{
		{"buggy server fails assertion", false, 1, "[FAIL] business_effect_count: expected 1, got 2"},
		{"safe server passes", true, 0, "[PASS] business_effect_count"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caseDir := t.TempDir()
			cfg := writeConfig(t, caseDir, startMock(t, mock, tc.safe))
			reportPath := filepath.Join(caseDir, "report.json")

			out, err := exec.Command(flowguard, "test", "--config="+cfg, "--report", reportPath).CombinedOutput()
			if got := exitCode(err); got != tc.wantCode {
				t.Fatalf("exit code = %d, want %d\n%s", got, tc.wantCode, out)
			}
			if !strings.Contains(string(out), tc.wantOut) {
				t.Fatalf("output missing %q:\n%s", tc.wantOut, out)
			}
			if _, err := os.Stat(reportPath); err != nil {
				t.Fatalf("report not written: %v", err)
			}
		})
	}

	// The shipped example declares a setup reset, so repeated runs against one
	// long-lived mock must give identical results.
	example, err := os.ReadFile("../flowguard.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		safe     bool
		wantCode int
		wantOut  string
	}{
		{"example is repeatable against buggy server", false, 1, "expected 1, got 2"},
		{"example is repeatable against safe server", true, 0, "[PASS] business_effect_count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseURL := startMock(t, mock, tc.safe)
			cfg := filepath.Join(t.TempDir(), "flowguard.yaml")
			body := strings.Replace(string(example), "http://127.0.0.1:8080", baseURL, 1)
			if err := os.WriteFile(cfg, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			for run := 1; run <= 3; run++ {
				out, err := exec.Command(flowguard, "test", "--config", cfg).CombinedOutput()
				if got := exitCode(err); got != tc.wantCode {
					t.Fatalf("run %d: exit code = %d, want %d\n%s", run, got, tc.wantCode, out)
				}
				if !strings.Contains(string(out), tc.wantOut) {
					t.Fatalf("run %d: output missing %q:\n%s", run, tc.wantOut, out)
				}
			}
		})
	}

	t.Run("without setup, state leaks between runs", func(t *testing.T) {
		// writeConfig declares no setup: documents why the example needs one.
		cfg := writeConfig(t, t.TempDir(), startMock(t, mock, false))
		var outs []string
		for run := 0; run < 2; run++ {
			out, _ := exec.Command(flowguard, "test", "--config", cfg).CombinedOutput()
			outs = append(outs, string(out))
		}
		if !strings.Contains(outs[0], "expected 1, got 2") || !strings.Contains(outs[1], "expected 1, got 4") {
			t.Fatalf("expected drift from 2 to 4 effects:\n%s\n---\n%s", outs[0], outs[1])
		}
	})

	t.Run("failing setup is a runtime error", func(t *testing.T) {
		cfg := writeConfig(t, t.TempDir(), startMock(t, mock, true))
		data, _ := os.ReadFile(cfg)
		data = []byte(strings.Replace(string(data), "    request:\n", "    setup: [{method: POST, path: /_test/no-such-hook}]\n    request:\n", 1))
		if err := os.WriteFile(cfg, data, 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(flowguard, "test", "--config", cfg).CombinedOutput()
		if got := exitCode(err); got != 2 || !strings.Contains(string(out), "setup[0]") {
			t.Fatalf("exit code = %d, want 2 with a setup[0] error\n%s", got, out)
		}
	})

	t.Run("unreachable target is a runtime error", func(t *testing.T) {
		cfg := writeConfig(t, t.TempDir(), "http://"+freeAddr(t))
		out, err := exec.Command(flowguard, "test", "--config", cfg).CombinedOutput()
		if got := exitCode(err); got != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", got, out)
		}
	})

	t.Run("errors are redacted in terminal output and report", func(t *testing.T) {
		dir := t.TempDir()
		cfg := filepath.Join(dir, "flowguard.yaml")
		// Synthetic credentials; the target is unreachable so the step fails
		// with a net/http error that embeds the request URL.
		yaml := fmt.Sprintf(`version: 1
target:
  base_url: http://fguser-SYNTH:fgpass-SYNTH@%s
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: leaky
    kind: idempotency
    request:
      method: POST
      path: /orders?api_key=sk_test_SYNTHETIC123
      headers: {Idempotency-Key: k1, Authorization: Bearer tok_SYNTHETIC}
      json: {password: pw_SYNTHETIC}
    repeat: 1
    assert: {status: 200}
`, freeAddr(t))
		if err := os.WriteFile(cfg, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}
		reportPath := filepath.Join(dir, "report.json")

		out, err := exec.Command(flowguard, "test", "--config", cfg, "--report", reportPath).CombinedOutput()
		if got := exitCode(err); got != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", got, out)
		}
		rep, err := os.ReadFile(reportPath)
		if err != nil {
			t.Fatal(err)
		}

		for _, secret := range []string{"fguser-SYNTH", "fgpass-SYNTH", "sk_test_SYNTHETIC123", "tok_SYNTHETIC", "pw_SYNTHETIC"} {
			if strings.Contains(string(out), secret) {
				t.Errorf("terminal output leaks %q:\n%s", secret, out)
			}
			if strings.Contains(string(rep), secret) {
				t.Errorf("report leaks %q:\n%s", secret, rep)
			}
		}
		if !strings.Contains(string(out), "api_key=REDACTED") {
			t.Errorf("terminal output lost diagnostic context:\n%s", out)
		}
	})

	t.Run("unknown config fields are a config error", func(t *testing.T) {
		// Against the buggy mock, a silently dropped assertion would exit 0.
		baseURL := startMock(t, mock, false)
		for _, typo := range []struct{ from, to, field string }{
			{"business_effect_count: 1", "business_effect_cnt: 1", "business_effect_cnt"},
			{"    request:\n", "    setp: [{method: POST, path: /_test/reset}]\n    request:\n", "setp"},
			{"      path: /orders\n", "      path: /orders\n      body: {amount: 1}\n", "body"},
		} {
			cfg := writeConfig(t, t.TempDir(), baseURL)
			data, _ := os.ReadFile(cfg)
			if !strings.Contains(string(data), typo.from) {
				t.Fatalf("test bug: %q not in config", typo.from)
			}
			if err := os.WriteFile(cfg, []byte(strings.Replace(string(data), typo.from, typo.to, 1)), 0o644); err != nil {
				t.Fatal(err)
			}

			out, err := exec.Command(flowguard, "test", "--config", cfg).CombinedOutput()
			if got := exitCode(err); got != 2 {
				t.Fatalf("%s: exit code = %d, want 2\n%s", typo.field, got, out)
			}
			if !strings.Contains(string(out), `unknown field "`+typo.field+`"`) {
				t.Fatalf("%s: error does not name the field:\n%s", typo.field, out)
			}
		}
	})

	t.Run("base_url path prefix cannot produce a false pass", func(t *testing.T) {
		// The safe mock serves /orders at the root. If /api/v1 were silently
		// dropped, this would PASS (exit 0) while testing the wrong endpoint.
		cfg := writeConfig(t, t.TempDir(), startMock(t, mock, true)+"/api/v1")
		out, err := exec.Command(flowguard, "test", "--config", cfg).CombinedOutput()
		if got := exitCode(err); got != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", got, out)
		}
		if !strings.Contains(string(out), `base_url path "/api/v1" is not supported`) {
			t.Fatalf("missing actionable error:\n%s", out)
		}
		if strings.Contains(string(out), "[PASS]") {
			t.Fatalf("no scenario may run with a rejected base_url:\n%s", out)
		}
	})

	t.Run("missing config flag is a runtime error", func(t *testing.T) {
		out, err := exec.Command(flowguard, "test").CombinedOutput()
		if got := exitCode(err); got != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", got, out)
		}
	})
}
