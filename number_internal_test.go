package toml

import (
	"math"
	"math/big"
	"testing"
)

func decodeOne(t *testing.T, src string) any {
	t.Helper()
	p, err := parse(src, 128, true)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return p.mapping["x"]
}

// The exact decimal spelling of the smallest positive subnormal float64
// (2^-1074) must convert with no rounding error.
func TestExactSubnormalFloat64(t *testing.T) {
	smallest := exactFloatRat(5e-324, 64) // exact value of the float64 literal 5e-324.
	src := "x = " + smallest.FloatString(1100)
	n := decodeOne(t, src).(Number)

	f, err := n.Float64()
	if err != nil {
		t.Fatalf("Float64 err = %v; want exact 2^-1074", err)
	}
	if bits := math.Float64bits(f); bits != 1 {
		t.Fatalf("Float64 bits = %#x, want 0x1", bits)
	}

	// float32 has no subnormal this small: nearest value is zero.
	f32, err := n.Float32()
	if f32 != 0 || err == nil {
		t.Fatalf("Float32 = %v, %v; want 0 and rounding error", f32, err)
	}

	// A decimal spelling with one extra trailing digit is not exact.
	n2 := decodeOne(t, "x = 5e-324").(Number)
	if f, err := n2.Float64(); f != 5e-324 || err == nil {
		t.Fatalf("5e-324 Float64 = %v, %v; want rounded", f, err)
	}
}

func TestExactFloatRatBasics(t *testing.T) {
	if r := exactFloatRat(0, 64); r.Sign() != 0 {
		t.Fatalf("exact 0 = %s", r)
	}
	if r := exactFloatRat(0.5, 64); r.RatString() != "1/2" {
		t.Fatalf("exact 0.5 = %s", r)
	}
	if r := exactFloatRat(1, 32); r.RatString() != "1" {
		t.Fatalf("exact float32(1) = %s", r)
	}
	// float32(0.1) is exactly the binary32 value:
	// (-1)^0 * 2^(123-127-23) * (2^23 + 0x4ccccd) = 13421773 / 2^27.
	want, _ := new(big.Rat).SetString("13421773/134217728")
	if r := exactFloatRat(float64(float32(0.1)), 32); r.Cmp(want) != 0 {
		t.Fatalf("exact float32(0.1) = %s, want %s", r, want)
	}
}
