// Package config loads and validates flowguard.yaml files (FR-01).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"reflect"
	"regexp"
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
	Name string `yaml:"name"`
	Kind string `yaml:"kind"`
	// Setup requests run, in order, before the scenario itself to put the
	// target in a known state (e.g. a test-only reset endpoint). Optional;
	// FlowGuard never resets anything that is not declared here.
	Setup   []Request  `yaml:"setup"`
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

// Header returns the value of the named request header, matching the name
// case-insensitively as HTTP does.
func (r Request) Header(name string) string {
	for k, v := range r.Headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// IdempotencyKeyHeader is the request header an idempotency scenario must
// set; its value keys the business-effect lookup.
const IdempotencyKeyHeader = "Idempotency-Key"

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

	cfg, err := decodeStrict(data)
	if err != nil {
		return nil, fmt.Errorf("parsing config %q: %w", path, err)
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %q: %w", path, err)
	}

	return cfg, nil
}

// decodeStrict parses data, rejecting unknown fields at every level so a
// typo (e.g. business_effect_cnt) fails loudly instead of silently
// dropping an assertion or setup step.
func decodeStrict(data []byte) (*Config, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("config is empty")
		}
		return nil, explainUnknownFields(err)
	}
	return &cfg, nil
}

// yamlSections maps config types to where they appear in flowguard.yaml.
var yamlSections = map[string]reflect.Type{
	"top level":           reflect.TypeOf(Config{}),
	"target":              reflect.TypeOf(Target{}),
	"scenario":            reflect.TypeOf(Scenario{}),
	"request/setup entry": reflect.TypeOf(Request{}),
	"assert":              reflect.TypeOf(AssertRule{}),
}

var unknownFieldPattern = regexp.MustCompile(`field (\S+) not found in type (\S+)`)

// explainUnknownFields rewrites yaml.v3's "field X not found in type
// config.T" into the YAML section name plus the fields that are valid there.
func explainUnknownFields(err error) error {
	msg := unknownFieldPattern.ReplaceAllStringFunc(err.Error(), func(m string) string {
		parts := unknownFieldPattern.FindStringSubmatch(m)
		field, typeName := parts[1], parts[2]
		for section, t := range yamlSections {
			if t.String() == typeName {
				return fmt.Sprintf("unknown field %q in %s (valid fields: %s)", field, section, strings.Join(yamlFields(t), ", "))
			}
		}
		return m
	})
	if msg == err.Error() {
		return err
	}
	return errors.New(msg)
}

func yamlFields(t reflect.Type) []string {
	var names []string
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ","); name != "" && name != "-" {
			names = append(names, name)
		}
	}
	return names
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
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("base_url scheme %q is not supported (use http or https)", u.Scheme)
	}
	// Request paths are resolved against the host root, so a base path would
	// be silently dropped and the scenario would test a different endpoint.
	// Path joining is not supported in v0.1; reject instead of guessing.
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("base_url path %q is not supported: use base_url %s://%s and put the prefix in each request path (e.g. path: %s/orders)",
			u.Path, u.Scheme, u.Host, strings.TrimRight(u.Path, "/"))
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
	for i, r := range s.Setup {
		if r.Method == "" {
			return fmt.Errorf("setup[%d].method is required", i)
		}
		if r.Path == "" {
			return fmt.Errorf("setup[%d].path is required", i)
		}
	}
	if s.Request.Method == "" {
		return fmt.Errorf("request.method is required")
	}
	if s.Request.Path == "" {
		return fmt.Errorf("request.path is required")
	}
	if s.Kind == "idempotency" && s.Request.Header(IdempotencyKeyHeader) == "" {
		return fmt.Errorf("request.headers.%s is required for idempotency scenarios", IdempotencyKeyHeader)
	}
	if s.Repeat < 1 {
		return fmt.Errorf("repeat must be >= 1, got %d", s.Repeat)
	}
	if s.Assert.Status == nil && s.Assert.BusinessEffectCount == nil {
		return fmt.Errorf("assert: at least one of status or business_effect_count is required")
	}
	return nil
}
