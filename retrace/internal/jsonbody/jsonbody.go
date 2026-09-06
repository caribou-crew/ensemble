// Package jsonbody decodes payloads for structural comparison without rounding
// JSON numbers. Diff and strict replay share the same numeric identity rules.
package jsonbody

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
)

// Decode returns the structural value of one complete JSON body. Invalid or
// absent JSON returns false so callers can compare the original bytes instead.
func Decode(body string) (any, bool) {
	// A decoder alone accepts the first value in malformed input such as
	// `{} {}`. Require the entire body to be one JSON value.
	if !json.Valid([]byte(body)) {
		return nil, false
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, false
	}
	return preserveNumbers(v), true
}

// IsInteger checks a JSON number's exact value without converting through a
// machine numeric type. After insignificant zeroes are removed, only a negative
// exponent can leave a fractional part. Exponents stay compact at any magnitude.
func IsInteger(number json.Number) bool {
	raw := string(number)
	if raw == "" || strings.TrimSpace(raw) != raw ||
		(raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) || !json.Valid([]byte(raw)) {
		return false
	}
	return !strings.Contains(canonicalNumber(raw), "e-")
}

// preserveNumbers keeps the existing float64 values where their shortest JSON
// representation is exact. Other numbers retain an exact, normalized decimal
// representation, so adjacent IDs cannot disappear into one float64. Keeping
// ordinary values as floats also preserves existing matcher/consumer behavior.
func preserveNumbers(v any) any {
	switch n := v.(type) {
	case json.Number:
		exact := canonicalNumber(string(n))
		if f, err := n.Float64(); err == nil && canonicalNumber(strconv.FormatFloat(f, 'g', -1, 64)) == exact {
			return f
		}
		return json.Number(exact)
	case map[string]any:
		for k, value := range n {
			n[k] = preserveNumbers(value)
		}
	case []any:
		for i, value := range n {
			n[i] = preserveNumbers(value)
		}
	}
	return v
}

// canonicalNumber normalizes a valid JSON number without rounding. Integers
// within int64 use plain decimal notation so existing json.Number.Int64
// consumers can still read their representation. Other values
// keep a compact exponent: even 1e-999999999 stays a small string instead of
// allocating a decimal with a billion zeroes.
func canonicalNumber(s string) string {
	coefficient, exp, _ := strings.Cut(strings.ToLower(s), "e")
	var exponent big.Int
	if exp != "" {
		exponent.SetString(exp, 10)
	}
	sign := ""
	if strings.HasPrefix(coefficient, "-") {
		sign, coefficient = "-", coefficient[1:]
	}
	if whole, fraction, found := strings.Cut(coefficient, "."); found {
		coefficient = whole + fraction
		exponent.Sub(&exponent, big.NewInt(int64(len(fraction))))
	}
	coefficient = strings.TrimLeft(coefficient, "0")
	if coefficient == "" {
		return "0"
	}
	trimmed := strings.TrimRight(coefficient, "0")
	exponent.Add(&exponent, big.NewInt(int64(len(coefficient)-len(trimmed))))
	if exponent.Sign() == 0 {
		return sign + trimmed
	}
	if exponent.IsInt64() {
		zeros := exponent.Int64()
		// Only a signed int64 can take this path: expansion is bounded to
		// nineteen digits, regardless of the exponent supplied in JSON.
		if zeros > 0 && zeros < 19 && int64(len(trimmed))+zeros <= 19 {
			decimal := sign + trimmed + strings.Repeat("0", int(zeros))
			if _, err := strconv.ParseInt(decimal, 10, 64); err == nil {
				return decimal
			}
		}
	}
	return sign + trimmed + "e" + exponent.String()
}
