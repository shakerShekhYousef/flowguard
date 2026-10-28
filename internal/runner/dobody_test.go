package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/config"
)

// captured is what a capture server saw for one request.
type captured struct {
	Method      string
	Path        string
	Body        []byte
	ContentType string
	HasType     bool
	Length      int64
	Header      http.Header
}

// captureServer records every request it receives and answers 200.
func captureServer(t *testing.T) (*httptest.Server, func() []captured) {
	t.Helper()
	var mu sync.Mutex
	var seen []captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, hasType := r.Header["Content-Type"]
		mu.Lock()
		seen = append(seen, captured{
			Method: r.Method, Path: r.URL.Path, Body: b,
			ContentType: r.Header.Get("Content-Type"), HasType: hasType,
			Length: r.ContentLength, Header: r.Header.Clone(),
		})
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() []captured {
		mu.Lock()
		defer mu.Unlock()
		return append([]captured(nil), seen...)
	}
}

func newClient() *Client { return New([]string{"127.0.0.1"}, time.Second) }

func TestDoBody_SendsExactBytes(t *testing.T) {
	bodies := map[string][]byte{
		"non-canonical json": []byte("{ \"b\": 1,\n  \"a\" : [2, 1] }\n"),
		"unicode":            []byte(`{"name":"Zoë 🚀","note":"é"}`),
		"not json at all":    []byte("plain=text&x=1"),
		"invalid utf-8":      {0xff, 0xfe, 0x00, '{', '}'},
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			srv, seen := captureServer(t)
			want := bytes.Clone(body)

			res := newClient().DoBody(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/webhooks"}, body)

			if res.Err != nil || res.Status != http.StatusOK {
				t.Fatalf("unexpected result: status=%d err=%v", res.Status, res.Err)
			}
			got := seen()
			if len(got) != 1 {
				t.Fatalf("expected 1 request, got %d", len(got))
			}
			if !bytes.Equal(got[0].Body, want) {
				t.Fatalf("body bytes changed in transit:\n got %q\nwant %q", got[0].Body, want)
			}
			if got[0].Length != int64(len(want)) {
				t.Fatalf("Content-Length = %d, want %d", got[0].Length, len(want))
			}
			if got[0].ContentType != "application/json" {
				t.Fatalf("default Content-Type = %q, want application/json", got[0].ContentType)
			}
		})
	}
}

func TestDoBody_DoesNotMutateInputs(t *testing.T) {
	srv, _ := captureServer(t)
	body := []byte(`{"id":"evt_1"}`)
	bodyCopy := bytes.Clone(body)
	req := config.Request{
		Method:  http.MethodPost,
		Path:    "/webhooks",
		Headers: map[string]string{"X-Event-Id": "evt_1"},
		JSON:    map[string]any{"ignored": true},
	}
	reqCopy := config.Request{
		Method:  req.Method,
		Path:    req.Path,
		Headers: map[string]string{"X-Event-Id": "evt_1"},
		JSON:    map[string]any{"ignored": true},
	}

	newClient().DoBody(context.Background(), srv.URL, req, body)

	if !bytes.Equal(body, bodyCopy) {
		t.Fatalf("body was modified: %q", body)
	}
	if !reflect.DeepEqual(req, reqCopy) {
		t.Fatalf("request was modified:\n got %+v\nwant %+v", req, reqCopy)
	}
}

func TestDoBody_IgnoresRequestJSON(t *testing.T) {
	srv, seen := captureServer(t)
	req := config.Request{Method: http.MethodPost, Path: "/webhooks", JSON: map[string]any{"from": "json"}}

	newClient().DoBody(context.Background(), srv.URL, req, []byte(`{"from":"body"}`))

	if got := string(seen()[0].Body); got != `{"from":"body"}` {
		t.Fatalf("DoBody must send body, not req.JSON; got %q", got)
	}
}

func TestDoBody_NilAndEmptyBody(t *testing.T) {
	for name, body := range map[string][]byte{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			srv, seen := captureServer(t)

			res := newClient().DoBody(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/x"}, body)

			if res.Err != nil {
				t.Fatalf("unexpected error: %v", res.Err)
			}
			got := seen()[0]
			if len(got.Body) != 0 || got.Length != 0 {
				t.Fatalf("expected no body, got %q (Content-Length %d)", got.Body, got.Length)
			}
			if got.HasType {
				t.Fatalf("no Content-Type expected without a body, got %q", got.ContentType)
			}
		})
	}
}

func TestDoBody_CustomHeaders(t *testing.T) {
	srv, seen := captureServer(t)
	req := config.Request{
		Method: http.MethodPost,
		Path:   "/webhooks",
		Headers: map[string]string{
			"content-type":   "application/cloudevents+json", // lower case on purpose
			"X-Event-Id":     "evt_1",
			"X-Webhook-Sig":  "sha256=abc",
			"User-Agent":     "flowguard-test",
			"X-Empty-Header": "",
		},
	}

	newClient().DoBody(context.Background(), srv.URL, req, []byte(`{"a":1}`))

	got := seen()[0]
	if got.ContentType != "application/cloudevents+json" {
		t.Fatalf("a Content-Type header must override the default, got %q", got.ContentType)
	}
	if n := len(got.Header.Values("Content-Type")); n != 1 {
		t.Fatalf("Content-Type sent %d times, want once", n)
	}
	for k, want := range map[string]string{"X-Event-Id": "evt_1", "X-Webhook-Sig": "sha256=abc", "User-Agent": "flowguard-test"} {
		if v := got.Header.Get(k); v != want {
			t.Errorf("header %s = %q, want %q", k, v, want)
		}
	}
	if _, ok := got.Header["X-Empty-Header"]; !ok {
		t.Errorf("empty-valued header should still be sent")
	}
}

func TestDoBody_RedirectResendsExactBytes(t *testing.T) {
	for _, code := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			var mu sync.Mutex
			var finalBody []byte
			var finalMethod string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/final" {
					b, _ := io.ReadAll(r.Body)
					mu.Lock()
					finalBody, finalMethod = b, r.Method
					mu.Unlock()
					return
				}
				http.Redirect(w, r, "/final", code)
			}))
			defer srv.Close()

			body := []byte("{ \"exact\" : true }")
			res := newClient().DoBody(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/start"}, body)

			if res.Err != nil || res.Status != http.StatusOK {
				t.Fatalf("same-host %d redirect should be followed: status=%d err=%v", code, res.Status, res.Err)
			}
			mu.Lock()
			defer mu.Unlock()
			if finalMethod != http.MethodPost || !bytes.Equal(finalBody, body) {
				t.Fatalf("redirected request: method=%s body=%q, want POST %q", finalMethod, finalBody, body)
			}
		})
	}
}

func TestDoBody_BlocksRedirectBeforeSendingBody(t *testing.T) {
	target, hit := outsideServer(t)
	srv := redirectServer(t, func(*http.Request) string { return target + "/steal" })

	body := []byte(`{"card":"4111111111111111"}`)
	res := newClient().DoBody(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/start"}, body)

	if !errors.Is(res.Err, ErrDisallowedTarget) {
		t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
	}
	if hit.Load() {
		t.Fatal("body was sent to a host outside allowed_hosts")
	}
	assertNoSecrets(t, res.Err.Error(), "4111111111111111")
}

func TestDoBody_TargetPolicy(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	req := config.Request{Method: http.MethodPost, Path: "/webhooks"}
	body := []byte(`{"a":1}`)

	t.Run("disallowed host", func(t *testing.T) {
		res := New([]string{"localhost"}, time.Second).DoBody(context.Background(), srv.URL, req, body)
		if !errors.Is(res.Err, ErrDisallowedTarget) {
			t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
		}
	})
	t.Run("non-http scheme", func(t *testing.T) {
		res := newClient().DoBody(context.Background(), "ftp://127.0.0.1", req, body)
		if !errors.Is(res.Err, ErrDisallowedTarget) {
			t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
		}
	})
	t.Run("absolute path to another host", func(t *testing.T) {
		abs := config.Request{Method: http.MethodPost, Path: "http://example.com/webhooks"}
		res := newClient().DoBody(context.Background(), srv.URL, abs, body)
		if !errors.Is(res.Err, ErrDisallowedTarget) {
			t.Fatalf("expected ErrDisallowedTarget, got %v", res.Err)
		}
	})
	if hits.Load() != 0 {
		t.Fatalf("a rejected request reached the server %d times", hits.Load())
	}
}

// slowServer never answers on its own. Handlers return when the client goes
// away or when the test ends, so httptest.Server.Close never has to wait.
func slowServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// net/http only notices a client disconnect after the body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // cleanups run in reverse: this runs first
	return srv, &hits
}

func TestDoBody_ContextCancellation(t *testing.T) {
	req := config.Request{Method: http.MethodPost, Path: "/webhooks"}

	t.Run("already cancelled", func(t *testing.T) {
		srv, hits := slowServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		res := newClient().DoBody(ctx, srv.URL, req, []byte(`{"a":1}`))

		if !errors.Is(res.Err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", res.Err)
		}
		if hits.Load() != 0 {
			t.Fatal("a cancelled request should not reach the server")
		}
	})

	t.Run("cancelled while waiting", func(t *testing.T) {
		srv, _ := slowServer(t)
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(50*time.Millisecond, cancel)

		start := time.Now()
		res := New([]string{"127.0.0.1"}, 10*time.Second).DoBody(ctx, srv.URL, req, []byte(`{"a":1}`))

		if !errors.Is(res.Err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", res.Err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("cancellation took %v", elapsed)
		}
	})

	t.Run("context deadline", func(t *testing.T) {
		srv, _ := slowServer(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		res := newClient().DoBody(ctx, srv.URL, req, []byte(`{"a":1}`))

		if !errors.Is(res.Err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded, got %v", res.Err)
		}
	})
}

func TestDoBody_ClientTimeout(t *testing.T) {
	srv, _ := slowServer(t)

	start := time.Now()
	res := New([]string{"127.0.0.1"}, 100*time.Millisecond).DoBody(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/x"}, []byte(`{}`))

	if res.Err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("client timeout not enforced: took %v", elapsed)
	}
}

func TestDoBody_ErrorsDoNotExposeBodiesOrQueryValues(t *testing.T) {
	body := []byte(`{"password":"pw_SYNTH","card":"4111111111111111"}`)

	t.Run("connection refused", func(t *testing.T) {
		res := newClient().DoBody(context.Background(), "http://"+synthUser+":"+synthPass+"@127.0.0.1:1",
			config.Request{Method: http.MethodPost, Path: "/webhooks?token=" + synthQuerySecret}, body)
		if res.Err == nil {
			t.Fatal("expected a connection error")
		}
		assertNoSecrets(t, res.Err.Error(), "pw_SYNTH", "4111111111111111", synthQuerySecret, synthUser, synthPass)
		if !strings.Contains(res.Err.Error(), "token=REDACTED") {
			t.Fatalf("error lost diagnostic context: %v", res.Err)
		}
	})

	t.Run("response body stays in StepResult only", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(synthBodySecret))
		}))
		defer srv.Close()

		res := newClient().DoBody(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/x"}, body)

		if res.Err != nil || res.Status != http.StatusInternalServerError {
			t.Fatalf("a 500 is a response, not an error: status=%d err=%v", res.Status, res.Err)
		}
		if string(res.Body) != synthBodySecret {
			t.Fatalf("response body should be available in memory to the caller")
		}
	})
}

// Do must keep its v0.1.1 behavior now that it delegates to DoBody.
func TestDo_DelegationPreservesBehavior(t *testing.T) {
	t.Run("sends json.Marshal of req.JSON", func(t *testing.T) {
		srv, seen := captureServer(t)
		payload := map[string]any{"b": 2, "a": map[string]any{"z": true, "y": []any{1, "x"}}}
		want, _ := json.Marshal(payload)

		res := newClient().Do(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/orders", JSON: payload})

		if res.Err != nil {
			t.Fatal(res.Err)
		}
		got := seen()[0]
		if !bytes.Equal(got.Body, want) || got.ContentType != "application/json" {
			t.Fatalf("Do sent %q (%s), want %q (application/json)", got.Body, got.ContentType, want)
		}
	})

	t.Run("matches DoBody with the same encoding", func(t *testing.T) {
		srv, seen := captureServer(t)
		req := config.Request{Method: http.MethodPost, Path: "/orders", Headers: map[string]string{"Idempotency-Key": "k"}, JSON: map[string]any{"amount": 100}}
		encoded, _ := json.Marshal(req.JSON)

		newClient().Do(context.Background(), srv.URL, req)
		newClient().DoBody(context.Background(), srv.URL, req, encoded)

		got := seen()
		if !bytes.Equal(got[0].Body, got[1].Body) || got[0].ContentType != got[1].ContentType || got[0].Header.Get("Idempotency-Key") != got[1].Header.Get("Idempotency-Key") {
			t.Fatalf("Do and DoBody differ:\nDo:     %+v\nDoBody: %+v", got[0], got[1])
		}
	})

	t.Run("nil JSON sends no body", func(t *testing.T) {
		srv, seen := captureServer(t)
		newClient().Do(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/x"})
		if got := seen()[0]; len(got.Body) != 0 || got.HasType {
			t.Fatalf("expected no body and no Content-Type, got %q / %q", got.Body, got.ContentType)
		}
	})

	t.Run("empty JSON object is sent as {}", func(t *testing.T) {
		srv, seen := captureServer(t)
		newClient().Do(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/x", JSON: map[string]any{}})
		if got := seen()[0]; string(got.Body) != "{}" || got.ContentType != "application/json" {
			t.Fatalf("got %q / %q, want {} / application/json", got.Body, got.ContentType)
		}
	})

	t.Run("encoding error keeps URL", func(t *testing.T) {
		srv, seen := captureServer(t)
		res := newClient().Do(context.Background(), srv.URL, config.Request{Method: http.MethodPost, Path: "/x", JSON: map[string]any{"n": math.NaN()}})
		if res.Err == nil || !strings.Contains(res.Err.Error(), "encoding json body") || res.URL != srv.URL+"/x" {
			t.Fatalf("got URL=%q err=%v", res.URL, res.Err)
		}
		if len(seen()) != 0 {
			t.Fatal("nothing should be sent when encoding fails")
		}
	})

	t.Run("policy error takes precedence over encoding error", func(t *testing.T) {
		res := newClient().Do(context.Background(), "http://example.com", config.Request{Method: http.MethodPost, Path: "/x", JSON: map[string]any{"n": math.NaN()}})
		if !errors.Is(res.Err, ErrDisallowedTarget) {
			t.Fatalf("expected ErrDisallowedTarget first, got %v", res.Err)
		}
	})
}

func TestResponseBodyIsCappedAtMaxBodyBytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxBodyBytes+4096))
	}))
	defer srv.Close()
	req := config.Request{Method: http.MethodPost, Path: "/big"}

	for name, res := range map[string]StepResult{
		"DoBody": newClient().DoBody(context.Background(), srv.URL, req, []byte(`{}`)),
		"Do":     newClient().Do(context.Background(), srv.URL, req),
	} {
		if res.Err != nil {
			t.Fatalf("%s: an oversized response is truncated, not an error: %v", name, res.Err)
		}
		if len(res.Body) != maxBodyBytes {
			t.Fatalf("%s: response body = %d bytes, want capped at %d", name, len(res.Body), maxBodyBytes)
		}
	}
}
