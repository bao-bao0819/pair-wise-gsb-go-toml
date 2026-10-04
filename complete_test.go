package toml_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// ---- target types ---------------------------------------------------------

type cBase struct {
	Enabled bool
	Weight  int `toml:"weight"`
}

type cEndpoint struct {
	URL    string `toml:"url"`
	Weight int    `toml:"weight"`
}

type cServer struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

type cService struct {
	Name      string      `toml:"name"`
	Endpoints []cEndpoint `toml:"endpoints"`
}

type cConfig struct {
	Title    string            `toml:"title"`
	Base     cBase             `toml:"base"`
	Servers  []cServer         `toml:"servers"`
	Services []cService        `toml:"services"`
	Meta     map[string]string `toml:"meta"`
	// Anonymous (embedded) fields are promoted as decoded fields.
	cBase
	// "weird.name" needs quoting and is a dotted reference candidate.
	WeirdName string `toml:"weird.name"`
	// Custom decoder must never be invoked by completion.
	Track *cTracked `toml:"track"`
}

type cTracked struct{ calls int }

func (t *cTracked) UnmarshalTOML(any) error {
	customDecodeCalls++
	return nil
}

var customDecodeCalls int

// ---- helpers --------------------------------------------------------------

// apply inserts the chosen completion into doc and returns the patched bytes.
func apply(doc []byte, c toml.Completion) []byte {
	var b bytes.Buffer
	b.Write(doc[:c.Replace[0]])
	b.WriteString(c.InsertText)
	b.Write(doc[c.Replace[1]:])
	return b.Bytes()
}

func findComp(t *testing.T, doc []byte, cur int, v any, key string) toml.Completion {
	t.Helper()
	res, err := toml.Complete(doc, cur, v)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	for _, c := range res.Completions {
		if c.Key == key {
			return c
		}
	}
	var got []string
	for _, c := range res.Completions {
		got = append(got, c.Key)
	}
	t.Fatalf("completion %q not found; got %v (reason=%q)", key, got, res.Reason)
	return toml.Completion{}
}

func mustNotSuggest(t *testing.T, doc []byte, cur int, v any, key string) {
	t.Helper()
	res, err := toml.Complete(doc, cur, v)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	for _, c := range res.Completions {
		if c.Key == key {
			t.Fatalf("unexpected completion %q at scope %v", key, res.Scope)
		}
	}
}

// ---- tests ----------------------------------------------------------------

func TestCompleteRootScalarAndApply(t *testing.T) {
	doc := []byte("title = \"x\"\ntit")
	cur := len(doc)

	if _, err := toml.Complete(doc, cur, cConfig{}); err != nil {
		t.Fatal(err)
	}
	c := findComp(t, doc, cur, cConfig{}, "title")
	if len(c.Path) != 1 || c.Path[0] != "title" {
		t.Fatalf("path = %v", c.Path)
	}
	if c.InsertText != "title = \"\"" {
		t.Fatalf("insert = %q", c.InsertText)
	}
	if c.Replace[0] != len("title = \"x\"\n") {
		t.Fatalf("replace start = %d", c.Replace[0])
	}

	patched := apply(doc, c)
	var out cConfig
	if err := toml.Unmarshal(patched, &out); err != nil {
		t.Fatalf("patched decode: %v\n%s", err, patched)
	}
	if out.Title != "" {
		t.Fatalf("title = %q", out.Title)
	}
}

func TestCompleteDottedReference(t *testing.T) {
	// Cursor sits after the dot of "base." inside a [server]-style body; use a
	// root-scoped dotted key so completion descends through the table.
	doc := []byte("base.ena")
	cur := len(doc)

	c := findComp(t, doc, cur, cConfig{}, "Enabled")
	want := []string{"base", "Enabled"}
	if fmt.Sprint(c.Path) != fmt.Sprint(want) {
		t.Fatalf("path = %v want %v", c.Path, want)
	}
	if c.InsertText != "Enabled = false" {
		t.Fatalf("insert = %q", c.InsertText)
	}

	patched := apply(doc, c)
	var out cConfig
	if err := toml.Unmarshal(patched, &out); err != nil {
		t.Fatalf("patched decode: %v\n%s", err, patched)
	}
	if !out.Base.Enabled && !out.Enabled {
		// cBase is also embedded; either promotion is acceptable, but the dotted
		// form must populate the nested table.
		t.Fatalf("base.Enabled not set\n%s", patched)
	}
	if !out.Base.Enabled {
		t.Fatalf("dotted base.Enabled not decoded: %+v", out.Base)
	}
}

func TestCompleteQuotedDottedKey(t *testing.T) {
	// "weird.name" contains a dot, so the emitted key must be quoted.
	doc := []byte("wei")
	cur := len(doc)

	c := findComp(t, doc, cur, cConfig{}, `"weird.name"`)
	patched := apply(doc, c)
	if !strings.Contains(string(patched), `"weird.name" = ""`) {
		t.Fatalf("patched missing quoted key:\n%s", patched)
	}
	var out cConfig
	if err := toml.Unmarshal(patched, &out); err != nil {
		t.Fatalf("patched decode: %v\n%s", err, patched)
	}
	if out.WeirdName != "" {
		t.Fatalf("weird name = %q", out.WeirdName)
	}
}

func TestCompleteNestedArrayTables(t *testing.T) {
	doc := []byte(
		"[[services]]\n" +
			"name = \"api\"\n" +
			"[[services.endpoints]]\n" +
			"")
	cur := len(doc)

	res, err := toml.Complete(doc, cur, cConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(res.Scope) != "[services endpoints]" {
		t.Fatalf("scope = %v", res.Scope)
	}
	c := findComp(t, doc, cur, cConfig{}, "url")
	if c.Type.Name != "string" {
		t.Fatalf("type = %q", c.Type.Name)
	}

	patched := apply(doc, c)
	var out cConfig
	if err := toml.Unmarshal(patched, &out); err != nil {
		t.Fatalf("patched decode: %v\n%s", err, patched)
	}
	if len(out.Services) != 1 || len(out.Services[0].Endpoints) != 1 {
		t.Fatalf("services = %+v", out.Services)
	}
	if out.Services[0].Endpoints[0].URL != "" {
		t.Fatalf("url = %q", out.Services[0].Endpoints[0].URL)
	}

	// Adding a second sibling element keeps completion scoped to the new
	// (empty) element, so "url" is still suggested.
	doc2 := append(append([]byte{}, doc...), []byte("url = \"u\"\n[[services.endpoints]]\n")...)
	mustFind := findComp(t, doc2, len(doc2), cConfig{}, "url")
	if got := apply(doc2, mustFind); !bytes.Contains(got, []byte(`url = ""`)) {
		t.Fatalf("second element completion wrong:\n%s", got)
	}
}

func TestCompleteArrayTableHeader(t *testing.T) {
	doc := []byte("[ser")
	cur := len(doc)
	res, err := toml.Complete(doc, cur, cConfig{})
	if err != nil {
		t.Fatal(err)
	}
	// Single-bracket header: arrays-of-tables need "[[" and are not offered;
	// only a real table is. Servers is []cServer so it must be excluded.
	for _, c := range res.Completions {
		if c.Key == "servers" {
			t.Fatalf("array-table offered for [ ] header")
		}
	}

	doc = []byte("[[ser")
	cur = len(doc)
	c := findComp(t, doc, cur, cConfig{}, "servers")
	if c.Type.Kind != toml.CompletionArrayTable {
		t.Fatalf("kind = %v", c.Type.Kind)
	}
	patched := apply(doc, c)
	if string(patched) != "[[servers]]" {
		t.Fatalf("patched = %q", patched)
	}
	var out cConfig
	if err := toml.Unmarshal(patched, &out); err != nil {
		t.Fatalf("patched decode: %v", err)
	}
	if len(out.Servers) != 0 {
		t.Fatalf("servers = %+v", out.Servers)
	}
}

func TestCompleteAnonymousEmbed(t *testing.T) {
	doc := []byte("ena")
	cur := len(doc)

	// The embedded cBase promotes "Enabled" and "weight" to the root alongside
	// any concrete fields; they must appear once and decode.
	c := findComp(t, doc, cur, cConfig{}, "Enabled")
	if fmt.Sprint(c.Path) != "[Enabled]" {
		t.Fatalf("path = %v", c.Path)
	}
	patched := apply(doc, c)
	var out cConfig
	if err := toml.Unmarshal(patched, &out); err != nil {
		t.Fatalf("patched decode: %v\n%s", err, patched)
	}
	if !out.Enabled {
		t.Fatalf("promoted embedded Enabled not set")
	}
}

func TestCompleteUTF8Cursor(t *testing.T) {
	// A multi-byte value before the cursor must not shift byte offsets.
	doc := []byte("title = \"café\"\ntit")
	cur := len(doc)
	c := findComp(t, doc, cur, cConfig{}, "title")
	patched := apply(doc, c)
	if _, err := toml.Complete(doc, cur, cConfig{}); err != nil {
		t.Fatal(err)
	}
	var out cConfig
	if err := toml.Unmarshal(patched, &out); err != nil {
		t.Fatalf("patched decode: %v\n%s", err, patched)
	}

	// Cursor landing in the middle of the multi-byte rune "é" is rejected.
	mid := bytes.Index(doc, []byte("café")) + len("caf")
	if _, err := toml.Complete(doc, mid, cConfig{}); err == nil {
		t.Fatalf("expected error for mid-rune cursor at %d", mid)
	}
}

func TestCompleteNotInStringOrComment(t *testing.T) {
	doc := []byte("title = \"ser\"\n# ser\n")

	inString := bytes.Index(doc, []byte("ser\""))
	res, err := toml.Complete(doc, inString+1, cConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason == "" || len(res.Completions) != 0 {
		t.Fatalf("string cursor: reason=%q comps=%v", res.Reason, res.Completions)
	}

	hash := bytes.Index(doc, []byte("# ser"))
	res, err = toml.Complete(doc, hash+3, cConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Reason == "" || len(res.Completions) != 0 {
		t.Fatalf("comment cursor: reason=%q comps=%v", res.Reason, res.Completions)
	}
}

func TestCompleteAlreadyDeclared(t *testing.T) {
	doc := []byte(
		"[[servers]]\n" +
			"host = \"a\"\n" +
			"")
	cur := len(doc)
	mustNotSuggest(t, doc, cur, cConfig{}, "host")

	// A fresh array-table element starts empty again.
	doc2 := append(append([]byte{}, doc...), []byte("[[servers]]\n")...)
	findComp(t, doc2, len(doc2), cConfig{}, "host")
}

func TestCompleteInlineTableNotExtended(t *testing.T) {
	doc := []byte("base = { Enabled = true }\n")
	cur := len(doc)
	// Completing at document root: "base" is a closed inline table value, so
	// starting it again is not suggested.
	mustNotSuggest(t, doc, cur, cConfig{}, "base")
}

func TestCompleteScopeError(t *testing.T) {
	// A broken header on a prior line destroys knowledge of the current scope.
	doc := []byte("[servers\nhost = \"a\"\n")
	cur := len(doc)
	_, err := toml.Complete(doc, cur, cConfig{})
	var se *toml.ScopeError
	if !errors.As(err, &se) {
		t.Fatalf("want *ScopeError, got %v", err)
	}
	if se.Reason == "" {
		t.Fatalf("empty reason")
	}

	// Duplicate keys before the cursor also leave the table ambiguous.
	dup := []byte("title = \"a\"\ntitle = \"b\"\n")
	if _, err := toml.Complete(dup, len(dup), cConfig{}); !errors.As(err, &se) {
		t.Fatalf("duplicate key: want ScopeError, got %v", err)
	}
}

func TestCompleteDoesNotInvokeCustomDecoder(t *testing.T) {
	customDecodeCalls = 0
	doc := []byte("trac")
	cur := len(doc)

	var cfg cConfig
	res, err := toml.Complete(doc, cur, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range res.Completions {
		if c.Key == "track" {
			found = true
		}
	}
	if !found {
		t.Fatalf("track not suggested")
	}
	if customDecodeCalls != 0 || cfg.Track != nil {
		t.Fatalf("custom decoder invoked or value allocated: calls=%d track=%v", customDecodeCalls, cfg.Track)
	}
}
