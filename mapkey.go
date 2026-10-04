package toml

import (
	"errors"
	"fmt"
	"reflect"
)

// MapKeyCodec describes how map keys of one specific Go type are converted to
// and from TOML keys.
//
// TOML keys are always strings, so maps with a non-string key type (for
// example an enum such as map[Region]Endpoint) can only be encoded or decoded
// when a codec for the key type is registered on the [Encoder] or [Decoder]
// through their MapKeyCodecs field. Maps with string keys never need a codec,
// although a codec registered for a named string type takes precedence over
// the default behaviour.
//
// Encode converts a Go map key to its TOML key text, and Decode converts it
// back. The two must be each other's inverse: when encoding a map every key
// is passed through Decode as well, and encoding fails if a key does not
// survive the round trip. The encoded text is treated as a single TOML key
// and is quoted and escaped as needed, so encoded keys containing e.g. dots
// do not turn into nested tables.
//
// Use [NewMapKeyCodec] to construct a codec from typed functions.
type MapKeyCodec struct {
	// Type is the Go map key type this codec applies to. It must be
	// comparable, as only comparable types can be map keys.
	Type reflect.Type

	// Encode converts a Go map key to its TOML key text.
	Encode func(key any) (string, error)

	// Decode converts TOML key text back to a Go map key. The returned value
	// must be assignable to Type.
	Decode func(text string) (any, error)
}

// NewMapKeyCodec creates a [MapKeyCodec] for the map key type K from typed
// encode and decode functions.
//
// For example, for an enum key type:
//
//	codec := toml.NewMapKeyCodec(
//		func(r Region) (string, error) { return r.String(), nil },
//		func(s string) (Region, error) { return ParseRegion(s) },
//	)
func NewMapKeyCodec[K comparable](encode func(K) (string, error), decode func(string) (K, error)) MapKeyCodec {
	t := reflect.TypeOf((*K)(nil)).Elem()
	return MapKeyCodec{
		Type: t,
		Encode: func(key any) (string, error) {
			if encode == nil {
				return "", fmt.Errorf("toml: map key codec for %s has no encode function", t)
			}
			k, ok := key.(K)
			if !ok {
				return "", fmt.Errorf("toml: map key codec for %s called with key of type %T", t, key)
			}
			return encode(k)
		},
		Decode: func(text string) (any, error) {
			if decode == nil {
				return nil, fmt.Errorf("toml: map key codec for %s has no decode function", t)
			}
			return decode(text)
		},
	}
}

func (c MapKeyCodec) validate() error {
	switch {
	case c.Type == nil:
		return errors.New("toml: map key codec has no Type set")
	case !c.Type.Comparable():
		return fmt.Errorf("toml: map key codec type %s is not comparable and cannot be used as a map key", c.Type)
	case c.Encode == nil:
		return fmt.Errorf("toml: map key codec for type %s has no Encode function", c.Type)
	case c.Decode == nil:
		return fmt.Errorf("toml: map key codec for type %s has no Decode function", c.Type)
	}
	return nil
}

// validateMapKeyCodecs validates all registered codecs, including ones that
// end up unused, so configuration errors are reported eagerly.
func validateMapKeyCodecs(codecs []MapKeyCodec) error {
	for i := range codecs {
		if err := codecs[i].validate(); err != nil {
			return err
		}
		for j := i + 1; j < len(codecs); j++ {
			if codecs[i].Type != nil && codecs[j].Type == codecs[i].Type {
				return fmt.Errorf("toml: map key codec for type %s is registered more than once", codecs[i].Type)
			}
		}
	}
	return nil
}

// findMapKeyCodec returns the codec registered for the exact key type t, or
// nil if there is none. A registered but invalid codec is an error.
func findMapKeyCodec(codecs []MapKeyCodec, t reflect.Type) (*MapKeyCodec, error) {
	for i := range codecs {
		if codecs[i].Type == t {
			if err := codecs[i].validate(); err != nil {
				return nil, err
			}
			return &codecs[i], nil
		}
	}
	return nil, nil
}
