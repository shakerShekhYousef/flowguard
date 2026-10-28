package config

import (
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// StateAssertions holds assert.state: dot-paths mapped to expected scalar
// values. It decodes like map[string]any, except that numbers YAML would
// round to float64 (fractions, exponents, integers beyond 64 bits) are kept
// exactly as json.Number, so a comparison can never pass only because the
// expected value was rounded. Integers that fit in 64 bits stay int, int64
// or uint64, which are already exact.
type StateAssertions map[string]any

// UnmarshalYAML implements yaml.Unmarshaler. Numbers that cannot be kept
// exactly are stored as invalidNumber, so validation can report them with
// their path instead of failing the whole decode without context.
func (s *StateAssertions) UnmarshalYAML(node *yaml.Node) error {
	var raw map[string]yaml.Node
	if err := node.Decode(&raw); err != nil {
		return err
	}
	out := make(StateAssertions, len(raw))
	for path, n := range raw {
		v, err := stateScalar(&n)
		if err != nil {
			return err
		}
		out[path] = v
	}
	*s = out
	return nil
}

// yamlFloat is the YAML 1.2 core-schema float syntax yaml.v3 resolves
// (after removing "_"), without .inf and .nan.
var yamlFloat = regexp.MustCompile(`^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE]([-+]?[0-9]+))?$`)

// Bounds on exact numbers, so a pathological literal such as 1e-99999999
// cannot make validation compute huge values.
const (
	maxNumberExponent = 1000
	maxNumberDigits   = 2000
)

// invalidNumber is a numeric literal that cannot be compared exactly.
type invalidNumber struct{ reason string }

func stateScalar(n *yaml.Node) (any, error) {
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, err
	}
	if n.Kind != yaml.ScalarNode {
		return v, nil
	}
	switch v.(type) {
	case float64:
		// Finite floats are re-read from the literal; .inf and .nan stay
		// float64 for validateStateValue to reject.
		if lit := strings.ReplaceAll(n.Value, "_", ""); yamlFloat.MatchString(lit) {
			return exactNumber(lit), nil
		}
	case string:
		// yaml.v3 resolves a plain float literal that overflows float64
		// (e.g. 1e400) to a string. Unquoted, it was meant as a number.
		plain := n.Style&(yaml.TaggedStyle|yaml.DoubleQuotedStyle|yaml.SingleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) == 0
		if lit := strings.ReplaceAll(n.Value, "_", ""); plain && yamlFloat.MatchString(lit) {
			return exactNumber(lit), nil
		}
	}
	return v, nil
}

// exactNumber converts a YAML float literal (already matching yamlFloat)
// to a json.Number holding the same value in canonical decimal form, e.g.
// 1.50 -> 1.5, 1e3 -> 1000, .5 -> 0.5, 12345678901234567890123 unchanged.
func exactNumber(lit string) any {
	if exp := yamlFloat.FindStringSubmatch(lit)[4]; exp != "" {
		e, err := strconv.Atoi(exp)
		if err != nil || e > maxNumberExponent || e < -maxNumberExponent {
			return invalidNumber{fmt.Sprintf("has an exponent outside ±%d", maxNumberExponent)}
		}
	}
	r, ok := new(big.Rat).SetString(strings.TrimPrefix(lit, "+"))
	if !ok {
		return invalidNumber{"is not a number FlowGuard can compare exactly"}
	}
	s, ok := decimalString(r)
	if !ok {
		return invalidNumber{fmt.Sprintf("has more than %d digits", maxNumberDigits)}
	}
	return json.Number(s)
}

// decimalString writes r, which came from a decimal literal and so has a
// denominator of the form 2^a·5^b, as an exact decimal with no trailing
// zeros. It fails if that takes more than maxNumberDigits digits.
func decimalString(r *big.Rat) (string, bool) {
	// 4 bits per digit over-approximates, keeping the work below bounded.
	if r.Num().BitLen() > 4*maxNumberDigits || r.Denom().BitLen() > 4*maxNumberDigits {
		return "", false
	}
	s := r.Num().String()
	if !r.IsInt() {
		d := new(big.Int).Set(r.Denom())
		twos := d.TrailingZeroBits()
		d.Rsh(d, twos)
		fives := uint(0)
		five, rem := big.NewInt(5), new(big.Int)
		for d.Cmp(big.NewInt(1)) != 0 {
			d.QuoRem(d, five, rem)
			if rem.Sign() != 0 {
				return "", false // not a finite decimal; cannot come from a literal
			}
			fives++
		}
		s = r.FloatString(int(max(twos, fives)))
	}
	if strings.Count(s, "")-1-strings.Count(s, "-")-strings.Count(s, ".") > maxNumberDigits {
		return "", false
	}
	return s, true
}
