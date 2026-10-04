package toml

import (
	"encoding"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// CompletionKind describes the TOML construct a completion produces.
type CompletionKind int

const (
	// CompletionScalar is a single key = value pair, such as "name = \"\"".
	CompletionScalar CompletionKind = iota
	// CompletionTable introduces a table header, such as "[server]".
	CompletionTable
	// CompletionArrayTable introduces an array-of-tables header "[[name]]".
	CompletionArrayTable
)

// CompletionType is a hint about the Go type a completion writes to.
//
// It is a hint for editor UI only; it is not needed to apply a completion.
type CompletionType struct {
	// Name is the Go type name without a package qualifier, e.g. "int",
	// "Config", "[]Server", or "map[string]string".
	Name string
	// Kind is the broad TOML construct the value uses.
	Kind CompletionKind
}

func (t CompletionType) String() string { return t.Name }

// A Completion is a single key that may continue the document at the cursor.
//
// Replace is the byte range [Start, End) in the original document occupied by
// the partial key segment being typed. Applying a completion is:
//
//	patched := append(append([]byte{}, doc[:Replace.Start]...),
//	                   append([]byte(c.InsertText), doc[Replace.End:]...)...)
//
// InsertText is chosen so that the patched document is valid TOML that
// [Unmarshal] accepts for the type the completion was requested against.
type Completion struct {
	// Path is the full semantic path from the document root, e.g.
	// ["servers", "alpha", "host"]. It does not include an array index;
	// array-of-tables elements are reached through their table name.
	Path []string

	// Key is the single, already quoted if required, segment that replaces
	// the partial segment at the cursor, e.g. `host` or `"my.key"`.
	Key string

	// InsertText is the full text to write at Replace.Start; it includes Key
	// plus whatever is required to leave a valid TOML document (an " = " value
	// for scalar keys, or a closing bracket for an unfinished table header).
	InsertText string

	// Replace is the byte range of the partial key segment to replace.
	Replace [2]int

	// Type hints at the Go value the key maps to.
	Type CompletionType
}

// CompletionResult is the result of a [Complete] call.
//
// When the cursor is not positioned on a key that may be extended (for example
// it sits inside a string value or a comment), Completions is empty and Reason
// explains why no key completion is available.
type CompletionResult struct {
	// Scope is the semantic table path the cursor completes inside, starting
	// from the document root. The segments of a dotted key already typed on
	// the active line are appended as the cursor descends into them.
	Scope []string

	// Completions are the applicable keys, excluding keys already declared in
	// the enclosing table (or array-of-tables element) and keys that cannot
	// follow the partial dotted path typed so far.
	Completions []Completion

	// Reason is non-empty when the location cannot complete a key.
	Reason string
}

// ScopeError reports that the scope at the cursor cannot be determined because
// the document contains a structural syntax error before the cursor.
//
// Completion deliberately does not fall back to the root table in this case,
// because the key would be inserted into the wrong table.
type ScopeError struct {
	// Offset is the byte offset of the offending token.
	Offset int
	// Line and Col are 1-based and refer to the offending token.
	Line int
	Col  int
	// Reason describes the structural problem.
	Reason string
}

func (e *ScopeError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("toml: cannot determine table scope at byte %d (line %d, column %d): %s",
			e.Offset, e.Line, e.Col, e.Reason)
	}
	return fmt.Sprintf("toml: cannot determine table scope at byte %d: %s", e.Offset, e.Reason)
}

// Complete returns the keys that may continue the TOML document doc at byte
// offset cursor, given the Go value type v decodes into.
//
// cursor is a byte offset (not a rune index) and must lie on a UTF-8 boundary;
// this makes completion correct for documents containing multi-byte runes.
//
// The scope is reconstructed with the same parser used by [Unmarshal], so
// table headers, dotted keys and array-of-tables elements are honored. Already
// declared keys are not suggested, and a closed inline table is never
// extended. The cursor may stand on an unfinished key or table header; it is
// not treated as a key when it lies inside a string value or a comment.
//
// A structural syntax error before the cursor is returned as a *[ScopeError]:
// the scope is genuinely unknown and completion must not guess. v is inspected
// through reflection only; it is never instantiated, and custom
// [Unmarshaler]/[encoding.TextUnmarshaler] implementations are never called.
func Complete(doc []byte, cursor int, v any) (CompletionResult, error) {
	if cursor < 0 || cursor > len(doc) {
		return CompletionResult{}, fmt.Errorf("toml: cursor %d is outside the document (length %d)", cursor, len(doc))
	}
	if cursor > 0 && !utf8Boundary(doc, cursor) {
		return CompletionResult{}, fmt.Errorf("toml: cursor %d is not on a UTF-8 boundary", cursor)
	}

	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return CompletionResult{}, fmt.Errorf("toml: cannot complete against a nil type")
	}
	root := rv.Type()
	for root.Kind() == reflect.Pointer {
		root = root.Elem()
	}
	if root.Kind() != reflect.Struct {
		return CompletionResult{}, fmt.Errorf("toml: completion requires a struct target, got %s", root)
	}

	input, bom := stripBOM(string(doc))
	cursor -= bom

	a, err := analyze(input, cursor)
	if err != nil {
		var se *ScopeError
		if errors.As(err, &se) {
			se.Offset += bom
		}
		return CompletionResult{}, err
	}
	if a.reason != "" {
		return CompletionResult{Scope: a.scope, Reason: a.reason}, nil
	}

	res := buildCompletions(input, cursor, root, a)
	if bom > 0 {
		for i := range res.Completions {
			res.Completions[i].Replace[0] += bom
			res.Completions[i].Replace[1] += bom
		}
	}
	return res, nil
}

// fieldKind classifies a struct field type for completion purposes. It mirrors
// the loose rules of unify() but never invokes user code.
type fieldKind int

const (
	kindScalar fieldKind = iota
	kindTable
	kindArrayTable
	kindOpaque // implements UnmarshalTOML; treated as an atomic leaf
)

var (
	unmarshalerType = reflect.TypeOf((*Unmarshaler)(nil)).Elem()
	textUnmarshaler = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	timeTimeType    = reflect.TypeOf(time.Time{})
)

func classify(ft reflect.Type) fieldKind {
	if ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	if ft.Implements(unmarshalerType) || reflect.PointerTo(ft).Implements(unmarshalerType) {
		return kindOpaque
	}
	if ft.Implements(textUnmarshaler) || reflect.PointerTo(ft).Implements(textUnmarshaler) {
		return kindOpaque
	}
	switch ft.Kind() {
	case reflect.Struct:
		if ft == timeTimeType {
			return kindScalar
		}
		return kindTable
	case reflect.Slice, reflect.Array:
		elem := ft.Elem()
		if elem.Kind() == reflect.Pointer {
			elem = elem.Elem()
		}
		if elem.Kind() == reflect.Struct && elem != timeTimeType {
			return kindArrayTable
		}
		return kindScalar
	default:
		return kindScalar
	}
}

// lookupField resolves one TOML key segment against a struct type, applying
// the exact-then-case-insensitive matching used by unifyStruct.
func lookupField(ft reflect.Type, name string) (field, bool) {
	var ci *field
	for _, f := range cachedTypeFields(ft) {
		if f.name == name {
			return f, true
		}
		if ci == nil && strings.EqualFold(f.name, name) {
			ff := f
			ci = &ff
		}
	}
	if ci != nil {
		return *ci, true
	}
	return field{}, false
}

// fieldType dereferences the field type the way decode does.
func fieldType(f field) reflect.Type {
	ft := f.typ
	if ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	return ft
}

// zeroLiteral returns a TOML literal for the zero value of ft so that an
// inserted key is immediately decodable.
func zeroLiteral(ft reflect.Type) string {
	if ft.Kind() == reflect.Pointer {
		ft = ft.Elem()
	}
	switch ft.Kind() {
	case reflect.Bool:
		return "false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return "0"
	case reflect.Struct:
		if ft == timeTimeType {
			return "0001-01-01T00:00:00Z"
		}
		return "" // never reached: tables use headers, not a value literal
	case reflect.Slice, reflect.Array:
		return "[]"
	default:
		return `""`
	}
}

func quoteSegment(s string) string {
	return Key{s}.maybeQuoted(0)
}

func stripBOM(s string) (string, int) {
	if strings.HasPrefix(s, "\xef\xbb\xbf") {
		return s[3:], 3
	}
	if strings.HasPrefix(s, "\xff\xfe") || strings.HasPrefix(s, "\xfe\xff") {
		return s[2:], 2
	}
	return s, 0
}

func utf8Boundary(doc []byte, i int) bool {
	if i >= len(doc) {
		return true
	}
	b := doc[i]
	return b < 0x80 || b >= 0xC0
}
