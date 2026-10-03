package toml

import (
	"testing"
)

func stateMap(sess *ConsumeSession) map[string]DeclState {
	m := make(map[string]DeclState)
	for _, d := range sess.Declarations() {
		m[d.Path.String()] = d.State
	}
	return m
}

func findDecl(sess *ConsumeSession, path string) Declaration {
	for _, d := range sess.Declarations() {
		if d.Path.String() == path {
			return d
		}
	}
	return Declaration{}
}

// Two equally named array-table elements keep independent ledgers: element 0
// decodes successfully and is accepted, element 1 fails, element 2 is never
// requested. Accepting one receipt changes only its instance.
func TestConsumeSessionArrayInstances(t *testing.T) {
	const doc = `
[[plugins]]
name = "alpha"
enabled = true
config.x = 1

[[plugins]]
name = "beta"
enabled = false

[[plugins]]
name = "gamma"
`
	type S struct{ Plugins []Primitive }
	var s S
	md, err := Decode(doc, &s)
	if err != nil {
		t.Fatal(err)
	}

	type Plug struct {
		Name    string
		Enabled bool
		Config  struct {
			X int `toml:"x"`
		}
	}

	sess := md.NewConsumeSession()

	// Element 0 succeeds; accepting its receipt marks only instance 0.
	var a Plug
	r0, err0 := sess.DecodePrimitive(s.Plugins[0], &a)
	if err0 != nil {
		t.Fatalf("element 0 decode: %v", err0)
	}
	if r0.Failed() {
		t.Fatalf("element 0 receipt marked failed")
	}
	if err := sess.Accept(r0); err != nil {
		t.Fatalf("accept element 0: %v", err)
	}

	// Element 1 fails: bool into string.
	var bad struct {
		Enabled string
	}
	r1, err1 := sess.DecodePrimitive(s.Plugins[1], &bad)
	if err1 == nil {
		t.Fatalf("element 1 decode unexpectedly succeeded")
	}
	if !r1.Failed() || r1.Err() == nil {
		t.Fatalf("element 1 receipt did not carry the failure")
	}

	states := stateMap(sess)
	for p, want := range map[string]DeclState{
		"plugins[0]":          StateConsumed,
		"plugins[0].name":     StateConsumed,
		"plugins[0].enabled":  StateConsumed,
		"plugins[0].config":   StateConsumed,
		"plugins[0].config.x": StateConsumed,
		"plugins[1]":          StateUntried,
		"plugins[1].name":     StateUntried,
		"plugins[1].enabled":  StateFailed,
		"plugins[2]":          StateUntried,
		"plugins[2].name":     StateUntried,
	} {
		if got := states[p]; got != want {
			t.Errorf("state[%s] = %v, want %v", p, got, want)
		}
	}

	failed := findDecl(sess, "plugins[1].enabled")
	if failed.Reason == nil || failed.Reason.Error() == "" {
		t.Errorf("failed declaration did not retain a reason")
	}

	// A failed receipt can never be accepted.
	if err := sess.Accept(r1); err == nil {
		t.Errorf("accepting a failed receipt must error")
	}

	// Now succeed on element 1 with a compatible type and accept: only
	// instance 1 changes, instance 2 stays untried.
	var good struct {
		Name    string
		Enabled bool
	}
	r1ok, err := sess.DecodePrimitive(s.Plugins[1], &good)
	if err != nil {
		t.Fatalf("retry element 1: %v", err)
	}
	if err := sess.Accept(r1ok); err != nil {
		t.Fatalf("accept retried element 1: %v", err)
	}
	states = stateMap(sess)
	for p, want := range map[string]DeclState{
		"plugins[1]":         StateConsumed,
		"plugins[1].name":    StateConsumed,
		"plugins[1].enabled": StateConsumed,
		"plugins[2]":         StateUntried,
		"plugins[2].name":    StateUntried,
	} {
		if got := states[p]; got != want {
			t.Errorf("after retry state[%s] = %v, want %v", p, got, want)
		}
	}
}

// Two sessions decoding the same Primitive are independent.
func TestConsumeSessionIndependent(t *testing.T) {
	const doc = `
[[plugins]]
name = "alpha"
enabled = true
`
	type S struct{ Plugins []Primitive }
	var s S
	md, err := Decode(doc, &s)
	if err != nil {
		t.Fatal(err)
	}
	type Plug struct {
		Name string
	}

	s1 := md.NewConsumeSession()
	s2 := md.NewConsumeSession()

	var p Plug
	r, err := s1.DecodePrimitive(s.Plugins[0], &p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Accept(r); err != nil {
		t.Fatal(err)
	}

	// A receipt from one session can't be accepted by another.
	if err := s2.Accept(r); err == nil {
		t.Errorf("cross-session accept must error")
	}

	if got := stateMap(s2)["plugins[0].name"]; got != StateUntried {
		t.Errorf("session 2 affected by session 1: state = %v", got)
	}
	if got := stateMap(s1)["plugins[0].name"]; got != StateConsumed {
		t.Errorf("session 1 state = %v, want consumed", got)
	}
}

// A receipt is bound to the document and Primitive ownership; it cannot be
// replayed against an identical document decoded separately.
func TestConsumeSessionDocumentBinding(t *testing.T) {
	const doc = `
[[plugins]]
name = "alpha"
`
	type S struct{ Plugins []Primitive }
	var s1, s2 S
	md1, err := Decode(doc, &s1)
	if err != nil {
		t.Fatal(err)
	}
	md2, err := Decode(doc, &s2)
	if err != nil {
		t.Fatal(err)
	}

	sess := md2.NewConsumeSession()
	type Plug struct{ Name string }
	var p Plug
	r, err := sess.DecodePrimitive(s1.Plugins[0], &p)
	if err == nil {
		t.Fatalf("decoding a foreign Primitive must fail")
	}
	if !r.Failed() {
		t.Fatalf("foreign decode receipt must be failed")
	}
	if err := sess.Accept(r); err == nil {
		t.Fatalf("foreign receipt must not be accepted")
	}

	// The foreign Primitive must not be decodable at all through md2, even
	// though the documents are byte-identical.
	if got := stateMap(sess)["plugins[0].name"]; got != StateUntried {
		t.Errorf("foreign decode changed ledger: %v", got)
	}
	_ = md1
}

// A custom Unmarshaler consumes its scope opaquely; internal fields are marked
// as consumed under the opaque root rather than guessed individually.
func TestConsumeSessionOpaqueUnmarshaler(t *testing.T) {
	const doc = `
[[plugins]]
name = "alpha"
config.a = 1
config.b = 2
`
	type S struct{ Plugins []Primitive }
	var s S
	md, err := Decode(doc, &s)
	if err != nil {
		t.Fatal(err)
	}

	sess := md.NewConsumeSession()
	var o opaqueTarget
	r, err := sess.DecodePrimitive(s.Plugins[0], &o)
	if err != nil {
		t.Fatalf("opaque decode: %v", err)
	}
	if len(r.Read()) != 1 || r.Read()[0].String() != "plugins[0]" {
		t.Fatalf("opaque receipt should list only the root, got %v", r.Read())
	}
	if err := sess.Accept(r); err != nil {
		t.Fatalf("accept opaque receipt: %v", err)
	}

	for _, d := range sess.Declarations() {
		if d.State != StateConsumed {
			t.Errorf("%s state = %v, want consumed", d.Path, d.State)
		}
		if !d.Opaque {
			t.Errorf("%s should be marked opaque", d.Path)
		}
	}
}

type opaqueTarget struct {
	seen any
}

func (o *opaqueTarget) UnmarshalTOML(v any) error {
	o.seen = v
	return nil
}

// A custom Unmarshaler that errors leaves its declaration failed and nothing
// consumed.
func TestConsumeSessionOpaqueFailure(t *testing.T) {
	const doc = `
[[plugins]]
name = "alpha"
`
	type S struct{ Plugins []Primitive }
	var s S
	md, err := Decode(doc, &s)
	if err != nil {
		t.Fatal(err)
	}

	sess := md.NewConsumeSession()
	fail := failingUnmarshaler{}
	r, err := sess.DecodePrimitive(s.Plugins[0], &fail)
	if err == nil {
		t.Fatalf("failing unmarshaler should error")
	}
	if !r.Failed() {
		t.Fatalf("receipt should be failed")
	}
	if got := stateMap(sess)["plugins[0]"]; got != StateFailed {
		t.Errorf("opaque failure state = %v, want failed", got)
	}
	if findDecl(sess, "plugins[0]").Reason == nil {
		t.Errorf("opaque failure reason not retained")
	}
}

type failingUnmarshaler struct{}

func (failingUnmarshaler) UnmarshalTOML(any) error { return errCustom }

type sentinelError struct{ msg string }

func (e sentinelError) Error() string { return e.msg }

var errCustom = sentinelError{msg: "plugin rejected configuration"}

// The legacy Undecoded API keeps working exactly as before, whether or not
// sessions are ever created.
func TestConsumeSessionLegacyUndecoded(t *testing.T) {
	const doc = `
title = "x"
[band]
started = 1973
extra = true
`
	type Band struct {
		Started int
	}
	type C struct {
		Title string
		Band  Primitive
	}
	var c C
	md, err := Decode(doc, &c)
	if err != nil {
		t.Fatal(err)
	}
	if got := md.Undecoded(); len(got) != 2 {
		t.Fatalf("undecoded before PrimitiveDecode = %v, want 2 keys", got)
	}

	var b Band
	if err := md.PrimitiveDecode(c.Band, &b); err != nil {
		t.Fatal(err)
	}
	if got := md.Undecoded(); len(got) != 1 {
		t.Fatalf("undecoded after PrimitiveDecode = %v, want only band.extra", got)
	}
}
