package toml

import (
	"fmt"
	"reflect"
	"sort"
)

// MapKeyCodec converts between Go map keys of one specific type and the
// strings used as TOML keys.
//
// TOML keys are always strings, so a map with a non-string key type (such as
// an enum or a small struct) needs an explicit conversion. Register a codec
// with [Encoder.RegisterMapKey] or [Decoder.RegisterMapKey]; without one only
// string (and interface, for decoding) key types are accepted.
//
// Marshal converts a Go map key to the string used as the TOML key; the
// string is quoted and escaped as needed, so it always stays a single TOML
// key even if it contains e.g. "." or quotes.
//
// Unmarshal converts a TOML key back to a Go map key. The returned value
// must be non-nil, comparable, and assignable to the registered key type.
//
// Encoding verifies that every key round-trips: Marshal's output is passed
// through Unmarshal and must produce the original key, so both functions
// must be set to encode (only Unmarshal is needed to decode).
type MapKeyCodec struct {
	Marshal   func(key any) (string, error)
	Unmarshal func(text string) (any, error)
}

// RegisterMapKey registers c to encode maps with keys of type keyType.
//
// Maps with key types that have no registered codec behave as before: only
// string key types are encoded.
func (enc *Encoder) RegisterMapKey(keyType reflect.Type, c MapKeyCodec) {
	if enc.mapKeyCodecs == nil {
		enc.mapKeyCodecs = make(map[reflect.Type]MapKeyCodec)
	}
	enc.mapKeyCodecs[keyType] = c
}

// RegisterMapKey registers c to decode TOML keys to maps with keys of type
// keyType.
//
// Maps with key types that have no registered codec behave as before: only
// string and interface key types are decoded.
func (dec *Decoder) RegisterMapKey(keyType reflect.Type, c MapKeyCodec) {
	if dec.mapKeyCodecs == nil {
		dec.mapKeyCodecs = make(map[reflect.Type]MapKeyCodec)
	}
	dec.mapKeyCodecs[keyType] = c
}

// A mapKey pairs a Go map key with its encoded TOML key.
type mapKey struct {
	goKey reflect.Value
	key   string
}

// mapKeys encodes all keys of the map rv exactly once, checks for
// collisions, verifies the encoding is reversible, and returns the keys
// sorted by their encoded form.
func (enc *Encoder) mapKeys(rv reflect.Value) []mapKey {
	kt := rv.Type().Key()
	codec, hasCodec := enc.mapKeyCodecs[kt]
	if !hasCodec && kt.Kind() != reflect.String {
		encPanic(errNonString)
	}

	var (
		keys  = rv.MapKeys()
		pairs = make([]mapKey, 0, len(keys))
		seen  = make(map[string]reflect.Value, len(keys))
	)
	for _, k := range keys {
		s := k.String()
		if hasCodec {
			s = codec.marshalKey(kt, k)
		}
		if prev, dup := seen[s]; dup {
			encPanic(fmt.Errorf("toml: map keys %#v and %#v both encode to TOML key %q",
				prev.Interface(), k.Interface(), s))
		}
		seen[s] = k
		pairs = append(pairs, mapKey{goKey: k, key: s})
	}
	if hasCodec {
		for _, p := range pairs {
			codec.checkReversible(kt, p.goKey, p.key)
		}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })
	return pairs
}

// marshalKey encodes a single map key.
func (c MapKeyCodec) marshalKey(kt reflect.Type, k reflect.Value) string {
	if c.Marshal == nil {
		encPanic(fmt.Errorf("toml: map key codec for type %s has no Marshal function", kt))
	}
	s, err := c.Marshal(k.Interface())
	if err != nil {
		encPanic(fmt.Errorf("toml: unable to encode map key %#v of type %s: %w", k.Interface(), kt, err))
	}
	return s
}

// checkReversible verifies the encoded key s decodes back to the original
// Go key k, rather than trusting e.g. a String() method to be invertible.
func (c MapKeyCodec) checkReversible(kt reflect.Type, k reflect.Value, s string) {
	if c.Unmarshal == nil {
		encPanic(fmt.Errorf("toml: map key codec for type %s has no Unmarshal function: unable to verify key %q is reversible", kt, s))
	}
	back, err := c.Unmarshal(s)
	if err != nil {
		encPanic(fmt.Errorf("toml: map key %#v of type %s encodes to %q, which fails to decode back: %w",
			k.Interface(), kt, s, err))
	}
	if back == nil {
		encPanic(fmt.Errorf("toml: map key codec for type %s decoded %q to a nil key", kt, s))
	}
	if bt := reflect.TypeOf(back); !bt.Comparable() {
		encPanic(fmt.Errorf("toml: map key codec for type %s decoded %q to a value of non-comparable type %s", kt, s, bt))
	} else if bt != kt {
		encPanic(fmt.Errorf("toml: map key codec for type %s decoded %q to %#v of type %s", kt, s, back, bt))
	}
	if back != k.Interface() {
		encPanic(fmt.Errorf("toml: map key %#v encodes to %q, but that decodes to %#v: encoding is not reversible",
			k.Interface(), s, back))
	}
}

// unifyMapKey decodes a TOML table in to a map with a registered key codec.
//
// The table is decoded in to a temporary map first, so the target map is
// left unmodified if any key or value fails to decode.
func (md *MetaData) unifyMapKey(tmap map[string]any, rv reflect.Value, c MapKeyCodec) error {
	kt := rv.Type().Key()
	if c.Unmarshal == nil {
		return md.e("map key codec for type %s has no Unmarshal function", kt)
	}

	var (
		tmp  = reflect.MakeMap(rv.Type())
		seen = make(map[any]string, len(tmap))
	)
	for k, v := range tmap {
		md.decoded[md.context.add(k).String()] = struct{}{}
		md.context = append(md.context, k)

		key, err := c.Unmarshal(k)
		if err != nil {
			return md.e("unable to decode TOML key %q to map key type %s: %s", k, kt, err)
		}
		if key == nil {
			return md.e("map key codec for type %s decoded TOML key %q to a nil key", kt, k)
		}
		kv := reflect.ValueOf(key)
		if !kv.Type().Comparable() {
			return md.e("map key codec for type %s decoded TOML key %q to a value of non-comparable type %s", kt, k, kv.Type())
		}
		if !kv.Type().AssignableTo(kt) {
			return md.e("map key codec for type %s decoded TOML key %q to %#v of type %s", kt, k, key, kv.Type())
		}
		if prev, dup := seen[key]; dup {
			return md.e("TOML keys %q and %q both decode to map key %#v", prev, k, key)
		}
		seen[key] = k

		rvval := reflect.Indirect(reflect.New(rv.Type().Elem()))
		if err := md.unify(v, indirect(rvval)); err != nil {
			return err
		}
		md.context = md.context[0 : len(md.context)-1]

		tmp.SetMapIndex(kv, rvval)
	}

	if rv.IsNil() {
		rv.Set(tmp)
	} else {
		for _, k := range tmp.MapKeys() {
			rv.SetMapIndex(k, tmp.MapIndex(k))
		}
	}
	return nil
}
