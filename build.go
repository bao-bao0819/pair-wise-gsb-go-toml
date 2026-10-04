package toml

import (
	"reflect"
	"sort"
	"strings"
)

// buildCompletions turns the analyzed cursor location into candidate keys.
func buildCompletions(input string, cursor int, root reflect.Type, loc *location) CompletionResult {
	res := CompletionResult{Scope: append([]string(nil), loc.scope...)}

	if loc.frame != nil && loc.frame.inline {
		res.Reason = "cursor is inside a closed inline table"
		return res
	}

	// Resolve the struct type at the active table scope.
	st, ok := scopeType(root, loc.frame)
	if !ok {
		res.Reason = "table maps to a non-struct value; cannot add keys"
		return res
	}

	var declared map[string]declared
	if loc.frame != nil {
		declared = loc.frame.declared
	}

	fields := cachedTypeFields(st)
	type cand struct {
		f field
		k fieldKind
	}
	var cands []cand
	seen := map[string]bool{}
	for i := range fields {
		f := fields[i]
		if !hasFoldPrefix(f.name, loc.prefix) {
			continue
		}
		if seen[f.name] {
			continue
		}
		if d, dup := declared[f.name]; dup {
			if loc.mode == locHeader && !d.leaf {
				// Re-entering an existing subtable header is allowed, but it is
				// never a useful "new key" suggestion.
				continue
			}
			continue
		}
		ft := fieldType(f)
		k := classify(ft)
		if loc.mode == locHeader && loc.arrayHeader && k != kindArrayTable {
			continue
		}
		if loc.mode == locHeader && !loc.arrayHeader && k == kindScalar {
			continue
		}
		seen[f.name] = true
		cands = append(cands, cand{f: f, k: k})
	}

	sort.Slice(cands, func(i, j int) bool { return cands[i].f.name < cands[j].f.name })

	for _, c := range cands {
		res.Completions = append(res.Completions, makeCompletion(input, loc, c.f, c.k))
	}
	return res
}

func hasFoldPrefix(s, prefix string) bool {
	if len(prefix) == 0 {
		return true
	}
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

// scopeType walks root to the struct type represented by frame. Array-table
// element names descend into the slice element struct, and such descent is
// only allowed where the frame really is an array-table element: a dotted
// scalar cannot reach into a slice that was never established as [[array]].
func scopeType(root reflect.Type, fr *frame) (reflect.Type, bool) {
	t := root
	idPos := 0
	for _, seg := range fr.semantic {
		t = deref(t)
		if t.Kind() != reflect.Struct {
			return nil, false
		}
		if idPos >= len(fr.id) || fr.id[idPos] != seg {
			return nil, false
		}
		f, ok := lookupField(t, seg)
		if !ok {
			return nil, false
		}
		t = fieldType(f)
		switch {
		case t.Kind() == reflect.Struct:
			if t == timeTimeType {
				return nil, false
			}
			idPos++
		case t.Kind() == reflect.Slice || t.Kind() == reflect.Array:
			t = deref(t.Elem())
			if t.Kind() != reflect.Struct {
				return nil, false
			}
			if idPos+1 >= len(fr.id) || !strings.HasPrefix(fr.id[idPos+1], elementMarker) {
				return nil, false
			}
			idPos += 2
		default:
			return nil, false
		}
	}
	t = deref(t)
	if t.Kind() != reflect.Struct {
		return nil, false
	}
	return t, true
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func makeCompletion(input string, loc *location, f field, k fieldKind) Completion {
	ft := fieldType(f)
	key := quoteSegment(f.name)

	full := append([]string(nil), loc.scope...)
	full = append(full, f.name)

	c := Completion{
		Path:    full,
		Key:     key,
		Replace: [2]int{loc.activeStart, loc.activeEnd},
		Type:    CompletionType{Name: typeName(ft), Kind: kindToKind(k)},
	}

	switch loc.mode {
	case locHeader:
		closer := "]"
		if loc.arrayHeader {
			closer = "]]"
		}
		// Replace only the active segment; the opener and prior pieces stay.
		if loc.appendCloser != "" {
			c.InsertText = key + closer
		} else {
			c.InsertText = key
		}
	default:
		if loc.emitValue {
			c.InsertText = key + " = " + valueFor(ft, k)
		} else {
			c.InsertText = key
		}
	}

	return c
}

func kindToKind(k fieldKind) CompletionKind {
	switch k {
	case kindTable:
		return CompletionTable
	case kindArrayTable:
		return CompletionArrayTable
	default:
		return CompletionScalar
	}
}

// valueFor returns a decodable literal for a body-key completion.
func valueFor(ft reflect.Type, k fieldKind) string {
	switch k {
	case kindTable:
		return "{}"
	case kindArrayTable:
		return "[]"
	case kindOpaque:
		return `""`
	default:
		return zeroLiteral(ft)
	}
}

func typeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Pointer:
		return "*" + typeName(t.Elem())
	case reflect.Slice:
		return "[]" + typeName(t.Elem())
	case reflect.Array:
		return "[" + reflectArrayLen(t) + "]" + typeName(t.Elem())
	case reflect.Map:
		return "map[" + typeName(t.Key()) + "]" + typeName(t.Elem())
	default:
		if name := t.Name(); name != "" {
			return name
		}
		return t.String()
	}
}

func reflectArrayLen(t reflect.Type) string {
	return intToString(t.Len())
}

func intToString(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
