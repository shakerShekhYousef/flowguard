// Package runner executes HTTP steps against an allow-listed target,
// enforcing timeouts and recording status, body and latency (FR-02).
package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shakeryoussef/flowguard/internal/config"
)

const maxBodyBytes = 1 << 20 // 1 MiB; bound response reads defensively.

type StepResult struct {
	Method   string
	URL      string
	Status   int
	Body     []byte
	Duration time.Duration
	Err      error
}

// Client sends requests only to hosts present in AllowedHosts (fail closed),
// matching the target safety policy.
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

	if !c.AllowedHosts[strings.ToLower(full.Hostname())] {
		return StepResult{Method: req.Method, URL: full.String(), Err: fmt.Errorf("host %q is not in allowed_hosts", full.Hostname())}
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
	resp, err := c.HTTPClient.Do(httpReq)
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
	if !c.AllowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("host %q is not in allowed_hosts", u.Hostname())
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from %s: %s", resp.StatusCode, u.String(), string(body))
	}
	return json.Unmarshal(body, out)
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
