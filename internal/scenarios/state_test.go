package scenarios

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

// stateServer answers GET /_test/orders/ord_1 with body and status.
func stateServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func observeOrder() config.Observe {
	return config.Observe{Request: config.Request{Method: http.MethodGet, Path: "/_test/orders/ord_1"}}
}

func stateScenario(state map[string]any) config.Scenario {
	obs := observeOrder()
	return config.Scenario{Name: "s", Kind: "webhook", Observe: &obs, Assert: config.AssertRule{State: state}}
}

func client() *runner.Client { return runner.New([]string{"127.0.0.1"}, time.Second) }

func TestObserveState_Success(t *testing.T) {
	srv, _ := stateServer(t, http.StatusOK, `{"status":"paid","charges":1,"customer":{"id":"customer_123"},"items":[{"quantity":2}]}`)

	results, err := checkState(context.Background(), client(), srv.URL, stateScenario(map[string]any{
		"status": "paid", "charges": 1, "customer.id": "customer_123", "items.0.quantity": 2,
	}))

	if err != nil {
		t.Fatalf("unexpected runtime error: %v", err)
	}
	if len(results) != 4 || !assertions.AllPassed(results) {
		t.Fatalf("want 4 passing results, got %+v", results)
	}
}

func TestObserveState_MismatchIsAssertionNotRuntimeError(t *testing.T) {
	srv, _ := stateServer(t, http.StatusOK, `{"status":"paid","charges":3}`)

	results, err := checkState(context.Background(), client(), srv.URL, stateScenario(map[string]any{"charges": 1, "status": "paid", "refund_id": "r1"}))

	if err != nil {
		t.Fatalf("mismatches and missing paths must not be runtime errors: %v", err)
	}
	reasons := map[string]string{}
	for _, r := range results {
		reasons[r.Rule] = r.Reason
	}
	if reasons["state.charges"] != "state.charges: expected 1, got 3" {
		t.Fatalf("charges reason = %q", reasons["state.charges"])
	}
	if !strings.Contains(reasons["state.refund_id"], "path not found") {
		t.Fatalf("refund_id reason = %q", reasons["state.refund_id"])
	}
	if reasons["state.status"] != "" {
		t.Fatalf("status should pass, got %q", reasons["state.status"])
	}
}

func TestObserveState_SendsDeclaredHeaders(t *testing.T) {
	var method, header string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, header = r.Method, r.Header.Get("X-Test-Tenant")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	obs := config.Observe{Request: config.Request{Method: http.MethodGet, Path: "/state", Headers: map[string]string{"X-Test-Tenant": "t1"}}}
	if _, err := observeState(context.Background(), client(), srv.URL, obs); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodGet || header != "t1" {
		t.Fatalf("observe sent %s with X-Test-Tenant=%q, want GET t1", method, header)
	}
}

func TestObserveState_RuntimeErrors(t *testing.T) {
	const secretBody = `{"password":"pw_SYNTH","card":"4111111111111111"}`
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"404", http.StatusNotFound, secretBody, "returned status 404, want 200"},
		{"500", http.StatusInternalServerError, secretBody, "returned status 500, want 200"},
		{"201 is not 200", http.StatusCreated, `{"status":"paid"}`, "returned status 201, want 200"},
		{"invalid JSON", http.StatusOK, `{"password":"pw_SYNTH", 4111111111111111`, "returned invalid JSON"},
		{"empty body", http.StatusOK, ``, "returned invalid JSON"},
		{"HTML", http.StatusOK, `<html>pw_SYNTH</html>`, "returned invalid JSON"},
		{"trailing data", http.StatusOK, `{"status":"paid"} {"password":"pw_SYNTH"}`, "unexpected data after the first value"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := stateServer(t, tc.status, tc.body)

			results, err := checkState(context.Background(), client(), srv.URL, stateScenario(map[string]any{"status": "paid"}))

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want runtime error containing %q, got %v", tc.want, err)
			}
			if results != nil {
				t.Fatalf("no assertion results on a runtime error, got %+v", results)
			}
			for _, secret := range []string{"pw_SYNTH", "4111111111111111", "<html>"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposes the response body (%q): %v", secret, err)
				}
			}
			if !strings.Contains(err.Error(), "body not shown") {
				t.Fatalf("error should say the body is withheld: %v", err)
			}
		})
	}
}

func TestObserveState_NetworkAndPolicyErrors(t *testing.T) {
	t.Run("connection refused", func(t *testing.T) {
		obs := observeOrder()
		obs.Request.Path = "/_test/orders/ord_1?token=tok_SYNTH"
		_, err := observeState(context.Background(), client(), "http://127.0.0.1:1", obs)
		if err == nil || !strings.Contains(err.Error(), "observe:") {
			t.Fatalf("want an observe runtime error, got %v", err)
		}
		if strings.Contains(err.Error(), "tok_SYNTH") {
			t.Fatalf("error leaks a query value: %v", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		srv, hits := stateServer(t, http.StatusOK, `{}`)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := observeState(ctx, client(), srv.URL, observeOrder())
		if !errors.Is(err, context.Canceled) || hits.Load() != 0 {
			t.Fatalf("want context.Canceled with no request, got %v (%d hits)", err, hits.Load())
		}
	})

	t.Run("disallowed host", func(t *testing.T) {
		srv, hits := stateServer(t, http.StatusOK, `{}`)
		_, err := observeState(context.Background(), runner.New([]string{"localhost"}, time.Second), srv.URL, observeOrder())
		if !errors.Is(err, runner.ErrDisallowedTarget) || hits.Load() != 0 {
			t.Fatalf("want ErrDisallowedTarget with no request, got %v (%d hits)", err, hits.Load())
		}
	})

	t.Run("redirect to disallowed host", func(t *testing.T) {
		var outsideHits atomic.Int32
		outside := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			outsideHits.Add(1)
			_, _ = w.Write([]byte(`{"status":"paid"}`))
		}))
		defer outside.Close()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, strings.Replace(outside.URL, "127.0.0.1", "localhost", 1)+"/state", http.StatusFound)
		}))
		defer srv.Close()

		_, err := observeState(context.Background(), client(), srv.URL, observeOrder())
		if !errors.Is(err, runner.ErrDisallowedTarget) || outsideHits.Load() != 0 {
			t.Fatalf("want ErrDisallowedTarget with no outside request, got %v (%d hits)", err, outsideHits.Load())
		}
	})

	t.Run("same-host redirect is followed", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/final" {
				_, _ = w.Write([]byte(`{"status":"paid"}`))
				return
			}
			http.Redirect(w, r, "/final", http.StatusFound)
		}))
		defer srv.Close()

		results, err := checkState(context.Background(), client(), srv.URL, stateScenario(map[string]any{"status": "paid"}))
		if err != nil || !assertions.AllPassed(results) {
			t.Fatalf("want a passing check after a same-host redirect, got %+v / %v", results, err)
		}
	})

	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}))
		defer srv.Close()
		defer close(release)

		start := time.Now()
		_, err := observeState(context.Background(), runner.New([]string{"127.0.0.1"}, 100*time.Millisecond), srv.URL, observeOrder())
		if err == nil || time.Since(start) > 2*time.Second {
			t.Fatalf("want a prompt timeout error, got %v after %v", err, time.Since(start))
		}
	})
}

func TestCheckState_NoObserveMeansNoChecks(t *testing.T) {
	results, err := checkState(context.Background(), client(), "http://127.0.0.1:1", config.Scenario{Kind: "webhook"})
	if err != nil || results != nil {
		t.Fatalf("want nothing to do, got %+v / %v", results, err)
	}
}
