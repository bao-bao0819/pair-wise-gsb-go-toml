package toml

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// mkRegion is an enum map key type used in the tests.
type mkRegion int

const (
	mkUSEast mkRegion = iota
	mkEUWest
	mkAPSouth
)

var mkRegionNames = map[mkRegion]string{
	mkUSEast:  "us-east-1",
	mkEUWest:  "eu-west-1",
	mkAPSouth: "ap-south-1",
}

func mkRegionCodec() MapKeyCodec {
	return MapKeyCodec{
		Marshal: func(k any) (string, error) {
			r, ok := k.(mkRegion)
			if !ok {
				return "", fmt.Errorf("expected mkRegion, got %T", k)
			}
			s, ok := mkRegionNames[r]
			if !ok {
				return "", fmt.Errorf("unknown region %d", int(r))
			}
			return s, nil
		},
		Unmarshal: func(s string) (any, error) {
			for r, name := range mkRegionNames {
				if name == s {
					return r, nil
				}
			}
			return nil, fmt.Errorf("unknown region %q", s)
		},
	}
}

func mkRegionType() reflect.Type { return reflect.TypeOf(mkRegion(0)) }

func TestMapKeyRoundTrip(t *testing.T) {
	in := map[mkRegion]int{mkUSEast: 1, mkEUWest: 2, mkAPSouth: 3}

	buf := new(bytes.Buffer)
	enc := NewEncoder(buf)
	enc.RegisterMapKey(mkRegionType(), mkRegionCodec())
	if err := enc.Encode(in); err != nil {
		t.Fatal(err)
	}

	want := "ap-south-1 = 3\neu-west-1 = 2\nus-east-1 = 1\n"
	if buf.String() != want {
		t.Fatalf("wrong output\nhave: %q\nwant: %q", buf.String(), want)
	}

	var out map[mkRegion]int
	dec := NewDecoder(strings.NewReader(buf.String()))
	dec.RegisterMapKey(mkRegionType(), mkRegionCodec())
	if _, err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch\nhave: %#v\nwant: %#v", out, in)
	}
}

// Two keys that differ only in case must be reported when the codec folds
// case, on both encode and decode.
func TestMapKeyCaseFoldCollision(t *testing.T) {
	type foldKey string
	foldType := reflect.TypeOf(foldKey(""))
	codec := MapKeyCodec{
		Marshal:   func(k any) (string, error) { return strings.ToLower(string(k.(foldKey))), nil },
		Unmarshal: func(s string) (any, error) { return foldKey(strings.ToLower(s)), nil },
	}

	t.Run("encode", func(t *testing.T) {
		enc := NewEncoder(new(bytes.Buffer))
		enc.RegisterMapKey(foldType, codec)
		err := enc.Encode(map[foldKey]int{"FOO": 1, "foo": 2})
		if err == nil {
			t.Fatal("expected error")
		}
		for _, want := range []string{"FOO", "foo", `both encode to TOML key "foo"`} {
			if !errorContains(err, want) {
				t.Fatalf("error %q does not contain %q", err, want)
			}
		}
	})

	t.Run("decode", func(t *testing.T) {
		m := map[foldKey]int{"sentinel": 9}
		dec := NewDecoder(strings.NewReader("FOO = 1\nfoo = 2\n"))
		dec.RegisterMapKey(foldType, codec)
		_, err := dec.Decode(&m)
		if err == nil {
			t.Fatal("expected error")
		}
		for _, want := range []string{`"FOO"`, `"foo"`, "both decode to map key"} {
			if !errorContains(err, want) {
				t.Fatalf("error %q does not contain %q", err, want)
			}
		}

		// The target map must be left unmodified.
		if !reflect.DeepEqual(m, map[foldKey]int{"sentinel": 9}) {
			t.Fatalf("target map modified after failed decode: %#v", m)
		}
	})
}

// Keys containing "." or quotes must be quoted as a single TOML key, not
// become a multi-level path.
func TestMapKeyDotsAndQuotes(t *testing.T) {
	type rawKey string
	rawType := reflect.TypeOf(rawKey(""))
	codec := MapKeyCodec{
		Marshal:   func(k any) (string, error) { return string(k.(rawKey)), nil },
		Unmarshal: func(s string) (any, error) { return rawKey(s), nil },
	}

	in := map[string]map[rawKey]int{
		"tbl": {"a.b": 1, `q"q`: 2, "sp ace": 3},
	}

	buf := new(bytes.Buffer)
	enc := NewEncoder(buf)
	enc.RegisterMapKey(rawType, codec)
	if err := enc.Encode(in); err != nil {
		t.Fatal(err)
	}

	want := "[tbl]\n" +
		`  "a.b" = 1` + "\n" +
		`  "q\"q" = 2` + "\n" +
		`  "sp ace" = 3` + "\n"
	if buf.String() != want {
		t.Fatalf("wrong output\nhave: %q\nwant: %q", buf.String(), want)
	}

	// The encoded keys must decode back to the same single-level map.
	var out map[string]map[rawKey]int
	dec := NewDecoder(strings.NewReader(buf.String()))
	dec.RegisterMapKey(rawType, codec)
	if _, err := dec.Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip mismatch\nhave: %#v\nwant: %#v", out, in)
	}

	// And a generic decode must see "a.b" as one key, not a nested table.
	var generic map[string]map[string]any
	if _, err := Decode(buf.String(), &generic); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a.b", `q"q`, "sp ace"} {
		if _, ok := generic["tbl"][k]; !ok {
			t.Fatalf("key %q not a single TOML key in %#v", k, generic["tbl"])
		}
	}
}

// An encoder whose output can't be reversed back to the original key must
// fail, rather than silently writing keys that decode to something else.
func TestMapKeyNotReversible(t *testing.T) {
	type key int
	kt := reflect.TypeOf(key(0))
	m := map[key]int{1: 1}

	run := func(name string, codec MapKeyCodec, want string) {
		t.Run(name, func(t *testing.T) {
			enc := NewEncoder(new(bytes.Buffer))
			enc.RegisterMapKey(kt, codec)
			err := enc.Encode(m)
			if !errorContains(err, want) {
				t.Fatalf("wrong error\nhave: %v\nwant substring: %q", err, want)
			}
		})
	}

	marshal := MapKeyCodec{Marshal: func(k any) (string, error) { return "k", nil }}

	run("unmarshal returns different key", MapKeyCodec{
		Marshal:   marshal.Marshal,
		Unmarshal: func(s string) (any, error) { return key(2), nil },
	}, "not reversible")

	run("unmarshal fails", MapKeyCodec{
		Marshal:   marshal.Marshal,
		Unmarshal: func(s string) (any, error) { return nil, fmt.Errorf("no such key") },
	}, "fails to decode back: no such key")

	run("unmarshal returns nil", MapKeyCodec{
		Marshal:   marshal.Marshal,
		Unmarshal: func(s string) (any, error) { return nil, nil },
	}, "nil key")

	run("unmarshal returns non-comparable", MapKeyCodec{
		Marshal:   marshal.Marshal,
		Unmarshal: func(s string) (any, error) { return []int{1}, nil },
	}, "non-comparable type []int")

	run("unmarshal returns wrong type", MapKeyCodec{
		Marshal:   marshal.Marshal,
		Unmarshal: func(s string) (any, error) { return "k", nil },
	}, "of type string")

	run("no unmarshal func", marshal, "no Unmarshal function")

	run("no marshal func", MapKeyCodec{
		Unmarshal: func(s string) (any, error) { return key(1), nil },
	}, "no Marshal function")

	run("marshal fails", MapKeyCodec{
		Marshal:   func(k any) (string, error) { return "", fmt.Errorf("bad key") },
		Unmarshal: func(s string) (any, error) { return key(1), nil },
	}, "unable to encode map key")
}

// Decoder-side diagnostics for bad Unmarshal results.
func TestMapKeyDecodeBadKey(t *testing.T) {
	type key int
	kt := reflect.TypeOf(key(0))

	run := func(name string, codec MapKeyCodec, want string) {
		t.Run(name, func(t *testing.T) {
			var m map[key]int
			dec := NewDecoder(strings.NewReader("k = 1\n"))
			dec.RegisterMapKey(kt, codec)
			_, err := dec.Decode(&m)
			if !errorContains(err, want) {
				t.Fatalf("wrong error\nhave: %v\nwant substring: %q", err, want)
			}
			if m != nil {
				t.Fatalf("target map modified after failed decode: %#v", m)
			}
		})
	}

	run("unmarshal fails", MapKeyCodec{
		Unmarshal: func(s string) (any, error) { return nil, fmt.Errorf("no such key") },
	}, "no such key")

	run("unmarshal returns nil", MapKeyCodec{
		Unmarshal: func(s string) (any, error) { return nil, nil },
	}, "nil key")

	run("unmarshal returns non-comparable", MapKeyCodec{
		Unmarshal: func(s string) (any, error) { return []int{1}, nil },
	}, "non-comparable type []int")

	run("unmarshal returns wrong type", MapKeyCodec{
		Unmarshal: func(s string) (any, error) { return "k", nil },
	}, "of type string")

	run("no unmarshal func", MapKeyCodec{
		Marshal: func(k any) (string, error) { return "k", nil },
	}, "no Unmarshal function")
}

// The codec functions must be called exactly once per key, regardless of Go's
// randomized map iteration order.
func TestMapKeyCallCount(t *testing.T) {
	in := map[mkRegion]int{mkUSEast: 1, mkEUWest: 2, mkAPSouth: 3}
	doc := "us-east-1 = 1\neu-west-1 = 2\nap-south-1 = 3\n"

	for i := 0; i < 50; i++ {
		var nMarshal, nUnmarshal int
		codec := mkRegionCodec()
		m, u := codec.Marshal, codec.Unmarshal
		codec.Marshal = func(k any) (string, error) { nMarshal++; return m(k) }
		codec.Unmarshal = func(s string) (any, error) { nUnmarshal++; return u(s) }

		enc := NewEncoder(new(bytes.Buffer))
		enc.RegisterMapKey(mkRegionType(), codec)
		if err := enc.Encode(in); err != nil {
			t.Fatal(err)
		}
		if nMarshal != 3 || nUnmarshal != 3 {
			t.Fatalf("encode: Marshal called %d times, Unmarshal %d times; want 3 each",
				nMarshal, nUnmarshal)
		}

		nUnmarshal = 0
		var out map[mkRegion]int
		dec := NewDecoder(strings.NewReader(doc))
		dec.RegisterMapKey(mkRegionType(), codec)
		if _, err := dec.Decode(&out); err != nil {
			t.Fatal(err)
		}
		if nUnmarshal != 3 {
			t.Fatalf("decode: Unmarshal called %d times; want 3", nUnmarshal)
		}
	}
}

// Without a registered codec the default behaviour is unchanged.
func TestMapKeyDefaultUnchanged(t *testing.T) {
	err := NewEncoder(new(bytes.Buffer)).Encode(map[mkRegion]int{mkUSEast: 1})
	if !errorContains(err, errNonString.Error()) {
		t.Fatalf("wrong error: %v", err)
	}

	var m map[mkRegion]int
	_, err = Decode("us-east-1 = 1\n", &m)
	if !errorContains(err, "cannot decode to a map with non-string key type") {
		t.Fatalf("wrong error: %v", err)
	}
}
