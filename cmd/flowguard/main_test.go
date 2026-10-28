package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// receiver is a minimal webhook receiver for CLI tests. Without dedupe it
// charges on every order.paid delivery.
func receiver(t *testing.T, dedupe bool, stateStatus int) string {
	t.Helper()
	var mu sync.Mutex
	charges, seen := 0, map[string]bool{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /_test/reset", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		charges, seen = 0, map[string]bool{}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /webhooks", func(w http.ResponseWriter, r *http.Request) {
		var evt struct{ ID, Type string }
		_ = json.NewDecoder(r.Body).Decode(&evt)
		if r.Header.Get("X-Reject") != "" {
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if dedupe && seen[evt.ID] {
			return
		}
		seen[evt.ID] = true
		if evt.Type == "order.paid" {
			charges++
		}
	})
	mux.HandleFunc("GET /_test/orders/ord_1", func(w http.ResponseWriter, r *http.Request) {
		if stateStatus != http.StatusOK {
			w.WriteHeader(stateStatus)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		status := "pending"
		if charges > 0 {
			status = "paid"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "charges": charges})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

const webhookConfig = `version: 1
target:
  base_url: %s
  allowed_hosts: [127.0.0.1]
scenarios:
  - name: duplicate-delivery
    kind: webhook
    setup: [{method: POST, path: /_test/reset}]
    endpoint: {method: POST, path: /webhooks}
    events:
      - id: evt_paid_1
        json: {id: evt_paid_1, type: order.paid, data: {order_id: ord_1, amount: 100}}
      - id: evt_note
        headers: {X-Reject: "1"}
        json: {id: evt_note, type: order.note}
    deliveries:
%s
    observe:
      request: {method: %s, path: /_test/orders/ord_1}
    assert:
      state: {status: paid, charges: 1}
`

const twoPaid = "      - event: evt_paid_1\n      - event: evt_paid_1"

// runCLI runs `flowguard test` on a config and returns the exit code and
// combined stdout/stderr.
func runCLI(t *testing.T, cfg string, extra ...string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "flowguard.yaml")
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() { _, _ = io.Copy(&buf, r); close(done) }()

	code := run(append([]string{"test", "--config", path}, extra...))

	w.Close()
	<-done
	os.Stdout, os.Stderr = oldOut, oldErr
	return code, buf.String()
}

func mustContain(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestWebhookCLI_ExitCodes(t *testing.T) {
	t.Run("0: all assertions pass", func(t *testing.T) {
		code, out := runCLI(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), twoPaid, "GET"))
		if code != exitOK {
			t.Fatalf("exit = %d, want 0\n%s", code, out)
		}
		mustContain(t, out,
			"Running: duplicate-delivery",
			"  delivery[0] evt_paid_1: 200 delivered\n",
			"  delivery[1] evt_paid_1: 200 delivered\n",
			"  [PASS] delivery[0].outcome\n",
			"  [PASS] delivery[1].outcome\n",
			"  [PASS] state.charges\n",
			"  [PASS] state.status\n")
		if strings.Contains(out, "[FAIL]") || strings.Contains(out, "[ERROR]") {
			t.Fatalf("unexpected failure lines:\n%s", out)
		}
	})

	t.Run("1: duplicate-charge bug fails state", func(t *testing.T) {
		code, out := runCLI(t, fmt.Sprintf(webhookConfig, receiver(t, false, http.StatusOK), twoPaid, "GET"))
		if code != exitAssertionFail {
			t.Fatalf("exit = %d, want 1\n%s", code, out)
		}
		mustContain(t, out, "  [FAIL] state.charges: expected 1, got 2\n", "  [PASS] state.status\n")
	})

	t.Run("1: delivery assertion failure still checks state", func(t *testing.T) {
		deliveries := "      - event: evt_note\n      - event: evt_paid_1"
		code, out := runCLI(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), deliveries, "GET"))
		if code != exitAssertionFail {
			t.Fatalf("exit = %d, want 1\n%s", code, out)
		}
		mustContain(t, out,
			"  delivery[0] evt_note: 422 rejected\n",
			"  [FAIL] delivery[0].outcome: expected delivered, got rejected (status 422)\n",
			"  [PASS] delivery[1].outcome\n",
			"  [PASS] state.charges\n")
	})

	t.Run("2: state read fails", func(t *testing.T) {
		code, out := runCLI(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusInternalServerError), twoPaid, "GET"))
		if code != exitRuntimeError {
			t.Fatalf("exit = %d, want 2\n%s", code, out)
		}
		mustContain(t, out, "  [PASS] delivery[1].outcome\n", "  [ERROR] observe: GET /_test/orders/ord_1 returned status 500, want 200")
		if strings.Index(out, "[PASS] delivery[1].outcome") > strings.Index(out, "[ERROR]") {
			t.Fatalf("assertions must be printed before the error:\n%s", out)
		}
	})

	t.Run("2: setup fails", func(t *testing.T) {
		cfg := strings.Replace(fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), twoPaid, "GET"), "path: /_test/reset", "path: /_test/no-such-reset", 1)
		code, out := runCLI(t, cfg)
		if code != exitRuntimeError {
			t.Fatalf("exit = %d, want 2\n%s", code, out)
		}
		mustContain(t, out, "  [ERROR] setup[0]: POST /_test/no-such-reset returned status 404")
		if strings.Contains(out, "delivery[") {
			t.Fatalf("no delivery may run after setup fails:\n%s", out)
		}
	})

	t.Run("2: observe method must be GET", func(t *testing.T) {
		code, out := runCLI(t, fmt.Sprintf(webhookConfig, receiver(t, true, http.StatusOK), twoPaid, "POST"))
		if code != exitRuntimeError {
			t.Fatalf("exit = %d, want 2\n%s", code, out)
		}
		mustContain(t, out, `observe.request.method "POST" is not supported: state is read with GET only`)
		if strings.Contains(out, "Running:") {
			t.Fatalf("an invalid config must not run anything:\n%s", out)
		}
	})
}

func TestWebhookCLI_DeliveryRuntimeError(t *testing.T) {
	// The receiver drops the connection for evt_drop, so delivery[1] is a
	// runtime error after delivery[0] already failed its assertion.
	mux := http.NewServeMux()
	var observed atomic.Bool
	mux.HandleFunc("POST /webhooks", func(w http.ResponseWriter, r *http.Request) {
		var evt struct{ ID string }
		_ = json.NewDecoder(r.Body).Decode(&evt)
		switch evt.ID {
		case "evt_note":
			w.WriteHeader(http.StatusUnprocessableEntity)
		case "evt_drop":
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				conn.Close()
			}
		}
	})
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) { observed.Store(true) })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := fmt.Sprintf(`version: 1
target: {base_url: %s, allowed_hosts: [127.0.0.1]}
scenarios:
  - name: dropped
    kind: webhook
    endpoint: {method: POST, path: /webhooks}
    events:
      - {id: evt_note, json: {id: evt_note}}
      - {id: evt_drop, json: {id: evt_drop}}
      - {id: evt_never, json: {id: evt_never}}
    deliveries: [{event: evt_note}, {event: evt_drop}, {event: evt_never}]
    observe: {request: {method: GET, path: /state}}
    assert: {state: {ok: true}}
`, srv.URL)

	code, out := runCLI(t, cfg)

	if code != exitRuntimeError {
		t.Fatalf("exit = %d, want 2\n%s", code, out)
	}
	mustContain(t, out,
		"  delivery[0] evt_note: 422 rejected\n",
		"  delivery[1] evt_drop: not delivered (runtime error)\n",
		"  [FAIL] delivery[0].outcome: expected delivered, got rejected (status 422)\n",
		`  [ERROR] delivery[1] (event "evt_drop"): `)
	if strings.Contains(out, "evt_never") || strings.Contains(out, "delivery[2]") || observed.Load() {
		t.Fatalf("no delivery or observation may follow a runtime error (observed=%v):\n%s", observed.Load(), out)
	}
}

func TestWebhookCLI_Report(t *testing.T) {
	report := filepath.Join(t.TempDir(), "report.json")
	code, out := runCLI(t, fmt.Sprintf(webhookConfig, receiver(t, false, http.StatusOK), twoPaid, "GET"), "--report", report)
	if code != exitAssertionFail {
		t.Fatalf("exit = %d, want 1\n%s", code, out)
	}
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatal(err)
	}
	var rep struct {
		Passed    bool
		Scenarios []struct {
			Name       string
			Passed     bool
			Steps      []map[string]any
			Assertions []struct{ Rule, Reason string }
		}
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatal(err)
	}
	sc := rep.Scenarios[0]
	if rep.Passed || sc.Name != "duplicate-delivery" || sc.Passed || len(sc.Steps) != 2 || len(sc.Assertions) != 4 {
		t.Fatalf("unexpected report: %s", data)
	}
	if sc.Assertions[2].Rule != "state.charges" || sc.Assertions[2].Reason != "state.charges: expected 1, got 2" {
		t.Fatalf("state.charges in report = %+v", sc.Assertions[2])
	}
}
