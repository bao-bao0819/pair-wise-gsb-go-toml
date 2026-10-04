package toml_test

import (
	"bytes"
	"errors"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func strictDecode(t *testing.T, input string, dst any) {
	t.Helper()
	if _, err := toml.NewDecoder(strings.NewReader(input)).SetStrictNumbers(true).Decode(dst); err != nil {
		t.Fatalf("decode %q: %v", input, err)
	}
}

func numberAt(t *testing.T, m map[string]any, key string) toml.Number {
	t.Helper()
	n, ok := m[key].(toml.Number)
	if !ok {
		t.Fatalf("key %q: expected toml.Number, got %T (%v)", key, m[key], m[key])
	}
	return n
}

func ratEqual(t *testing.T, n toml.Number, want *big.Rat) {
	t.Helper()
	r, err := n.Rat()
	if err != nil {
		t.Fatalf("Rat(%q): %v", n.Lexeme(), err)
	}
	if r.Cmp(want) != 0 {
		t.Fatalf("Rat(%q) = %s, want %s", n.Lexeme(), r, want)
	}
}

func mustRat(s string) *big.Rat {
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		panic("bad rational: " + s)
	}
	return r
}

// An integer above the contiguous float64 integer range (2^53) but still a
// legal TOML integer must keep its exact value and integer kind.
func TestStrictLargeInteger(t *testing.T) {
	var m map[string]any
	strictDecode(t, "big = 9007199254740993\nbigger = 9223372036854775807\n", &m)

	n := numberAt(t, m, "big")
	if n.Kind() != toml.NumberInteger {
		t.Fatalf("kind = %s", n.Kind())
	}
	if n.Lexeme() != "9007199254740993" {
		t.Fatalf("lexeme = %q", n.Lexeme())
	}
	ratEqual(t, n, mustRat("9007199254740993"))

	bi, err := n.BigInt()
	if err != nil || bi.String() != "9007199254740993" {
		t.Fatalf("BigInt = %v, %v", bi, err)
	}
	i64, err := n.Int64()
	if err != nil || i64 != 9007199254740993 {
		t.Fatalf("Int64 = %d, %v", i64, err)
	}

	// float64 cannot represent this integer exactly: the nearest value is
	// 9007199254740992 and the loss must be reported.
	f, ferr := n.Float64()
	if !errors.Is(ferr, toml.ErrNumberRounded) {
		t.Fatalf("Float64 err = %v, want ErrNumberRounded", ferr)
	}
	if f != 9007199254740992.0 {
		t.Fatalf("Float64 = %v, want nearest 9007199254740992", f)
	}

	nMax := numberAt(t, m, "bigger")
	if _, err := nMax.Int64(); err != nil {
		t.Fatalf("Int64 max: %v", err)
	}

	// One more than int64 max is a parse error: integers stay within signed
	// 64-bit per TOML.
	var overflow map[string]any
	_, err = toml.NewDecoder(strings.NewReader("x = 9223372036854775808")).SetStrictNumbers(true).Decode(&overflow)
	if err == nil || !strings.Contains(err.Error(), "out of range for int64") {
		t.Fatalf("expected int64 range parse error, got %v", err)
	}
}

// A huge but legal lexical integer beyond int64 must still be rejected.
func TestStrictIntegerBeyondInt64Rejected(t *testing.T) {
	var m map[string]any
	_, err := toml.NewDecoder(strings.NewReader("x = 1_000_000_000_000_000_000_000")).SetStrictNumbers(true).Decode(&m)
	if err == nil {
		t.Fatal("expected parse error for integer beyond int64")
	}
}

// Non-decimal integers keep lexeme and kind and convert exactly; canonical
// encoding renders them as decimal while keeping the integer kind.
func TestStrictNonDecimalIntegers(t *testing.T) {
	input := "hex = 0xDEAD_BEEF\noct = 0o777\nbin = 0b1101\nexact = 0x1_0000\n"
	var m map[string]any
	strictDecode(t, input, &m)

	hex := numberAt(t, m, "hex")
	if hex.Lexeme() != "0xDEAD_BEEF" {
		t.Fatalf("lexeme = %q", hex.Lexeme())
	}
	bi, _ := hex.BigInt()
	if bi.String() != "3735928559" {
		t.Fatalf("hex BigInt = %s", bi)
	}
	// Even though 0xDEADBEEF is above 2^32, its value happens to be exactly
	// representable in float64 (its significand needs no dropped bits).
	f, ferr := hex.Float64()
	if ferr != nil || f != float64(3735928559) {
		t.Fatalf("hex Float64 = %v, %v", f, ferr)
	}

	// 0x10000 = 65536 is exactly representable and must convert without error.
	exact := numberAt(t, m, "exact")
	f, ferr = exact.Float64()
	if ferr != nil || f != 65536.0 {
		t.Fatalf("exact Float64 = %v, %v", f, ferr)
	}

	oct := numberAt(t, m, "oct")
	bi, _ = oct.BigInt()
	if bi.String() != "511" {
		t.Fatalf("oct BigInt = %s", bi)
	}

	bin := numberAt(t, m, "bin")
	bi, _ = bin.BigInt()
	if bi.String() != "13" {
		t.Fatalf("bin BigInt = %s", bi)
	}

	buf, err := toml.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(buf), "hex = 3735928559\n") {
		t.Fatalf("canonical encoding missing decimal hex:\n%s", buf)
	}
	if !strings.Contains(string(buf), "bin = 13\n") {
		t.Fatalf("canonical encoding missing decimal bin:\n%s", buf)
	}

	var again map[string]any
	if _, err := toml.NewDecoder(bytes.NewReader(buf)).SetStrictNumbers(true).Decode(&again); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	n2 := numberAt(t, again, "hex")
	if n2.Kind() != toml.NumberInteger {
		t.Fatalf("re-decoded kind = %s", n2.Kind())
	}
	ratEqual(t, n2, mustRat("3735928559"))
}

// 0.1 is not exactly a float64; a tiny finite float is exact as float64 but
// rounds to zero as float32; -0.0 keeps its sign.
func TestStrictFiniteFloatExactness(t *testing.T) {
	var m map[string]any
	strictDecode(t, "one = 0.1\ntiny = 4.9406564584124654417656879286822137236505980e-324\nshort = 5e-324\nnegzero = -0.0\nposzero = 0.0\nunder = 1e-325\n", &m)

	one := numberAt(t, m, "one")
	if one.Kind() != toml.NumberFloat {
		t.Fatalf("kind = %s", one.Kind())
	}
	ratEqual(t, one, mustRat("1/10"))

	f, ferr := one.Float64()
	if f != 0.1 {
		t.Fatalf("Float64 = %v", f)
	}
	if !errors.Is(ferr, toml.ErrNumberRounded) {
		t.Fatalf("Float64 err = %v, want ErrNumberRounded", ferr)
	}

	// A float Number cannot be converted to integer types, even 1.0-style ones.
	if _, err := one.BigInt(); !errors.Is(err, toml.ErrNumberKind) {
		t.Fatalf("BigInt err = %v, want ErrNumberKind", err)
	}
	if _, err := one.Int64(); !errors.Is(err, toml.ErrNumberKind) {
		t.Fatalf("Int64 err = %v, want ErrNumberKind", err)
	}
	if _, err := one.Uint64(); !errors.Is(err, toml.ErrNumberKind) {
		t.Fatalf("Uint64 err = %v, want ErrNumberKind", err)
	}

	// Smallest positive subnormal float64: the shortest decimal spelling
	// rounds, but parses to 5e-324; the float32 conversion rounds to zero.
	tiny := numberAt(t, m, "tiny")
	f, ferr = tiny.Float64()
	if f != 5e-324 || !errors.Is(ferr, toml.ErrNumberRounded) {
		t.Fatalf("tiny Float64 = %v, %v", f, ferr)
	}
	f32, ferr := tiny.Float32()
	if !errors.Is(ferr, toml.ErrNumberRounded) || f32 != 0 {
		t.Fatalf("tiny Float32 = %v, %v; want 0 + ErrNumberRounded", f32, ferr)
	}

	_ = numberAt(t, m, "short")

	// Below the subnormal range: nearest float64 is zero, reported as rounding.
	under := numberAt(t, m, "under")
	f, ferr = under.Float64()
	if f != 0 || !errors.Is(ferr, toml.ErrNumberRounded) {
		t.Fatalf("under Float64 = %v, %v; want 0 + ErrNumberRounded", f, ferr)
	}
	r, rerr := under.Rat()
	if rerr != nil || r.Sign() <= 0 {
		t.Fatalf("under Rat = %v, %v; want positive exact value", r, rerr)
	}

	neg := numberAt(t, m, "negzero")
	pos := numberAt(t, m, "poszero")
	if !neg.Signbit() || pos.Signbit() {
		t.Fatalf("sign bits: neg=%v pos=%v", neg.Signbit(), pos.Signbit())
	}
	nf, nerr := neg.Float64()
	if nerr != nil || nf != 0 || !math.Signbit(nf) {
		t.Fatalf("neg zero Float64 = %v sign=%v err=%v", nf, math.Signbit(nf), nerr)
	}
	ratEqual(t, neg, mustRat("0"))
	ratEqual(t, pos, mustRat("0"))
}

// inf/NaN are separately expressed and cannot be turned into integers/Rat.
func TestStrictInfNaN(t *testing.T) {
	var m map[string]any
	strictDecode(t, "a = inf\nb = -inf\nc = nan\nd = -nan\ne = +inf\n", &m)

	pinf := numberAt(t, m, "a")
	ninf := numberAt(t, m, "b")
	nan := numberAt(t, m, "c")
	snan := numberAt(t, m, "d")

	if !pinf.IsInf() || pinf.Signbit() {
		t.Fatal("a should be +inf")
	}
	if !ninf.IsInf() || !ninf.Signbit() {
		t.Fatal("b should be -inf")
	}
	if !nan.IsNaN() || nan.Signbit() {
		t.Fatal("c should be unsigned nan")
	}
	if !snan.IsNaN() || !snan.Signbit() {
		t.Fatal("d should be signed -nan")
	}

	f, err := ninf.Float64()
	if err != nil || !math.IsInf(f, -1) {
		t.Fatalf("-inf Float64 = %v, %v", f, err)
	}
	f, err = nan.Float64()
	if err != nil || !math.IsNaN(f) {
		t.Fatalf("nan Float64 = %v, %v", f, err)
	}

	for name, n := range map[string]toml.Number{"a": pinf, "b": ninf, "c": nan, "d": snan} {
		if _, err := n.Rat(); !errors.Is(err, toml.ErrNumberSpecial) {
			t.Fatalf("%s Rat err = %v, want ErrNumberSpecial", name, err)
		}
		if _, err := n.BigInt(); !errors.Is(err, toml.ErrNumberSpecial) {
			t.Fatalf("%s BigInt err = %v, want ErrNumberSpecial", name, err)
		}
	}
}

// A finite float with a huge exponent is accepted in exact mode even though it
// overflows float64; its Rat still holds the exact decimal.
func TestStrictHugeExponentFloat(t *testing.T) {
	var m map[string]any
	strictDecode(t, "huge = 1e400\nneg = -2.5e500\n", &m)

	huge := numberAt(t, m, "huge")
	f, err := huge.Float64()
	if !math.IsInf(f, 1) || !errors.Is(err, toml.ErrNumberOverflow) {
		t.Fatalf("huge Float64 = %v, %v; want +Inf + ErrNumberOverflow", f, err)
	}
	r, err := huge.Rat()
	if err != nil || !r.IsInt() {
		t.Fatalf("huge Rat = %v, %v; want exact integer 10^400", r, err)
	}
	if bi := r.Num(); bi.Cmp(new(big.Int).Exp(big.NewInt(10), big.NewInt(400), nil)) != 0 {
		t.Fatalf("huge Rat != 10^400")
	}

	neg := numberAt(t, m, "neg")
	f, err = neg.Float64()
	if !math.IsInf(f, -1) || !errors.Is(err, toml.ErrNumberOverflow) {
		t.Fatalf("neg Float64 = %v, %v", f, err)
	}

	// Same input must be rejected by the default decoder (out of float64).
	var def map[string]any
	_, derr := toml.Decode("huge = 1e400\n", &def)
	if derr == nil {
		t.Fatal("default decoder must reject 1e400")
	}
}

// Grammar checks still reject underscores/prefixes that big.Int would accept
// loosely; the parser does not widen the accepted syntax.
func TestStrictNumberGrammarRejected(t *testing.T) {
	bad := []string{
		"x = 0123\n",
		"x = 0x_12\n",
		"x = 0x1__2\n",
		"x = 1__2\n",
		"x = 1_\n",
		"x = _1\n",
		"x = 1.2.3\n",
		"x = 1.\n",
		"x = .1\n",
		"x = 1e\n",
		"x = +0x10\n",
		"x = 0b2\n",
		"x = 0o9\n",
		"x = 1e_1\n",
		"x = 1e+_1\n",
		"x = 1_e2\n",
		"x = 1e1_\n",
	}
	for _, src := range bad {
		var m map[string]any
		_, err := toml.NewDecoder(strings.NewReader(src)).SetStrictNumbers(true).Decode(&m)
		if err == nil {
			t.Errorf("expected parse error for %q, got %v", src, m)
		}
	}
}

// Default decoding keeps its historical types; exact mode is opt-in only.
func TestDefaultModeUnchanged(t *testing.T) {
	var m map[string]any
	if _, err := toml.Decode("i = 9007199254740993\nf = 0.1\nnz = -0.0\nhex = 0xFF\ninf = inf\n", &m); err != nil {
		t.Fatal(err)
	}
	if i, ok := m["i"].(int64); !ok || i != 9007199254740993 {
		t.Fatalf("i = %#v", m["i"])
	}
	if f, ok := m["f"].(float64); !ok || f != 0.1 {
		t.Fatalf("f = %#v", m["f"])
	}
	if f, ok := m["nz"].(float64); !ok || f != 0 || !math.Signbit(f) {
		t.Fatalf("nz = %#v", m["nz"])
	}
	if i, ok := m["hex"].(int64); !ok || i != 255 {
		t.Fatalf("hex = %#v", m["hex"])
	}
	if f, ok := m["inf"].(float64); !ok || !math.IsInf(f, 1) {
		t.Fatalf("inf = %#v", m["inf"])
	}
}

// Typed destinations work in both modes and give exact conversions plus
// reported overflow/rounding via the returned decode error.
func TestTypedDestinations(t *testing.T) {
	type Dst struct {
		N   toml.Number
		I   big.Int
		R   big.Rat
		I64 int64
		U64 uint64
		I8  int8
		F64 float64
		F32 float32
	}

	var d Dst
	if _, err := toml.Decode("n = 0.1\ni = 9007199254740993\nr = 0.5\ni64 = 42\nu64 = 7\ni8 = 5\nf64 = 1.5\nf32 = 1.5\n", &d); err != nil {
		t.Fatal(err)
	}
	if d.N.Kind() != toml.NumberFloat || d.N.Lexeme() != "0.1" {
		t.Fatalf("N = %#v", d.N)
	}
	if d.I.String() != "9007199254740993" {
		t.Fatalf("I = %v", d.I.String())
	}
	wantRat, _ := new(big.Rat).SetString("1/2")
	if d.R.Cmp(wantRat) != 0 {
		t.Fatalf("R = %v", d.R.String())
	}
	if d.I64 != 42 || d.U64 != 7 || d.I8 != 5 || d.F64 != 1.5 || d.F32 != 1.5 {
		t.Fatalf("scalars = %#v", d)
	}

	// big.Int refuses a float.
	var badInt struct{ X big.Int }
	if _, err := toml.Decode("x = 1.0\n", &badInt); err == nil {
		t.Fatal("expected error decoding float into big.Int")
	}

	// big.Rat refuses inf.
	var badRat struct{ X big.Rat }
	if _, err := toml.Decode("x = inf\n", &badRat); err == nil {
		t.Fatal("expected error decoding inf into big.Rat")
	}

	// A float decoded into float64 with rounding: 0.1 rounds but the default
	// loose conversion accepts it; in exact mode the rounding error surfaces.
	var loose struct{ X float64 }
	if _, err := toml.Decode("x = 0.1\n", &loose); err != nil {
		t.Fatalf("default float64 decode: %v", err)
	}
	_, err := toml.NewDecoder(strings.NewReader("x = 0.1\n")).SetStrictNumbers(true).Decode(new(struct{ X float64 }))
	if err == nil || !strings.Contains(err.Error(), "cannot be represented exactly") {
		t.Fatalf("exact float64 rounding error expected, got %v", err)
	}

	// int8 overflow from a large exact integer.
	var tinyInt struct{ X int8 }
	_, err = toml.NewDecoder(strings.NewReader("x = 300\n")).SetStrictNumbers(true).Decode(&tinyInt)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("int8 overflow expected, got %v", err)
	}

	// uint64 refuses a negative integer.
	var negUint struct{ X uint64 }
	_, err = toml.NewDecoder(strings.NewReader("x = -1\n")).SetStrictNumbers(true).Decode(&negUint)
	if err == nil || !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("uint64 negative expected, got %v", err)
	}

	// *big.Int accepts exact large integers beyond float64 contiguous range.
	var bigDst struct{ X big.Int }
	if _, err := toml.Decode("x = 9007199254740993\n", &bigDst); err != nil {
		t.Fatalf("big.Int decode: %v", err)
	}
	if bigDst.X.String() != "9007199254740993" {
		t.Fatalf("big.Int = %v", bigDst.X.String())
	}
}

// Exact-mode *big.Rat gets the true decimal, without float64 in between.
func TestExactBigRatDecimal(t *testing.T) {
	var d struct{ R big.Rat }
	if _, err := toml.NewDecoder(strings.NewReader("r = 0.1\n")).SetStrictNumbers(true).Decode(&d); err != nil {
		t.Fatal(err)
	}
	want, _ := new(big.Rat).SetString("1/10")
	if d.R.Cmp(want) != 0 {
		t.Fatalf("R = %s, want 1/10", d.R.String())
	}
}

// Encoding a decoded container can preserve the original lexeme or emit a
// canonical form; re-decoding either keeps the kind and numeric semantics.
func TestEncodeNumberStyles(t *testing.T) {
	input := strings.Join([]string{
		"hex = 0xDEAD_BEEF",
		"us = 1_000",
		"one = 0.1",
		"nz = -0.0",
		"exp = 3E2",
		"big = 9007199254740993",
		"pinf = inf",
		"ninf = -inf",
		"nan = nan",
		"snan = -nan",
		"plain = 42.0",
		"",
	}, "\n")

	var m map[string]any
	strictDecode(t, input, &m)

	var lex bytes.Buffer
	if err := toml.NewEncoder(&lex).SetNumberStyle(toml.NumberStyleLexical).Encode(m); err != nil {
		t.Fatalf("lexical encode: %v", err)
	}
	var can bytes.Buffer
	if err := toml.NewEncoder(&can).SetNumberStyle(toml.NumberStyleCanonical).Encode(m); err != nil {
		t.Fatalf("canonical encode: %v", err)
	}

	// Lexical style reproduces the original spelling.
	for _, want := range []string{
		"hex = 0xDEAD_BEEF\n",
		"us = 1_000\n",
		"one = 0.1\n",
		"nz = -0.0\n",
		"exp = 3E2\n",
		"snan = -nan\n",
	} {
		if !strings.Contains(lex.String(), want) {
			t.Errorf("lexical output missing %q in:\n%s", want, lex.String())
		}
	}

	// Canonical style normalizes.
	for _, want := range []string{
		"hex = 3735928559\n",
		"us = 1000\n",
		"one = 0.1\n",
		"nz = -0.0\n",
		"exp = 3e2\n",
		"big = 9007199254740993\n",
		"plain = 42.0\n",
		"pinf = inf\n",
		"snan = -nan\n",
	} {
		if !strings.Contains(can.String(), want) {
			t.Errorf("canonical output missing %q in:\n%s", want, can.String())
		}
	}

	// Re-decode both outputs in exact mode and compare kinds and exact values.
	for name, doc := range map[string]string{"lexical": lex.String(), "canonical": can.String()} {
		var again map[string]any
		strictDecode(t, doc, &again)
		for k, v := range m {
			a := v.(toml.Number)
			b := again[k].(toml.Number)
			if a.Kind() != b.Kind() {
				t.Errorf("%s: %s kind changed: %s -> %s", name, k, a.Kind(), b.Kind())
				continue
			}
			if a.IsInf() != b.IsInf() || a.IsNaN() != b.IsNaN() ||
				a.Signbit() != b.Signbit() {
				t.Errorf("%s: %s specials/sign changed", name, k)
				continue
			}
			ra, ea := a.Rat()
			rb, eb := b.Rat()
			if (ea == nil) != (eb == nil) {
				t.Errorf("%s: %s finiteness changed", name, k)
				continue
			}
			if ea == nil && ra.Cmp(rb) != 0 {
				t.Errorf("%s: %s value changed: %s -> %s", name, k, ra, rb)
			}
		}
	}
}

// Number values inside arrays and structs encode and re-decode consistently.
func TestEncodeNumberNested(t *testing.T) {
	input := "ints = [1, 0x10, 7]\nfloats = [2.0, -0.0, 0.1]\ntab = { a = 3, b = 0.1 }\n"
	var m map[string]any
	strictDecode(t, input, &m)

	buf, err := toml.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(buf), "ints = [1, 16, 7]") ||
		!strings.Contains(string(buf), "floats = [2.0, -0.0, 0.1]") {
		t.Fatalf("nested array encoding:\n%s", buf)
	}

	var again map[string]any
	strictDecode(t, string(buf), &again)
	farr := again["floats"].([]any)
	if farr[1].(toml.Number).Signbit() != true {
		t.Fatal("nested -0.0 lost its sign")
	}
	iarr := again["ints"].([]any)
	if iarr[0].(toml.Number).Kind() != toml.NumberInteger ||
		farr[0].(toml.Number).Kind() != toml.NumberFloat {
		t.Fatal("nested integer/float kinds changed")
	}
}
