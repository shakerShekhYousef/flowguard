// Package runner executes HTTP steps against an allow-listed target,
// enforcing timeouts and recording status, body and latency (FR-02).
//
// Target policy: every URL the client contacts — the initial request and
// each redirect hop — must use http or https and have a hostname listed in
// AllowedHosts. This is hostname allow-listing, not network-level isolation:
// it does not restrict what IP addresses an allowed hostname resolves to, so
// it does not by itself prevent SSRF via DNS (an allowed name pointing at an
// internal address, or DNS rebinding). Run FlowGuard where its network
// access is already limited to the intended targets.
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/redact"
)

const maxBodyBytes = 1 << 20 // 1 MiB; bound response reads defensively.

// maxRedirects matches net/http's default limit, which a custom
// CheckRedirect replaces.
const maxRedirects = 10

// ErrDisallowedTarget is returned (wrapped) when a request or redirect would
// leave the target policy: a non-http(s) scheme or a host outside
// AllowedHosts.
var ErrDisallowedTarget = errors.New("target not allowed")

type StepResult struct {
	Method   string
	URL      string
	Status   int
	Body     []byte
	Duration time.Duration
	Err      error
}

// Client sends requests only to http(s) URLs whose host is present in
// AllowedHosts (fail closed), including every redirect it follows.
type Client struct {
	HTTPClient   *http.Client
	AllowedHosts map[string]bool
}

func New(allowedHosts []string, timeout time.Duration) *Client {
	allowed := make(map[string]bool, len(allowedHosts))
	for _, h := range allowedHosts {
		allowed[strings.ToLower(h)] = true
	}
	return &Client{
		HTTPClient:   &http.Client{Timeout: timeout},
		AllowedHosts: allowed,
	}
}

// Do sends req against baseURL+path and reports the outcome. A disallowed
// host or network error is captured as a failed StepResult rather than a
// panic, so callers can surface it as a structured failure (FR-02).
func (c *Client) Do(ctx context.Context, baseURL string, req config.Request) StepResult {
	full, err := buildURL(baseURL, req.Path)
	if err != nil {
		return StepResult{Method: req.Method, Err: fmt.Errorf("building request url: %w", err)}
	}

	if err := c.checkURL(full); err != nil {
		return StepResult{Method: req.Method, URL: full.String(), Err: err}
	}

	var body io.Reader
	if req.JSON != nil {
		encoded, err := json.Marshal(req.JSON)
		if err != nil {
			return StepResult{Method: req.Method, URL: full.String(), Err: fmt.Errorf("encoding json body: %w", err)}
		}
		body = bytes.NewReader(encoded)
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, full.String(), body)
	if err != nil {
		return StepResult{Method: req.Method, URL: full.String(), Err: fmt.Errorf("creating request: %w", err)}
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := c.send(httpReq)
	duration := time.Since(start)
	if err != nil {
		return StepResult{Method: req.Method, URL: full.String(), Duration: duration, Err: err}
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return StepResult{Method: req.Method, URL: full.String(), Status: resp.StatusCode, Duration: duration, Err: fmt.Errorf("reading response body: %w", err)}
	}

	return StepResult{
		Method:   req.Method,
		URL:      full.String(),
		Status:   resp.StatusCode,
		Body:     respBody,
		Duration: duration,
	}
}

// GetJSON is a small helper for test-state endpoints (e.g. a mock server's
// business-effect counter) that aren't part of the scenario's own request.
func (c *Client) GetJSON(ctx context.Context, rawURL string, out any) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parsing url: %w", err)
	}
	if err := c.checkURL(u); err != nil {
		return err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.send(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The body may hold sensitive data; report only its size.
		return fmt.Errorf("unexpected status %d from %s (%d-byte body not shown)", resp.StatusCode, redact.URL(u.String()), len(body))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", redact.URL(u.String()), err)
	}
	return nil
}

// checkURL enforces the target policy on a single URL.
func (c *Client) checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not http or https", ErrDisallowedTarget, u.Scheme)
	}
	if !c.AllowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("%w: host %q is not in allowed_hosts", ErrDisallowedTarget, u.Hostname())
	}
	return nil
}

// send executes req with the target policy applied to every redirect. The
// policy is attached per request on a shallow copy, so it holds even if
// HTTPClient was replaced or built without it.
func (c *Client) send(req *http.Request) (*http.Response, error) {
	hc := http.Client{}
	if c.HTTPClient != nil {
		hc = *c.HTTPClient
	}
	hc.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		if err := c.checkURL(next.URL); err != nil {
			return fmt.Errorf("redirect to %s: %w", redact.URL(next.URL.String()), err)
		}
		return nil
	}
	resp, err := hc.Do(req)
	return resp, redactURLError(err)
}

// redactURLError strips credentials and query values from the URL that
// net/http embeds in its errors (it hides only the password). The error
// chain is preserved for errors.Is/As.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = redact.URL(ue.URL)
	}
	return err
}

func buildURL(baseURL, path string) (*url.URL, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	rel, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	return base.ResolveReference(rel), nil
}
