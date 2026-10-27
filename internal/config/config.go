// Package config loads and validates flowguard.yaml files (FR-01).
package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Version   int        `yaml:"version"`
	Target    Target     `yaml:"target"`
	Scenarios []Scenario `yaml:"scenarios"`
}

type Target struct {
	BaseURL      string   `yaml:"base_url"`
	AllowedHosts []string `yaml:"allowed_hosts"`
}

type Scenario struct {
	Name    string     `yaml:"name"`
	Kind    string     `yaml:"kind"`
	Request Request    `yaml:"request"`
	Repeat  int        `yaml:"repeat"`
	Assert  AssertRule `yaml:"assert"`
}

type Request struct {
	Method  string            `yaml:"method"`
	Path    string            `yaml:"path"`
	Headers map[string]string `yaml:"headers"`
	JSON    map[string]any    `yaml:"json"`
}

type AssertRule struct {
	Status              *int `yaml:"status"`
	BusinessEffectCount *int `yaml:"business_effect_count"`
}

// supportedKinds lists scenario kinds this version of FlowGuard can execute.
// "idempotency" ships in v0.1; webhook-fault kinds arrive in v0.2 (FR-08).
var supportedKinds = map[string]bool{
	"idempotency": true,
}

// Load reads, parses and validates a flowguard.yaml file, returning a
// specific, actionable error for malformed or incomplete input (FR-01).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %q: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
	}

	return &cfg, nil
}

func (c *Config) validate() error {
	if c.Version != 1 {
		return fmt.Errorf("version: expected 1, got %d", c.Version)
	}

	if err := c.Target.validate(); err != nil {
		return fmt.Errorf("target: %w", err)
	}

	if len(c.Scenarios) == 0 {
		return fmt.Errorf("scenarios: at least one scenario is required")
	}

	seen := map[string]bool{}
	for i, s := range c.Scenarios {
		if err := s.validate(); err != nil {
			return fmt.Errorf("scenarios[%d]: %w", i, err)
		}
		if seen[s.Name] {
			return fmt.Errorf("scenarios[%d]: duplicate scenario name %q", i, s.Name)
		}
		seen[s.Name] = true
	}

	return nil
}

func (t *Target) validate() error {
	if t.BaseURL == "" {
		return fmt.Errorf("base_url is required")
	}
	u, err := url.Parse(t.BaseURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("base_url %q is not a valid absolute URL", t.BaseURL)
	}
	if len(t.AllowedHosts) == 0 {
		return fmt.Errorf("allowed_hosts must list at least one host (fail closed by default)")
	}
	hostname := u.Hostname()
	allowed := false
	for _, h := range t.AllowedHosts {
		if strings.EqualFold(h, hostname) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("base_url host %q is not in allowed_hosts %v", hostname, t.AllowedHosts)
	}
	return nil
}

func (s *Scenario) validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	if !supportedKinds[s.Kind] {
		return fmt.Errorf("kind %q is not supported (supported: idempotency)", s.Kind)
	}
	if s.Request.Method == "" {
		return fmt.Errorf("request.method is required")
	}
	if s.Request.Path == "" {
		return fmt.Errorf("request.path is required")
	}
	if s.Repeat < 1 {
		return fmt.Errorf("repeat must be >= 1, got %d", s.Repeat)
	}
	if s.Assert.Status == nil && s.Assert.BusinessEffectCount == nil {
		return fmt.Errorf("assert: at least one of status or business_effect_count is required")
	}
	return nil
}
