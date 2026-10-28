package scenarios

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/redact"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

// observeState sends obs.Request through the runner (allow-list, redirect
// policy, timeouts, cancellation and redaction all apply) and decodes the
// body as a single JSON value with numbers kept exact. A transport error,
// a status other than 200 or an invalid JSON body is an error; the body is
// never included in it.
func observeState(ctx context.Context, client *runner.Client, baseURL string, obs config.Observe) (any, error) {
	sr := client.Do(ctx, baseURL, obs.Request)
	if sr.Err != nil {
		return nil, fmt.Errorf("observe: %w", sr.Err)
	}
	where := obs.Request.Method + " " + redact.URL(obs.Request.Path)
	if sr.Status != http.StatusOK {
		return nil, fmt.Errorf("observe: %s returned status %d, want 200 (%d-byte body not shown)", where, sr.Status, len(sr.Body))
	}

	dec := json.NewDecoder(bytes.NewReader(sr.Body))
	dec.UseNumber()
	var state any
	if err := dec.Decode(&state); err != nil {
		return nil, fmt.Errorf("observe: %s returned invalid JSON (%d bytes, body not shown): %v", where, len(sr.Body), err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("observe: %s returned invalid JSON (%d bytes, body not shown): unexpected data after the first value", where, len(sr.Body))
	}
	return state, nil
}

// checkState reads the scenario's observed state and evaluates assert.state.
// A mismatch is an assertion result; only failing to read state is an error.
// Scenarios without observe have nothing to check.
func checkState(ctx context.Context, client *runner.Client, baseURL string, sc config.Scenario) ([]assertions.Result, error) {
	if sc.Observe == nil {
		return nil, nil
	}
	state, err := observeState(ctx, client, baseURL, *sc.Observe)
	if err != nil {
		return nil, err
	}
	return assertions.EvaluateState(sc.Assert.State, state), nil
}
