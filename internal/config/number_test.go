package config

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/shakerShekhYousef/flowguard/internal/assertions"
)

// loadState loads the valid webhook scenario with assert.state set to the
// YAML mapping body (e.g. "{v: 1.5}") and returns the decoded state.
func loadState(t *testing.T, state string) (StateAssertions, error) {
	t.Helper()
	cfg, err := Load(writeTemp(t, webhookWith(t, "      state:\n        status: paid\n        charges: 1\n", "      state: "+state+"\n")))
	if err != nil {
		return nil, err
	}
	return cfg.Scenarios[0].Assert.State, nil
}

func TestLoad_StateNumbersAreExact(t *testing.T) {
	cases := []struct {
		yaml string
		want any
	}{
		// The two values T10 showed being rounded into false PASS results.
		{"12345678901234567890123", json.Number("12345678901234567890123")},
		{"0.10000000000000001", json.Number("0.10000000000000001")},
		// Fractions and exponents, in canonical decimal form.
		{"1.0", json.Number("1")},
		{"-2.50", json.Number("-2.5")},
		{"1e3", json.Number("1000")},
		{"1.5E-3", json.Number("0.0015")},
		{"-1.5e+2", json.Number("-150")},
		{".5", json.Number("0.5")},
		{"-.5", json.Number("-0.5")},
		{"+1.5", json.Number("1.5")},
		{"5.", json.Number("5")},
		{"1_000.25", json.Number("1000.25")},
		{"-0.0", json.Number("0")},
		{"0.000000000000000000000000000001", json.Number("0.000000000000000000000000000001")},
		{"-99999999999999999999999.99999999999999999999", json.Number("-99999999999999999999999.99999999999999999999")},
		// yaml.v3 would turn these into the string "1e400"; unquoted, they are numbers.
		{"1e400", json.Number("1" + strings.Repeat("0", 400))},
		{"-1e-400", json.Number("-0." + strings.Repeat("0", 399) + "1")},
		// Integers that fit in 64 bits were already exact and keep their types.
		{"42", 42},
		{"-7", -7},
		{"9223372036854775807", 9223372036854775807},
		{"-9223372036854775808", -9223372036854775808},
		{"18446744073709551615", uint64(18446744073709551615)},
		{"0x1F", 31},
		{"0o17", 15},
		{"010", 8}, // YAML 1.1 octal, as documented: quote it to mean "010"
		{"08", json.Number("8")},
		// Quoted or tagged values stay strings; other scalars keep their types.
		{`"1.5"`, "1.5"},
		{`'1e400'`, "1e400"},
		{`!!str 0.1`, "0.1"},
		{"1.2.3", "1.2.3"},
		{"true", true},
		{"null", nil},
		{"paid", "paid"},
	}
	for _, tc := range cases {
		t.Run(tc.yaml, func(t *testing.T) {
			state, err := loadState(t, "{v: "+tc.yaml+"}")
			if err != nil {
				t.Fatal(err)
			}
			if got := state["v"]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("v = %#v (%T), want %#v (%T)", got, got, tc.want, tc.want)
			}
		})
	}
}

func TestLoad_StateRejectsNumbersItCannotCompareExactly(t *testing.T) {
	cases := []struct{ name, yaml, want string }{
		{"huge exponent", "1e1001", "has an exponent outside ±1000"},
		{"tiny exponent", "1e-1001", "has an exponent outside ±1000"},
		{"exponent overflowing int", "1e99999999999999999999", "has an exponent outside ±1000"},
		{"too many digits", strings.Repeat("9", 2001), "has more than 2000 digits"},
		{"too many fraction digits", "0." + strings.Repeat("1", 2000), "has more than 2000 digits"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadState(t, "{v: "+tc.yaml+"}")
			if err == nil || !strings.Contains(err.Error(), "assert.state.v: this number "+tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}

	t.Run("the largest accepted values still load", func(t *testing.T) {
		if _, err := loadState(t, "{a: 1e1000, b: -1e-1000, c: "+strings.Repeat("9", 2000)+", d: 0."+strings.Repeat("1", 1999)+"}"); err != nil {
			t.Fatal(err)
		}
	})
}

// TestLoad_StateNumbersCompareExactly runs YAML through Load and the state
// comparison together: different exact values never pass because one of
// them was rounded.
func TestLoad_StateNumbersCompareExactly(t *testing.T) {
	cases := []struct {
		name, yaml, observed string
		pass                 bool
	}{
		{"T10 large integer: rounded actual", "12345678901234567890123", "12345678901234568000000", false},
		{"T10 large integer: exact actual", "12345678901234567890123", "12345678901234567890123", true},
		{"T10 precise fraction vs 0.1", "0.10000000000000001", "0.1", false},
		{"T10 precise fraction exact", "0.10000000000000001", "0.10000000000000001", true},
		{"0.1 vs 0.1", "0.1", "0.1", true},
		{"0.1 vs its float64 expansion", "0.1", "0.1000000000000000055511151231257827", false},
		{"1 vs 1.0", "1", "1.0", true},
		{"1.0 vs 1", "1.0", "1", true},
		{"1e3 vs 1000", "1e3", "1000", true},
		{"1000 vs 1e3", "1000", "1E+3", true},
		{"negative fraction", "-0.5", "-5e-1", true},
		{"negative vs positive", "-0.5", "0.5", false},
		{"zero vs negative zero", "0", "-0.0", true},
		{"2^53+1 exact", "9007199254740993", "9007199254740993", true},
		{"2^53+1 vs 2^53", "9007199254740993", "9007199254740992", false},
		{"beyond uint64", "18446744073709551617", "18446744073709551616", false},
		{"1e400 exact", "1e400", "1e400", true},
		{"1e400 vs 1e399", "1e400", "1e399", false},
		{"tiny exponent", "1e-400", "0", false},
		{"number vs numeric string", "1.5", `"1.5"`, false},
		{"quoted number vs number", `"1.5"`, "1.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			state, err := loadState(t, "{v: "+tc.yaml+"}")
			if err != nil {
				t.Fatal(err)
			}
			dec := json.NewDecoder(bytes.NewReader([]byte(`{"v": ` + tc.observed + `}`)))
			dec.UseNumber()
			var observed any
			if err := dec.Decode(&observed); err != nil {
				t.Fatal(err)
			}
			r := assertions.EvaluateState(state, observed)[0]
			if r.Passed != tc.pass {
				t.Fatalf("expected %s vs observed %s: Passed = %v, want %v (%+v)", tc.yaml, tc.observed, r.Passed, tc.pass, r)
			}
		})
	}
}

func TestLoad_StateKeepsNullAndDuplicateRules(t *testing.T) {
	t.Run("state: null is the same as no state", func(t *testing.T) {
		_, err := loadState(t, "null")
		if err == nil || !strings.Contains(err.Error(), "observe and assert.state must be given together") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("duplicate paths are rejected", func(t *testing.T) {
		_, err := loadState(t, "{v: 1, v: 2}")
		if err == nil || !strings.Contains(err.Error(), `mapping key "v" already defined`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a non-mapping state is rejected", func(t *testing.T) {
		if _, err := loadState(t, "[1, 2]"); err == nil {
			t.Fatal("want an error for a list")
		}
	})
}
