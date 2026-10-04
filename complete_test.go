package toml

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test types
// ---------------------------------------------------------------------------

type completeServer struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
}

type completeDB struct {
	Enabled bool      `toml:"enabled"`
	Host    string    `toml:"host"`
	When    time.Time `toml:"when"`
}

type completeBase struct {
	BaseName string `toml:"base-name"`
}

type completeService struct {
	completeBase
	Name    string           `toml:"name"`
	Server  completeServer   `toml:"server"`
	Servers []completeServer `toml:"servers"`
}

type completeRoot struct {
	Title    string          `toml:"title"`
	Count    int             `toml:"count"`
	Ratio    float64         `toml:"ratio"`
	On       bool            `toml:"on"`
	When     time.Time       `toml:"when"`
	Tags     []string        `toml:"tags"`
	DB       completeDB      `toml:"db"`
	Service  completeService `toml:"service"`
	Weird    string          `toml:"weird key"`
	Encoding completeCustom  `toml:"encoding"`
	IgnoreMe string          `toml:"-"`
	skipped  string
}

type completeCustom struct {
	calls int
	value string
}

func (c *completeCustom) UnmarshalTOML(any) error {
	c.calls++
	return nil
}

func (c *completeCustom) UnmarshalText(b []byte) error {
	c.calls++
	c.value = string(b)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func names(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = pathString(c.Path)
	}
	return out
}

func pathString(p []string) string { return strings.Join(p, ".") }

func findCandidate(cs []Candidate, path string) (Candidate, bool) {
	for _, c := range cs {
		if pathString(c.Path) == path {
			return c, true
		}
	}
	return Candidate{}, false
}

func assertCompleteError(t *testing.T, err error, reason string) *CompletionError {
	t.Helper()
	var ce *CompletionError
	if !errors.As(err, &ce) {
		t.Fatalf("want *CompletionError (%s), got %v", reason, err)
	}
	if ce.Reason != reason {
		t.Fatalf("want reason %q, got %q (%s)", reason, ce.Reason, ce.Message)
	}
	return ce
}

// applyCandidate replaces [Start,End) with c.InsertText and returns the doc.
func applyCandidate(doc string, cursor int, c Candidate) string {
	return doc[:c.Start] + c.InsertText + doc[c.End:]
}

// ---------------------------------------------------------------------------
// Basic field completion
// ---------------------------------------------------------------------------

func TestCompleteRootFields(t *testing.T) {
	doc := "ti"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "title")
	if !ok {
		t.Fatalf("expected title candidate; got %v", names(cs))
	}
	if c.InsertText != "title" || c.Start != 0 || c.End != 2 {
		t.Fatalf("bad candidate: %+v", c)
	}
	if c.TOMLHint != "String" || c.TypeHint != "string" {
		t.Fatalf("bad hints: %s/%s", c.TOMLHint, c.TypeHint)
	}
	if c.Kind != CandidateKey {
		t.Fatalf("want CandidateKey, got %v", c.Kind)
	}

	// All expected root fields are offered once; unexported and toml:"-" are
	// excluded.
	want := []string{"count", "db", "encoding", "on", "ratio", "service",
		"tags", "title", "weird key", "when"}
	got := names(CompleteAtOrPanic(t, "", completeRoot{}))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("root fields mismatch:\nwant %v\ngot  %v", want, got)
	}
}

func CompleteAtOrPanic(t *testing.T, doc string, target any) []Candidate {
	t.Helper()
	cs, err := CompleteAt(doc, len(doc), target)
	if err != nil {
		t.Fatalf("CompleteAt(%q): %v", doc, err)
	}
	return cs
}

func TestCompletePrefixFiltersByPrefix(t *testing.T) {
	// "wh" matches "when" but nothing else.
	cs, err := CompleteAt("wh", 2, completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if got := names(cs); !reflect.DeepEqual(got, []string{"when"}) {
		t.Fatalf("got %v", got)
	}

	// Case-insensitive fallback: "TI" still matches title.
	cs, _ = CompleteAt("TI", 2, completeRoot{})
	if _, ok := findCandidate(cs, "title"); !ok {
		t.Fatalf("case-insensitive match failed: %v", names(cs))
	}
}

func TestCompleteDeclaredKeysExcluded(t *testing.T) {
	doc := "title = \"hi\"\n"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "title"); ok {
		t.Fatal("already declared title was suggested")
	}
	if _, ok := findCandidate(cs, "count"); !ok {
		t.Fatal("undeclared count missing")
	}

	// Case-insensitive collision: "TITLE = 1" declares title too.
	cs, _ = CompleteAt("TITLE = 1\n", len("TITLE = 1\n"), completeRoot{})
	if _, ok := findCandidate(cs, "title"); ok {
		t.Fatal("case-insensitive declared key suggested")
	}
}

func TestCompleteDottedKey(t *testing.T) {
	// db.en -> db.enabled
	doc := "db.en"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "db.enabled")
	if !ok {
		t.Fatalf("want db.enabled, got %v", names(cs))
	}
	if c.InsertText != "enabled" || c.Start != 3 || c.End != 5 {
		t.Fatalf("bad range/text: %+v", c)
	}

	// Filled back with a value must decode.
	filled := doc[:c.Start] + c.InsertText + " = true" + doc[c.End:]
	var got completeRoot
	if _, err := Decode(filled, &got); err != nil {
		t.Fatalf("decode after completion failed: %v\n%s", err, filled)
	}
	if !got.DB.Enabled {
		t.Fatal("db.enabled not set")
	}

	// A scalar dotted parent cannot be descended.
	cs, _ = CompleteAt("title.en", len("title.en"), completeRoot{})
	if len(cs) != 0 {
		t.Fatalf("expected no candidates under scalar, got %v", names(cs))
	}
}

func TestCompleteDottedKeyDeclaredExcluded(t *testing.T) {
	doc := "db.enabled = true\ndb."
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "db.enabled"); ok {
		t.Fatal("db.enabled already declared")
	}
	if _, ok := findCandidate(cs, "db.host"); !ok {
		t.Fatal("db.host should remain")
	}
}

func TestCompleteDottedReferenceKeyRoundTrip(t *testing.T) {
	// Typing the dotted reference "db.ho" yields a candidate whose full
	// semantic path is db.host and whose insertion text is the bare last part.
	doc := "db.ho"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "db.host")
	if !ok {
		t.Fatalf("want db.host, got %v", names(cs))
	}
	if c.InsertText != "host" {
		t.Fatalf("insert %q", c.InsertText)
	}
	out := doc[:c.Start] + c.InsertText + ` = "localhost"` + doc[c.End:]
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.DB.Host != "localhost" {
		t.Fatalf("host=%q", got.DB.Host)
	}

	// Cursor inside a typed prefix: the replacement range is exactly the
	// typed bytes up to the cursor, so a caller can splice InsertText in.
	doc2 := "db.hos"
	cs2, err := CompleteAt(doc2, len(doc2), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c2, ok := findCandidate(cs2, "db.host")
	if !ok {
		t.Fatalf("prefix: %v", names(cs2))
	}
	if c2.Start != 3 || c2.End != len("db.hos") {
		t.Fatalf("range=%v", [2]int{c2.Start, c2.End})
	}
	out2 := doc2[:c2.Start] + c2.InsertText + ` = "localhost"` + doc2[c2.End:]
	var got2 completeRoot
	if _, err := Decode(out2, &got2); err != nil {
		t.Fatalf("decode prefix: %v\n%s", err, out2)
	}
	if got2.DB.Host != "localhost" {
		t.Fatalf("prefix host=%q", got2.DB.Host)
	}
}

func TestCompleteQuotedKey(t *testing.T) {
	// The field "weird key" needs quoting.
	cs, err := CompleteAt("wei", 3, completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "weird key")
	if !ok {
		t.Fatalf("want weird key, got %v", names(cs))
	}
	if c.InsertText != `"weird key"` {
		t.Fatalf("got insert %q", c.InsertText)
	}
	filled := "`" // replaced below
	_ = filled
	doc := "wei"
	out := doc[:c.Start] + c.InsertText + ` = "x"` + doc[c.End:]
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.Weird != "x" {
		t.Fatalf("got %q", got.Weird)
	}

	// Completing inside an unclosed quote also yields the quoted field.
	cs, _ = CompleteAt(`"wei`, 4, completeRoot{})
	c2, ok := findCandidate(cs, "weird key")
	if !ok {
		t.Fatalf("inside quote: %v", names(cs))
	}
	// Replacement covers the opening quote and partial text.
	doc2 := `"wei`
	out2 := doc2[:c2.Start] + c2.InsertText + ` = "x"` + doc2[c2.End:]
	var got2 completeRoot
	if _, err := Decode(out2, &got2); err != nil {
		t.Fatalf("decode quoted partial: %v\n%s", err, out2)
	}
	if got2.Weird != "x" {
		t.Fatalf("got %q", got2.Weird)
	}
}

// ---------------------------------------------------------------------------
// Tables and array tables
// ---------------------------------------------------------------------------

func TestCompleteTableHeader(t *testing.T) {
	doc := "[d"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "db")
	if !ok {
		t.Fatalf("want db header, got %v", names(cs))
	}
	if c.Kind != CandidateTable || c.TOMLHint != "Table" {
		t.Fatalf("bad kind/hint: %v %s", c.Kind, c.TOMLHint)
	}
	out := doc[:c.Start] + c.InsertText + "]\nenabled = true" + doc[c.End:]
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode header: %v\n%s", err, out)
	}
	if !got.DB.Enabled {
		t.Fatal("db.enabled not set after header completion")
	}
}

func TestCompleteHeaderInScopeDoesNotResetToRoot(t *testing.T) {
	// After [db], an incomplete [service...] header is still root-relative.
	doc := "[db]\nenabled = true\n[s"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "service"); !ok {
		t.Fatalf("want service header, got %v", names(cs))
	}
}

func TestCompleteDottedHeader(t *testing.T) {
	// [service.ser] -> server
	doc := "[service.ser"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "service.server")
	if !ok {
		t.Fatalf("want service.server, got %v", names(cs))
	}
	if c.Kind != CandidateTable {
		t.Fatalf("want table, got %v", c.Kind)
	}
	out := doc[:c.Start] + c.InsertText + "]\nhost = \"h\"\nport = 1" + doc[c.End:]
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.Service.Server.Host != "h" || got.Service.Server.Port != 1 {
		t.Fatalf("nested table not decoded: %+v", got.Service.Server)
	}
}

func TestCompleteArrayTableHeader(t *testing.T) {
	doc := "[[service.ser"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "service.servers")
	if !ok {
		t.Fatalf("want service.servers array header, got %v", names(cs))
	}
	if c.Kind != CandidateArrayTable || c.TOMLHint != "ArrayTable" {
		t.Fatalf("bad kind/hint: %v %s", c.Kind, c.TOMLHint)
	}

	// Fill back: complete the header and add an element's keys, then decode.
	docFull := "[[service.servers]]\n"
	cursor := len("[[service.ser")
	cs2, err := CompleteAt(docFull[:cursor], cursor, completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c2, ok := findCandidate(cs2, "service.servers")
	if !ok {
		t.Fatalf("second: %v", names(cs2))
	}
	prefix := docFull[:cursor]
	out := prefix[:c2.Start] + c2.InsertText + "]]\nhost = \"a\"\nport = 1"
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode array table: %v\n%s", err, out)
	}
	if len(got.Service.Servers) != 1 || got.Service.Servers[0].Host != "a" {
		t.Fatalf("array table element wrong: %+v", got.Service.Servers)
	}
}

func TestCompleteNestedArrayTableScope(t *testing.T) {
	// Inside the latest [[service.servers]] element, complete element fields.
	doc := "[[service.servers]]\nhost = \"a\"\nho"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	// "host" is already declared in this element, so only "port" matches "ho"?
	// Actually nothing starts with ho other than host; test "po" instead.
	doc2 := "[[service.servers]]\nhost = \"a\"\npo"
	cs2, err := CompleteAt(doc2, len(doc2), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs2, "service.servers.port")
	if !ok {
		t.Fatalf("want element port; got %v", names(cs2))
	}
	out := applyCandidate(doc2, len(doc2), c) + " = 8080"
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if got.Service.Servers[0].Port != 8080 {
		t.Fatalf("got %d", got.Service.Servers[0].Port)
	}
	_ = cs
}

func TestCompleteMultipleArrayTableElementsUseLast(t *testing.T) {
	doc := "[[service.servers]]\nhost = \"a\"\n" +
		"[[service.servers]]\n"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	// In the second element, host is not declared and must be suggested.
	if _, ok := findCandidate(cs, "service.servers.host"); !ok {
		t.Fatalf("host should be available in a fresh element: %v", names(cs))
	}
}

func TestCompleteExistingPlainTableNotSuggested(t *testing.T) {
	// A document that already defined [db] concretely; cursor is in a fresh,
	// unfinished header on the next line ("[" typed, no closing bracket yet).
	doc := "[db]\n["
	cs2, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs2, "db"); ok {
		t.Fatalf("concrete table db re-suggested: %v", names(cs2))
	}
}

func TestCompleteImplicitTableCanBeIntroduced(t *testing.T) {
	// "db.enabled = true" implicitly creates db; a [db] header is legal.
	doc := "db.enabled = true\n["
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "db"); !ok {
		t.Fatalf("implicit db header should be offered: %v", names(cs))
	}
}

// ---------------------------------------------------------------------------
// Inline tables
// ---------------------------------------------------------------------------

func TestCompleteInlineTable(t *testing.T) {
	doc := "db = { en"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "db.enabled")
	if !ok {
		t.Fatalf("want db.enabled inside inline table, got %v", names(cs))
	}
	out := doc[:c.Start] + c.InsertText + " = true }" + doc[c.End:]
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if !got.DB.Enabled {
		t.Fatal("inline db.enabled not set")
	}
}

func TestCompleteInlineTableDeclaredExcluded(t *testing.T) {
	doc := "db = { enabled = true, en"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "db.enabled"); ok {
		t.Fatal("inline declared key re-suggested")
	}

	// After a comma, undeclared fields are offered.
	doc2 := "db = { enabled = true, "
	cs2, err := CompleteAt(doc2, len(doc2), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs2, "db.enabled"); ok {
		t.Fatal("enabled still present after comma")
	}
	if _, ok := findCandidate(cs2, "db.host"); !ok {
		t.Fatalf("host missing: %v", names(cs2))
	}
}

func TestCompleteInlineTableClosedNoExtension(t *testing.T) {
	doc := "db = { enabled = true } x"
	_, err := CompleteAt(doc, len(doc), completeRoot{})
	assertCompleteError(t, err, ReasonInvalidContinuation)

	// A properly closed inline table with only whitespace/comment is just a
	// value position: no candidates and no error.
	cs, err := CompleteAt("db = { enabled = true } ", len("db = { enabled = true } "), completeRoot{})
	if err != nil || len(cs) != 0 {
		t.Fatalf("got cs=%v err=%v", names(cs), err)
	}
}

func TestCompleteNestedInlineTable(t *testing.T) {
	doc := "service = { server = { ho"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "service.server.host")
	if !ok {
		t.Fatalf("want nested host, got %v", names(cs))
	}
	out := doc[:c.Start] + c.InsertText + ` = "h" } }` + doc[c.End:]
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode nested: %v\n%s", err, out)
	}
	if got.Service.Server.Host != "h" {
		t.Fatal("nested inline host not set")
	}

	// Sibling key already written in nested table is excluded.
	doc2 := "service = { server = { host = \"h\", ho"
	cs2, err := CompleteAt(doc2, len(doc2), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs2, "service.server.host"); ok {
		t.Fatal("nested host re-suggested")
	}
}

// ---------------------------------------------------------------------------
// Embedded fields
// ---------------------------------------------------------------------------

func TestCompleteEmbeddedFields(t *testing.T) {
	// Anonymous completeBase is promoted; toml tags on promoted fields apply.
	cs, err := CompleteAt("service = { base", len("service = { base"), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "service.base-name")
	if !ok {
		t.Fatalf("promoted embedded field missing: %v", names(cs))
	}
	out := "service = { " + c.InsertText + ` = "b" }`
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode embedded: %v\n%s", err, out)
	}
	if got.Service.BaseName != "b" {
		t.Fatalf("base-name=%q", got.Service.BaseName)
	}

	// Embedded struct table header is also reachable by promotion when typing a
	// top-level key: here base-name lives under service, verify via header.
	cs2, err := CompleteAt("service = { ", len("service = { "), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs2, "service.base-name"); !ok {
		t.Fatalf("promoted name absent at empty prefix: %v", names(cs2))
	}
}

// ---------------------------------------------------------------------------
// UTF-8 cursor
// ---------------------------------------------------------------------------

type utf8Root struct {
	Name string `toml:"名称"`
	Host string `toml:"host"`
}

func TestCompleteUTF8Cursor(t *testing.T) {
	// Non-ASCII keys must be quoted; the cursor sits on a UTF-8 boundary
	// after the first Chinese rune inside the quote.
	doc := "host = \"h\"\n\"名"
	cursor := len(doc)
	if cursor != len("host = \"h\"\n\"")+3 {
		t.Fatalf("test setup wrong cursor %d", cursor)
	}
	cs, err := CompleteAt(doc, cursor, utf8Root{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs, "名称")
	if !ok {
		t.Fatalf("want 名称, got %v", names(cs))
	}
	if c.Start != len("host = \"h\"\n") || c.End != cursor {
		t.Fatalf("range must be byte offsets on UTF-8 boundary: %+v", c)
	}
	out := doc[:c.Start] + c.InsertText + ` = "x"` + doc[c.End:]
	var got utf8Root
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode utf8: %v\n%s", err, out)
	}
	if got.Name != "x" {
		t.Fatal("utf8 key not set")
	}

	// Cursor in the middle of a multi-byte rune is an invalid-cursor error.
	bad := len("host = \"h\"\n\"") + 1
	_, err = CompleteAt(doc, bad, utf8Root{})
	assertCompleteError(t, err, ReasonInvalidCursor)
}

// ---------------------------------------------------------------------------
// Strings, comments, arrays: no key completion
// ---------------------------------------------------------------------------

func TestCompleteNoCandidatesInsideValueOrComment(t *testing.T) {
	cases := []string{
		"title = \"ti",
		"title = 'ti",
		"# ti",
		"   # foo ba",
		"tags = [ \"a\",\n \"b",
		"title = \"\"\"still\nwriting",
	}
	for _, doc := range cases {
		cs, err := CompleteAt(doc, len(doc), completeRoot{})
		if err != nil {
			t.Errorf("%q: unexpected error %v", doc, err)
			continue
		}
		if len(cs) != 0 {
			t.Errorf("%q: expected no candidates, got %v", doc, names(cs))
		}
	}
}

// ---------------------------------------------------------------------------
// Broken scope
// ---------------------------------------------------------------------------

func TestCompleteScopeBroken(t *testing.T) {
	cases := []string{
		"no equals here\n",
		"x = 1\nbroken line\n",
		"[unclosed\nhost = 1\n",
		"title = \"unterminated\nhost = 1\n",
	}
	for _, doc := range cases {
		_, err := CompleteAt(doc, len(doc), completeRoot{})
		ce := assertCompleteError(t, err, ReasonScopeBroken)
		if ce.Offset < 0 {
			t.Errorf("%q: expected offset, got %d", doc, ce.Offset)
		}
	}
}

func TestCompleteScopeBrokenDoesNotFallBackToRoot(t *testing.T) {
	// A duplicate key is a scope error; completion must not guess the scope.
	doc := "[db]\nhost = \"a\"\nhost = \"b\"\n"
	_, err := CompleteAt(doc, len(doc), completeRoot{})
	assertCompleteError(t, err, ReasonScopeBroken)
}

func TestCompleteOpenArrayDoesNotBreakScope(t *testing.T) {
	// An unterminated array spanning lines is an open value, not a broken
	// scope; the cursor inside yields no candidates.
	doc := "tags = [\n  \"a\",\n"
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cs) != 0 {
		t.Fatalf("got %v", names(cs))
	}

	// A new line after the array is closed completes normally.
	doc2 := "tags = [\n  \"a\"\n]\n"
	cs2, err := CompleteAt(doc2, len(doc2), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs2, "count"); !ok {
		t.Fatalf("root keys unavailable after closed array: %v", names(cs2))
	}
}

// ---------------------------------------------------------------------------
// Custom decoders must never be invoked
// ---------------------------------------------------------------------------

func TestCompleteNeverInvokesCustomDecoder(t *testing.T) {
	custom := &completeCustom{}
	cs, err := CompleteAt("enco", 4, struct {
		Encoding completeCustom `toml:"encoding"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) == 0 {
		t.Fatal("expected encoding candidate")
	}
	_ = custom

	// Completing repeatedly must not allocate or call the custom type.
	before := completeCustom{}.calls
	for i := 0; i < 10; i++ {
		if _, err := CompleteAt("encoding = { ", len("encoding = { "), struct {
			Encoding completeCustom `toml:"encoding"`
		}{}); err != nil {
			t.Fatal(err)
		}
	}
	if (completeCustom{}).calls != before {
		t.Fatal("custom decoder method was invoked during completion")
	}
}

func TestCompleteTypeHints(t *testing.T) {
	cs := CompleteAtOrPanic(t, "", completeRoot{})
	hints := map[string][2]string{}
	for _, c := range cs {
		hints[pathString(c.Path)] = [2]string{c.TOMLHint, c.TypeHint}
	}
	expect := map[string][2]string{
		"count": {"Integer", "int"},
		"ratio": {"Float", "float64"},
		"on":    {"Bool", "bool"},
		"tags":  {"Array", "[]string"},
		"db":    {"Table", "toml.completeDB"},
		"when":  {"Datetime", "time.Time"},
	}
	for k, v := range expect {
		if got, ok := hints[k]; !ok {
			t.Errorf("missing hint for %s", k)
		} else if got != v {
			t.Errorf("hint %s = %v, want %v", k, got, v)
		}
	}
}

func TestCompleteInvalidArguments(t *testing.T) {
	if _, err := CompleteAt("abc", 10, completeRoot{}); err == nil {
		t.Fatal("expected out-of-range error")
	}
	if _, err := CompleteAt("abc", 1, 42); err == nil {
		t.Fatal("expected invalid target error")
	}
	ce := func() *CompletionError {
		_, err := CompleteAt("abc", 10, completeRoot{})
		var ce *CompletionError
		errors.As(err, &ce)
		return ce
	}()
	if ce.Reason != ReasonInvalidCursor {
		t.Fatalf("got reason %q", ce.Reason)
	}

	// reflect.Type is accepted.
	cs, err := CompleteAt("ti", 2, reflect.TypeOf(completeRoot{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "title"); !ok {
		t.Fatal("reflect.Type target failed")
	}

	// Pointer to struct is accepted.
	cs, err = CompleteAt("ti", 2, &completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "title"); !ok {
		t.Fatal("pointer target failed")
	}
}

func TestCompleteMapTargetYieldsNoFieldCandidates(t *testing.T) {
	cs, err := CompleteAt("ab", 2, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 0 {
		t.Fatalf("map targets have no static fields, got %v", names(cs))
	}
}

func TestCompleteArrayTableNewElementResetsDeclared(t *testing.T) {
	// Inside the first element, "host" declared means it is excluded; after
	// opening a second [[service.servers]], it becomes available again.
	first := "[[service.servers]]\nhost = \"a\"\nho"
	cs, err := CompleteAt(first, len(first), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "service.servers.host"); ok {
		t.Fatal("host must be excluded in the same element")
	}

	second := "[[service.servers]]\nhost = \"a\"\n[[service.servers]]\nh"
	cs2, err := CompleteAt(second, len(second), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := findCandidate(cs2, "service.servers.host")
	if !ok {
		t.Fatalf("host should be offered in the new element: %v", names(cs2))
	}
	out := second[:c.Start] + c.InsertText + ` = "b"` + second[c.End:]
	var got completeRoot
	if _, err := Decode(out, &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, out)
	}
	if len(got.Service.Servers) != 2 || got.Service.Servers[1].Host != "b" {
		t.Fatalf("elements wrong: %+v", got.Service.Servers)
	}
}

func TestCompleteHeaderArrayAppendsAfterDeclaredArrayTable(t *testing.T) {
	// An existing [[service.servers]] should still be suggested as an array
	// header because adding another element is legal.
	doc := "[[service.servers]]\nhost = \"a\"\n[[service."
	cs, err := CompleteAt(doc, len(doc), completeRoot{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := findCandidate(cs, "service.servers"); !ok {
		t.Fatalf("array header append missing: %v", names(cs))
	}
}

func TestCompletePrimitiveTypeHints(t *testing.T) {
	type prim struct {
		I int       `toml:"i"`
		F float64   `toml:"f"`
		B bool      `toml:"b"`
		S string    `toml:"s"`
		T time.Time `toml:"t"`
	}
	cs, err := CompleteAt("", 0, prim{})
	if err != nil {
		t.Fatal(err)
	}
	hints := map[string]string{}
	for _, c := range cs {
		hints[pathString(c.Path)] = c.TOMLHint
	}
	want := map[string]string{"i": "Integer", "f": "Float", "b": "Bool", "s": "String", "t": "Datetime"}
	if !reflect.DeepEqual(hints, want) {
		t.Fatalf("hints=%v want=%v", hints, want)
	}
}

func TestCompleteDoesNotMutateTarget(t *testing.T) {
	// Passing a value must not require or perform any allocation in it.
	var root completeRoot
	_, err := CompleteAt("ti", 2, root)
	if err != nil {
		t.Fatal(err)
	}
	if root.Title != "" {
		t.Fatal("target was mutated")
	}
}

// Ensure the common documentation sentence reads sensibly via Error().
func TestCompletionErrorMessage(t *testing.T) {
	_, err := CompleteAt("no eq\n", len("no eq\n"), completeRoot{})
	ce := assertCompleteError(t, err, ReasonScopeBroken)
	if !strings.Contains(ce.Error(), "syntax error") {
		t.Fatalf("message = %q", ce.Error())
	}
}
