package assertions

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/shakerShekhYousef/flowguard/internal/redact"
)

// decode parses JSON the way the observer does: numbers stay json.Number.
func decode(t *testing.T, s string) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("bad test JSON %q: %v", s, err)
	}
	return v
}

const order = `{
  "status": "paid",
  "charges": 1,
  "total": 99.5,
  "refunded": false,
  "note": null,
  "customer": {"id": "customer_123", "address": {"city": "Dubai"}},
  "items": [{"sku": "A1", "quantity": 2}, {"sku": "B2", "quantity": 1}],
  "tags": ["new", "vip"],
  "big": 9007199254740993,
  "ratio": 0.1,
  "count_str": "1"
}`

func one(t *testing.T, path string, want any, observed string) Result {
	t.Helper()
	r := EvaluateState(map[string]any{path: want}, decode(t, observed))
	if len(r) != 1 {
		t.Fatalf("want 1 result, got %d", len(r))
	}
	return r[0]
}

func TestEvaluateState_Matches(t *testing.T) {
	cases := []struct {
		path string
		want any
	}{
		{"status", "paid"},
		{"charges", 1},
		{"total", 99.5},
		{"refunded", false},
		{"note", nil},
		{"customer.id", "customer_123"},
		{"customer.address.city", "Dubai"},
		{"items.0.quantity", 2},
		{"items.1.sku", "B2"},
		{"tags.1", "vip"},
		{"count_str", "1"},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := one(t, tc.path, tc.want, order)
			if !r.Passed || r.Reason != "" || r.Rule != "state."+tc.path {
				t.Fatalf("want PASS for %s=%v, got %+v", tc.path, tc.want, r)
			}
		})
	}
}

func TestEvaluateState_NumericEquivalence(t *testing.T) {
	cases := []struct {
		name     string
		want     any
		observed string
		equal    bool
	}{
		{"int vs 1.0", 1, `{"v": 1.0}`, true},
		{"int vs 1e0", 1, `{"v": 1e0}`, true},
		{"int vs 1.00", 1, `{"v": 1.00}`, true},
		{"float 1.0 vs int", 1.0, `{"v": 1}`, true},
		{"100 vs 1e2", 100, `{"v": 1e2}`, true},
		{"0.1 vs 0.1 (no float error)", 0.1, `{"v": 0.1}`, true},
		{"0.3 vs 0.30", 0.3, `{"v": 0.30}`, true},
		{"negative", -5, `{"v": -5.0}`, true},
		{"zero vs -0", 0, `{"v": -0}`, true},
		{"large int beyond float64 precision", int64(9007199254740993), `{"v": 9007199254740993}`, true},
		{"large int off by one", int64(9007199254740992), `{"v": 9007199254740993}`, false},
		{"uint64 max", uint64(18446744073709551615), `{"v": 18446744073709551615}`, true},
		{"1 vs 2", 1, `{"v": 2}`, false},
		{"1 vs 1.5", 1, `{"v": 1.5}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if r := one(t, "v", tc.want, tc.observed); r.Passed != tc.equal {
				t.Fatalf("Passed = %v, want %v (%+v)", r.Passed, tc.equal, r)
			}
		})
	}
}

func TestEvaluateState_Mismatches(t *testing.T) {
	cases := []struct {
		name, path string
		want       any
		reason     string
	}{
		{"number", "charges", 3, "state.charges: expected 3, got 1"},
		{"string", "status", "pending", `state.status: expected "pending", got "paid"`},
		{"boolean", "refunded", true, "state.refunded: expected true, got false"},
		{"number vs numeric string", "count_str", 1, `state.count_str: expected 1 (number), got "1" (string)`},
		{"string vs number", "charges", "1", `state.charges: expected "1" (string), got 1 (number)`},
		{"bool vs string", "status", true, `state.status: expected true (boolean), got "paid" (string)`},
		{"null vs value", "status", nil, `state.status: expected null (null), got "paid" (string)`},
		{"value vs null", "note", "", `state.note: expected "" (string), got null (null)`},
		{"zero vs null", "note", 0, "state.note: expected 0 (number), got null (null)"},
		{"false vs null", "note", false, "state.note: expected false (boolean), got null (null)"},
		{"scalar vs object", "customer", "x", `state.customer: expected "x" (string), got <object> (object)`},
		{"scalar vs array", "tags", "new", `state.tags: expected "new" (string), got <array> (array)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := one(t, tc.path, tc.want, order)
			if r.Passed || r.Reason != tc.reason {
				t.Fatalf("want FAIL %q, got %+v", tc.reason, r)
			}
		})
	}
}

func TestEvaluateState_MissingPathsFailExplicitly(t *testing.T) {
	cases := []struct {
		name, path string
		want       any
		reason     string
	}{
		{"missing key", "customer.email", "", `state.customer.email: expected "", but path not found: no key "email" in object at "customer"`},
		{"missing top-level as zero", "discount", 0, `state.discount: expected 0, but path not found: no key "discount" in object at the top level`},
		{"missing as false", "shipped", false, `state.shipped: expected false, but path not found: no key "shipped" in object at the top level`},
		{"missing as null", "deleted_at", nil, `state.deleted_at: expected null, but path not found: no key "deleted_at" in object at the top level`},
		{"index out of range", "items.5.sku", "A1", `state.items.5.sku: expected "A1", but path not found: index 5 out of range (length 2) at "items"`},
		{"negative index", "items.-1.sku", "A1", `state.items.-1.sku: expected "A1", but path not found: "-1" is not an array index at "items"`},
		{"non-numeric index", "items.first.sku", "A1", `state.items.first.sku: expected "A1", but path not found: "first" is not an array index at "items"`},
		{"padded index", "items.01.sku", "B2", `state.items.01.sku: expected "B2", but path not found: "01" is not an array index at "items"`},
		{"through a scalar", "status.code", "x", `state.status.code: expected "x", but path not found: cannot look up "code" in string at "status"`},
		{"through null", "note.text", "x", `state.note.text: expected "x", but path not found: cannot look up "text" in null at "note"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := one(t, tc.path, tc.want, order)
			if r.Passed {
				t.Fatalf("a missing path must never pass: %+v", r)
			}
			if r.Reason != tc.reason {
				t.Fatalf("Reason = %q\n want %q", r.Reason, tc.reason)
			}
			if r.Actual != nil {
				t.Fatalf("Actual should be empty for a missing path, got %v", r.Actual)
			}
		})
	}
}

func TestEvaluateState_TopLevelArray(t *testing.T) {
	r := EvaluateState(map[string]any{"0.id": "a", "1.id": "b"}, decode(t, `[{"id":"a"},{"id":"b"}]`))
	if !AllPassed(r) {
		t.Fatalf("top-level arrays should be indexable: %+v", r)
	}
}

func TestEvaluateState_OneResultPerPathSorted(t *testing.T) {
	r := EvaluateState(map[string]any{"status": "paid", "charges": 1, "customer.id": "customer_123", "items.0.quantity": 2}, decode(t, order))
	var rules []string
	for _, x := range r {
		rules = append(rules, x.Rule)
	}
	want := []string{"state.charges", "state.customer.id", "state.items.0.quantity", "state.status"}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %v, want %v", rules, want)
	}
	for i := 0; i < 20; i++ { // map order must not leak into results
		again := EvaluateState(map[string]any{"status": "paid", "charges": 1, "customer.id": "customer_123", "items.0.quantity": 2}, decode(t, order))
		if !reflect.DeepEqual(again, r) {
			t.Fatal("results are not deterministic")
		}
	}
}

func TestEvaluateState_ReportsOnlyAssertedFields(t *testing.T) {
	observed := decode(t, `{"status":"pending","password":"pw_SYNTH","internal":{"token":"tok_SYNTH"},"card":"4111111111111111"}`)
	r := EvaluateState(map[string]any{"status": "paid"}, observed)

	blob, _ := json.Marshal(r)
	for _, secret := range []string{"pw_SYNTH", "tok_SYNTH", "4111111111111111", "internal", "card"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("result leaks unasserted field data %q: %s", secret, blob)
		}
	}
}

func TestEvaluateState_RedactsSensitiveValues(t *testing.T) {
	observed := decode(t, `{
	  "customer": {"api_key": "sk_live_SYNTH", "name": "Ana"},
	  "auth": {"refresh_token": "rt_SYNTH"},
	  "webhook_signature": "sha256=SYNTH",
	  "callback": "https://user:pw_SYNTH@example.com/cb?token=tok_SYNTH",
	  "header": "Authorization: Bearer eyJSYNTH"
	}`)

	cases := []struct {
		path string
		want any
	}{
		{"customer.api_key", "sk_live_WRONG"},        // sensitive leaf
		{"auth.refresh_token", "rt_WRONG"},           // sensitive leaf
		{"webhook_signature", "sha256=WRONG"},        // sensitive leaf
		{"callback", "https://example.com/other"},    // credentials inside a URL value
		{"header", "Authorization: Bearer eyJWRONG"}, // bearer token inside a value
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := EvaluateState(map[string]any{tc.path: tc.want}, observed)[0]
			if r.Passed {
				t.Fatalf("test setup: expected a mismatch")
			}
			blob, _ := json.Marshal(r)
			for _, secret := range []string{"sk_live_SYNTH", "rt_SYNTH", "sha256=SYNTH", "pw_SYNTH", "tok_SYNTH", "eyJSYNTH", "sk_live_WRONG", "rt_WRONG", "sha256=WRONG", "eyJWRONG"} {
				if strings.Contains(string(blob), secret) {
					t.Fatalf("result leaks %q: %s", secret, blob)
				}
			}
			if !strings.Contains(r.Reason, "REDACTED") {
				t.Fatalf("reason should show REDACTED: %q", r.Reason)
			}
		})
	}

	t.Run("comparison still uses the raw value", func(t *testing.T) {
		r := EvaluateState(map[string]any{"customer.api_key": "sk_live_SYNTH"}, observed)[0]
		if !r.Passed {
			t.Fatalf("a matching secret must still pass: %+v", r)
		}
		if r.Expected != "REDACTED" || r.Actual != "REDACTED" {
			t.Fatalf("expected/actual must be redacted even on PASS: %+v", r)
		}
	})

	t.Run("non-sensitive values are shown", func(t *testing.T) {
		r := EvaluateState(map[string]any{"customer.name": "Bob"}, observed)[0]
		if r.Reason != `state.customer.name: expected "Bob", got "Ana"` {
			t.Fatalf("Reason = %q", r.Reason)
		}
	})
}

func TestEvaluateState_TruncatesDisplayedValues(t *testing.T) {
	long := strings.Repeat("é", 300) // multi-byte, to check runes not bytes
	observed := decode(t, `{"note":"`+long+`"}`)

	r := EvaluateState(map[string]any{"note": strings.Repeat("x", 500)}, observed)[0]

	for name, v := range map[string]any{"Actual": r.Actual, "Expected": r.Expected} {
		s, _ := v.(string)
		if n := utf8.RuneCountInString(s); n != maxDisplayRunes || !strings.HasSuffix(s, "…") {
			t.Fatalf("%s has %d runes (suffix %q), want %d ending in …", name, n, s[len(s)-3:], maxDisplayRunes)
		}
		if !utf8.ValidString(s) {
			t.Fatalf("%s was cut inside a rune", name)
		}
	}
	if strings.Contains(r.Reason, long) || strings.Contains(r.Reason, strings.Repeat("x", 500)) {
		t.Fatal("reason contains an untruncated value")
	}

	short := EvaluateState(map[string]any{"note": "abc"}, decode(t, `{"note":"`+strings.Repeat("y", maxDisplayRunes)+`"}`))[0]
	if s := short.Actual.(string); s != strings.Repeat("y", maxDisplayRunes) {
		t.Fatalf("a value of exactly %d runes must not be truncated", maxDisplayRunes)
	}
}

func TestEvaluateState_ReportTypes(t *testing.T) {
	r := EvaluateState(map[string]any{"charges": 3, "refunded": true, "note": "x", "customer": "x"}, decode(t, order))
	got := map[string]any{}
	for _, x := range r {
		got[x.Rule] = x.Actual
	}

	if n, ok := got["state.charges"].(json.Number); !ok || n.String() != "1" {
		t.Fatalf("numbers should stay JSON numbers, got %#v", got["state.charges"])
	}
	if got["state.refunded"] != false {
		t.Fatalf("booleans should stay booleans, got %#v", got["state.refunded"])
	}
	if got["state.note"] != nil {
		t.Fatalf("null should stay null, got %#v", got["state.note"])
	}
	if got["state.customer"] != "<object>" {
		t.Fatalf("objects should be summarized, got %#v", got["state.customer"])
	}
}

// TestEvaluateState_ExactExpectedNumbers uses json.Number expected values,
// the form config produces for every non-integer YAML number.
func TestEvaluateState_ExactExpectedNumbers(t *testing.T) {
	cases := []struct {
		name, want, observed string
		equal                bool
	}{
		{"T10: large integer vs its rounded value", "12345678901234567890123", "12345678901234568000000", false},
		{"T10: precise fraction vs 0.1", "0.10000000000000001", "0.1", false},
		{"large integer exact", "12345678901234567890123", "12345678901234567890123", true},
		{"precise fraction exact", "0.10000000000000001", "0.100000000000000010", true},
		{"1 vs 1.0", "1", "1.0", true},
		{"negative", "-12.5", "-1.25e1", true},
		{"negative vs positive", "-12.5", "12.5", false},
		{"scientific", "1000", "1E3", true},
		{"tiny difference", "1.0000000000000000000001", "1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := one(t, "v", json.Number(tc.want), `{"v": `+tc.observed+`}`)
			if r.Passed != tc.equal {
				t.Fatalf("Passed = %v, want %v (%+v)", r.Passed, tc.equal, r)
			}
			if r.Expected != json.Number(tc.want) {
				t.Fatalf("Expected must be reported exactly, got %#v", r.Expected)
			}
		})
	}

	t.Run("reason shows the exact values", func(t *testing.T) {
		r := one(t, "v", json.Number("12345678901234567890123"), `{"v": 12345678901234568000000}`)
		if want := "state.v: expected 12345678901234567890123, got 12345678901234568000000"; r.Reason != want {
			t.Fatalf("Reason = %q, want %q", r.Reason, want)
		}
	})
}

// TestEvaluateState_SensitivePathReasonsSurviveRedaction checks the reason
// as the terminal and report show it: after redact.Text.
func TestEvaluateState_SensitivePathReasonsSurviveRedaction(t *testing.T) {
	observed := decode(t, `{"api_token": "tok_ACTUAL_SYNTH", "auth": {"session": {"id": 7}}, "password": "pw_SYNTH"}`)
	cases := []struct {
		path string
		want any
		text string
	}{
		{"api_token", "tok_EXPECTED_SYNTH", "state.api_token (sensitive path, values redacted): expected REDACTED, got REDACTED"},
		{"auth.session.id", 8, "state.auth.session.id (sensitive path, values redacted): expected REDACTED, got REDACTED"},
		// A type mismatch must not reveal the hidden values' types.
		{"password", 1, "state.password (sensitive path, values redacted): expected REDACTED, got REDACTED"},
		{"refresh_token", "x", `state.refresh_token (sensitive path, values redacted): expected REDACTED, but path not found: no key "refresh_token" in object at the top level`},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			r := EvaluateState(map[string]any{tc.path: tc.want}, observed)[0]
			if r.Passed {
				t.Fatal("test setup: expected a mismatch")
			}
			if r.Reason != tc.text {
				t.Fatalf("Reason = %q, want %q", r.Reason, tc.text)
			}
			if shown := redact.Text(r.Reason); shown != tc.text {
				t.Fatalf("redaction changes the reason to %q", shown)
			}
			for _, secret := range []string{"tok_ACTUAL_SYNTH", "tok_EXPECTED_SYNTH", "pw_SYNTH"} {
				if strings.Contains(r.Reason, secret) {
					t.Fatalf("reason leaks %q", secret)
				}
			}
		})
	}
}
