// Package config loads and validates flowguard.yaml files (FR-01).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

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
	Setup []Request `yaml:"setup"`

	// Idempotency fields.
	Request Request `yaml:"request"`
	Repeat  int     `yaml:"repeat"`

	// Webhook fields: FlowGuard simulates a provider delivering Events to
	// Endpoint, then reads business state via Observe.
	Endpoint   *Request   `yaml:"endpoint"`
	Events     []Event    `yaml:"events"`
	Deliveries []Delivery `yaml:"deliveries"`
	Observe    *Observe   `yaml:"observe"`

	Assert AssertRule `yaml:"assert"`
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

// Event is a synthetic webhook event. Its JSON body is sent exactly as
// written; FlowGuard adds no fields.
type Event struct {
	ID      string            `yaml:"id"`
	Headers map[string]string `yaml:"headers"`
	JSON    map[string]any    `yaml:"json"`
}

// Delivery sends one event to the scenario's endpoint.
type Delivery struct {
	Event  string  `yaml:"event"`
	Expect *Expect `yaml:"expect"`
}

// Delivery outcomes.
const (
	OutcomeDelivered = "delivered" // the receiver answered 2xx
	OutcomeRejected  = "rejected"  // the receiver answered any other status
)

// Expect is checked against a delivery's response.
type Expect struct {
	Outcome string `yaml:"outcome"`
	Status  *int   `yaml:"status"`
}

// ExpectedOutcome returns the declared outcome, defaulting to delivered so
// an unexpected rejection can never pass silently.
func (d Delivery) ExpectedOutcome() string {
	if d.Expect == nil || d.Expect.Outcome == "" {
		return OutcomeDelivered
	}
	return d.Expect.Outcome
}

// Observe says how to read the receiver's final business state.
type Observe struct {
	Request Request `yaml:"request"`
}

type AssertRule struct {
	Status              *int `yaml:"status"`
	BusinessEffectCount *int `yaml:"business_effect_count"`
	// State maps dot-paths in the observed JSON (e.g. order.status,
	// charges.0.amount) to expected scalar values. Webhook scenarios only.
	State map[string]any `yaml:"state"`
}

// supportedKinds lists scenario kinds this version of FlowGuard can execute.
var supportedKinds = map[string]bool{
	"idempotency": true,
	"webhook":     true,
}

// Per-scenario limits that keep a webhook run bounded.
const (
	maxEvents     = 100
	maxDeliveries = 100
)

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
	"top level": reflect.TypeOf(Config{}),
	"target":    reflect.TypeOf(Target{}),
	"scenario":  reflect.TypeOf(Scenario{}),
	"request entry (request, setup, endpoint or observe.request)": reflect.TypeOf(Request{}),
	"assert":   reflect.TypeOf(AssertRule{}),
	"event":    reflect.TypeOf(Event{}),
	"delivery": reflect.TypeOf(Delivery{}),
	"expect":   reflect.TypeOf(Expect{}),
	"observe":  reflect.TypeOf(Observe{}),
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
		return fmt.Errorf("kind %q is not supported (supported: %s)", s.Kind, strings.Join(sortedKinds(), ", "))
	}
	for i, r := range s.Setup {
		if r.Method == "" {
			return fmt.Errorf("setup[%d].method is required", i)
		}
		if r.Path == "" {
			return fmt.Errorf("setup[%d].path is required", i)
		}
	}
	if s.Kind == "webhook" {
		return s.validateWebhook()
	}
	return s.validateIdempotency()
}

func sortedKinds() []string {
	kinds := make([]string, 0, len(supportedKinds))
	for k := range supportedKinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	return kinds
}

func (s *Scenario) validateIdempotency() error {
	switch {
	case s.Endpoint != nil:
		return onlyFor("endpoint", "webhook")
	case s.Events != nil:
		return onlyFor("events", "webhook")
	case s.Deliveries != nil:
		return onlyFor("deliveries", "webhook")
	case s.Observe != nil:
		return onlyFor("observe", "webhook")
	case s.Assert.State != nil:
		return onlyFor("assert.state", "webhook")
	}

	if s.Request.Method == "" {
		return fmt.Errorf("request.method is required")
	}
	if s.Request.Path == "" {
		return fmt.Errorf("request.path is required")
	}
	if s.Request.Header(IdempotencyKeyHeader) == "" {
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

func (s *Scenario) validateWebhook() error {
	switch {
	case !reflect.DeepEqual(s.Request, Request{}):
		return fmt.Errorf("request is only valid for idempotency scenarios (webhook scenarios use endpoint, events and deliveries)")
	case s.Repeat != 0:
		return fmt.Errorf("repeat is only valid for idempotency scenarios (repeat a delivery by listing it again)")
	case s.Assert.Status != nil:
		return fmt.Errorf("assert.status is only valid for idempotency scenarios (use deliveries[].expect.status)")
	case s.Assert.BusinessEffectCount != nil:
		return fmt.Errorf("assert.business_effect_count is only valid for idempotency scenarios (use observe and assert.state)")
	}

	if s.Endpoint == nil {
		return fmt.Errorf("endpoint is required")
	}
	if s.Endpoint.Method == "" {
		return fmt.Errorf("endpoint.method is required")
	}
	if s.Endpoint.Path == "" {
		return fmt.Errorf("endpoint.path is required")
	}
	if s.Endpoint.JSON != nil {
		return fmt.Errorf("endpoint.json is not allowed: each delivery sends its event's json")
	}
	if err := validateWebhookHeaders(s.Endpoint.Headers); err != nil {
		return fmt.Errorf("endpoint.headers: %w", err)
	}

	if len(s.Events) == 0 {
		return fmt.Errorf("events: at least one event is required")
	}
	if len(s.Events) > maxEvents {
		return fmt.Errorf("events: at most %d events are allowed, got %d", maxEvents, len(s.Events))
	}
	ids := make(map[string]bool, len(s.Events))
	for i, e := range s.Events {
		if e.ID == "" {
			return fmt.Errorf("events[%d].id is required", i)
		}
		if ids[e.ID] {
			return fmt.Errorf("events[%d]: duplicate event id %q", i, e.ID)
		}
		ids[e.ID] = true
		if e.JSON == nil {
			return fmt.Errorf("events[%d].json is required", i)
		}
		if err := validateWebhookHeaders(e.Headers); err != nil {
			return fmt.Errorf("events[%d].headers: %w", i, err)
		}
	}

	if len(s.Deliveries) == 0 {
		return fmt.Errorf("deliveries: at least one delivery is required")
	}
	if len(s.Deliveries) > maxDeliveries {
		return fmt.Errorf("deliveries: at most %d deliveries are allowed, got %d", maxDeliveries, len(s.Deliveries))
	}
	for i, d := range s.Deliveries {
		if d.Event == "" {
			return fmt.Errorf("deliveries[%d].event is required", i)
		}
		if !ids[d.Event] {
			return fmt.Errorf("deliveries[%d].event %q does not match any declared event id", i, d.Event)
		}
		if err := d.validateExpect(); err != nil {
			return fmt.Errorf("deliveries[%d].expect: %w", i, err)
		}
	}

	if (s.Observe == nil) != (s.Assert.State == nil) {
		return fmt.Errorf("observe and assert.state must be given together (observe says where to read state, assert.state what to expect)")
	}
	if s.Observe != nil {
		if s.Observe.Request.Method == "" {
			return fmt.Errorf("observe.request.method is required")
		}
		// Reading state must not change it, so only GET is allowed.
		if s.Observe.Request.Method != http.MethodGet {
			return fmt.Errorf("observe.request.method %q is not supported: state is read with GET only", s.Observe.Request.Method)
		}
		if s.Observe.Request.Path == "" {
			return fmt.Errorf("observe.request.path is required")
		}
		if len(s.Assert.State) == 0 {
			return fmt.Errorf("assert.state: at least one path is required")
		}
		for _, path := range sortedPaths(s.Assert.State) {
			if err := validateStatePath(path); err != nil {
				return fmt.Errorf("assert.state: %w", err)
			}
			if err := validateStateValue(s.Assert.State[path]); err != nil {
				return fmt.Errorf("assert.state.%s: %w", path, err)
			}
		}
	}
	return nil
}

// transportHeaders are set by net/http from the request itself; a value in
// config would be ignored or would corrupt message framing.
var transportHeaders = map[string]bool{
	"Connection": true, "Content-Length": true, "Host": true, "Keep-Alive": true,
	"Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

// validateWebhookHeaders keeps header merging deterministic: endpoint
// headers are defaults and event headers override them by canonical name,
// so one map must not name the same header twice in different case.
func validateWebhookHeaders(headers map[string]string) error {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)

	seen := make(map[string]string, len(names))
	for _, name := range names {
		canonical := http.CanonicalHeaderKey(name)
		if transportHeaders[canonical] {
			return fmt.Errorf("%q is managed by the HTTP transport and cannot be set", name)
		}
		if prev, ok := seen[canonical]; ok {
			return fmt.Errorf("%q and %q are the same header (names are case-insensitive)", prev, name)
		}
		seen[canonical] = name
	}
	return nil
}

func onlyFor(field, kind string) error {
	return fmt.Errorf("%s is only valid for %s scenarios", field, kind)
}

func (d Delivery) validateExpect() error {
	outcome := d.ExpectedOutcome()
	if outcome != OutcomeDelivered && outcome != OutcomeRejected {
		return fmt.Errorf("outcome %q is not supported (supported: %s, %s)", outcome, OutcomeDelivered, OutcomeRejected)
	}
	if d.Expect == nil || d.Expect.Status == nil {
		return nil
	}
	status := *d.Expect.Status
	if status < 100 || status > 599 {
		return fmt.Errorf("status %d is not a valid HTTP status", status)
	}
	is2xx := status >= 200 && status <= 299
	if outcome == OutcomeDelivered && !is2xx {
		return fmt.Errorf("status %d contradicts outcome %q (delivered means 2xx; use outcome: %s)", status, outcome, OutcomeRejected)
	}
	if outcome == OutcomeRejected && is2xx {
		return fmt.Errorf("status %d contradicts outcome %q (rejected means non-2xx)", status, outcome)
	}
	return nil
}

func sortedPaths(state map[string]any) []string {
	paths := make([]string, 0, len(state))
	for p := range state {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

// validateStatePath accepts dot-paths such as status, order.status or
// charges.0.amount.
func validateStatePath(path string) error {
	for _, seg := range strings.Split(path, ".") {
		if strings.TrimSpace(seg) == "" {
			return fmt.Errorf("path %q has an empty segment", path)
		}
	}
	return nil
}

// validateStateValue requires a JSON scalar so comparisons stay exact.
func validateStateValue(v any) error {
	switch x := v.(type) {
	case nil, string, bool, int, int64, uint64:
		return nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return fmt.Errorf("%v cannot match any JSON value", x)
		}
		return nil
	case time.Time:
		return fmt.Errorf("YAML read this value as a timestamp; quote it to compare as a string")
	default:
		return fmt.Errorf("expected a scalar (string, number, boolean or null), got %T", v)
	}
}
