package toml

import (
	"math/big"
	"strconv"
	"strings"
)

// maxNumberExponent bounds the decimal exponent accepted when parsing finite
// floats. It is far beyond any practical value while preventing pathological
// inputs (e.g. "1e999999999999") from exhausting memory.
const maxNumberExponent = 1_000_000

var (
	maxInt64    = new(big.Int).SetInt64(1<<63 - 1)
	maxInt64Abs = new(big.Int).Lsh(big.NewInt(1), 63)
)

// numberSyntaxError marks a lexeme that does not follow the TOML number
// grammar; numberRangeError marks an out-of-range value. Both are converted
// to ParseError by the parser (which needs the reported target size); exported
// through ParseNumber they are wrapped in NumberError with an ErrNumber*
// sentinel.
type numberSyntaxError struct{ msg string }

func (e numberSyntaxError) Error() string { return e.msg }

type numberRangeError struct {
	lexeme, size string
	sentinel     error
}

func (e numberRangeError) Error() string {
	return e.lexeme + " is out of range for " + e.size
}

func rangeNumberError(lexeme, size string) numberRangeError {
	return numberRangeError{lexeme: lexeme, size: size, sentinel: ErrNumberOverflow}
}

func parseNumberLexeme(lexeme string) (Number, error) {
	if lexeme == "" {
		return Number{}, numberSyntaxError{"invalid number: empty value"}
	}

	// Base-prefixed lexemes (which may contain letters such as "0xdead") are
	// integers; everything else carrying float syntax ('.', 'e'/'E', or the
	// inf/nan spellings) is a float.
	if hasIntegerBasePrefix(lexeme) ||
		(!strings.ContainsAny(lexeme, ".eE") && !isSpecialFloatWord(lexeme)) {
		return parseIntegerLexeme(lexeme)
	}
	return parseFloatLexeme(lexeme)
}

func hasIntegerBasePrefix(s string) bool {
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'o' || s[1] == 'b') {
		return true
	}
	return false
}

func isSpecialFloatWord(s string) bool {
	switch s {
	case "inf", "+inf", "-inf", "nan", "+nan", "-nan":
		return true
	}
	return false
}

func parseIntegerLexeme(lexeme string) (Number, error) {
	s := lexeme
	neg := false
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return Number{}, numberSyntaxError{"invalid integer " + strconv.Quote(lexeme)}
	}

	base := 10
	digits := s
	switch {
	case len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'o' || s[1] == 'b'):
		switch s[1] {
		case 'x':
			base = 16
		case 'o':
			base = 8
		case 'b':
			base = 2
		}
		digits = s[2:]
		// Signed non-decimal numbers are rejected by the lexer, but
		// ParseNumber must enforce the rule on its own.
		if neg || lexeme[0] == '+' {
			return Number{}, numberSyntaxError{"invalid integer " + strconv.Quote(lexeme) + ": sign cannot be used with non-decimal numbers"}
		}
	case len(s) > 1 && s[0] == '0':
		return Number{}, numberSyntaxError{"invalid integer " + strconv.Quote(lexeme) + ": cannot have leading zeroes"}
	}

	if digits == "" {
		return Number{}, numberSyntaxError{"invalid integer " + strconv.Quote(lexeme) + ": expected at least one digit"}
	}

	// Check every character ourselves, with a fixed base: big.Int.SetString
	// accepts things (e.g. underscores, base prefixes, signed hex) that TOML
	// does not, so it must never get the raw lexeme.
	for _, r := range digits {
		if r == '_' {
			continue
		}
		if !digitInBase(r, base) {
			return Number{}, numberSyntaxError{"invalid integer " + strconv.Quote(lexeme) + ": digit " + strconv.QuoteRune(r) + " is not valid for base " + strconv.Itoa(base)}
		}
	}
	if !integerUnderscoresOK(digits, base) {
		return Number{}, numberSyntaxError{"invalid integer " + strconv.Quote(lexeme) + ": underscores must be surrounded by digits"}
	}

	core := strings.ReplaceAll(digits, "_", "")
	v, ok := new(big.Int).SetString(core, base)
	if !ok {
		return Number{}, numberSyntaxError{"invalid integer " + strconv.Quote(lexeme)}
	}

	// TOML mandates the signed 64-bit range, even in exact mode.
	if (neg && v.Cmp(maxInt64Abs) > 0) || (!neg && v.Cmp(maxInt64) > 0) {
		return Number{}, rangeNumberError(lexeme, "int64")
	}

	return Number{
		lexeme: lexeme,
		kind:   NumberInteger,
		neg:    neg && v.Sign() != 0,
		intVal: v,
	}, nil
}

func digitInBase(r rune, base int) bool {
	switch base {
	case 2:
		return r == '0' || r == '1'
	case 8:
		return r >= '0' && r <= '7'
	case 10:
		return r >= '0' && r <= '9'
	case 16:
		return isHex(r)
	}
	return false
}

// integerUnderscoresOK checks that underscores sit between two digits. It
// assumes the string contains no base prefix.
func integerUnderscoresOK(s string, base int) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '_' {
			if i == 0 || i == len(s)-1 {
				return false
			}
			if !digitInBase(rune(s[i-1]), base) || !digitInBase(rune(s[i+1]), base) {
				return false
			}
		}
	}
	return true
}

func parseFloatLexeme(lexeme string) (Number, error) {
	n := Number{lexeme: lexeme, kind: NumberFloat}

	// Underscore placement must be checked per "."/"e" segment.
	parts := strings.FieldsFunc(lexeme, func(r rune) bool {
		switch r {
		case '.', 'e', 'E':
			return true
		}
		return false
	})
	if len(parts) == 0 {
		return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme)}
	}
	for _, part := range parts {
		if part == "" {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": expected digits"}
		}
		if !numUnderscoresOK(part) {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": underscores must be surrounded by digits"}
		}
	}

	plain := strings.ReplaceAll(lexeme, "_", "")

	switch plain {
	case "inf", "+inf":
		n.special = numberSpecialInf
		return n, nil
	case "-inf":
		n.special = numberSpecialInf
		n.neg = true
		return n, nil
	case "nan", "+nan":
		n.special = numberSpecialNaN
		return n, nil
	case "-nan":
		n.special = numberSpecialNaN
		n.neg = true
		return n, nil
	}

	mantExp := strings.FieldsFunc(plain, func(r rune) bool { return r == 'e' || r == 'E' })
	if strings.Count(plain, "e")+strings.Count(plain, "E") > 1 {
		return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": multiple exponents"}
	}
	if len(mantExp) < 1 || len(mantExp) > 2 {
		return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme)}
	}
	mant := mantExp[0]
	hasExp := len(mantExp) == 2

	neg := false
	switch mant[0] {
	case '-':
		neg = true
		mant = mant[1:]
	case '+':
		mant = mant[1:]
	}
	if mant == "" || mant[0] == '.' {
		return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": must start with a digit"}
	}

	intPart, fracPart, hasPoint := strings.Cut(mant, ".")
	if hasPoint {
		if strings.Contains(intPart, ".") || strings.Contains(fracPart, ".") {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": multiple decimal points"}
		}
		if intPart == "" || !allDigits(intPart) {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": integer part must be digits"}
		}
		if fracPart == "" || !allDigits(fracPart) {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": '.' must be followed by one or more digits"}
		}
		if numHasLeadingZero(intPart) {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": cannot have leading zeroes"}
		}
	} else {
		if intPart == "" || !allDigits(intPart) {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme)}
		}
		if numHasLeadingZero(intPart) {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": cannot have leading zeroes"}
		}
	}

	// An exponent or a decimal point is mandatory.
	if !hasPoint && !hasExp {
		return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme)}
	}

	exp := 0
	if hasExp {
		es := mantExp[1]
		if es == "" {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": exponent requires digits"}
		}
		esign := byte(0)
		if es[0] == '+' || es[0] == '-' {
			esign = es[0]
			es = es[1:]
		}
		if es == "" || !allDigits(es) {
			return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme) + ": exponent requires digits"}
		}
		if esign != 0 {
			mantExp[1] = string(esign) + es
		}
		e, err := strconv.Atoi(mantExp[1])
		if err != nil {
			return Number{}, rangeNumberError(lexeme, "float64")
		}
		exp = e
	}

	digits := intPart + fracPart

	v, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return Number{}, numberSyntaxError{"invalid float " + strconv.Quote(lexeme)}
	}

	// Normalize to v / 10^scale.
	scale := len(fracPart) - exp
	if scale < 0 {
		if -scale > maxNumberExponent {
			return Number{}, rangeNumberError(lexeme, "float64")
		}
		v.Mul(v, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-scale)), nil))
		scale = 0
	}
	if scale > maxNumberExponent {
		return Number{}, rangeNumberError(lexeme, "float64")
	}

	n.neg = neg
	n.intVal = v
	n.scale = scale
	// Negative zero carries no sign in the stored magnitude, but the explicit
	// minus must be preserved; n.neg already does so for any other value.
	return n, nil
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !isDigit(r) {
			return false
		}
	}
	return true
}
