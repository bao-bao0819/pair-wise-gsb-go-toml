package toml

import (
	"encoding"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// CompletionError reports that completion cannot be performed.
//
// This is returned when the document cannot be edited at the cursor for a
// structural reason: the cursor is inside a string value or comment, the
// document before the cursor contains a syntax error that makes the active
// table undeterminable, or the requested continuation is illegal at the
// cursor (for example extending an already closed inline table).
type CompletionError struct {
	// Reason is a short machine-oriented category describing why completion
	// is not possible, e.g. "cursor_in_value".
	Reason string
	// Message is a human readable explanation.
	Message string
	// Offset is the byte offset related to the reason, if any. It is -1 when
	// there is no specific location.
	Offset int
}

func (e *CompletionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return e.Message
}

const (
	// ReasonCursorInValue is used when the cursor is inside a string value,
	// multiline value, array, or comment, where typing is not a key.
	ReasonCursorInValue = "cursor_in_value"
	// ReasonInvalidContinuation is used when the cursor position cannot
	// legally hold a new key, e.g. after a closed inline table.
	ReasonInvalidContinuation = "invalid_continuation"
	// ReasonScopeBroken is used when a syntax error before the cursor makes
	// the active table impossible to determine.
	ReasonScopeBroken = "scope_broken"
	// ReasonInvalidCursor is used when the cursor is out of range or not on a
	// UTF-8 boundary.
	ReasonInvalidCursor = "invalid_cursor"
	// ReasonInvalidTarget is used when the target type is not a struct,
	// pointer to a struct, map, or a reflect.Type describing one.
	ReasonInvalidTarget = "invalid_target"
)

// Candidate is a single key completion offered at the cursor.
type Candidate struct {
	// Path is the full semantic path of the key from the root table, with
	// each dotted part as an element. Table header candidates use the path of
	// the table itself.
	Path []string

	// InsertText is the text that replaces [Start, End) in the document.
	// It is the key part (or table header content) that can be written at the
	// cursor, quoted as required.
	InsertText string

	// Start is the byte offset where the replacement begins.
	Start int
	// End is the byte offset where the replacement ends (exclusive).
	End int

	// Kind reports whether the candidate completes a regular key ("key") or a
	// table header ("table" / "array-table").
	Kind CandidateKind

	// TypeHint describes the Go type the key decodes into, e.g. "int",
	// "time.Time", or "[]*pkg.Server".
	TypeHint string
	// TOMLHint describes the TOML kind of the key: "String", "Integer",
	// "Float", "Bool", "Datetime", "Array", "Table" or "ArrayTable".
	TOMLHint string
}

// CandidateKind distinguishes regular key continuations from table headers.
type CandidateKind int

const (
	// CandidateKey completes a key = value pair.
	CandidateKey CandidateKind = iota
	// CandidateTable completes a [table] header.
	CandidateTable
	// CandidateArrayTable completes a [[array-of-tables]] header.
	CandidateArrayTable
)

func (k CandidateKind) String() string {
	switch k {
	case CandidateTable:
		return "table"
	case CandidateArrayTable:
		return "array-table"
	default:
		return "key"
	}
}

// CompleteAt returns the keys that can be written at the byte offset cursor in
// doc.
//
// target is the Go type the document decodes into. It may be a value, pointer,
// or a reflect.Type, but the target type must ultimately be a struct or a
// map[string]... (only structs produce key candidates; map targets report no
// candidates because their keys are not known statically).
//
// CompleteAt never calls UnmarshalTOML, UnmarshalText, or any other user
// method; it uses reflection on types only.
//
// A nil slice with a nil error is returned when the cursor is not in a
// position that can start a key (for example inside a string value or a
// comment). A *CompletionError is returned when the active scope cannot be
// determined or the continuation is illegal.
func CompleteAt(doc string, cursor int, target any) ([]Candidate, error) {
	if cursor < 0 || cursor > len(doc) {
		return nil, &CompletionError{
			Reason:  ReasonInvalidCursor,
			Message: fmt.Sprintf("cursor %d is outside the document (length %d)", cursor, len(doc)),
			Offset:  cursor,
		}
	}
	if !utf8Boundary(doc, cursor) {
		return nil, &CompletionError{
			Reason:  ReasonInvalidCursor,
			Message: "cursor is not on a UTF-8 boundary",
			Offset:  cursor,
		}
	}

	rt, err := resolveTargetType(target)
	if err != nil {
		return nil, err
	}

	bom := 0
	if strings.HasPrefix(doc, "\xef\xbb\xbf") {
		bom = 3
	}

	lineStart := strings.LastIndexByte(doc[:cursor], '\n') + 1
	if lineStart < bom {
		lineStart = bom
	}

	// Scan everything before the cursor line to determine the lexical mode at
	// the start of the line and to catch hard syntax errors that destroy the
	// scope.
	pre := doc[:lineStart]
	st, hardErr, hardOffset := scanPrefix(pre, bom)
	if hardErr != "" {
		return nil, &CompletionError{
			Reason:  ReasonScopeBroken,
			Message: "syntax error before cursor: " + hardErr,
			Offset:  hardOffset,
		}
	}
	if st.mode != modeTop {
		// The cursor is physically inside an unterminated multiline value
		// (string, array) that started on a previous line; the text there is a
		// value, never a key.
		return nil, nil
	}

	// Parse the complete prefix strictly so that the active table context,
	// implicit tables, duplicate keys, and header conflicts are understood
	// with the same rules the decoder uses. An unfinished table header on the
	// cursor line is itself incomplete, so the strict prefix excludes it.
	// The strict prefix only ever contains complete lines before the cursor.
	// Everything on the cursor line is an in-progress edit and is understood
	// tolerantly by analyzeLine.
	p, perr := parse(doc[:lineStart], 0)
	if perr != nil {
		pe, ok := perr.(ParseError)
		if !ok {
			return nil, &CompletionError{
				Reason:  ReasonScopeBroken,
				Message: perr.Error(),
				Offset:  -1,
			}
		}
		off := pe.Position.Start
		return nil, &CompletionError{
			Reason:  ReasonScopeBroken,
			Message: "syntax error before cursor: " + pe.Message,
			Offset:  off,
		}
	}

	scope := append(Key(nil), p.context...)

	// Analyze the text from the start of the cursor line up to the cursor.
	la := analyzeLine(doc[lineStart:cursor])

	switch la.kind {
	case lineClosedExt:
		return nil, &CompletionError{
			Reason:  ReasonInvalidContinuation,
			Message: "cannot extend an inline table after it is closed",
			Offset:  cursor,
		}
	case lineValue, lineComment, lineInvalid:
		return nil, nil
	}

	switch la.kind {
	case lineHeader:
		return completeHeader(la, p, scope, rt, lineStart)
	case lineKey, lineEmpty:
		return completeKey(la, p, scope, rt, lineStart)
	}
	return nil, nil
}

func utf8Boundary(s string, i int) bool {
	if i == 0 || i == len(s) {
		return true
	}
	return (s[i] & 0xc0) != 0x80
}

func resolveTargetType(target any) (reflect.Type, error) {
	var rt reflect.Type
	switch t := target.(type) {
	case nil:
		return nil, &CompletionError{Reason: ReasonInvalidTarget, Message: "target type is nil", Offset: -1}
	case reflect.Type:
		rt = t
	default:
		rt = reflect.TypeOf(target)
	}
	for rt != nil && rt.Kind() == reflect.Pointer {
		rt = rt.Elem()
	}
	if rt == nil || (rt.Kind() != reflect.Struct && rt.Kind() != reflect.Map) {
		return nil, &CompletionError{
			Reason:  ReasonInvalidTarget,
			Message: fmt.Sprintf("target must decode to a struct or map, got %s", rt),
			Offset:  -1,
		}
	}
	return rt, nil
}

// preMode describes the lexical context at a line boundary.
type preMode int

const (
	modeTop preMode = iota
	modeBracket
	modeString
	modeMLString
	modeLit
	modeMLLit
)

type preFrame struct {
	// array is true for '[', false for inline table '{'.
	array bool
}

type preScanner struct {
	s     string
	pos   int
	line  int
	mode  preMode
	stack []preFrame
	// err is a hard syntax error that makes scopes undeterminable. Inline
	// table newlines are hard errors; unclosed constructs at the end are not.
	err     string
	errPos  int
	topLine bool // scanning at top level on the current (new) line
}

// scanPrefix scans everything before the cursor line. It returns the lexical
// mode at the end of the prefix, plus any hard syntax error that destroys
// scope information (message and byte offset).
func scanPrefix(s string, bom int) (preScanner, string, int) {
	st := preScanner{s: s, pos: bom, line: 1, mode: modeTop, topLine: true}
	st.run()
	return st, st.err, st.errPos
}

func (st *preScanner) run() {
	for st.pos < len(st.s) {
		switch st.mode {
		case modeTop:
			st.scanTop()
		case modeBracket:
			st.scanBracket()
		case modeString:
			st.scanQuoted('"', false)
		case modeLit:
			st.scanQuoted('\'', false)
		case modeMLString:
			st.scanQuoted('"', true)
		case modeMLLit:
			st.scanQuoted('\'', true)
		}
		if st.err != "" {
			return
		}
	}
}

func (st *preScanner) hardf(offset int, msg string) {
	if st.err == "" {
		st.err = msg
		st.errPos = offset
	}
}

// scanTop handles a position at the top level (outside values).
func (st *preScanner) scanTop() {
	i := st.pos
	switch c := st.s[i]; {
	case c == ' ' || c == '\t':
		st.pos++
	case c == '\n' || c == '\r':
		if c == '\r' && i+1 < len(st.s) && st.s[i+1] == '\n' {
			i++
		}
		st.pos = i + 1
		st.line++
		st.topLine = true
	case c == '#':
		j := strings.IndexByte(st.s[i:], '\n')
		if j < 0 {
			st.pos = len(st.s)
		} else {
			st.pos = i + j
		}
	case c == '[' && st.topLine:
		// Table header: scan the header name without treating '[' as a value.
		if i+1 < len(st.s) && st.s[i+1] == '[' {
			st.scanHeaderName(i + 2)
		} else {
			st.scanHeaderName(i + 1)
		}
	case c == '"' || c == '\'':
		st.scanStringStart(i)
	default:
		// Start of a key path; find '=' then enter value scanning.
		st.scanTopKey(i)
	}
}

// scanHeaderName skips a [..] / [[..]] header, treating quoted segments
// literally. The closing bracket(s) are consumed; the remainder of the line is
// handled by the ordinary top-level scan (comments/whitespace).
func (st *preScanner) scanHeaderName(start int) {
	i := start
	for i < len(st.s) {
		switch c := st.s[i]; {
		case c == ' ' || c == '\t' || c == '.' || c == '\r' || c == '\n':
			i++
			if c == '\n' {
				st.line++
			}
		case c == '"' || c == '\'':
			end, ok := skipQuoted(st.s, i)
			if !ok {
				// Unterminated quoted header at end of prefix: the header is
				// incomplete, but it is still a top-level context.
				st.pos = len(st.s)
				return
			}
			i = end
		case c == ']':
			if i+1 < len(st.s) && st.s[i+1] == ']' {
				i++
			}
			i++
			st.pos = i
			st.topLine = false
			return
		default:
			i++
		}
	}
	st.pos = i
}

// scanTopKey scans a top-level key path starting at i until '=' and then
// switches to value scanning.
func (st *preScanner) scanTopKey(start int) {
	i := start
	for i < len(st.s) {
		c := st.s[i]
		switch {
		case c == ' ' || c == '\t' || c == '.':
			i++
		case c == '"' || c == '\'':
			end, ok := skipQuoted(st.s, i)
			if !ok {
				st.pos = len(st.s)
				st.mode = modeTop
				return
			}
			i = end
		case c == '=':
			st.pos = i + 1
			st.enterValue()
			return
		case c == '\n' || c == '\r':
			// A line without '=' is a hard syntax error: the scope after it
			// cannot be trusted.
			st.hardf(i, "expected '=' in key/value pair")
			return
		default:
			i++
		}
	}
	st.pos = i
}

func (st *preScanner) enterValue() {
	st.skipSpace()
	if st.pos >= len(st.s) {
		return
	}
	c := st.s[st.pos]
	switch {
	case c == '[':
		st.stack = append(st.stack, preFrame{array: true})
		st.pos++
		st.mode = modeBracket
	case c == '{':
		st.stack = append(st.stack, preFrame{array: false})
		st.pos++
		st.mode = modeBracket
	case c == '"' || c == '\'':
		st.scanStringStart(st.pos)
	default:
		// Scalar: consume until comma/bracket/comment/newline at the current
		// bracket depth; simplest is to let scanBracket handle scalars with an
		// empty stack by scanning to end-of-line.
		st.mode = modeBracket
	}
}

// scanBracket scans while inside an array/inline table or after a top-level
// scalar. With an empty stack it only skips to end of line.
func (st *preScanner) scanBracket() {
	if len(st.stack) == 0 {
		st.scanScalarToLineEnd()
		return
	}
	fr := st.stack[len(st.stack)-1]
	i := st.pos
	if i >= len(st.s) {
		return
	}
	c := st.s[i]
	switch {
	case c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ',':
		if c == '\n' {
			if !fr.array {
				st.hardf(i, "newlines are not allowed in inline tables")
				return
			}
			st.line++
		}
		if c == '\r' {
			if i+1 < len(st.s) && st.s[i+1] == '\n' {
				i++
			}
		}
		st.pos = i + 1
	case c == '#':
		j := strings.IndexByte(st.s[i:], '\n')
		if j < 0 {
			st.pos = len(st.s)
		} else {
			st.pos = i + j
		}
	case c == '[':
		st.stack = append(st.stack, preFrame{array: true})
		st.pos++
	case c == '{':
		st.stack = append(st.stack, preFrame{array: false})
		st.pos++
	case c == ']':
		if !fr.array {
			st.hardf(i, "expected '}' to close inline table")
			return
		}
		st.popFrame()
	case c == '}':
		if fr.array {
			st.hardf(i, "expected ']' to close array")
			return
		}
		st.popFrame()
	case c == '"' || c == '\'':
		st.scanStringStart(i)
	default:
		st.pos++
	}
}

func (st *preScanner) popFrame() {
	st.pos++
	st.stack = st.stack[:len(st.stack)-1]
	if len(st.stack) == 0 {
		// Value complete; skip remainder of the top-level line.
		st.mode = modeTop
		st.topLine = false
		st.scanScalarToLineEnd()
	}
}

// scanScalarToLineEnd consumes a top-level scalar value (number, bool, date)
// and any trailing comment until the newline, then returns to top mode.
func (st *preScanner) scanScalarToLineEnd() {
	i := st.pos
	for i < len(st.s) {
		c := st.s[i]
		if c == '\n' || c == '\r' {
			st.pos = i
			st.mode = modeTop
			return
		}
		i++
	}
	st.pos = i
}

func (st *preScanner) scanStringStart(i int) {
	q := st.s[i]
	triple := i+2 < len(st.s) && st.s[i+1] == q && st.s[i+2] == q
	if triple {
		st.pos = i + 3
		if q == '"' {
			st.mode = modeMLString
		} else {
			st.mode = modeMLLit
		}
	} else {
		st.pos = i + 1
		if q == '"' {
			st.mode = modeString
		} else {
			st.mode = modeLit
		}
	}
}

// scanQuoted scans the inside of a string from st.pos. q is the quote byte; ml
// indicates a triple-quoted string.
func (st *preScanner) scanQuoted(q byte, ml bool) {
	s := st.s
	for st.pos < len(s) {
		c := s[st.pos]
		if ml {
			if c == q && st.pos+2 < len(s) && s[st.pos+1] == q && s[st.pos+2] == q {
				st.pos += 3
				st.afterString()
				return
			}
			if q == '"' && c == '\\' {
				st.pos++
				if st.pos < len(s) && (s[st.pos] == '\n' || s[st.pos] == '\r') {
					if s[st.pos] == '\r' && st.pos+1 < len(s) && s[st.pos+1] == '\n' {
						st.pos++
					}
					st.line++
				}
			}
			if c == '\n' {
				st.line++
			}
			st.pos++
			continue
		}
		// Single-line.
		if c == '\n' || c == '\r' {
			// A single-line string cannot contain a raw newline. This makes
			// every following line's scope undeterminable, so it is a hard
			// error. (A cursor on the same line as the unterminated string is
			// handled separately by the cursor-line analysis.)
			st.hardf(st.pos, "newline is not allowed in a single-line string")
			st.pos = len(s)
			return
		}
		if q == '"' && c == '\\' {
			st.pos += 2
			continue
		}
		if c == q {
			st.pos++
			st.afterString()
			return
		}
		st.pos++
	}
	// Unterminated at end of prefix: remains in a string mode.
}

func (st *preScanner) afterString() {
	if len(st.stack) == 0 {
		st.mode = modeTop
		st.topLine = false
		st.scanScalarToLineEnd()
	} else {
		st.mode = modeBracket
	}
}

func (st *preScanner) skipSpace() {
	for st.pos < len(st.s) {
		c := st.s[st.pos]
		if c != ' ' && c != '\t' {
			return
		}
		st.pos++
	}
}

// skipQuoted skips a quoted key segment starting at s[i] (the quote byte). It
// returns the offset just after the closing quote. ok is false if the string
// is unterminated or a newline appears in a single-line string.
func skipQuoted(s string, i int) (end int, ok bool) {
	q := s[i]
	j := i + 1
	for j < len(s) {
		c := s[j]
		if c == '\n' || c == '\r' {
			return len(s), false
		}
		if q == '"' && c == '\\' {
			j += 2
			continue
		}
		if c == q {
			return j + 1, true
		}
		j++
	}
	return len(s), false
}

// ---------------------------------------------------------------------------
// Cursor line analysis
// ---------------------------------------------------------------------------

type lineKind int

const (
	lineEmpty  lineKind = iota
	lineKey             // cursor can complete a key path (top level or inline table)
	lineValue           // cursor is in a value position
	lineHeader          // cursor is inside [..] / [[..]]
	lineComment
	lineInvalid   // generic; no completion
	lineClosedExt // text after a closed inline table: illegal continuation
)

type segInfo struct {
	text   string
	start  int
	end    int
	quoted bool
	closed bool
}

type lineAnalysis struct {
	kind       lineKind
	arrayTable bool // header is [[ ]]

	// Complete segments preceding the partial one, from the root of the
	// current key expression.
	complete []string
	partial  segInfo
	hasPart  bool

	replStart int
	replEnd   int

	// inInline indicates that the key expression is a field of an inline
	// table value. inlineDeclared holds the complete sibling key paths and
	// parent names used by that expression.
	inlineDeclared [][]string
	// inlineOwners is the sequence of key parts of the enclosing inline table
	// value, used to locate the parent map for "already declared" checks.
	inlineOwners []string
}

func analyzeLine(s string) lineAnalysis {
	la := lineAnalysis{kind: lineEmpty}
	p := &lineParser{s: s}
	p.ws()
	if p.i >= len(s) {
		la.replStart = p.i
		la.replEnd = p.i
		return la
	}
	if s[p.i] == '#' {
		la.kind = lineComment
		return la
	}
	if s[p.i] == '[' {
		return parseHeader(p)
	}
	return parseTopLevelKey(p)
}

type lineParser struct {
	s string
	i int
}

func (p *lineParser) ws() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func parseHeader(p *lineParser) lineAnalysis {
	la := lineAnalysis{kind: lineHeader}
	if p.i+1 < len(p.s) && p.s[p.i+1] == '[' {
		la.arrayTable = true
		p.i++
	}
	p.i++ // consume '['

	for {
		p.ws()
		if p.i >= len(p.s) {
			la.replStart = p.i
			la.replEnd = p.i
			return la
		}
		if p.s[p.i] == ']' {
			return lineAnalysis{kind: lineInvalid}
		}
		seg, ok := p.readSeg()
		if !ok {
			return lineAnalysis{kind: lineInvalid}
		}
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == '.' {
			if !seg.closed {
				return lineAnalysis{kind: lineInvalid}
			}
			la.complete = append(la.complete, seg.text)
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == ']' {
			return lineAnalysis{kind: lineInvalid}
		}
		// Partial last segment.
		la.partial = seg
		la.hasPart = true
		la.replStart = seg.start
		la.replEnd = seg.end
		return la
	}
}

// parseTopLevelKey reads a top-level key path until '='. When '=' is found it
// continues with value handling; if the cursor is reached while still in the
// key path, the partial key is returned.
func parseTopLevelKey(p *lineParser) lineAnalysis {
	var complete []string
	for {
		p.ws()
		if p.i >= len(p.s) {
			return keyResult(nil, complete, p.i)
		}
		if p.s[p.i] == '#' {
			return lineAnalysis{kind: lineComment}
		}
		if p.s[p.i] == '}' || p.s[p.i] == ']' {
			return lineAnalysis{kind: lineInvalid}
		}
		seg, ok := p.readSeg()
		if !ok {
			return lineAnalysis{kind: lineInvalid}
		}
		p.ws()
		if p.i < len(p.s) && p.s[p.i] == '.' {
			if !seg.closed {
				return lineAnalysis{kind: lineInvalid}
			}
			complete = append(complete, seg.text)
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == '=' {
			p.i++
			return afterEq(p, nil, append(complete, seg.text))
		}
		return keyResultWithSeg(nil, complete, seg)
	}
}

// frame represents one open inline table while scanning a cursor line.
type frame struct {
	owners   []string   // key path of the table itself
	declared [][]string // immediate child key paths completed in the table
}

// parseFrame parses pairs inside an inline table; frames holds the open tables.
func parseFrame(p *lineParser, frames []frame) lineAnalysis {
	for {
		p.ws()
		if p.i >= len(p.s) {
			return keyResult(frames, nil, p.i)
		}
		switch p.s[p.i] {
		case '#':
			return lineAnalysis{kind: lineComment}
		case '}':
			p.i++
			frames = frames[:len(frames)-1]
			return afterClosedInline(p, frames)
		case ',':
			p.i++
			continue
		}

		seg, ok := p.readSeg()
		if !ok {
			return lineAnalysis{kind: lineInvalid}
		}
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != '=' {
			// Partial field name inside the (possibly nested) inline table.
			return keyResultWithSeg(frames, nil, seg)
		}
		p.i++

		p.ws()
		if p.i >= len(p.s) {
			return lineAnalysis{kind: lineValue}
		}
		cur := frames[len(frames)-1]
		cur.declared = append(cur.declared, []string{seg.text})
		frames[len(frames)-1] = cur

		// Nested inline table value.
		if p.s[p.i] == '{' {
			p.i++
			childOwners := append(append([]string{}, cur.owners...), seg.text)
			frames = append(frames, frame{owners: childOwners})
			continue
		}
		// Any other value: skip scalars/arrays/strings to ',' or '}'.
		skipInlineScalar(p)
	}
}

// afterClosedInline resumes scanning after a '}' closes one inline table. If
// there are still open frames it returns to pair scanning; at the top level,
// trailing non-comment text is an illegal continuation (but we return
// lineInvalid since key completion is impossible either way).
func afterClosedInline(p *lineParser, frames []frame) lineAnalysis {
	p.ws()
	if p.i >= len(p.s) || p.s[p.i] == '#' {
		return lineAnalysis{kind: lineValue}
	}
	if len(frames) > 0 {
		switch p.s[p.i] {
		case ',':
			p.i++
			return parseFrame(p, frames)
		case '}':
			p.i++
			return afterClosedInline(p, frames[:len(frames)-1])
		}
	}
	return lineAnalysis{kind: lineClosedExt}
}

// afterEq handles text after '=' for a pair whose full key path is keyPath.
// frames holds any enclosing inline tables.
func afterEq(p *lineParser, frames []frame, keyPath []string) lineAnalysis {
	p.ws()
	if p.i >= len(p.s) {
		return lineAnalysis{kind: lineValue}
	}
	switch p.s[p.i] {
	case '#':
		return lineAnalysis{kind: lineComment}
	case '"', '\'', '[':
		return lineAnalysis{kind: lineValue}
	case '{':
		p.i++
		frames = append(frames, frame{owners: keyPath})
		return parseFrame(p, frames)
	default:
		return lineAnalysis{kind: lineValue}
	}
}

// keyResult builds a lineKey result for an empty partial segment at pos.
func keyResult(frames []frame, complete []string, pos int) lineAnalysis {
	la := lineAnalysis{
		kind:      lineKey,
		complete:  append([]string{}, complete...),
		replStart: pos,
		replEnd:   pos,
	}
	attachFrame(&la, frames)
	return la
}

func keyResultWithSeg(frames []frame, complete []string, seg segInfo) lineAnalysis {
	la := lineAnalysis{
		kind:      lineKey,
		complete:  append([]string{}, complete...),
		partial:   seg,
		hasPart:   true,
		replStart: seg.start,
		replEnd:   seg.end,
	}
	attachFrame(&la, frames)
	return la
}

func attachFrame(la *lineAnalysis, frames []frame) {
	if len(frames) == 0 {
		return
	}
	cur := frames[len(frames)-1]
	la.inlineOwners = append([]string{}, cur.owners...)
	la.inlineDeclared = append([][]string{}, cur.declared...)
}

// skipInlineScalar advances over a scalar or array value until the next ','
// or '}' at the current nesting. Quoted strings are skipped atomically.
func skipInlineScalar(p *lineParser) {
	depth := 0
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"' || c == '\'':
			if end, ok := skipQuoted(p.s, p.i); ok {
				p.i = end
				continue
			}
			p.i = len(p.s)
			return
		case c == '[':
			depth++
		case c == ']':
			depth--
		case c == '}' && depth == 0:
			return
		case c == ',' && depth == 0:
			return
		}
		p.i++
	}
}

// readSeg reads one dotted key segment starting at p.i and advances p.i to the
// first byte after the segment. Quoted, unterminated segments are accepted as
// partial input.
func (p *lineParser) readSeg() (segInfo, bool) {
	start := p.i
	if p.i >= len(p.s) {
		return segInfo{}, false
	}
	q := p.s[p.i]
	if q == '"' || q == '\'' {
		segStart := p.i
		p.i++
		textStart := p.i
		var b strings.Builder
		for p.i < len(p.s) {
			c := p.s[p.i]
			if c == '\n' || c == '\r' {
				break
			}
			if q == '"' && c == '\\' && p.i+1 < len(p.s) {
				r, w, _ := decodeEscape(p.s[p.i:])
				b.WriteString(p.s[textStart:p.i])
				b.WriteRune(r)
				p.i += w
				textStart = p.i
				continue
			}
			if c == q {
				b.WriteString(p.s[textStart:p.i])
				text := b.String()
				p.i++
				return segInfo{text: text, start: segStart, end: p.i, quoted: true, closed: true}, true
			}
			p.i++
		}
		b.WriteString(p.s[textStart:p.i])
		return segInfo{text: b.String(), start: segStart, end: p.i, quoted: true, closed: false}, true
	}

	for p.i < len(p.s) {
		r, w := utf8.DecodeRuneInString(p.s[p.i:])
		if r == utf8.RuneError && w == 1 {
			return segInfo{}, false
		}
		if !isBareKeyChar(r) {
			// Any non-bare rune (delimiter, whitespace, or multi-byte rune
			// that isn't part of a bare key) terminates the segment.
			break
		}
		p.i += w
	}
	if p.i == start {
		return segInfo{}, false
	}
	return segInfo{text: p.s[start:p.i], start: start, end: p.i, quoted: false, closed: true}, true
}

// ---------------------------------------------------------------------------
// Candidate generation
// ---------------------------------------------------------------------------

var (
	timeTimeType        = reflect.TypeOf(time.Time{})
	textUnmarshalerType = reflect.TypeOf((*encoding.TextUnmarshaler)(nil)).Elem()
	unmarshalTomlType   = reflect.TypeOf((*Unmarshaler)(nil)).Elem()
)

func completeHeader(la lineAnalysis, p *parser, scope Key, root reflect.Type, lineStart int) ([]Candidate, error) {
	path := append(Key{}, la.complete...)
	t, node, ok := navigateForHeader(p, path, root)
	if !ok {
		return nil, nil
	}
	if t == nil || (t.Kind() != reflect.Struct && t.Kind() != reflect.Map) {
		return nil, nil
	}

	prefix := ""
	if la.hasPart {
		prefix = la.partial.text
	}

	fields := collectFields(t)
	var out []Candidate
	for _, f := range fields {
		if !nameMatch(f.name, prefix) {
			continue
		}
		ft := f.typ
		hint, tomlKind := typeHint(ft)
		if la.arrayTable {
			et, isSlice := tableSliceElem(ft)
			if !isSlice {
				continue
			}
			ft = et
			hint, tomlKind = typeHint(et)
			tomlKind = "ArrayTable"
		} else {
			if !isTableType(ft) {
				continue
			}
			tomlKind = "Table"
		}
		fullPath := append(append(Key{}, path...), f.name)
		if headerNameTaken(p, node, f.name, la.arrayTable, fullPath) {
			continue
		}
		out = append(out, Candidate{
			Path:       fullPath,
			InsertText: quoteSingle(f.name),
			Start:      lineStart + la.replStart,
			End:        lineStart + la.replEnd,
			Kind:       boolKind(la.arrayTable),
			TypeHint:   hint,
			TOMLHint:   tomlKind,
		})
	}
	sortCandidates(out)
	return out, nil
}

func boolKind(array bool) CandidateKind {
	if array {
		return CandidateArrayTable
	}
	return CandidateTable
}

func completeKey(la lineAnalysis, p *parser, scope Key, root reflect.Type, lineStart int) ([]Candidate, error) {
	// Determine the type and mapping node to complete fields of.
	var (
		t        reflect.Type
		node     any
		declared map[string]struct{}
	)

	if len(la.inlineOwners) > 0 {
		// Inline table value: descend from the scope type along the owner
		// path, then along complete dotted parts within the table.
		t2, node2, ok := navigatePath(root, p, scope, la.inlineOwners)
		if !ok || t2 == nil {
			return nil, nil
		}
		t3, node3, ok := descendInType(t2, node2, la.complete, p)
		if !ok {
			return nil, nil
		}
		t, node = t3, node3
		declared = inlineDeclaredSet(la.inlineDeclared)
	} else {
		t2, node2, ok := navigatePath(root, p, scope, la.complete)
		if !ok {
			return nil, nil
		}
		t, node = t2, node2
		declared = documentDeclared(node)
	}

	if t == nil || (t.Kind() != reflect.Struct && t.Kind() != reflect.Map) {
		return nil, nil
	}

	prefix := ""
	if la.hasPart {
		prefix = la.partial.text
	}

	fields := collectFields(t)
	var out []Candidate
	for _, f := range fields {
		if !nameMatch(f.name, prefix) {
			continue
		}
		if _, used := declared[f.name]; used {
			continue
		}
		// Case-insensitive collision with a declared key (decoder would
		// accept it as the same key).
		if ciUsed(declared, f.name) {
			continue
		}
		hint, tomlKind := typeHint(f.typ)
		fullPath := fullKeyPath(scope, la, f.name)
		out = append(out, Candidate{
			Path:       fullPath,
			InsertText: quoteSingle(f.name),
			Start:      lineStart + la.replStart,
			End:        lineStart + la.replEnd,
			Kind:       CandidateKey,
			TypeHint:   hint,
			TOMLHint:   tomlKind,
		})
	}
	sortCandidates(out)
	return out, nil
}

func fullKeyPath(scope Key, la lineAnalysis, name string) []string {
	out := append(Key{}, scope...)
	out = append(out, la.inlineOwners...)
	out = append(out, la.complete...)
	out = append(out, name)
	return out
}

func ciUsed(set map[string]struct{}, name string) bool {
	for k := range set {
		if strings.EqualFold(k, name) {
			return true
		}
	}
	return false
}

func inlineDeclaredSet(paths [][]string) map[string]struct{} {
	out := make(map[string]struct{})
	for _, pth := range paths {
		if len(pth) == 0 {
			continue
		}
		// Only immediate children matter; nested keys establish the parent.
		out[pth[0]] = struct{}{}
	}
	return out
}

// documentDeclared returns the immediate child names already present in a
// mapping node.
func documentDeclared(node any) map[string]struct{} {
	out := make(map[string]struct{})
	switch m := node.(type) {
	case map[string]any:
		for k := range m {
			out[k] = struct{}{}
		}
	}
	return out
}

// headerNameTaken reports whether a header with the leaf name cannot be
// suggested because the name is already used at this level.
//
// For [[array]] headers, an existing array of tables allows appending another
// element, so the name is not "taken".
//
// For [table] headers, an existing concrete table conflicts; only a table that
// was merely created implicitly (e.g. as the parent of a dotted key) may be
// introduced explicitly.
func headerNameTaken(p *parser, node any, name string, arrayHeader bool, fullPath Key) bool {
	m, ok := node.(map[string]any)
	if !ok {
		// No mapping node yet (e.g. completing a brand new table): nothing is
		// taken.
		return false
	}
	v, exists := m[name]
	if !exists {
		return false
	}
	if arrayHeader {
		_, isAppend := v.([]map[string]any)
		return !isAppend
	}
	if _, isMap := v.(map[string]any); !isMap {
		return true
	}
	// Explicitly defined table: redefining it is illegal.
	return !p.isImplicit(fullPath)
}

// navigatePath descends from the root type and the document mapping at scope
// through parts (dotted key parts or header parts). Returns the leaf type and
// mapping node. ok is false if the path conflicts structurally.
func navigatePath(root reflect.Type, p *parser, scope Key, parts []string) (reflect.Type, any, bool) {
	t, node, ok := descendInType(root, p.mapping, scope, p)
	if !ok {
		return nil, nil, false
	}
	return descendInType(t, node, parts, p)
}

// descendInType walks through parts, following struct fields and mapping
// values. For slices-of-struct fields (array tables) it descends into the
// element type and the most recent table in the mapping.
func descendInType(t reflect.Type, node any, parts []string, p *parser) (reflect.Type, any, bool) {
	for _, part := range parts {
		if t == nil {
			return nil, nil, false
		}
		if t.Kind() == reflect.Map {
			// Maps have no static fields; descend into value type if present.
			vt := t.Elem()
			vt = deref(vt)
			t = vt
			if m, ok := node.(map[string]any); ok {
				node = m[part]
			} else {
				node = nil
			}
			continue
		}
		if t.Kind() != reflect.Struct {
			return nil, nil, false
		}
		f := findField(t, part)
		if f == nil {
			return nil, nil, false
		}
		ft := deref(f.typ)
		if m, ok := node.(map[string]any); ok {
			node = m[part]
		}
		if ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array {
			et := deref(ft.Elem())
			if et.Kind() != reflect.Struct && et.Kind() != reflect.Map {
				return nil, nil, false
			}
			// Array table: use the last element's mapping.
			if arr, ok := node.([]map[string]any); ok && len(arr) > 0 {
				node = arr[len(arr)-1]
			} else {
				node = nil
			}
			t = et
			continue
		}
		t = ft
	}
	return t, node, true
}

func navigateForHeader(p *parser, parts Key, root reflect.Type) (reflect.Type, any, bool) {
	t, node, ok := navigatePath(root, p, nil, parts)
	if !ok {
		return nil, nil, false
	}
	return t, node, true
}

// scopeNode returns the mapping node for the current table scope, resolving
// array-table elements to their last table.
func scopeNode(p *parser, scope Key) any {
	var node any = p.mapping
	for _, k := range scope {
		m, ok := node.(map[string]any)
		if !ok {
			return nil
		}
		node = m[k]
		if arr, ok := node.([]map[string]any); ok {
			if len(arr) == 0 {
				return nil
			}
			node = arr[len(arr)-1]
		}
	}
	return node
}

func deref(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func findField(t reflect.Type, name string) *field {
	if t.Kind() != reflect.Struct {
		return nil
	}
	fields := cachedTypeFields(t)
	var ci *field
	for i := range fields {
		if fields[i].name == name {
			return &fields[i]
		}
		if ci == nil && strings.EqualFold(fields[i].name, name) {
			ci = &fields[i]
		}
	}
	return ci
}

func collectFields(t reflect.Type) []field {
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	return cachedTypeFields(t)
}

func isTableType(t reflect.Type) bool {
	t = deref(t)
	if t == nil {
		return false
	}
	switch t.Kind() {
	case reflect.Struct, reflect.Map:
		return true
	}
	return false
}

func tableSliceElem(t reflect.Type) (reflect.Type, bool) {
	t = deref(t)
	if t == nil {
		return nil, false
	}
	if t.Kind() != reflect.Slice && t.Kind() != reflect.Array {
		return nil, false
	}
	et := deref(t.Elem())
	if et.Kind() != reflect.Struct && et.Kind() != reflect.Map {
		return nil, false
	}
	return et, true
}

// nameMatch reports whether name is a valid completion for a typed prefix.
func nameMatch(name, prefix string) bool {
	if prefix == "" {
		return true
	}
	if strings.HasPrefix(name, prefix) {
		return true
	}
	return strings.HasPrefix(strings.ToLower(name), strings.ToLower(prefix))
}

func sortCandidates(cs []Candidate) {
	sort.SliceStable(cs, func(i, j int) bool {
		return strings.Join(cs[i].Path, ".") < strings.Join(cs[j].Path, ".")
	})
}

// quoteSingle returns the TOML spelling for a single key part, quoting it if it
// is not a valid bare key.
func quoteSingle(name string) string {
	return Key{name}.maybeQuoted(0)
}

// typeHint returns the Go type hint and the TOML kind label for t.
func typeHint(t reflect.Type) (string, string) {
	t = deref(t)
	if t == nil {
		return "", ""
	}

	// Custom decoders are detected via type checks only; none of their methods
	// are ever called.
	if t.Implements(unmarshalTomlType) || reflect.PointerTo(t).Implements(unmarshalTomlType) {
		return t.String(), "Custom"
	}

	if t == timeTimeType {
		return "time.Time", "Datetime"
	}

	switch t.Kind() {
	case reflect.String:
		if t.Implements(textUnmarshalerType) || reflect.PointerTo(t).Implements(textUnmarshalerType) {
			return t.String(), "String"
		}
		return t.String(), "String"
	case reflect.Bool:
		return t.String(), "Bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Uintptr:
		if t.PkgPath() == "time" && t.Name() == "Duration" {
			return t.String(), "Integer"
		}
		return t.String(), "Integer"
	case reflect.Float32, reflect.Float64:
		return t.String(), "Float"
	case reflect.Slice, reflect.Array:
		if et, ok := tableSliceElem(t); ok {
			_ = et
			return t.String(), "ArrayTable"
		}
		return t.String(), "Array"
	case reflect.Struct:
		return t.String(), "Table"
	case reflect.Map:
		return t.String(), "Table"
	case reflect.Interface:
		return t.String(), "Any"
	default:
		return t.String(), "Any"
	}
}

// decodeEscape decodes a TOML basic-string escape sequence beginning with
// '\\'. It returns the decoded rune and the byte length of the sequence. It
// only needs to be correct for well-formed typed prefixes; unknown escapes
// return a replacement rune.
func decodeEscape(s string) (rune, int, bool) {
	if len(s) < 2 || s[0] != '\\' {
		return utf8.RuneError, 0, false
	}
	switch s[1] {
	case 'b':
		return '\b', 2, true
	case 't':
		return '\t', 2, true
	case 'n':
		return '\n', 2, true
	case 'f':
		return '\f', 2, true
	case 'r':
		return '\r', 2, true
	case '"':
		return '"', 2, true
	case '\\':
		return '\\', 2, true
	case 'u':
		if len(s) < 6 {
			return utf8.RuneError, len(s), false
		}
		r := parseHex4(s[2:])
		if r < 0 {
			return utf8.RuneError, 6, false
		}
		return rune(r), 6, true
	case 'U':
		if len(s) < 10 {
			return utf8.RuneError, len(s), false
		}
		r := parseHex8(s[2:])
		if r < 0 {
			return utf8.RuneError, 10, false
		}
		return rune(r), 10, true
	}
	return utf8.RuneError, 2, false
}

func parseHex4(s string) rune {
	var r rune
	for i := 0; i < 4; i++ {
		c, ok := hexVal(s[i])
		if !ok {
			return -1
		}
		r = r<<4 | c
	}
	return r
}

func parseHex8(s string) rune {
	var r rune
	for i := 0; i < 8; i++ {
		c, ok := hexVal(s[i])
		if !ok {
			return -1
		}
		r = r<<4 | c
	}
	return r
}

func hexVal(c byte) (rune, bool) {
	switch {
	case c >= '0' && c <= '9':
		return rune(c - '0'), true
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10, true
	}
	return 0, false
}
