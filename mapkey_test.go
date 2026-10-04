package toml

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// A realistic enum key type, as in map[region]Endpoint.
type region int

const (
	regionEUWest region = iota
	regionUSEast
	regionAPSouth
)

func (r region) String() string {
	switch r {
	case regionEUWest:
		return "eu-west"
	case regionUSEast:
		return "us-east"
	case regionAPSouth:
		return "ap-south"
	}
	return "unknown"
}

func parseRegion(s string) (region, error) {
	switch s {
	case "eu-west":
		return regionEUWest, nil
	case "us-east":
		return regionUSEast, nil
	case "ap-south":
		return regionAPSouth, nil
	}
	return 0, fmt.Errorf("unknown region %q", s)
}

var regionCodec = NewMapKeyCodec(
	func(r region) (string, error) { return r.String(), nil },
	parseRegion,
)

// ciKey folds case when converting to and from TOML keys.
type ciKey string

var ciKeyCodec = NewMapKeyCodec(
	func(k ciKey) (string, error) { return strings.ToLower(string(k)), nil },
	func(s string) (ciKey, error) { return ciKey(strings.ToLower(s)), nil },
)

func TestMapKeyCodecRoundTrip(t *testing.T) {
	in := map[region]int{
		regionEUWest:  1,
		regionUSEast:  2,
		regionAPSouth: 3,
	}

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	enc.MapKeyCodecs = []MapKeyCodec{regionCodec}
	if err := enc.Encode(in); err != nil {
		t.Fatalf("Encode: %s", err)
	}

	want := "ap-south = 3\neu-west = 1\nus-east = 2\n"
	if buf.String() != want {
		t.Errorf("wrong output:\nhave: %q\nwant: %q", buf.String(), want)
	}

	var out map[region]int
	dec := NewDecoder(&buf)
	dec.MapKeyCodecs = []MapKeyCodec{regionCodec}
	if _, err := dec.Decode(&out); err != nil {
		t.Fatalf("Decode: %s", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\nhave: %v\nwant: %v", out, in)
	}
}

func TestMapKeyCodecCollision(t *testing.T) {
	// "Foo" and "foo" fold to the same TOML key. Depending on map iteration
	// order this is reported either as a collision or as a non-reversible
	// encoding; both must mention the colliding TOML key.
	m := map[ciKey]int{"Foo": 1, "foo": 2}
	for i := 0; i < 50; i++ {
		var buf bytes.Buffer
		enc := NewEncoder(&buf)
		enc.MapKeyCodecs = []MapKeyCodec{ciKeyCodec}
		err := enc.Encode(m)
		if err == nil {
			t.Fatal("expected an error for case-folding collision")
		}
		if !strings.Contains(err.Error(), `"foo"`) {
			t.Fatalf("error does not mention colliding TOML key: %s", err)
		}
	}
}

func TestMapKeyCodecCollisionMessage(t *testing.T) {
	// Seed the seen-keys map so the collision is detected deterministically,
	// and check both Go keys are identified in the error.
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic")
		}
		terr, ok := r.(tomlEncodeError)
		if !ok {
			t.Fatalf("expected tomlEncodeError, got %T", r)
		}
		for _, want := range []string{"Foo", "foo", "both encode to TOML key"} {
			if !strings.Contains(terr.Error(), want) {
				t.Errorf("error does not contain %s: %s", want, terr)
			}
		}
	}()

	seen := map[string]reflect.Value{"foo": reflect.ValueOf(ciKey("Foo"))}
	encodeMapKey(&ciKeyCodec, reflect.ValueOf(ciKey("foo")), seen)
}

func TestMapKeyCodecQuotedKeys(t *testing.T) {
	// Encoded keys containing dots or quotes must stay a single TOML key:
	// "a.b" is one key, not the path a → b.
	type dotted string
	codec := NewMapKeyCodec(
		func(k dotted) (string, error) { return string(k), nil },
		func(s string) (dotted, error) { return dotted(s), nil },
	)

	in := map[dotted]int{
		"a.b":   1,
		`q"q`:   2,
		"plain": 3,
	}

	var buf bytes.Buffer
	enc := NewEncoder(&buf)
	enc.MapKeyCodecs = []MapKeyCodec{codec}
	if err := enc.Encode(in); err != nil {
		t.Fatalf("Encode: %s", err)
	}

	want := "\"a.b\" = 1\nplain = 3\n\"q\\\"q\" = 2\n"
	if buf.String() != want {
		t.Errorf("wrong output:\nhave: %q\nwant: %q", buf.String(), want)
	}

	var out map[dotted]int
	dec := NewDecoder(&buf)
	dec.MapKeyCodecs = []MapKeyCodec{codec}
	if _, err := dec.Decode(&out); err != nil {
		t.Fatalf("Decode: %s", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round trip mismatch:\nhave: %v\nwant: %v", out, in)
	}
}

func TestMapKeyCodecNotReversible(t *testing.T) {
	t.Run("decode mismatch", func(t *testing.T) {
		codec := NewMapKeyCodec(
			func(k ciKey) (string, error) { return strings.ToUpper(string(k)), nil },
			func(s string) (ciKey, error) { return ciKey(s), nil },
		)
		var buf bytes.Buffer
		enc := NewEncoder(&buf)
		enc.MapKeyCodecs = []MapKeyCodec{codec}
		err := enc.Encode(map[ciKey]int{"abc": 1})
		if !errorContains(err, "must be reversible") {
			t.Fatalf("expected reversibility error, got: %v", err)
		}
	})

	t.Run("decode error", func(t *testing.T) {
		codec := NewMapKeyCodec(
			func(r region) (string, error) { return r.String(), nil },
			func(s string) (region, error) { return 0, errors.New("cannot parse") },
		)
		var buf bytes.Buffer
		enc := NewEncoder(&buf)
		enc.MapKeyCodecs = []MapKeyCodec{codec}
		err := enc.Encode(map[region]int{regionEUWest: 1})
		if !errorContains(err, "must be reversible") || !errorContains(err, "cannot parse") {
			t.Fatalf("expected reversibility error with cause, got: %v", err)
		}
	})
}

func TestMapKeyCodecCallCounts(t *testing.T) {
	// The number of codec calls must not depend on map iteration order:
	// exactly one Encode and one Decode (reversibility check) per key.
	var encCalls, decCalls int
	codec := NewMapKeyCodec(
		func(r region) (string, error) { encCalls++; return r.String(), nil },
		func(s string) (region, error) { decCalls++; return parseRegion(s) },
	)

	m := map[region]int{regionEUWest: 1, regionUSEast: 2, regionAPSouth: 3}
	for i := 0; i < 50; i++ {
		encCalls, decCalls = 0, 0
		var buf bytes.Buffer
		enc := NewEncoder(&buf)
		enc.MapKeyCodecs = []MapKeyCodec{codec}
		if err := enc.Encode(m); err != nil {
			t.Fatalf("Encode: %s", err)
		}
		if encCalls != 3 || decCalls != 3 {
			t.Fatalf("iteration %d: Encode called %d times, Decode %d times; want 3 and 3",
				i, encCalls, decCalls)
		}
	}

	// Decoding calls Decode exactly once per TOML key.
	const doc = "ap-south = 3\neu-west = 1\nus-east = 2\n"
	for i := 0; i < 50; i++ {
		encCalls, decCalls = 0, 0
		var out map[region]int
		dec := NewDecoder(strings.NewReader(doc))
		dec.MapKeyCodecs = []MapKeyCodec{codec}
		if _, err := dec.Decode(&out); err != nil {
			t.Fatalf("Decode: %s", err)
		}
		if encCalls != 0 || decCalls != 3 {
			t.Fatalf("iteration %d: Encode called %d times, Decode %d times; want 0 and 3",
				i, encCalls, decCalls)
		}
	}
}

func TestMapKeyCodecDecodeCollision(t *testing.T) {
	// "Foo" and "foo" both decode to the same Go key; the target map must be
	// left untouched.
	target := map[ciKey]int{"keep": 9}
	dec := NewDecoder(strings.NewReader("Foo = 1\nfoo = 2\n"))
	dec.MapKeyCodecs = []MapKeyCodec{ciKeyCodec}
	_, err := dec.Decode(&target)
	if !errorContains(err, `both decode to the same map key`) {
		t.Fatalf("expected duplicate key error, got: %v", err)
	}
	if !reflect.DeepEqual(target, map[ciKey]int{"keep": 9}) {
		t.Errorf("target map modified on failure: %v", target)
	}
}

func TestMapKeyCodecDecodeFailureKeepsMap(t *testing.T) {
	// A value that fails to decode halfway through must also leave the
	// target map untouched.
	target := map[region]int{regionEUWest: 9}
	dec := NewDecoder(strings.NewReader("eu-west = 1\nus-east = \"oops\"\n"))
	dec.MapKeyCodecs = []MapKeyCodec{regionCodec}
	_, err := dec.Decode(&target)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !reflect.DeepEqual(target, map[region]int{regionEUWest: 9}) {
		t.Errorf("target map modified on failure: %v", target)
	}
}

func TestMapKeyCodecDiagnostics(t *testing.T) {
	t.Run("no codec registered", func(t *testing.T) {
		var buf bytes.Buffer
		err := NewEncoder(&buf).Encode(map[int]int{1: 1})
		if !errors.Is(err, errNonString) {
			t.Errorf("encode: expected errNonString, got: %v", err)
		}

		var m map[int]int
		_, err = Decode("a = 1", &m)
		if !errorContains(err, "non-string key type") {
			t.Errorf("decode: expected non-string key error, got: %v", err)
		}
	})

	t.Run("invalid codecs", func(t *testing.T) {
		intCodec := NewMapKeyCodec(func(i int) (string, error) { return fmt.Sprint(i), nil },
			func(s string) (int, error) { return 0, nil })

		for _, c := range []struct {
			name  string
			codec MapKeyCodec
			want  string
		}{
			{"nil type", MapKeyCodec{Encode: intCodec.Encode, Decode: intCodec.Decode}, "no Type set"},
			{"not comparable", MapKeyCodec{Type: reflect.TypeOf([]int{}), Encode: intCodec.Encode, Decode: intCodec.Decode}, "not comparable"},
			{"nil encode", MapKeyCodec{Type: reflect.TypeOf(0), Decode: intCodec.Decode}, "no Encode function"},
			{"nil decode", MapKeyCodec{Type: reflect.TypeOf(0), Encode: intCodec.Encode}, "no Decode function"},
		} {
			t.Run(c.name, func(t *testing.T) {
				var buf bytes.Buffer
				enc := NewEncoder(&buf)
				enc.MapKeyCodecs = []MapKeyCodec{c.codec}
				if err := enc.Encode(map[int]int{1: 1}); !errorContains(err, c.want) {
					t.Errorf("encode: expected error containing %q, got: %v", c.want, err)
				}

				var m map[int]int
				dec := NewDecoder(strings.NewReader("a = 1"))
				dec.MapKeyCodecs = []MapKeyCodec{c.codec}
				if _, err := dec.Decode(&m); !errorContains(err, c.want) {
					t.Errorf("decode: expected error containing %q, got: %v", c.want, err)
				}
			})
		}
	})

	t.Run("encode method error", func(t *testing.T) {
		codec := NewMapKeyCodec(
			func(r region) (string, error) { return "", errors.New("boom") },
			parseRegion,
		)
		var buf bytes.Buffer
		enc := NewEncoder(&buf)
		enc.MapKeyCodecs = []MapKeyCodec{codec}
		err := enc.Encode(map[region]int{regionEUWest: 1})
		if !errorContains(err, "cannot encode map key") || !errorContains(err, "boom") {
			t.Errorf("expected wrapped encode error, got: %v", err)
		}
	})

	t.Run("decode method error", func(t *testing.T) {
		var m map[region]int
		dec := NewDecoder(strings.NewReader("atlantis = 1"))
		dec.MapKeyCodecs = []MapKeyCodec{regionCodec}
		_, err := dec.Decode(&m)
		if !errorContains(err, `cannot decode TOML key "atlantis"`) || !errorContains(err, "unknown region") {
			t.Errorf("expected wrapped decode error, got: %v", err)
		}
		if m != nil {
			t.Errorf("target map modified on failure: %v", m)
		}
	})

	t.Run("decode returns nil", func(t *testing.T) {
		codec := NewMapKeyCodec(
			func(k ciKey) (string, error) { return string(k), nil },
			func(s string) (ciKey, error) { return ciKey(s), nil },
		)
		codec.Decode = func(string) (any, error) { return nil, nil }
		var m map[ciKey]int
		dec := NewDecoder(strings.NewReader("a = 1"))
		dec.MapKeyCodecs = []MapKeyCodec{codec}
		if _, err := dec.Decode(&m); !errorContains(err, "decoded TOML key") {
			t.Errorf("expected nil key error, got: %v", err)
		}
	})

	t.Run("decode returns wrong type", func(t *testing.T) {
		codec := ciKeyCodec
		codec.Decode = func(string) (any, error) { return 42, nil }
		var m map[ciKey]int
		dec := NewDecoder(strings.NewReader("a = 1"))
		dec.MapKeyCodecs = []MapKeyCodec{codec}
		if _, err := dec.Decode(&m); !errorContains(err, "not assignable") {
			t.Errorf("expected assignability error, got: %v", err)
		}
	})

	t.Run("nil map key", func(t *testing.T) {
		ptrCodec := NewMapKeyCodec(
			func(p *int) (string, error) { return fmt.Sprint(*p), nil },
			func(s string) (*int, error) { return nil, nil },
		)
		var buf bytes.Buffer
		enc := NewEncoder(&buf)
		enc.MapKeyCodecs = []MapKeyCodec{ptrCodec}
		err := enc.Encode(map[*int]int{nil: 1})
		if !errorContains(err, "cannot encode nil map key") {
			t.Errorf("expected nil key error, got: %v", err)
		}
	})

	t.Run("duplicate registration", func(t *testing.T) {
		var buf bytes.Buffer
		enc := NewEncoder(&buf)
		enc.MapKeyCodecs = []MapKeyCodec{regionCodec, regionCodec}
		if err := enc.Encode(map[string]int{"a": 1}); !errorContains(err, "more than once") {
			t.Errorf("expected duplicate registration error, got: %v", err)
		}
	})
}
