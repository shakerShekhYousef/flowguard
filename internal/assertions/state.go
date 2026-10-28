package assertions

import (
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/shakerShekhYousef/flowguard/internal/redact"
)

// maxDisplayRunes bounds how much of any expected or observed value is shown
// in terminal output and reports.
const maxDisplayRunes = 200

// EvaluateState checks each expected dot-path against observed, which is
// decoded JSON (ideally with json.Decoder.UseNumber). It returns one result
// per path, sorted by path. Comparison uses the raw values; Expected, Actual
// and Reason hold only redacted, truncated display forms, and the rest of
// observed is never reported.
func EvaluateState(expected map[string]any, observed any) []Result {
	paths := make([]string, 0, len(expected))
	for p := range expected {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	results := make([]Result, 0, len(paths))
	for _, path := range paths {
		results = append(results, evaluateStatePath(path, expected[path], observed))
	}
	return results
}

func evaluateStatePath(path string, want, observed any) Result {
	rule := "state." + path
	secret := sensitivePath(path)
	r := Result{Rule: rule, Expected: displayValue(want, secret)}

	// "state.api_token: expected …" would read as a credential pair to
	// redact.Text, which output and reports apply to every reason, and the
	// word "expected" would be masked. Marking the path keeps the colon
	// away from the sensitive name.
	prefix := rule
	if secret {
		prefix += " (sensitive path, values redacted)"
	}

	got, err := lookup(observed, path)
	if err != nil {
		r.Reason = fmt.Sprintf("%s: expected %s, but %v", prefix, displayText(want, secret), err)
		return r
	}
	r.Actual = displayValue(got, secret)

	if equalJSON(want, got) {
		r.Passed = true
		return r
	}
	// Types are omitted for sensitive paths: they say something about the
	// hidden values.
	if wk, gk := jsonKind(want), jsonKind(got); wk != gk && !secret {
		r.Reason = fmt.Sprintf("%s: expected %s (%s), got %s (%s)", prefix, displayText(want, secret), wk, displayText(got, secret), gk)
	} else {
		r.Reason = fmt.Sprintf("%s: expected %s, got %s", prefix, displayText(want, secret), displayText(got, secret))
	}
	return r
}

// lookup walks a dot-path through decoded JSON. Object keys match exactly;
// array segments must be non-negative integer indexes. A missing key or
// out-of-range index is an error, never a zero value.
func lookup(v any, path string) (any, error) {
	cur := v
	walked := ""
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, fmt.Errorf("path not found: no key %q in object%s", seg, at(walked))
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || strconv.Itoa(i) != seg {
				return nil, fmt.Errorf("path not found: %q is not an array index%s", seg, at(walked))
			}
			if i >= len(node) {
				return nil, fmt.Errorf("path not found: index %d out of range (length %d)%s", i, len(node), at(walked))
			}
			cur = node[i]
		default:
			return nil, fmt.Errorf("path not found: cannot look up %q in %s%s", seg, jsonKind(cur), at(walked))
		}
		if walked == "" {
			walked = seg
		} else {
			walked += "." + seg
		}
	}
	return cur, nil
}

func at(walked string) string {
	if walked == "" {
		return " at the top level"
	}
	return fmt.Sprintf(" at %q", walked)
}

// equalJSON compares by JSON type and value. Numbers compare exactly as
// rationals, so 1 == 1.0 == 1e0 and large integers keep full precision;
// strings never equal numbers.
func equalJSON(want, got any) bool {
	if jsonKind(want) != jsonKind(got) {
		return false
	}
	switch w := want.(type) {
	case nil:
		return got == nil
	case string:
		return w == got.(string)
	case bool:
		return w == got.(bool)
	}
	wr, ok1 := toRat(want)
	gr, ok2 := toRat(got)
	return ok1 && ok2 && wr.Cmp(gr) == 0
}

func jsonKind(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case json.Number, float64, float32, int, int64, uint64:
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// toRat converts a number to an exact rational. json.Number (observed JSON,
// and every non-integer YAML number from config) and the integer types are
// exact. A float64 has already been rounded by whoever produced it, so it is
// read as its shortest decimal form (0.1, not 0.1000000000000000055…);
// config never produces one for a finite value.
func toRat(v any) (*big.Rat, bool) {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = n.String()
	case float64:
		s = strconv.FormatFloat(n, 'g', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(n), 'g', -1, 32)
	case int:
		s = strconv.Itoa(n)
	case int64:
		s = strconv.FormatInt(n, 10)
	case uint64:
		s = strconv.FormatUint(n, 10)
	default:
		return nil, false
	}
	return new(big.Rat).SetString(s)
}

// sensitivePath reports whether any segment of path names a secret.
func sensitivePath(path string) bool {
	for _, seg := range strings.Split(path, ".") {
		if redact.SensitiveName(seg) {
			return true
		}
	}
	return false
}

// displayValue is the report form of v: scalars keep their JSON type,
// strings are redacted and truncated, containers are summarized.
func displayValue(v any, secret bool) any {
	if secret {
		return redact.Placeholder
	}
	switch x := v.(type) {
	case string:
		return truncate(redact.Text(x))
	case map[string]any:
		return "<object>"
	case []any:
		return "<array>"
	}
	return v
}

// displayText is the message form of v: strings are quoted so "1" and 1
// stay distinguishable.
func displayText(v any, secret bool) string {
	switch d := displayValue(v, secret).(type) {
	case nil:
		return "null"
	case string:
		if secret || d == "<object>" || d == "<array>" {
			return d
		}
		return strconv.Quote(d)
	default:
		return fmt.Sprint(d)
	}
}

// truncate keeps at most maxDisplayRunes runes, marking a cut with "…".
func truncate(s string) string {
	if utf8.RuneCountInString(s) <= maxDisplayRunes {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxDisplayRunes-1]) + "…"
}
