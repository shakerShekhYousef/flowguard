package tests

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// webhookExample returns the shipped webhook example pointed at baseURL,
// with replace applied as old/new pairs, written to a fresh directory.
func webhookExample(t *testing.T, baseURL string, replace ...string) string {
	t.Helper()
	data, err := os.ReadFile("../flowguard.webhooks.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(string(data), "http://127.0.0.1:8080", baseURL, 1)
	for i := 0; i+1 < len(replace); i += 2 {
		if !strings.Contains(body, replace[i]) {
			t.Fatalf("test bug: %q not in the webhook example", replace[i])
		}
		body = strings.Replace(body, replace[i], replace[i+1], 1)
	}
	path := filepath.Join(t.TempDir(), "flowguard.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// runFlowGuard runs `flowguard test` with a report and returns the exit
// code, combined output and raw report ("" if none was written).
func runFlowGuard(t *testing.T, flowguard, cfg string) (int, string, string) {
	t.Helper()
	reportPath := filepath.Join(t.TempDir(), "report.json")
	out, err := exec.Command(flowguard, "test", "--config", cfg, "--report", reportPath).CombinedOutput()
	rep, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		return exitCode(err), string(out), ""
	}
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(reportPath)
		if statErr != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("report mode = %v (%v), want 0600", info.Mode().Perm(), statErr)
		}
	}
	return exitCode(err), string(out), string(rep)
}

type webhookReport struct {
	Passed    bool `json:"passed"`
	Scenarios []struct {
		Name       string           `json:"name"`
		Passed     bool             `json:"passed"`
		Error      string           `json:"error"`
		Steps      []map[string]any `json:"steps"`
		Assertions []map[string]any `json:"assertions"`
	} `json:"scenarios"`
}

func decodeReport(t *testing.T, raw string) webhookReport {
	t.Helper()
	var rep webhookReport
	if err := json.Unmarshal([]byte(raw), &rep); err != nil {
		t.Fatalf("report is not valid JSON: %v\n%s", err, raw)
	}
	if len(rep.Scenarios) != 1 {
		t.Fatalf("want 1 scenario, got %s", raw)
	}
	return rep
}

func TestWebhookEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries; skipped in -short mode")
	}

	dir := t.TempDir()
	flowguard := build(t, dir, "./cmd/flowguard")
	mock := build(t, dir, "./examples/mock-server")

	for _, tc := range []struct {
		name        string
		safe        bool
		wantCode    int
		wantCharges float64
		wantOut     []string
	}{
		{"buggy receiver charges twice", false, 1, 2, []string{"  [FAIL] state.charges: expected 1, got 2\n"}},
		{"safe receiver charges once", true, 0, 1, []string{"  [PASS] state.charges\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := webhookExample(t, startMock(t, mock, tc.safe))

			// One long-lived mock, three runs: setup resets it each time.
			for run := 1; run <= 3; run++ {
				code, out, raw := runFlowGuard(t, flowguard, cfg)
				if code != tc.wantCode {
					t.Fatalf("run %d: exit code = %d, want %d\n%s", run, code, tc.wantCode, out)
				}
				for _, want := range append([]string{
					"Running: duplicate-payment\n",
					"  delivery[0] evt_paid_1: 200 delivered\n",
					"  delivery[1] evt_paid_1: 200 delivered\n",
					"  [PASS] delivery[0].outcome\n",
					"  [PASS] delivery[0].status\n",
					"  [PASS] delivery[1].outcome\n",
					"  [PASS] delivery[1].status\n",
					"  [PASS] state.status\n",
				}, tc.wantOut...) {
					if !strings.Contains(out, want) {
						t.Fatalf("run %d: output missing %q:\n%s", run, want, out)
					}
				}

				rep := decodeReport(t, raw)
				sc := rep.Scenarios[0]
				if rep.Passed != (tc.wantCode == 0) || sc.Passed != (tc.wantCode == 0) || sc.Error != "" {
					t.Fatalf("run %d: report pass/error wrong:\n%s", run, raw)
				}
				if len(sc.Steps) != 2 {
					t.Fatalf("run %d: want 2 steps:\n%s", run, raw)
				}
				for i, step := range sc.Steps {
					if step["index"] != float64(i) || step["event_id"] != "evt_paid_1" || step["status"] != float64(200) || step["outcome"] != "delivered" {
						t.Fatalf("run %d: steps[%d] = %v", run, i, step)
					}
				}
				state := map[string]map[string]any{}
				for _, a := range sc.Assertions {
					if rule := a["rule"].(string); strings.HasPrefix(rule, "state.") {
						state[rule] = a
					}
				}
				charges, status := state["state.charges"], state["state.status"]
				if len(state) != 2 || charges["expected"] != float64(1) || charges["actual"] != tc.wantCharges || charges["passed"] != (tc.wantCode == 0) {
					t.Fatalf("run %d: state.charges = %v (state assertions %v)", run, charges, state)
				}
				if status["expected"] != "paid" || status["actual"] != "paid" || status["passed"] != true {
					t.Fatalf("run %d: state.status = %v", run, status)
				}
			}
		})
	}

	t.Run("disallowed target is a runtime error", func(t *testing.T) {
		baseURL := startMock(t, mock, false)
		cfg := webhookExample(t, baseURL, "allowed_hosts: [127.0.0.1, localhost]", "allowed_hosts: [localhost]")

		code, out, _ := runFlowGuard(t, flowguard, cfg)
		if code != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", code, out)
		}
		if strings.Contains(out, "[PASS]") || strings.Contains(out, "delivery[") {
			t.Fatalf("nothing may be delivered to a disallowed host:\n%s", out)
		}
		// The mock never saw the event.
		resp, err := http.Get(baseURL + "/_test/orders/ord_1")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("mock received a delivery: order state status %d", resp.StatusCode)
		}
	})

	t.Run("unavailable observation endpoint is a runtime error", func(t *testing.T) {
		cfg := webhookExample(t, startMock(t, mock, true), "path: /_test/orders/ord_1", "path: /_test/orders/ord_missing")

		code, out, raw := runFlowGuard(t, flowguard, cfg)
		if code != 2 {
			t.Fatalf("exit code = %d, want 2\n%s", code, out)
		}
		wantErr := "observe: GET /_test/orders/ord_missing returned status 404, want 200"
		if !strings.Contains(out, "  [ERROR] "+wantErr) {
			t.Fatalf("output missing the observe error:\n%s", out)
		}
		sc := decodeReport(t, raw).Scenarios[0]
		if sc.Passed || !strings.HasPrefix(sc.Error, wantErr) || len(sc.Steps) != 2 {
			t.Fatalf("report should keep both deliveries and the observe error:\n%s", raw)
		}
		for _, a := range sc.Assertions {
			if strings.HasPrefix(a["rule"].(string), "state.") {
				t.Fatalf("no state assertion without observed state:\n%s", raw)
			}
		}
	})

	t.Run("no secrets or bodies in output or report", func(t *testing.T) {
		// Synthetic secrets in a delivery header, the event JSON and the
		// observe query; the mock's response bodies must not be copied either.
		cfg := webhookExample(t, startMock(t, mock, false),
			"      path: /webhooks\n", "      path: /webhooks\n      headers: {Authorization: \"Bearer tok_E2E_SYNTH\"}\n",
			"data: {order_id: ord_1, amount: 100}", "data: {order_id: ord_1, amount: 100, card: \"4111111111111111\"}",
			"path: /_test/orders/ord_1", "path: /_test/orders/ord_1?api_key=sk_E2E_SYNTH")

		code, out, raw := runFlowGuard(t, flowguard, cfg)
		if code != 1 {
			t.Fatalf("exit code = %d, want 1\n%s", code, out)
		}
		for _, leak := range []string{
			"tok_E2E_SYNTH", "4111111111111111", "sk_E2E_SYNTH", // secrets
			`"received"`, `"applied"`, "processed_events", "last_sequence", // response bodies
		} {
			if strings.Contains(raw, leak) || strings.Contains(out, leak) {
				t.Fatalf("%q leaked\n--- report ---\n%s\n--- output ---\n%s", leak, raw, out)
			}
		}
	})
}
