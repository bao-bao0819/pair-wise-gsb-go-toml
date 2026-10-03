package toml

import (
	"reflect"
	"strings"
	"testing"
)

func declStates(t *testing.T, s *Session) map[string]DeclState {
	t.Helper()
	m := make(map[string]DeclState)
	for _, d := range s.Declarations() {
		if _, ok := m[d.Path]; ok {
			t.Fatalf("duplicate declaration path %q", d.Path)
		}
		m[d.Path] = d.State
	}
	return m
}

func declErr(s *Session, path string) error {
	for _, d := range s.Declarations() {
		if d.Path == path {
			return d.Err
		}
	}
	return nil
}

// Two (three) plugin elements with the same name: one decodes successfully
// and its receipt is accepted, one fails, and one is never requested.
func TestSessionPluginElements(t *testing.T) {
	data := `
[[plugins]]
name = "auth"
enabled = true

[[plugins]]
name = "auth"
enabled = "not-a-bool"

[[plugins]]
name = "auth"
enabled = false
`
	var cfg struct {
		Plugins []Primitive `toml:"plugins"`
	}
	md, err := Decode(data, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Plugins) != 3 {
		t.Fatalf("expected 3 plugins, got %d", len(cfg.Plugins))
	}

	s := md.Session()
	if s == nil {
		t.Fatal("Session() returned nil")
	}

	// The initial decode consumed the "plugins" key itself; everything
	// behind the Primitives is pending.
	st := declStates(t, s)
	for path, want := range map[string]DeclState{
		"plugins":            DeclConsumed,
		"plugins[0]":         DeclPending,
		"plugins[0].name":    DeclPending,
		"plugins[0].enabled": DeclPending,
		"plugins[1]":         DeclPending,
		"plugins[1].name":    DeclPending,
		"plugins[1].enabled": DeclPending,
		"plugins[2]":         DeclPending,
		"plugins[2].name":    DeclPending,
		"plugins[2].enabled": DeclPending,
	} {
		if st[path] != want {
			t.Errorf("initial state of %q: have %s, want %s", path, st[path], want)
		}
	}

	type plugin struct {
		Name    string `toml:"name"`
		Enabled bool   `toml:"enabled"`
	}

	// Instance 0: successful decode. Nothing is consumed until the receipt
	// is accepted.
	var p0 plugin
	r0, err := s.Decode(cfg.Plugins[0], &p0)
	if err != nil {
		t.Fatal(err)
	}
	if p0.Name != "auth" || !p0.Enabled {
		t.Fatalf("unexpected decode result: %+v", p0)
	}
	if st := declStates(t, s); st["plugins[0].name"] != DeclPending {
		t.Errorf("plugins[0].name consumed before Accept: %s", st["plugins[0].name"])
	}

	// The receipt reports what the attempt read.
	var r0paths []string
	for _, d := range r0.Declarations() {
		r0paths = append(r0paths, d.Path)
	}
	wantR0 := []string{"plugins[0]", "plugins[0].name", "plugins[0].enabled"}
	if !reflect.DeepEqual(r0paths, wantR0) {
		t.Errorf("receipt declarations: have %v, want %v", r0paths, wantR0)
	}

	if err := r0.Accept(); err != nil {
		t.Fatal(err)
	}
	if err := r0.Accept(); err == nil {
		t.Error("second Accept did not fail")
	}

	// Accepting the receipt changes only this instance's ledger.
	st = declStates(t, s)
	for path, want := range map[string]DeclState{
		"plugins[0]":         DeclConsumed,
		"plugins[0].name":    DeclConsumed,
		"plugins[0].enabled": DeclConsumed,
		"plugins[1]":         DeclPending,
		"plugins[1].name":    DeclPending,
		"plugins[1].enabled": DeclPending,
		"plugins[2]":         DeclPending,
		"plugins[2].name":    DeclPending,
		"plugins[2].enabled": DeclPending,
	} {
		if st[path] != want {
			t.Errorf("after accept, state of %q: have %s, want %s", path, st[path], want)
		}
	}

	// Instance 1: the decode fails. The receipt keeps the reason, the
	// declarations it read are marked failed, and nothing is consumed.
	var p1 plugin
	r1, err := s.Decode(cfg.Plugins[1], &p1)
	if err == nil {
		t.Fatal("expected decode error for plugins[1]")
	}
	if r1.Err() == nil {
		t.Fatal("failed receipt has no reason")
	}
	if err := r1.Accept(); err == nil {
		t.Error("accepting a failed receipt did not fail")
	}
	st = declStates(t, s)
	if st["plugins[1]"] != DeclFailed {
		t.Errorf("plugins[1]: have %s, want failed", st["plugins[1]"])
	}
	if st["plugins[1].enabled"] != DeclFailed {
		t.Errorf("plugins[1].enabled: have %s, want failed", st["plugins[1].enabled"])
	}
	if declErr(s, "plugins[1].enabled") == nil {
		t.Error("plugins[1].enabled has no failure reason")
	}
	// The failure touched only instance 1.
	if st["plugins[0].enabled"] != DeclConsumed {
		t.Errorf("plugins[0].enabled changed by failed attempt: %s", st["plugins[0].enabled"])
	}
	if st["plugins[2].enabled"] != DeclPending {
		t.Errorf("plugins[2].enabled changed by failed attempt: %s", st["plugins[2].enabled"])
	}

	// A second, independent attempt on the same Primitive with a
	// compatible target succeeds; accepting it consumes the declarations.
	var p1retry struct {
		Name    string `toml:"name"`
		Enabled string `toml:"enabled"`
	}
	r1b, err := s.Decode(cfg.Plugins[1], &p1retry)
	if err != nil {
		t.Fatal(err)
	}
	if p1retry.Enabled != "not-a-bool" {
		t.Fatalf("unexpected retry result: %+v", p1retry)
	}
	if err := r1b.Accept(); err != nil {
		t.Fatal(err)
	}
	st = declStates(t, s)
	for path, want := range map[string]DeclState{
		"plugins[1]":         DeclConsumed,
		"plugins[1].name":    DeclConsumed,
		"plugins[1].enabled": DeclConsumed,
		"plugins[2]":         DeclPending,
		"plugins[2].name":    DeclPending,
		"plugins[2].enabled": DeclPending,
	} {
		if st[path] != want {
			t.Errorf("after retry, state of %q: have %s, want %s", path, st[path], want)
		}
	}
	if declErr(s, "plugins[1].enabled") != nil {
		t.Error("failure reason not cleared after accepted retry")
	}

	// Instance 2 was never requested and stays pending.

	// The session is independent from Undecoded: none of the above changed
	// what the old API reports.
	wantUndecoded := []Key{
		{"plugins", "name"}, {"plugins", "enabled"},
		{"plugins", "name"}, {"plugins", "enabled"},
		{"plugins", "name"}, {"plugins", "enabled"},
	}
	if und := md.Undecoded(); !reflect.DeepEqual(und, wantUndecoded) {
		t.Errorf("Undecoded changed by session activity: have %v, want %v", und, wantUndecoded)
	}

	// The old PrimitiveDecode API keeps working and is recorded by the
	// session as direct consumption.
	var p2 plugin
	if err := md.PrimitiveDecode(cfg.Plugins[2], &p2); err != nil {
		t.Fatal(err)
	}
	if p2.Name != "auth" || p2.Enabled {
		t.Fatalf("unexpected PrimitiveDecode result: %+v", p2)
	}
	st = declStates(t, s)
	if st["plugins[2].name"] != DeclConsumed || st["plugins[2].enabled"] != DeclConsumed {
		t.Errorf("PrimitiveDecode not recorded: plugins[2].name=%s plugins[2].enabled=%s",
			st["plugins[2].name"], st["plugins[2].enabled"])
	}
	// Note: Undecoded is key-name based, so decoding one element marks the
	// same keys of every other element as decoded too; the session ledger
	// above is the one that keeps the instances apart.
	if und := md.Undecoded(); len(und) != 0 {
		t.Errorf("Undecoded after PrimitiveDecode: have %v", und)
	}
}

// A receipt is bound to its document and Primitive: it cannot be used with
// another document, even one with identical contents.
func TestSessionReceiptBinding(t *testing.T) {
	data := "[[plugins]]\nname = \"auth\"\n"
	var c1, c2 struct {
		Plugins []Primitive `toml:"plugins"`
	}
	md1, err := Decode(data, &c1)
	if err != nil {
		t.Fatal(err)
	}
	md2, err := Decode(data, &c2)
	if err != nil {
		t.Fatal(err)
	}
	s1, s2 := md1.Session(), md2.Session()
	if s1 == s2 {
		t.Fatal("identical documents share a session")
	}

	var p struct {
		Name string `toml:"name"`
	}
	if _, err := s1.Decode(c2.Plugins[0], &p); err == nil {
		t.Error("decoded a Primitive from another document")
	}
	if _, err := s1.Decode(Primitive{}, &p); err == nil {
		t.Error("decoded a zero Primitive")
	}

	// Accepting a receipt only affects its own document's ledger.
	r, err := s1.Decode(c1.Plugins[0], &p)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Accept(); err != nil {
		t.Fatal(err)
	}
	if st := declStates(t, s1); st["plugins[0].name"] != DeclConsumed {
		t.Errorf("doc1 plugins[0].name: have %s, want consumed", st["plugins[0].name"])
	}
	if st := declStates(t, s2); st["plugins[0].name"] != DeclPending {
		t.Errorf("doc2 plugins[0].name changed by doc1 receipt: %s", st["plugins[0].name"])
	}
}

// Dotted keys and table headers land on the same declarations.
func TestSessionDottedKeys(t *testing.T) {
	var docs = []string{
		"[[plugins]]\nname = \"x\"\n[plugins.settings]\ndepth = 1\n",
		"[[plugins]]\nname = \"x\"\nsettings.depth = 1\n",
	}
	var paths [][]string
	for _, data := range docs {
		var cfg struct {
			Plugins []Primitive `toml:"plugins"`
		}
		md, err := Decode(data, &cfg)
		if err != nil {
			t.Fatal(err)
		}
		var p struct {
			Name     string `toml:"name"`
			Settings struct {
				Depth int `toml:"depth"`
			} `toml:"settings"`
		}
		r, err := md.Session().Decode(cfg.Plugins[0], &p)
		if err != nil {
			t.Fatal(err)
		}
		if p.Settings.Depth != 1 {
			t.Fatalf("unexpected decode result: %+v", p)
		}
		var got []string
		for _, d := range r.Declarations() {
			got = append(got, d.Path)
		}
		paths = append(paths, got)
	}
	want := []string{"plugins[0]", "plugins[0].name", "plugins[0].settings", "plugins[0].settings.depth"}
	for i, got := range paths {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("doc %d: receipt declarations: have %v, want %v", i, got, want)
		}
	}
}

type opaquePlugin struct{ raw any }

func (o *opaquePlugin) UnmarshalTOML(v any) error { o.raw = v; return nil }

// A custom Unmarshaler takes over its whole scope; the session marks the
// entire subtree consumed instead of guessing what was read.
func TestSessionUnmarshalerOpaque(t *testing.T) {
	data := "[plugin]\nname = \"x\"\n[plugin.settings]\ndepth = 1\n"
	var cfg struct {
		Plugin opaquePlugin `toml:"plugin"`
	}
	md, err := Decode(data, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range md.Session().Declarations() {
		if d.State != DeclConsumed {
			t.Errorf("%s: have %s, want consumed", d.Path, d.State)
		}
	}

	// Same for a scope taken over inside a session decode.
	data = "[[plugins]]\nname = \"x\"\n[plugins.settings]\ndepth = 1\n"
	var cfg2 struct {
		Plugins []Primitive `toml:"plugins"`
	}
	md2, err := Decode(data, &cfg2)
	if err != nil {
		t.Fatal(err)
	}
	var p struct {
		Settings opaquePlugin `toml:"settings"`
	}
	r, err := md2.Session().Decode(cfg2.Plugins[0], &p)
	if err != nil {
		t.Fatal(err)
	}
	var rpaths []string
	for _, d := range r.Declarations() {
		rpaths = append(rpaths, d.Path)
	}
	want := []string{"plugins[0]", "plugins[0].settings", "plugins[0].settings.depth"}
	if !reflect.DeepEqual(rpaths, want) {
		t.Errorf("receipt declarations: have %v, want %v", rpaths, want)
	}
	if err := r.Accept(); err != nil {
		t.Fatal(err)
	}
	st := declStates(t, md2.Session())
	if st["plugins[0].settings.depth"] != DeclConsumed {
		t.Errorf("plugins[0].settings.depth: have %s, want consumed", st["plugins[0].settings.depth"])
	}
	if st["plugins[0].name"] != DeclPending {
		t.Errorf("plugins[0].name: have %s, want pending", st["plugins[0].name"])
	}
}

// Declarations are listed in document order, with array element indices.
func TestSessionDeclarationOrder(t *testing.T) {
	data := `
title = "x"
[[plugins]]
name = "a"
[[plugins]]
name = "b"
[plugins.extra]
deep = 1
`
	var cfg struct {
		Title   string      `toml:"title"`
		Plugins []Primitive `toml:"plugins"`
	}
	md, err := Decode(data, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range md.Session().Declarations() {
		got = append(got, d.Path)
	}
	want := []string{
		"title",
		"plugins",
		"plugins[0]", "plugins[0].name",
		"plugins[1]", "plugins[1].name", "plugins[1].extra", "plugins[1].extra.deep",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("have %v, want %v", got, want)
	}
	if got := DeclConsumed.String(); got != "consumed" {
		t.Errorf("DeclConsumed.String() = %q", got)
	}
	if !strings.Contains(DeclFailed.String(), "fail") {
		t.Errorf("DeclFailed.String() = %q", DeclFailed.String())
	}
}
