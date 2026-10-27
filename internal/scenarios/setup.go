package scenarios

import (
	"context"
	"fmt"

	"github.com/shakerShekhYousef/flowguard/internal/config"
	"github.com/shakerShekhYousef/flowguard/internal/redact"
	"github.com/shakerShekhYousef/flowguard/internal/runner"
)

// runSetup sends a scenario's declared setup requests in order. Any
// transport error or non-2xx status aborts the scenario as a runtime error:
// asserting against a target in an unknown state would be misleading.
func runSetup(ctx context.Context, client *runner.Client, baseURL string, setup []config.Request) error {
	for i, req := range setup {
		sr := client.Do(ctx, baseURL, req)
		if sr.Err != nil {
			return fmt.Errorf("setup[%d]: %w", i, sr.Err)
		}
		if sr.Status < 200 || sr.Status > 299 {
			return fmt.Errorf("setup[%d]: %s %s returned status %d, want 2xx", i, req.Method, redact.URL(req.Path), sr.Status)
		}
	}
	return nil
}
