package toml

import (
	"errors"
	"fmt"
	"strings"
)

// elementMarker is the synthetic final id segment that identifies one
// array-of-tables element. It is not a real TOML key, so it is never rendered
// in a semantic path.
const elementMarker = "\x00aot"

// frame is one table scope: the root, a [table], one [[array-table]] element,
// or an inline table value.
type frame struct {
	id       []string // unique path, including elementMarker segments
	semantic []string // real key path from the document root
	declared map[string]declared
	inline   bool
}

type declared struct{ leaf bool }

func newFrame(id, semantic []string, inline bool) *frame {
	return &frame{
		id:       append([]string(nil), id...),
		semantic: append([]string(nil), semantic...),
		declared: map[string]declared{},
		inline:   inline,
	}
}

func idString(id []string) string { return strings.Join(id, "\x00") }

// location describes where the cursor sits and what it is completing.
type location struct {
	mode locMode

	scope []string // semantic table path the active key belongs to

	// Dotted header/key segments already committed before the active one.
	pieces []string
	// Byte range (post-BOM) of the active segment on the active line.
	activeStart int
	activeEnd   int
	prefix      string // decoded text of the active segment up to the cursor
	quoted      bool

	arrayHeader bool // [[ ...

	// emitValue is true when a body-key completion must append " = <value>".
	emitValue bool
	// appendCloser is the header terminator to add (" ]" / " ]]") when the
	// active header has no closing bracket yet; empty otherwise.
	appendCloser string

	frame  *frame // immediate frame the active segments descend from
	rep    *replayState
	reason string
}

type locMode int

const (
	locKey locMode = iota
	locHeader
	locNone
)

func analyze(input string, cursor int) (*location, error) {
	lineStart := 0
	if i := strings.LastIndexByte(input[:cursor], '\n'); i >= 0 {
		lineStart = i + 1
	}

	rep, openValue, err := replayPrefix(input, lineStart)
	if err != nil {
		return nil, err
	}

	lineEnd := len(input)
	if i := strings.IndexByte(input[cursor:], '\n'); i >= 0 {
		lineEnd = cursor + i
	}
	full := input[lineStart:lineEnd]
	col := cursor - lineStart

	loc := &location{rep: rep, frame: rep.current}

	if openValue {
		loc.mode = locNone
		loc.reason = "cursor is inside a value that spans multiple lines"
		return loc, nil
	}

	if err := classifyLine(full, col, loc); err != nil {
		return nil, err
	}
	// classifyLine works with line-relative columns; report document offsets.
	if loc.mode != locNone {
		loc.activeStart += lineStart
		loc.activeEnd += lineStart
	}
	return loc, nil
}

// replayState holds every frame discovered in the valid prefix.
type replayState struct {
	current *frame
	frames  map[string]*frame
	arrays  map[string][]*frame // array id path → its elements, in order
}

// replayPrefix lexes and interprets every complete line before the active one.
// It also reports whether a multi-line value (open array or multiline string)
// is still being written at the boundary.
func replayPrefix(input string, lineStart int) (*replayState, bool, error) {
	root := newFrame(nil, nil, false)
	rep := &replayState{
		current: root,
		frames:  map[string]*frame{"": root},
		arrays:  map[string][]*frame{},
	}
	prefix := input[:lineStart]
	if prefix == "" {
		return rep, false, nil
	}

	lx := lex(prefix)
	var items []item
	var lexErr *item
	arrayDepth := 0
	for {
		it := lx.nextItem()
		if it.typ == itemEOF {
			break
		}
		if it.typ == itemError {
			lexErr = &it
			break
		}
		if it.typ == itemArray {
			arrayDepth++
		}
		if it.typ == itemArrayEnd {
			arrayDepth--
		}
		items = append(items, it)
	}

	if lexErr != nil {
		if isMultilineEOFError(lexErr) {
			return rep, true, nil
		}
		pos := lexErr.pos.withCol(input)
		reason := lexErr.val
		if lexErr.err != nil {
			reason = lexErr.err.Error()
		}
		return nil, false, &ScopeError{
			Offset: lexErr.pos.Start,
			Line:   pos.Line,
			Col:    pos.Col,
			Reason: strings.TrimSpace(reason),
		}
	}

	rep.interpret(items)
	if arrayDepth == 0 {
		if _, perr := parse(prefix, 128); perr != nil {
			var pe ParseError
			if errors.As(perr, &pe) {
				pos := pe.Position.withCol(input)
				return nil, false, &ScopeError{
					Offset: pos.Start,
					Line:   pos.Line,
					Col:    pos.Col,
					Reason: pe.Message,
				}
			}
			return nil, false, &ScopeError{Offset: 0, Reason: perr.Error()}
		}
	}
	return rep, arrayDepth > 0, nil
}

func isMultilineEOFError(it *item) bool {
	msg := it.val
	if it.err != nil {
		msg = it.err.Error()
	}
	return strings.Contains(msg, `expected '"""'`) || strings.Contains(msg, `expected "'''"`)
}

func (r *replayState) interpret(items []item) {
	for i := 0; i < len(items); {
		switch items[i].typ {
		case itemCommentStart:
			i += 2
		case itemTableStart:
			i = r.readHeader(items, i, false)
		case itemArrayTableStart:
			i = r.readHeader(items, i, true)
		case itemKeyStart:
			i = r.readKeyValue(items, i)
		default:
			i++
		}
	}
}

func readHeaderKey(items []item, i int) (Key, int) {
	var key Key
	for ; i < len(items); i++ {
		t := items[i].typ
		if t == itemTableEnd || t == itemArrayTableEnd || t == itemEOF {
			i++
			break
		}
		if t == itemText || isStringItem(t) {
			key = append(key, decodeKeyItem(items[i]))
		}
	}
	return key, i
}

func (r *replayState) readHeader(items []item, i int, array bool) int {
	key, ni := readHeaderKey(items, i+1)
	i = ni

	f := r.root()
	for _, seg := range key.parent() {
		f.declared[seg] = declared{leaf: false}
		f = r.descend(f, seg)
	}
	last := key.last()
	f.declared[last] = declared{leaf: false}

	if array {
		arrayID := idString(append(append([]string(nil), f.id...), last))
		n := len(r.arrays[arrayID])
		elem := newFrame(
			append(append([]string(nil), f.id...), last, elementMarker+fmt.Sprint(n)),
			append(append([]string(nil), f.semantic...), last),
			false,
		)
		r.frames[idString(elem.id)] = elem
		r.arrays[arrayID] = append(r.arrays[arrayID], elem)
		r.current = elem
	} else {
		r.current = r.descend(f, last)
	}
	return i
}

func (r *replayState) root() *frame { return r.frames[""] }

// descend moves from f into the table named seg, entering the latest
// array-table element when seg names an array, otherwise getting or creating
// the implicit table.
func (r *replayState) descend(f *frame, seg string) *frame {
	arrayID := idString(append(append([]string(nil), f.id...), seg))
	if elems, ok := r.arrays[arrayID]; ok {
		return elems[len(elems)-1]
	}
	id := append(append([]string(nil), f.id...), seg)
	if cf, ok := r.frames[idString(id)]; ok {
		return cf
	}
	cf := newFrame(id, append(append([]string(nil), f.semantic...), seg), false)
	r.frames[idString(cf.id)] = cf
	return cf
}

func (r *replayState) readKeyValue(items []item, i int) int {
	key, ni := readKey(items, i)
	i = ni
	f := r.current
	for _, seg := range key.parent() {
		f.declared[seg] = declared{leaf: false}
		f = r.descend(f, seg)
	}
	f.declared[key.last()] = declared{leaf: true}
	i = r.skipValue(items, i, f, key.last())
	return i
}

// readKey consumes a "keyStart ... keyEnd" sequence and returns the decoded
// key plus the index of the first value token.
func readKey(items []item, i int) (Key, int) {
	var key Key
	for i++; items[i].typ != itemKeyEnd; i++ {
		t := items[i].typ
		if t == itemText || isStringItem(t) {
			key = append(key, decodeKeyItem(items[i]))
		}
	}
	return key, i + 1
}

// skipValue consumes one value token (and any array/inline-table body) and
// records inline-table keys.
func (r *replayState) skipValue(items []item, i int, enclosing *frame, name string) int {
	if i >= len(items) {
		return i
	}
	switch items[i].typ {
	case itemArray:
		depth := 1
		i++
		for i < len(items) && depth > 0 {
			switch items[i].typ {
			case itemArray:
				depth++
				i++
			case itemArrayEnd:
				depth--
				i++
			case itemInlineTableStart:
				i = r.skipInline(items, i, nil)
			case itemCommentStart:
				i += 2
			default:
				i++
			}
		}
		return i
	case itemInlineTableStart:
		cf := newFrame(
			append(append([]string(nil), enclosing.id...), name),
			append(append([]string(nil), enclosing.semantic...), name),
			true,
		)
		r.frames[idString(cf.id)] = cf
		return r.skipInline(items, i, cf)
	default:
		return i + 1
	}
}

func (r *replayState) skipInline(items []item, i int, cf *frame) int {
	i++ // InlineTableStart
	for i < len(items) && items[i].typ != itemInlineTableEnd {
		switch items[i].typ {
		case itemCommentStart:
			i += 2
		case itemKeyStart:
			key, ni := readKey(items, i)
			i = ni
			parent := cf
			for _, seg := range key.parent() {
				parent.declared[seg] = declared{leaf: false}
				nested := newFrame(
					append(append([]string(nil), parent.id...), seg),
					append(append([]string(nil), parent.semantic...), seg),
					true,
				)
				r.frames[idString(nested.id)] = nested
				parent = nested
			}
			parent.declared[key.last()] = declared{leaf: true}
			i = r.skipValue(items, i, parent, key.last())
		default:
			i++
		}
	}
	return i + 1 // past InlineTableEnd
}

func isStringItem(t itemType) bool {
	switch t {
	case itemString, itemStringEsc, itemRawString, itemMultilineString, itemRawMultilineString:
		return true
	}
	return false
}

func decodeKeyItem(it item) string {
	switch it.typ {
	case itemRawString, itemRawMultilineString:
		return it.val
	case itemString, itemMultilineString:
		return it.val
	case itemStringEsc:
		return decodeBasicString(it.val)
	default:
		return it.val
	}
}
