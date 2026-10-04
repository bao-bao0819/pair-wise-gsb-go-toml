package toml

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// NumberKind identifies whether a [Number] is a TOML integer or a TOML float.
type NumberKind uint8

const (
	// NumberInteger is the TOML "integer" kind.
	NumberInteger NumberKind = iota + 1

	// NumberFloat is the TOML "float" kind.
	NumberFloat
)

func (k NumberKind) String() string {
	switch k {
	case NumberInteger:
		return "Integer"
	case NumberFloat:
		return "Float"
	default:
		return "Invalid"
	}
}

// Sentinel errors reported by [Number]'s conversion methods and by
// [ParseNumber]. Errors returned wrap these with [errors.Is].
var (
	// ErrNumberOverflow reports that the value cannot be represented by the
	// target type because its magnitude is too large.
	ErrNumberOverflow = errors.New("toml: number overflows target type")

	// ErrNumberInexact reports that converting the value to the target type
	// requires rounding (for example a tiny finite float to a float64).
	ErrNumberInexact = errors.New("toml: value cannot be represented exactly")

	// ErrNumberNonFinite reports that an inf or NaN was passed to a
	// conversion that only accepts finite values (such as [Number.Rat]).
	ErrNumberNonFinite = errors.New("toml: value is not finite")

	// ErrNumberNotInteger reports that a float was passed to a conversion that
	// only accepts integers (such as [Number.Int64]).
	ErrNumberNotInteger = errors.New("toml: value is not an integer")
)

// NumberError wraps one of the ErrNumber* sentinel errors with the original
// TOML lexeme that caused the failure.
type NumberError struct {
	// Lexeme is the TOML source text of the value.
	Lexeme string

	// Op is a short description of the attempted conversion, e.g. "Int64".
	Op string

	err error
}

func (e *NumberError) Error() string {
	return fmt.Sprintf("toml.Number.%s: %s: %q", e.Op, e.err, e.Lexeme)
}

// Unwrap returns the wrapped ErrNumber* error.
func (e *NumberError) Unwrap() error { return e.err }

// Number stores a TOML integer or float without any loss of information.
//
// Values of this type are produced only when exact numbers have been enabled
// with [Decoder.SetStrictNumbers]; see that method for details. The original
// source text is retained by [Number.Lexeme], and the TOML kind (integer or
// float) is available from [Number.Kind].
//
// Integers obey the TOML range (-2^63 .. 2^63-1) and are stored exactly; use
// [Number.BigInt] to access them as a [math/big.Int]. Finite decimals are
// stored exactly as a [math/big.Rat], available from [Number.Rat]; inf, NaN
// and signed zero are represented separately and never collapse to an
// ordinary float64.
//
// [Number.String] returns a canonical decimal representation, while
// [Number.Lexeme] returns the exact source text. The zero value is not a
// valid Number.
type Number struct {
	lexeme string
	kind   NumberKind

	// neg records the sign, including the sign of a negative zero.
	neg bool

	// intVal is the exact unscaled absolute magnitude for integers and for
	// finite decimals.
	intVal *big.Int

	// For finite decimals the value is sign * intVal / 10^scale; scale >= 0.
	scale int

	// special identifies non-finite floats; numberSpecialFinite for finite
	// values and integers.
	special uint8
}

const (
	numberSpecialFinite uint8 = iota
	numberSpecialInf
	numberSpecialNaN
)

// ParseNumber parses a single TOML numeric lexeme and returns it as a [Number].
//
// The lexeme must follow the TOML number grammar: underscores, base prefixes
// (0x, 0o, 0b), decimal points and exponents are validated exactly as TOML
// defines them; the lenient syntax accepted by [math/big.Int.SetString] is
// rejected. Integers must fit in the signed 64-bit range mandated by TOML.
func ParseNumber(lexeme string) (Number, error) {
	n, err := parseNumberLexeme(lexeme)
	if err != nil {
		var rerr numberRangeError
		if errors.As(err, &rerr) {
			return Number{}, &NumberError{Lexeme: lexeme, Op: "Parse", err: rerr.sentinel}
		}
		if ne, ok := err.(*NumberError); ok {
			return Number{}, ne
		}
		return Number{}, &NumberError{Lexeme: lexeme, Op: "Parse", err: err}
	}
	return n, nil
}

// Lexeme returns the original TOML source text of the number, e.g. "0xDEAD",
// "1_000", "5e+22" or "-0.0".
func (n Number) Lexeme() string { return n.lexeme }

// Kind reports whether the number is a TOML integer or float.
func (n Number) Kind() NumberKind { return n.kind }

// IsInt reports whether the number is a TOML integer.
func (n Number) IsInt() bool { return n.kind == NumberInteger }

// IsFloat reports whether the number is a TOML float.
func (n Number) IsFloat() bool { return n.kind == NumberFloat }

// IsInf reports whether the number is +inf or -inf.
func (n Number) IsInf() bool { return n.special == numberSpecialInf }

// IsNaN reports whether the number is NaN (the sign of a NaN is meaningless).
func (n Number) IsNaN() bool { return n.special == numberSpecialNaN }

// Negative reports whether the number carries a minus sign.
//
// Unlike a comparison against zero, this distinguishes -0.0 from +0.0.
func (n Number) Negative() bool { return n.neg }

func (n Number) numErr(op string, err error) error {
	return &NumberError{Lexeme: n.lexeme, Op: op, err: err}
}

// BigInt returns the integer as a [math/big.Int].
//
// It fails with [ErrNumberNotInteger] for floats; a numerically integral
// float (e.g. "1e3") is still a TOML float. The returned value is an
// independent copy.
func (n Number) BigInt() (*big.Int, error) {
	if n.kind != NumberInteger {
		return nil, n.numErr("BigInt", ErrNumberNotInteger)
	}
	v := new(big.Int).Set(n.intVal)
	if n.neg {
		v.Neg(v)
	}
	return v, nil
}

// Rat returns the exact value of a finite number as a [math/big.Rat].
//
// Integers and finite decimals are represented exactly. It fails with
// [ErrNumberNonFinite] for inf or NaN.
func (n Number) Rat() (*big.Rat, error) {
	if n.special != numberSpecialFinite {
		return nil, n.numErr("Rat", ErrNumberNonFinite)
	}
	r := new(big.Rat).SetInt(n.intVal)
	if n.scale > 0 {
		den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n.scale)), nil)
		r.Quo(r, new(big.Rat).SetInt(den))
	}
	if n.neg {
		r.Neg(r)
	}
	return r, nil
}

// Int64 returns the value as an int64.
//
// It fails with [ErrNumberNotInteger] for floats and with [ErrNumberOverflow]
// if the integer is outside the int64 range.
func (n Number) Int64() (int64, error) {
	if n.kind != NumberInteger {
		return 0, n.numErr("Int64", ErrNumberNotInteger)
	}
	v, err := int64FromBig(n.neg, n.intVal)
	if err != nil {
		return 0, n.numErr("Int64", err)
	}
	return v, nil
}

// Uint64 returns the value as a uint64.
//
// It fails with [ErrNumberNotInteger] for floats and with [ErrNumberOverflow]
// for negative integers or integers outside the uint64 range.
func (n Number) Uint64() (uint64, error) {
	if n.kind != NumberInteger {
		return 0, n.numErr("Uint64", ErrNumberNotInteger)
	}
	if n.neg && n.intVal.Sign() != 0 {
		return 0, n.numErr("Uint64", ErrNumberOverflow)
	}
	if !n.intVal.IsUint64() {
		return 0, n.numErr("Uint64", ErrNumberOverflow)
	}
	return n.intVal.Uint64(), nil
}

func int64FromBig(neg bool, v *big.Int) (int64, error) {
	if neg {
		// The two's-complement minimum (-2^63) is one larger in magnitude
		// than the int64 maximum.
		if v.Cmp(maxInt64Abs) > 0 {
			return 0, ErrNumberOverflow
		}
		if v.Cmp(maxInt64Abs) == 0 {
			return math.MinInt64, nil
		}
		return -v.Int64(), nil
	}
	if !v.IsInt64() {
		return 0, ErrNumberOverflow
	}
	return v.Int64(), nil
}

// Float64 returns the value as a float64.
//
// +inf/-inf/NaN map to their float64 counterparts exactly, as do signed
// zeros. Integers are converted exactly when they fit in the float's
// contiguous integer range; a finite value that needs rounding (including one
// that underflows to zero) fails with [ErrNumberInexact], and the returned
// value is the best-effort conversion in that case.
func (n Number) Float64() (float64, error) {
	return n.floatBits(64)
}

// Float32 is like [Number.Float64] but targets float32.
func (n Number) Float32() (float32, error) {
	f, err := n.floatBits(32)
	return float32(f), err
}

func (n Number) floatBits(bits int) (float64, error) {
	switch n.special {
	case numberSpecialInf:
		return math.Inf(boolToSign(n.neg)), nil
	case numberSpecialNaN:
		return math.NaN(), nil
	}

	// Zero is always exact; preserve the explicit sign. The zero stored for an
	// integer is always positive.
	if n.intVal.Sign() == 0 {
		if n.neg && n.kind == NumberFloat {
			return math.Copysign(0, -1), nil
		}
		return 0, nil
	}

	r, _ := n.Rat()
	f, acc := new(big.Float).SetRat(r).Float64()
	if math.IsInf(f, 0) {
		return f, n.numErr(floatOp(bits), ErrNumberOverflow)
	}
	exact := acc == big.Exact
	if bits == 32 {
		f32 := float32(f)
		if math.IsInf(float64(f32), 0) || float64(f32) != f {
			exact = false
		}
	}
	if !exact {
		return f, n.numErr(floatOp(bits), ErrNumberInexact)
	}
	return f, nil
}

func floatOp(bits int) string {
	if bits == 32 {
		return "Float32"
	}
	return "Float64"
}

func boolToSign(neg bool) int {
	if neg {
		return -1
	}
	return 1
}

// String returns the canonical TOML representation of the number.
//
// Integers are rendered as signed decimal without underscores or base
// prefixes; finite floats as plain decimals (using an exponent only when the
// magnitude is very large or small); inf/NaN and signed zero use the TOML
// spellings.
func (n Number) String() string {
	if n.kind == NumberInteger {
		s := n.intVal.String()
		if n.neg && !strings.HasPrefix(s, "-") {
			s = "-" + s
		}
		return s
	}

	prefix := ""
	if n.neg {
		prefix = "-"
	}
	switch n.special {
	case numberSpecialInf:
		return prefix + "inf"
	case numberSpecialNaN:
		return prefix + "nan"
	}

	return prefix + canonicalDecimal(n.intVal, n.scale)
}

// canonicalDecimal renders the non-negative terminating decimal m/10^scale.
func canonicalDecimal(m *big.Int, scale int) string {
	if m.Sign() == 0 {
		return "0.0"
	}

	// Value equals the digit string with the decimal point shifted `scale`
	// places left; e is the position of the first significant digit.
	digits := m.String()
	e := len(digits) - 1 - scale

	if e >= -6 && e <= 15 {
		if e >= 0 {
			if e+1 < len(digits) {
				frac := strings.TrimRight(digits[e+1:], "0")
				if frac == "" {
					return digits[:e+1] + ".0"
				}
				return digits[:e+1] + "." + frac
			}
			return digits + strings.Repeat("0", e+1-len(digits)) + ".0"
		}
		return "0." + strings.Repeat("0", -e-1) + strings.TrimRight(digits, "0")
	}

	frac := strings.TrimRight(digits[1:], "0")
	if frac == "" {
		return string(digits[0]) + ".0e" + strconv.Itoa(e)
	}
	return string(digits[0]) + "." + frac + "e" + strconv.Itoa(e)
}
