package runner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/config"
)

func TestDo_SendsRequest(t *testing.T) {
	var gotBody, gotKey, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotKey = r.Header.Get("Idempotency-Key")
		gotType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := New([]string{"127.0.0.1"}, time.Second)
	res := c.Do(context.Background(), srv.URL, config.Request{
		Method:  http.MethodPost,
		Path:    "/orders",
		Headers: map[string]string{"Idempotency-Key": "k1"},
		JSON:    map[string]any{"amount": 100},
	})

	if res.Err != nil {
		t.Fatalf("unexpected error: %v", res.Err)
	}
	if res.Status != http.StatusCreated || string(res.Body) != `{"ok":true}` {
		t.Fatalf("unexpected result: status=%d body=%q", res.Status, res.Body)
	}
	if gotBody != `{"amount":100}` || gotKey != "k1" || gotType != "application/json" {
		t.Fatalf("server saw body=%q key=%q content-type=%q", gotBody, gotKey, gotType)
	}
}

func TestDo_RejectsDisallowedHost(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()

	c := New([]string{"localhost"}, time.Second) // srv listens on 127.0.0.1
	res := c.Do(context.Background(), srv.URL, config.Request{Method: http.MethodGet, Path: "/"})

	if !errors.Is(res.Err, ErrDisallowedTarget) {
		t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
	}
	if called {
		t.Fatal("request reached a host outside allowed_hosts")
	}
}

func TestGetJSON_RejectsDisallowedHost(t *testing.T) {
	c := New([]string{"127.0.0.1"}, time.Second)
	var out map[string]any
	if err := c.GetJSON(context.Background(), "http://example.com/x", &out); !errors.Is(err, ErrDisallowedTarget) {
		t.Fatalf("expected ErrDisallowedTarget, got %v", err)
	}
}

// redirectServer answers every path with a redirect to location, except
// /final, which returns 200 {"ok":true}.
func redirectServer(t *testing.T, location func(r *http.Request) string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/final" {
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		http.Redirect(w, r, location(r), http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// outsideServer is reachable via "localhost", which the tests leave out of
// allowed_hosts; hit reports whether any request reached it.
func outsideServer(t *testing.T) (url string, hit *atomic.Bool) {
	t.Helper()
	hit = &atomic.Bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), hit
}

func TestDo_FollowsSameHostRedirect(t *testing.T) {
	srv := redirectServer(t, func(*http.Request) string { return "/final" })

	c := New([]string{"127.0.0.1"}, time.Second)
	res := c.Do(context.Background(), srv.URL, config.Request{Method: http.MethodGet, Path: "/start"})

	if res.Err != nil {
		t.Fatalf("same-host redirect should be followed, got error: %v", res.Err)
	}
	if res.Status != http.StatusOK || string(res.Body) != `{"ok":true}` {
		t.Fatalf("unexpected result: status=%d body=%q", res.Status, res.Body)
	}
}

func TestDo_FollowsRedirectToOtherAllowedHost(t *testing.T) {
	target, hit := outsideServer(t)
	srv := redirectServer(t, func(*http.Request) string { return target + "/x" })

	c := New([]string{"127.0.0.1", "localhost"}, time.Second)
	res := c.Do(context.Background(), srv.URL, config.Request{Method: http.MethodGet, Path: "/start"})

	if res.Err != nil || !hit.Load() {
		t.Fatalf("redirect to an allowed host should be followed: err=%v hit=%v", res.Err, hit.Load())
	}
}

func TestDo_BlocksRedirectToDisallowedHost(t *testing.T) {
	target, hit := outsideServer(t)
	srv := redirectServer(t, func(*http.Request) string { return target + "/steal" })

	c := New([]string{"127.0.0.1"}, time.Second)
	res := c.Do(context.Background(), srv.URL, config.Request{
		Method: http.MethodPost,
		Path:   "/orders",
		JSON:   map[string]any{"amount": 100},
	})

	if !errors.Is(res.Err, ErrDisallowedTarget) {
		t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
	}
	if hit.Load() {
		t.Fatal("redirect reached a host outside allowed_hosts")
	}
	if !strings.Contains(res.Err.Error(), `"localhost"`) {
		t.Fatalf("error should name the rejected host: %v", res.Err)
	}
}

func TestDo_BlocksRedirectToDisallowedScheme(t *testing.T) {
	for _, loc := range []string{"ftp://127.0.0.1/x", "file:///etc/passwd", "gopher://127.0.0.1/"} {
		t.Run(loc, func(t *testing.T) {
			srv := redirectServer(t, func(*http.Request) string { return loc })

			c := New([]string{"127.0.0.1"}, time.Second)
			res := c.Do(context.Background(), srv.URL, config.Request{Method: http.MethodGet, Path: "/start"})

			if !errors.Is(res.Err, ErrDisallowedTarget) {
				t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
			}
		})
	}
}

func TestDo_RejectsDisallowedScheme(t *testing.T) {
	c := New([]string{"127.0.0.1"}, time.Second)
	res := c.Do(context.Background(), "ftp://127.0.0.1", config.Request{Method: http.MethodGet, Path: "/x"})

	if !errors.Is(res.Err, ErrDisallowedTarget) {
		t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
	}
}

func TestDo_StopsRedirectLoop(t *testing.T) {
	srv := redirectServer(t, func(r *http.Request) string { return r.URL.Path + "x" })

	c := New([]string{"127.0.0.1"}, time.Second)
	res := c.Do(context.Background(), srv.URL, config.Request{Method: http.MethodGet, Path: "/loop"})

	if res.Err == nil || !strings.Contains(res.Err.Error(), "redirects") {
		t.Fatalf("expected a redirect-limit error, got %v", res.Err)
	}
	if errors.Is(res.Err, ErrDisallowedTarget) {
		t.Fatalf("redirect limit should not be reported as a policy violation: %v", res.Err)
	}
}

func TestDo_PolicyAppliesToCustomHTTPClient(t *testing.T) {
	target, hit := outsideServer(t)
	srv := redirectServer(t, func(*http.Request) string { return target })

	// Built without New(): the redirect policy must still apply.
	c := &Client{HTTPClient: &http.Client{Timeout: time.Second}, AllowedHosts: map[string]bool{"127.0.0.1": true}}
	res := c.Do(context.Background(), srv.URL, config.Request{Method: http.MethodGet, Path: "/start"})

	if !errors.Is(res.Err, ErrDisallowedTarget) || hit.Load() {
		t.Fatalf("expected redirect to be blocked: err=%v hit=%v", res.Err, hit.Load())
	}
	if c.HTTPClient.CheckRedirect != nil {
		t.Fatal("caller's http.Client must not be mutated")
	}
}

func TestGetJSON_FollowsSameHostRedirect(t *testing.T) {
	srv := redirectServer(t, func(*http.Request) string { return "/final" })

	c := New([]string{"127.0.0.1"}, time.Second)
	var out struct{ OK bool }
	if err := c.GetJSON(context.Background(), srv.URL+"/start", &out); err != nil || !out.OK {
		t.Fatalf("expected redirect to be followed: err=%v out=%+v", err, out)
	}
}

func TestGetJSON_BlocksRedirectToDisallowedHost(t *testing.T) {
	target, hit := outsideServer(t)
	srv := redirectServer(t, func(*http.Request) string { return target })

	c := New([]string{"127.0.0.1"}, time.Second)
	var out map[string]any
	err := c.GetJSON(context.Background(), srv.URL+"/start", &out)

	if !errors.Is(err, ErrDisallowedTarget) || hit.Load() {
		t.Fatalf("expected redirect to be blocked: err=%v hit=%v", err, hit.Load())
	}
}

func TestGetJSON_RejectsDisallowedScheme(t *testing.T) {
	c := New([]string{"127.0.0.1"}, time.Second)
	var out map[string]any
	if err := c.GetJSON(context.Background(), "ftp://127.0.0.1/x", &out); !errors.Is(err, ErrDisallowedTarget) {
		t.Fatalf("expected ErrDisallowedTarget, got %v", err)
	}
}

// Synthetic credentials used by the redaction tests below.
const (
	synthQuerySecret = "sk_test_SYNTHETIC123"
	synthUser        = "fguser-SYNTH"
	synthPass        = "fgpass-SYNTH"
	synthBodySecret  = "card=4111111111111111;session=SYNTHSESSION"
)

func assertNoSecrets(t *testing.T, s string, secrets ...string) {
	t.Helper()
	for _, sec := range secrets {
		if strings.Contains(s, sec) {
			t.Fatalf("output leaks %q:\n%s", sec, s)
		}
	}
}

func TestDo_NetworkErrorRedactsURL(t *testing.T) {
	c := New([]string{"127.0.0.1"}, time.Second)
	// Nothing listens on port 1.
	res := c.Do(context.Background(), "http://"+synthUser+":"+synthPass+"@127.0.0.1:1", config.Request{
		Method:  http.MethodPost,
		Path:    "/orders?api_key=" + synthQuerySecret,
		Headers: map[string]string{"Authorization": "Bearer tok_SYNTH"},
	})

	if res.Err == nil {
		t.Fatal("expected a connection error")
	}
	msg := res.Err.Error()
	assertNoSecrets(t, msg, synthQuerySecret, synthUser, synthPass, "tok_SYNTH")
	for _, keep := range []string{"127.0.0.1:1/orders", "api_key=REDACTED", "connection refused"} {
		if !strings.Contains(msg, keep) {
			t.Fatalf("error lost diagnostic context %q: %s", keep, msg)
		}
	}
	var ue *url.Error
	if !errors.As(res.Err, &ue) {
		t.Fatalf("error chain should still expose *url.Error: %T", res.Err)
	}
}

func TestDo_DoesNotExposeBodyOrHeadersInErrors(t *testing.T) {
	target, _ := outsideServer(t)
	srv := redirectServer(t, func(*http.Request) string { return target + "/x?token=" + synthQuerySecret })

	c := New([]string{"127.0.0.1"}, time.Second)
	res := c.Do(context.Background(), srv.URL, config.Request{
		Method:  http.MethodPost,
		Path:    "/orders",
		Headers: map[string]string{"Authorization": "Bearer tok_SYNTH", "X-Api-Key": "ak_SYNTH"},
		JSON:    map[string]any{"password": "pw_SYNTH"},
	})

	if !errors.Is(res.Err, ErrDisallowedTarget) {
		t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
	}
	assertNoSecrets(t, res.Err.Error(), synthQuerySecret, "tok_SYNTH", "ak_SYNTH", "pw_SYNTH")
}

func TestGetJSON_ErrorOmitsBodyAndQueryValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(synthBodySecret))
	}))
	defer srv.Close()

	c := New([]string{"127.0.0.1"}, time.Second)
	var out map[string]any
	err := c.GetJSON(context.Background(), srv.URL+"/_test/business-effect-count?key="+synthQuerySecret, &out)

	if err == nil {
		t.Fatal("expected an error for status 500")
	}
	assertNoSecrets(t, err.Error(), synthBodySecret, "4111111111111111", "SYNTHSESSION", synthQuerySecret)
	if !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "body not shown") {
		t.Fatalf("error should keep status and body size: %v", err)
	}
}

func TestGetJSON_DecodeErrorOmitsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json " + synthBodySecret))
	}))
	defer srv.Close()

	c := New([]string{"127.0.0.1"}, time.Second)
	var out map[string]any
	err := c.GetJSON(context.Background(), srv.URL+"/x", &out)

	if err == nil {
		t.Fatal("expected a decode error")
	}
	assertNoSecrets(t, err.Error(), "4111111111111111", "SYNTHSESSION")
}
