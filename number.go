package toml

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// NumberKind describes whether a [Number] is an integer or a float.
type NumberKind uint8

const (
	// NumberInteger is the kind of a TOML integer.
	NumberInteger NumberKind = iota
	// NumberFloat is the kind of a TOML float.
	NumberFloat
)

func (k NumberKind) String() string {
	switch k {
	case NumberInteger:
		return "integer"
	case NumberFloat:
		return "float"
	default:
		return "unknown"
	}
}

// Number is the exact representation of a TOML integer or float, retaining the
// original lexical form and the integer/float kind.
//
// Values of this type are produced only when exact number mode is enabled
// (see [Decoder.SetStrictNumbers]) when decoding into an any/interface. They
// can also be requested explicitly by decoding into *Number, *big.Int, or
// *big.Rat. In the default decoding mode integers and floats decoded into
// interfaces are still represented as int64 and float64.
//
// A Number preserves information that a float64 conversion would lose: the
// distinction between integer and float, the sign of zero, the inf/NaN
// specials, and the exact decimal value of a finite float.
type Number struct {
	kind    NumberKind
	lexeme  string   // Original lexeme from the TOML document.
	rat     *big.Rat // Exact finite decimal value; nil for inf/NaN.
	special byte     // 0 finite, 'i' inf, 'n' NaN.
	neg     bool     // Sign for inf/NaN.
}

// Errors returned by the Number conversion methods.
//
// These can be checked with [errors.Is]; the returned errors additionally
// describe the value and target type in their Error() text.
var (
	// ErrNumberOverflow is returned when the value does not fit in the
	// requested integer or float range.
	ErrNumberOverflow = errors.New("toml: number is out of range")
	// ErrNumberRounded is returned when the value can be represented in the
	// requested floating-point type only after rounding.
	ErrNumberRounded = errors.New("toml: number cannot be represented exactly")
	// ErrNumberSpecial is returned when an infinite or NaN value is converted
	// to a type that has no representation for it (integers and big.Rat).
	ErrNumberSpecial = errors.New("toml: infinite or NaN value")
	// ErrNumberKind is returned when converting a float into an integer-only
	// type (or the reverse, where applicable).
	ErrNumberKind = errors.New("toml: number kind mismatch")
)

type numberError struct {
	kind error
	what string
}

func (e *numberError) Error() string { return e.what }
func (e *numberError) Unwrap() error { return e.kind }

// Kind reports whether the number is a TOML integer or float.
func (n Number) Kind() NumberKind { return n.kind }

// Lexeme returns the exact number text as it appeared in the TOML document.
func (n Number) Lexeme() string { return n.lexeme }

// IsInf reports whether the number is +inf or -inf.
func (n Number) IsInf() bool { return n.kind == NumberFloat && n.special == 'i' }

// IsNaN reports whether the number is NaN (signed or not).
func (n Number) IsNaN() bool { return n.kind == NumberFloat && n.special == 'n' }

// Signbit reports whether the number is negative (including -0.0 and -nan).
func (n Number) Signbit() bool {
	if n.special != 0 {
		return n.neg
	}
	if n.rat != nil {
		if n.rat.Sign() != 0 {
			return n.rat.Sign() < 0
		}
		return strings.HasPrefix(n.lexeme, "-")
	}
	return strings.HasPrefix(n.lexeme, "-")
}

// String returns a canonical TOML representation of the number.
//
// It never returns the original lexeme; use [Number.Lexeme] for that.
func (n Number) String() string { return n.canonical() }

// Rat returns the exact value of a finite integer or finite decimal float as
// a [big.Rat].
//
// It returns [ErrNumberSpecial] for inf and NaN. The returned *big.Rat is a
// copy and may be modified by the caller.
func (n Number) Rat() (*big.Rat, error) {
	if n.special != 0 {
		return nil, &numberError{ErrNumberSpecial, fmt.Sprintf("cannot convert %q to big.Rat: not a finite number", n.lexeme)}
	}
	if n.rat == nil {
		return nil, &numberError{ErrNumberKind, fmt.Sprintf("number %q has no exact rational value", n.lexeme)}
	}
	return new(big.Rat).Set(n.rat), nil
}

// BigInt returns the value of an integer number as a [big.Int].
//
// It returns [ErrNumberKind] for float numbers (including finite floats with
// an integral value such as 1.0).
func (n Number) BigInt() (*big.Int, error) {
	if n.special != 0 || n.rat == nil {
		return nil, &numberError{ErrNumberSpecial, fmt.Sprintf("cannot convert %q to big.Int", n.lexeme)}
	}
	if n.kind != NumberInteger {
		return nil, &numberError{ErrNumberKind, fmt.Sprintf("cannot convert float %q to big.Int", n.lexeme)}
	}
	return new(big.Int).Set(n.rat.Num()), nil
}

// Int64 returns the value of an integer number as an int64.
//
// It returns [ErrNumberOverflow] if the value does not fit in int64, and
// [ErrNumberKind] for float numbers.
func (n Number) Int64() (int64, error) {
	bi, err := n.BigInt()
	if err != nil {
		return 0, err
	}
	if !bi.IsInt64() {
		return 0, &numberError{ErrNumberOverflow, fmt.Sprintf("%s is out of range for int64", n.lexeme)}
	}
	return bi.Int64(), nil
}

// Uint64 returns the value of an integer number as a uint64.
//
// It returns [ErrNumberOverflow] if the value is negative or does not fit in
// uint64, and [ErrNumberKind] for float numbers.
func (n Number) Uint64() (uint64, error) {
	bi, err := n.BigInt()
	if err != nil {
		return 0, err
	}
	if bi.Sign() < 0 || !bi.IsUint64() {
		return 0, &numberError{ErrNumberOverflow, fmt.Sprintf("%s is out of range for uint64", n.lexeme)}
	}
	return bi.Uint64(), nil
}

// Float64 returns the float64 nearest to the number.
//
// Finite values that cannot be represented exactly return the nearest float64
// together with an error wrapping [ErrNumberRounded]; values with a magnitude
// beyond the float64 range return +/-Inf and an error wrapping
// [ErrNumberOverflow]. +inf, -inf and NaN return the corresponding float64
// with no error.
func (n Number) Float64() (float64, error) {
	return n.floatBits(64)
}

// Float32 returns the float32 nearest to the number.
//
// The error semantics are the same as for [Number.Float64], but the range and
// precision checks are those of float32.
func (n Number) Float32() (float32, error) {
	f, err := n.floatBits(32)
	return float32(f), err
}

func (n Number) floatBits(bits int) (float64, error) {
	if n.IsInf() {
		if n.neg {
			return math.Inf(-1), nil
		}
		return math.Inf(1), nil
	}
	if n.IsNaN() {
		if n.neg {
			return math.Copysign(math.NaN(), -1), nil
		}
		return math.NaN(), nil
	}

	s := n.cleanLexeme()
	if n.kind == NumberInteger {
		// Non-decimal lexemes (0x/0o/0b) are not accepted by ParseFloat; use
		// the canonical decimal rendering.
		s = n.canonical()
	}
	f, err := strconv.ParseFloat(s, bits)
	if err != nil {
		if math.IsInf(f, 0) {
			return f, &numberError{ErrNumberOverflow, fmt.Sprintf("%s is out of range for float%d", n.lexeme, bits)}
		}
		// Underflow: the nearest value is zero; this is a rounding, not an
		// overflow.
		return f, &numberError{ErrNumberRounded, fmt.Sprintf("%s cannot be represented exactly as float%d", n.lexeme, bits)}
	}

	// Compare against the exact rational value of the float rather than
	// re-parsing: "0e0" must read as exactly representable, and subnormal
	// values need exact bit-level comparison (big.Rat.SetFloat64 returns nil
	// for subnormals).
	if n.rat != nil {
		r := exactFloatRat(f, bits)
		if r.Cmp(n.rat) != 0 {
			return f, &numberError{ErrNumberRounded, fmt.Sprintf("%s cannot be represented exactly as float%d", n.lexeme, bits)}
		}
	}
	return f, nil
}

// exactFloatRat returns the exact rational value of a finite IEEE-754 binary
// float (bits 32 or 64).
func exactFloatRat(f float64, bits int) *big.Rat {
	if bits == 32 {
		return exactBinaryFloatRat(uint64(math.Float32bits(float32(f))), 23, 8, 127)
	}
	return exactBinaryFloatRat(math.Float64bits(f), 52, 11, 1023)
}

func exactBinaryFloatRat(u uint64, p, expBits, bias uint) *big.Rat {
	totalBits := p + uint(expBits) + 1 // +1 for the sign bit.
	neg := u>>(totalBits-1) == 1
	eRaw := int((u >> p) & ((1 << expBits) - 1))
	mant := u & ((1 << p) - 1)
	num := new(big.Int)
	den := new(big.Int)
	if eRaw == 0 {
		// Subnormal or zero: (-1)^s * 2^(1-bias-p) * mantissa.
		num.SetUint64(mant)
		ex := int(1) - int(bias) - int(p)
		if ex >= 0 {
			num.Lsh(num, uint(ex))
			den.SetInt64(1)
		} else {
			den.Lsh(big.NewInt(1), uint(-ex))
		}
	} else {
		// Normal: (-1)^s * 2^(e-bias-p) * (2^p + mantissa).
		num.SetUint64((1 << p) | mant)
		ex := eRaw - int(bias) - int(p)
		if ex >= 0 {
			num.Lsh(num, uint(ex))
			den.SetInt64(1)
		} else {
			den.Lsh(big.NewInt(1), uint(-ex))
		}
	}
	r := new(big.Rat).SetFrac(num, den)
	if neg {
		r.Neg(r)
	}
	return r
}

// cleanLexeme removes TOML underscores and a leading + sign; the result is
// still a strict decimal float (base prefixes are never valid for floats).
func (n Number) cleanLexeme() string {
	s := strings.ReplaceAll(n.lexeme, "_", "")
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	return s
}

// canonical renders an unambiguous TOML lexeme that re-decodes to the same kind
// and numeric value.
func (n Number) canonical() string {
	if n.IsInf() {
		if n.neg {
			return "-inf"
		}
		return "inf"
	}
	if n.IsNaN() {
		if n.neg {
			return "-nan"
		}
		return "nan"
	}

	if n.kind == NumberInteger {
		if n.rat != nil && n.rat.IsInt() {
			return n.rat.Num().String()
		}
		return strings.ReplaceAll(strings.TrimPrefix(n.lexeme, "+"), "_", "")
	}

	return canonicalFloat(n.cleanLexeme())
}

// canonicalFloat normalizes a cleaned decimal float lexeme into an unambiguous
// canonical form with a decimal point or exponent and a lower-case 'e'.
func canonicalFloat(s string) string {
	if !strings.ContainsAny(s, ".eE") {
		return s + ".0"
	}
	return strings.ToLower(s)
}

// parseDecimalRat converts a grammar-validated TOML decimal integer or float
// lexeme into a big.Rat without relying on the lenient parsing of big.Rat or
// big.Int (which would widen the accepted syntax).
func parseDecimalRat(lexeme string) (*big.Rat, error) {
	s := strings.ReplaceAll(lexeme, "_", "")
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	}

	neg := false
	if strings.HasPrefix(s, "-") {
		neg = true
		s = s[1:]
	}

	mantissa := s
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		mantissa = s[:i]
		var err error
		exp, err = strconv.Atoi(s[i+1:])
		if err != nil {
			return nil, &numberError{ErrNumberOverflow, fmt.Sprintf("exponent in %q is out of range", lexeme)}
		}
	}

	intPart := mantissa
	fracPart := ""
	if i := strings.IndexByte(mantissa, '.'); i >= 0 {
		intPart = mantissa[:i]
		fracPart = mantissa[i+1:]
	}
	if intPart == "" {
		intPart = "0"
	}

	digits := intPart + fracPart
	bi, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return nil, &numberError{ErrNumberKind, fmt.Sprintf("invalid decimal number %q", lexeme)}
	}

	r := new(big.Rat).SetInt(bi)
	if n := len(fracPart); n > 0 {
		r.Quo(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)))
	}
	if exp > 0 {
		r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)))
	} else if exp < 0 {
		r.Quo(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-int64(exp))), nil)))
	}
	if neg {
		r.Neg(r)
	}
	return r, nil
}

// decimalFromRat renders a rational with a denominator of the form 2^a*5^b as
// a finite decimal string with a fractional point (matching the float kind).
//
// Every value parsed from a finite TOML decimal has such a denominator. For
// rationals that do not terminate in base 10, ok is false.
func decimalFromRat(r *big.Rat) (s string, ok bool) {
	if r == nil {
		return "", false
	}
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())

	sign := ""
	if num.Sign() < 0 {
		sign = "-"
		num.Neg(num)
	}

	countFactor := func(p int64) int {
		n := 0
		base := big.NewInt(p)
		zero := new(big.Int)
		mod := new(big.Int)
		for {
			mod.Mod(den, base)
			if mod.Cmp(zero) != 0 {
				return n
			}
			den.Quo(den, base)
			n++
		}
	}

	a := countFactor(2)
	b := countFactor(5)
	if den.Cmp(big.NewInt(1)) != 0 {
		return "", false
	}

	k := a
	if b > k {
		k = b
	}

	// 10^k / denom = 2^(k-a) * 5^(k-b)
	scaled := new(big.Int).Set(num)
	if d := k - a; d > 0 {
		scaled.Mul(scaled, new(big.Int).Exp(big.NewInt(2), big.NewInt(int64(d)), nil))
	}
	if d := k - b; d > 0 {
		scaled.Mul(scaled, new(big.Int).Exp(big.NewInt(5), big.NewInt(int64(d)), nil))
	}

	digits := scaled.String()
	if k == 0 {
		return sign + digits + ".0", true
	}
	if len(digits) <= k {
		digits = strings.Repeat("0", k-len(digits)+1) + digits
	}
	intDigits := digits[:len(digits)-k]
	fracDigits := strings.TrimRight(digits[len(digits)-k:], "0")
	if fracDigits == "" {
		return sign + intDigits + ".0", true
	}
	return sign + intDigits + "." + fracDigits, true
}
