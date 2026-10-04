package toml

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"
)

func mustParseNumber(t *testing.T, lex string) Number {
	t.Helper()
	n, err := ParseNumber(lex)
	if err != nil {
		t.Fatalf("ParseNumber(%q): %v", lex, err)
	}
	return n
}

func mustRat(t *testing.T, s string) *big.Rat {
	t.Helper()
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		t.Fatalf("bad test rat %q", s)
	}
	return r
}

func TestParseNumberIntegers(t *testing.T) {
	cases := []struct {
		lex, dec string
		neg      bool
		v        int64
	}{
		{"0", "0", false, 0},
		{"42", "42", false, 42},
		{"-7", "-7", true, -7},
		{"+7", "7", false, 7},
		{"1_000", "1000", false, 1000},
		{"9_223_372_036_854_775_807", "9223372036854775807", false, math.MaxInt64},
		{"-9_223_372_036_854_775_808", "-9223372036854775808", true, math.MinInt64},
		// Larger than 2^53-1, still a perfectly legal TOML integer.
		{"9007199254740993", "9007199254740993", false, 9007199254740993},
		{"0xDEADBEEF", "3735928559", false, 3735928559},
		{"0xdead_beef", "3735928559", false, 3735928559},
		{"0o777", "511", false, 511},
		{"0o7_7_7", "511", false, 511},
		{"0b1010", "10", false, 10},
		{"0b1_0_1_0", "10", false, 10},
	}

	for _, c := range cases {
		t.Run(c.lex, func(t *testing.T) {
			n := mustParseNumber(t, c.lex)
			if !n.IsInt() || n.Kind() != NumberInteger || n.IsFloat() {
				t.Fatalf("kind = %s, want Integer", n.Kind())
			}
			if n.Lexeme() != c.lex {
				t.Errorf("lexeme = %q, want %q", n.Lexeme(), c.lex)
			}
			if n.Negative() != c.neg {
				t.Errorf("negative = %v, want %v", n.Negative(), c.neg)
			}
			if got := n.String(); got != c.dec {
				t.Errorf("canonical = %q, want %q", got, c.dec)
			}

			bi, err := n.BigInt()
			if err != nil {
				t.Fatalf("BigInt: %v", err)
			}
			want, _ := new(big.Int).SetString(c.dec, 10)
			if bi.Cmp(want) != 0 {
				t.Errorf("BigInt = %s, want %s", bi, want)
			}
			bi.Mul(bi, big.NewInt(7)) // BigInt must return an independent copy.
			if bi2, _ := n.BigInt(); bi2.Cmp(want) != 0 {
				t.Errorf("BigInt returned shared storage: %s", bi2)
			}

			got, err := n.Int64()
			if err != nil || got != c.v {
				t.Errorf("Int64 = %d, %v; want %d", got, err, c.v)
			}

			r, err := n.Rat()
			if err != nil {
				t.Fatalf("Rat: %v", err)
			}
			if r.Cmp(mustRat(t, c.dec)) != 0 {
				t.Errorf("Rat = %s, want %s", r, c.dec)
			}

			// Integers within 2^53-1 are exact as float64; larger ones are
			// reported inexact rather than silently rounded.
			_, ferr := n.Float64()
			// Values in the contiguous range are exact; powers of two beyond
			// it (e.g. -2^63) are also represented exactly by float64.
			abs := new(big.Int).Set(n.intVal)
			if abs.Sign() < 0 {
				abs.Neg(abs)
			}
			_, acc := new(big.Float).SetInt(abs).Float64()
			safe := abs.Cmp(new(big.Int).SetInt64(maxSafeFloat64Int)) <= 0 || acc == big.Exact
			if safe && ferr != nil {
				t.Errorf("Float64: %v, want exact", ferr)
			}
			if !safe && !errors.Is(ferr, ErrNumberInexact) {
				t.Errorf("Float64 err = %v, want ErrNumberInexact", ferr)
			}
		})
	}
}

func TestParseNumberUint64(t *testing.T) {
	n := mustParseNumber(t, "-1")
	if _, err := n.Uint64(); !errors.Is(err, ErrNumberOverflow) {
		t.Fatalf("Uint64(-1) err = %v, want ErrNumberOverflow", err)
	}
	n0 := mustParseNumber(t, "-0")
	if u, err := n0.Uint64(); err != nil || u != 0 {
		t.Fatalf("Uint64(-0) = %d, %v", u, err)
	}
	nmax := mustParseNumber(t, "9223372036854775807") // TOML int64 maximum
	if u, err := nmax.Uint64(); err != nil || u != math.MaxInt64 {
		t.Fatalf("Uint64(max) = %d, %v", u, err)
	}
}

// Integers must keep the TOML-mandated signed 64-bit range even in exact
// mode, rather than silently widening to arbitrary precision.
func TestParseNumberIntegerRange(t *testing.T) {
	for _, lex := range []string{
		"9223372036854775808",  // 2^63
		"18446744073709551616", // 2^64
		"-9223372036854775809", // minInt64 - 1
		"99999999999999999999", // way over
		"0xffffffffffffffff",   // 2^64-1 in hex
	} {
		_, err := ParseNumber(lex)
		if err == nil {
			t.Fatalf("ParseNumber(%q) succeeded, want range error", lex)
		}
		var ne *NumberError
		if !errors.As(err, &ne) {
			t.Fatalf("ParseNumber(%q): %v is not *NumberError", lex, err)
		}
		if !errors.Is(err, ErrNumberOverflow) {
			t.Fatalf("ParseNumber(%q): %v, want ErrNumberOverflow", lex, err)
		}
	}
}

func TestParseNumberFiniteFloats(t *testing.T) {
	cases := []struct {
		lex, canon, rat string
		neg             bool
	}{
		{"1.0", "1.0", "1", false},
		{"0.1", "0.1", "0.1", false},
		{"-0.1", "-0.1", "-0.1", true},
		{"3.141_592_653_589_793", "3.141592653589793", "3.141592653589793", false},
		{"5e+22", "5.0e22", "5e22", false},
		{"1e3", "1000.0", "1000", false},
		{"-1.5e-2", "-0.015", "-0.015", true},
		{"6.626e-34", "6.626e-34", "6.626e-34", false},
		// Very large exponent; canonical form keeps the exponent.
		{"1e100", "1.0e100", "1e100", false},
		// Very small finite decimal, far below the float64 underflow edge.
		{"1e-400", "1.0e-400", "1e-400", false},
		{"0.0000001", "1.0e-7", "1e-7", false},
		{"0.000001", "0.000001", "0.000001", false},
	}

	for _, c := range cases {
		t.Run(c.lex, func(t *testing.T) {
			n := mustParseNumber(t, c.lex)
			if !n.IsFloat() || n.Kind() != NumberFloat || n.IsInt() {
				t.Fatalf("kind = %s, want Float", n.Kind())
			}
			if n.IsInf() || n.IsNaN() {
				t.Fatalf("%q unexpectedly non-finite", c.lex)
			}
			if n.Negative() != c.neg {
				t.Errorf("negative = %v, want %v", n.Negative(), c.neg)
			}
			if got := n.String(); got != c.canon {
				t.Errorf("canonical = %q, want %q", got, c.canon)
			}
			r, err := n.Rat()
			if err != nil {
				t.Fatalf("Rat: %v", err)
			}
			if r.Cmp(mustRat(t, c.rat)) != 0 {
				t.Errorf("Rat = %s, want %s", r, c.rat)
			}

			// A float, even an integral one like 1e3, is not an integer.
			if _, err := n.Int64(); !errors.Is(err, ErrNumberNotInteger) {
				t.Errorf("Int64 err = %v, want ErrNumberNotInteger", err)
			}
			if _, err := n.BigInt(); !errors.Is(err, ErrNumberNotInteger) {
				t.Errorf("BigInt err = %v, want ErrNumberNotInteger", err)
			}
		})
	}
}

func TestParseNumberSignedZero(t *testing.T) {
	for _, c := range []struct {
		lex string
		neg bool
	}{
		{"0.0", false}, {"+0.0", false}, {"-0.0", true},
		{"0e0", false}, {"-0e0", true}, {"-0.0e0", true},
	} {
		t.Run(c.lex, func(t *testing.T) {
			n := mustParseNumber(t, c.lex)
			if n.Negative() != c.neg {
				t.Fatalf("negative = %v, want %v", n.Negative(), c.neg)
			}
			r, err := n.Rat()
			if err != nil || r.Sign() != 0 {
				t.Fatalf("Rat = %v, %v, want zero", r, err)
			}

			f, err := n.Float64()
			if err != nil {
				t.Fatalf("Float64: %v", err)
			}
			if f != 0 || math.Signbit(f) != c.neg {
				t.Errorf("Float64 = %v, signbit = %v, want neg %v", f, math.Signbit(f), c.neg)
			}
			f32, err := n.Float32()
			if err != nil || f32 != 0 || math.Signbit(float64(f32)) != c.neg {
				t.Errorf("Float32 = %v (%v), want signed zero neg=%v", f32, err, c.neg)
			}
		})
	}

	// Integer -0 keeps integer kind but carries no meaningful sign.
	ni := mustParseNumber(t, "-0")
	if !ni.IsInt() {
		t.Fatalf("-0 must be an integer")
	}
	if ni.Negative() {
		t.Errorf("integer -0 must not report negative")
	}
}

func TestParseNumberNonFinite(t *testing.T) {
	cases := []struct {
		lex      string
		neg, inf bool
		nan      bool
		canon    string
		f64Check func(float64) bool
	}{
		{"inf", false, true, false, "inf", func(f float64) bool { return math.IsInf(f, 1) }},
		{"+inf", false, true, false, "inf", func(f float64) bool { return math.IsInf(f, 1) }},
		{"-inf", true, true, false, "-inf", func(f float64) bool { return math.IsInf(f, -1) }},
		{"nan", false, false, true, "nan", math.IsNaN},
		{"+nan", false, false, true, "nan", math.IsNaN},
		{"-nan", true, false, true, "-nan", math.IsNaN},
	}
	for _, c := range cases {
		t.Run(c.lex, func(t *testing.T) {
			n := mustParseNumber(t, c.lex)
			if n.IsInf() != c.inf || n.IsNaN() != c.nan {
				t.Fatalf("inf=%v nan=%v", n.IsInf(), n.IsNaN())
			}
			if n.Negative() != c.neg {
				t.Errorf("negative = %v, want %v", n.Negative(), c.neg)
			}
			if n.String() != c.canon {
				t.Errorf("canonical = %q, want %q", n.String(), c.canon)
			}
			if _, err := n.Rat(); !errors.Is(err, ErrNumberNonFinite) {
				t.Errorf("Rat err = %v, want ErrNumberNonFinite", err)
			}
			if _, err := n.Int64(); !errors.Is(err, ErrNumberNotInteger) {
				t.Errorf("Int64 err = %v, want ErrNumberNotInteger", err)
			}
			f, err := n.Float64()
			if err != nil || !c.f64Check(f) {
				t.Errorf("Float64 = %v, %v", f, err)
			}
		})
	}
}

func TestNumberFloatExactness(t *testing.T) {
	// Exactly representable values must succeed without error.
	for _, lex := range []string{"0.5", "0.25", "1.0", "255.0", "0.0", "-0.0",
		"1024.0", "0.75", "3.0"} {
		n := mustParseNumber(t, lex)
		if f, err := n.Float64(); err != nil {
			t.Errorf("Float64(%q) = %v, %v; want exact", lex, f, err)
		}
	}

	// Values that round must be reported as inexact; the returned float is
	// still the best-effort conversion.
	inexact64 := []string{"0.1", "3.14", "1e-400", "5e+22", "1e-100"}
	for _, lex := range inexact64 {
		n := mustParseNumber(t, lex)
		f, err := n.Float64()
		if !errors.Is(err, ErrNumberInexact) {
			t.Errorf("Float64(%q) err = %v, want ErrNumberInexact", lex, err)
		}
		r, _ := n.Rat()
		if bf, _ := new(big.Float).SetRat(r).Float64(); bf != f {
			t.Errorf("Float64(%q) = %v, want best-effort %v", lex, f, bf)
		}
	}

	// 1e-400 rounds all the way to zero; the sign stays positive.
	n := mustParseNumber(t, "1e-400")
	f, err := n.Float64()
	if f != 0 || math.Signbit(f) {
		t.Errorf("Float64(1e-400) = %v, want +0.0", f)
	}
	_ = err

	// float32 is stricter: a 64-bit-exact value may still be inexact there.
	n = mustParseNumber(t, "0.125") // 1/8, exact in both
	if _, err := n.Float32(); err != nil {
		t.Errorf("Float32(0.125): %v", err)
	}
	n = mustParseNumber(t, "1e-300") // representable in float64, underflows float32
	if f32, err := n.Float32(); !errors.Is(err, ErrNumberInexact) || f32 != 0 {
		t.Errorf("Float32(1e-300) = %v, %v; want inexact zero", f32, err)
	}

	// A huge finite decimal overflows float64 to inf.
	n = mustParseNumber(t, "1e400")
	if f, err := n.Float64(); !errors.Is(err, ErrNumberOverflow) || !math.IsInf(f, 1) {
		t.Errorf("Float64(1e400) = %v, %v; want +inf overflow", f, err)
	}

	// Exactness compared against the rational value: 0.1 != its float64.
	n = mustParseNumber(t, "0.1")
	r, _ := n.Rat()
	f, _ = n.Float64()
	if new(big.Rat).SetFloat64(f).Cmp(r) == 0 {
		t.Errorf("0.1's float64 unexpectedly equalled the exact value")
	}
}

func TestParseNumberInvalidSyntax(t *testing.T) {
	// These are all rejected by the TOML grammar; big.Int's own permissive
	// parser must never widen what is accepted.
	invalid := []string{
		"",
		"01", "00", "-01", "+01",
		"1__2", "1_", "_1", "100_",
		"0b", "0b2", "0B1", "0o", "0o8", "0x", "0x1g",
		"+0x1", "-0b1", "+0o7",
		"1.", ".1", "1.2.3", ".",
		"1e", "1e+", "1e-", "1ee2", "1e2e3", "1e+e2",
		"1e2.0", "01.0", "1.2.3e4", "1_2.3_",
		"++1", "--1", "e5", "1E",
		"0X1", "0x1.2", "0b1.0",
		"inf ", " inf", "nan0", "infinity",
		"12345_", "_12345",
	}
	for _, lex := range invalid {
		if _, err := ParseNumber(lex); err == nil {
			t.Errorf("ParseNumber(%q) succeeded, want syntax error", lex)
		}
	}
}

func TestStrictDecodeInterface(t *testing.T) {
	const doc = `
i = 9007199254740993
tenth = 0.1
tiny = 1e-400
nz = -0.0
pz = 0.0
big_exp = 5e+22
hex = 0xdead_beef
oct = 0o777
bin = 0b1010
pinf = inf
ninf = -inf
n = nan
arr = [1, 2.0, -0.0, 0xff]
`

	var v map[string]any
	md, err := NewDecoder(strings.NewReader(doc)).SetStrictNumbers(true).Decode(&v)
	if err != nil {
		t.Fatal(err)
	}

	check := func(key string, kind NumberKind, canon string) Number {
		t.Helper()
		n, ok := v[key].(Number)
		if !ok {
			t.Fatalf("%s decoded as %T, want Number (%s)", key, v[key], md.Type(key))
		}
		if n.Kind() != kind {
			t.Fatalf("%s kind = %s, want %s", key, n.Kind(), kind)
		}
		if n.String() != canon {
			t.Fatalf("%s canonical = %q, want %q", key, n.String(), canon)
		}
		return n
	}

	i := check("i", NumberInteger, "9007199254740993")
	if iv, err := i.Int64(); err != nil || iv != 9007199254740993 {
		t.Errorf("i.Int64 = %d, %v", iv, err)
	}

	tenth := check("tenth", NumberFloat, "0.1")
	if r, _ := tenth.Rat(); r.Cmp(mustRat(t, "1/10")) != 0 {
		t.Errorf("tenth Rat = %s", r)
	}

	tiny := check("tiny", NumberFloat, "1.0e-400")
	if tiny.IsInf() || tiny.IsNaN() {
		t.Errorf("tiny must be finite")
	}

	nz := check("nz", NumberFloat, "-0.0")
	if !nz.Negative() {
		t.Errorf("nz must be negative zero")
	}
	pz := check("pz", NumberFloat, "0.0")
	if pz.Negative() {
		t.Errorf("pz must be positive zero")
	}

	check("big_exp", NumberFloat, "5.0e22")
	check("hex", NumberInteger, "3735928559")
	if h := v["hex"].(Number).Lexeme(); h != "0xdead_beef" {
		t.Errorf("hex lexeme = %q", h)
	}
	check("oct", NumberInteger, "511")
	check("bin", NumberInteger, "10")

	pinf := check("pinf", NumberFloat, "inf")
	if !pinf.IsInf() || pinf.Negative() {
		t.Errorf("pinf = %+v", pinf)
	}
	ninf := check("ninf", NumberFloat, "-inf")
	if !ninf.IsInf() || !ninf.Negative() {
		t.Errorf("ninf = %+v", ninf)
	}
	nv := check("n", NumberFloat, "nan")
	if !nv.IsNaN() {
		t.Errorf("n not NaN")
	}

	arr, ok := v["arr"].([]any)
	if !ok || len(arr) != 4 {
		t.Fatalf("arr = %#v", v["arr"])
	}
	wantKinds := []NumberKind{NumberInteger, NumberFloat, NumberFloat, NumberInteger}
	for i, e := range arr {
		n, ok := e.(Number)
		if !ok {
			t.Fatalf("arr[%d] = %T", i, e)
		}
		if n.Kind() != wantKinds[i] {
			t.Errorf("arr[%d] kind = %s, want %s", i, n.Kind(), wantKinds[i])
		}
	}
	if arr[2].(Number).Negative() != true {
		t.Errorf("arr[2] should be -0.0")
	}
}

// Default decoding must keep producing int64/float64 in interface values.
func TestDefaultDecodeUnchanged(t *testing.T) {
	var v map[string]any
	_, err := Decode(`i = 9007199254740993
f = 0.1
nz = -0.0
h = 0xff
`, &v)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := v["i"].(int64); !ok {
		t.Errorf("i = %T, want int64", v["i"])
	}
	if _, ok := v["f"].(float64); !ok {
		t.Errorf("f = %T, want float64", v["f"])
	}
	if nz, ok := v["nz"].(float64); !ok || nz != 0 || !math.Signbit(nz) {
		t.Errorf("nz = %v, want -0.0 float64", v["nz"])
	}
	if _, ok := v["h"].(int64); !ok {
		t.Errorf("h = %T, want int64", v["h"])
	}

	// Unmarshal (module-level) is also unchanged.
	var u map[string]any
	if err := Unmarshal([]byte("x = 1"), &u); err != nil {
		t.Fatal(err)
	}
	if _, ok := u["x"].(int64); !ok {
		t.Errorf("Unmarshal x = %T, want int64", u["x"])
	}
}

type strictTypes struct {
	N   Number
	BI  big.Int
	BR  big.Rat
	I8  int8
	I64 int64
	U64 uint64
	F64 float64
	F32 float32
}

func TestStrictDecodeTypedTargets(t *testing.T) {
	const doc = `
n = 0xdead_beef
bi = 9007199254740993
br = 0.1
i8 = 127
i64 = -9223372036854775808
u64 = 9223372036854775807
f64 = 0.5
f32 = 1.5
`
	var s strictTypes
	if _, err := NewDecoder(strings.NewReader(doc)).SetStrictNumbers(true).Decode(&s); err != nil {
		t.Fatal(err)
	}

	if s.N.Lexeme() != "0xdead_beef" || !s.N.IsInt() {
		t.Errorf("N = %+v", s.N)
	}
	if s.BI.String() != "9007199254740993" {
		t.Errorf("BI = %s", &s.BI)
	}
	if s.BR.Cmp(mustRat(t, "1/10")) != 0 {
		t.Errorf("BR = %s", &s.BR)
	}
	if s.I8 != 127 || s.I64 != math.MinInt64 || s.U64 != math.MaxInt64 {
		t.Errorf("ints = %d %d %d", s.I8, s.I64, s.U64)
	}
	if s.F64 != 0.5 || s.F32 != 1.5 {
		t.Errorf("floats = %v %v", s.F64, s.F32)
	}
}

func TestStrictDecodeTypedFailures(t *testing.T) {
	cases := []struct {
		name, doc, field string
		want             error
	}{
		{"int8 overflow", "i8 = 128", "i8", ErrNumberOverflow},
		{"uint negative", "u64 = -1", "u64", ErrNumberOverflow},
		{"int from float", "i64 = 1.0", "i64", nil},
		{"bigint from float", "bi = 1.0", "bi", nil},
		{"bigrat from inf", "br = inf", "br", ErrNumberNonFinite},
		{"float32 overflow", "f32 = 1e40", "f32", nil},
		{"unsafe integer to float", "f64 = 9007199254740993", "f64", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var s strictTypes
			_, err := NewDecoder(strings.NewReader(c.doc)).SetStrictNumbers(true).Decode(&s)
			switch c.field {
			case "i8", "u64", "f64", "f32", "i64", "bi", "br":
				if err == nil {
					t.Fatalf("decoding %q succeeded, want error", c.doc)
				}
			}
			if c.want != nil && !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want wrapping %v", err, c.want)
			}
		})
	}

	// The "unsafe integer to float" path reports the dedicated range error.
	var s strictTypes
	_, err := NewDecoder(strings.NewReader("f64 = 9007199254740993")).SetStrictNumbers(true).Decode(&s)
	if err == nil || !strings.Contains(err.Error(), "out of the safe float64 range") {
		t.Fatalf("err = %v, want unsafe-float error", err)
	}

	// 1e40 is a float too large for float32.
	var s32 struct{ F float32 }
	_, err = NewDecoder(strings.NewReader("f = 1e40")).SetStrictNumbers(true).Decode(&s32)
	if err == nil {
		t.Fatalf("float32 overflow accepted")
	}
}

// Finite decimals that round into an ordinary Go float do not error in loose
// fields: exactness checks live on Number itself.
func TestStrictDecodeLooseFloatField(t *testing.T) {
	var s struct{ F float64 }
	if _, err := NewDecoder(strings.NewReader("f = 0.1")).SetStrictNumbers(true).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.F != 0.1 {
		t.Errorf("f = %v", s.F)
	}
}

func TestStrictDecodePointerAndInterfaceTargets(t *testing.T) {
	var s struct {
		PI  *big.Int
		PR  *big.Rat
		PN  *Number
		Any any
	}
	doc := `
pi = 9007199254740993
pr = 0.1
pn = -0.0
any = 0x10
`
	if _, err := NewDecoder(strings.NewReader(doc)).SetStrictNumbers(true).Decode(&s); err != nil {
		t.Fatal(err)
	}
	if s.PI == nil || s.PI.String() != "9007199254740993" {
		t.Errorf("PI = %v", s.PI)
	}
	if s.PR == nil || s.PR.Cmp(mustRat(t, "1/10")) != 0 {
		t.Errorf("PR = %v", s.PR)
	}
	if s.PN == nil || !s.PN.Negative() {
		t.Errorf("PN = %v", s.PN)
	}
	n, ok := s.Any.(Number)
	if !ok || n.String() != "16" {
		t.Errorf("Any = %#v", s.Any)
	}

	// Non-finite into *big.Rat must fail with the sentinel.
	var bad struct{ PR *big.Rat }
	_, err := NewDecoder(strings.NewReader("pr = nan")).SetStrictNumbers(true).Decode(&bad)
	if !errors.Is(err, ErrNumberNonFinite) {
		t.Fatalf("err = %v, want ErrNumberNonFinite", err)
	}
}

func TestStrictDecodeMapOfBigValues(t *testing.T) {
	var m map[string]big.Int
	if _, err := NewDecoder(strings.NewReader("a = 9007199254740993\nb = 0xff\n")).
		SetStrictNumbers(true).Decode(&m); err != nil {
		t.Fatal(err)
	}
	a, b := m["a"], m["b"]
	if a.String() != "9007199254740993" {
		t.Errorf("m[a] = %s", a.String())
	}
	if b.String() != "255" {
		t.Errorf("m[b] = %s", b.String())
	}

	var r map[string]big.Rat
	if _, err := NewDecoder(strings.NewReader("x = 0.1\ny = 1.5\n")).
		SetStrictNumbers(true).Decode(&r); err != nil {
		t.Fatal(err)
	}
	x, y := r["x"], r["y"]
	if x.Cmp(mustRat(t, "1/10")) != 0 || y.Cmp(mustRat(t, "3/2")) != 0 {
		t.Errorf("rat map = %v", r)
	}

	// inf/nan into a map[string]big.Rat must fail.
	var bad map[string]big.Rat
	_, err := NewDecoder(strings.NewReader("x = inf")).SetStrictNumbers(true).Decode(&bad)
	if !errors.Is(err, ErrNumberNonFinite) {
		t.Fatalf("err = %v, want ErrNumberNonFinite", err)
	}
}

func TestStrictSyntaxErrorsMatchDefault(t *testing.T) {
	// Lexically invalid numbers are rejected identically with and without
	// exact mode; big.Int's lenient parser must not extend the grammar.
	for _, doc := range []string{
		"x = 01",
		"x = 1__2",
		"x = 0b2",
		"x = 1.",
		"x = 1e",
		"x = 9223372036854775808",
	} {
		var a, b map[string]any
		_, errA := Decode(doc, &a)
		_, errB := NewDecoder(strings.NewReader(doc)).SetStrictNumbers(true).Decode(&b)
		if errA == nil {
			t.Errorf("default accepted invalid %q", doc)
			continue
		}
		if errB == nil {
			t.Errorf("strict accepted invalid %q", doc)
		}
	}
}

func TestEncodeNumberVerbatim(t *testing.T) {
	const doc = `
a = 0xdead_beef
b = 1_000
c = 5e+22
d = -0.0
e = 0.1
f = 1e-400
g = inf
h = -nan
i = 0o777
`
	var v map[string]any
	if _, err := NewDecoder(strings.NewReader(doc)).SetStrictNumbers(true).Decode(&v); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(v); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	// Verbatim encoding preserves the original lexemes.
	for _, frag := range []string{
		"a = 0xdead_beef",
		"b = 1_000",
		"c = 5e+22",
		"d = -0.0",
		"e = 0.1",
		"f = 1e-400",
		"g = inf",
		"h = -nan",
		"i = 0o777",
	} {
		if !strings.Contains(out, frag) {
			t.Errorf("verbatim output missing %q in:\n%s", frag, out)
		}
	}

	// Re-decoding keeps the same kinds and exact values.
	var again map[string]any
	if _, err := NewDecoder(strings.NewReader(out)).SetStrictNumbers(true).Decode(&again); err != nil {
		t.Fatalf("re-decode: %v\n%s", err, out)
	}
	for key, val := range v {
		n1 := val.(Number)
		n2 := again[key].(Number)
		if n1.Kind() != n2.Kind() {
			t.Errorf("key %s kind changed: %s -> %s", key, n1.Kind(), n2.Kind())
		}
		r1, _ := n1.Rat()
		r2, _ := n2.Rat()
		if r1 != nil && (r2 == nil || r1.Cmp(r2) != 0) {
			t.Errorf("key %s value changed: %s -> %s", key, r1, r2)
		}
		if n1.Negative() != n2.Negative() || n1.IsInf() != n2.IsInf() || n1.IsNaN() != n2.IsNaN() {
			t.Errorf("key %s sign/special changed", key)
		}
	}
}

func TestEncodeNumberCanonical(t *testing.T) {
	const doc = `
a = 0xdead_beef
b = 1_000
c = 5e+22
d = -0.0
e = 0.1
f = 1e-400
g = -inf
h = 6.626e-34
`
	var v map[string]any
	if _, err := NewDecoder(strings.NewReader(doc)).SetStrictNumbers(true).Decode(&v); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := NewEncoder(&buf).CanonicalNumbers().Encode(v); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	want := []string{
		"a = 3735928559",
		"b = 1000",
		"c = 5.0e22",
		"d = -0.0",
		"e = 0.1",
		"f = 1.0e-400",
		"g = -inf",
		"h = 6.626e-34",
	}
	for _, frag := range want {
		if !strings.Contains(out, frag) {
			t.Errorf("canonical output missing %q in:\n%s", frag, out)
		}
	}
	for _, frag := range []string{"0x", "_", "e+22"} {
		if strings.Contains(out, frag) {
			t.Errorf("canonical output should not contain %q:\n%s", frag, out)
		}
	}

	// Canonical output is itself valid TOML and round-trips with identical
	// kinds and values through both strict and default decoding.
	var strict map[string]any
	if _, err := NewDecoder(strings.NewReader(out)).SetStrictNumbers(true).Decode(&strict); err != nil {
		t.Fatalf("strict re-decode: %v\n%s", err, out)
	}
	var loose map[string]any
	if _, err := Decode(out, &loose); err != nil {
		t.Fatalf("loose re-decode: %v\n%s", err, out)
	}

	n := strict["d"].(Number)
	if !n.Negative() {
		t.Errorf("canonical negative zero lost its sign")
	}
	if looseN := loose["d"].(float64); looseN != 0 || !math.Signbit(looseN) {
		t.Errorf("loose decode of -0.0 = %v", looseN)
	}

	// Integer stays an integer (no accidental ".0"), float stays a float.
	if strict["a"].(Number).Kind() != NumberInteger {
		t.Errorf("a must remain integer")
	}
	if strict["c"].(Number).Kind() != NumberFloat {
		t.Errorf("c must remain float")
	}
	if _, ok := loose["a"].(int64); !ok {
		t.Errorf("loose a = %T, want int64", loose["a"])
	}
	if _, ok := loose["c"].(float64); !ok {
		t.Errorf("loose c = %T, want float64", loose["c"])
	}

	// Exact values survive canonical round-trip.
	if r, _ := strict["e"].(Number).Rat(); r.Cmp(mustRat(t, "1/10")) != 0 {
		t.Errorf("e = %s", r)
	}
	if r, _ := strict["f"].(Number).Rat(); r.Cmp(mustRat(t, "1e-400")) != 0 {
		t.Errorf("f = %s", r)
	}
}

func TestEncodeStructWithNumbers(t *testing.T) {
	type row struct {
		Hex  Number
		Tiny Number
		NZ   Number
	}
	var r row
	_, err := NewDecoder(strings.NewReader("hex = 0xFF\ntiny = 1e-400\nnz = -0.0\n")).
		SetStrictNumbers(true).Decode(&r)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := NewEncoder(&buf).Encode(r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Hex = 0xFF") ||
		!strings.Contains(out, "Tiny = 1e-400") ||
		!strings.Contains(out, "NZ = -0.0") {
		t.Fatalf("struct encode:\n%s", out)
	}

	// Arrays mixing integers and floats encode with their own spellings.
	var withArr struct {
		Nums []Number
	}
	_, err = NewDecoder(strings.NewReader("nums = [0xFF, 1.0, -0.0, 9007199254740993]\n")).
		SetStrictNumbers(true).Decode(&withArr)
	if err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := NewEncoder(&buf).Encode(withArr); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), "Nums = [0xFF, 1.0, -0.0, 9007199254740993]\n"; got != want {
		t.Fatalf("array encode = %q, want %q", got, want)
	}
}
