package toml

import "unicode/utf8"

// classifyLine inspects the single active line (full) with the cursor at byte
// column col and fills loc.
func classifyLine(full string, col int, loc *location) error {
	start := skipSpaces(full, 0)
	if start >= len(full) {
		loc.mode = locKey
		loc.scope = loc.frame.semantic
		loc.activeStart = col
		loc.activeEnd = col
		return nil
	}

	switch full[start] {
	case '#':
		loc.scope = loc.frame.semantic
		loc.none("cursor is inside a comment")
		return nil
	case '[':
		return classifyHeader(full, col, start, loc)
	default:
		return classifyKey(full, col, start, loc)
	}
}

func (loc *location) none(reason string) {
	loc.mode = locNone
	loc.reason = reason
}

// classifyKey handles "a.b.c = value" where the cursor is before the '='.
func classifyKey(full string, col, start int, loc *location) error {
	eq := findTopLevel(full, '=')
	if eq >= 0 && col > eq {
		loc.scope = loc.frame.semantic
		loc.none("cursor is inside a value or a comment")
		return nil
	}

	chain, st := parseChain(full, col, start)
	switch st {
	case chainValueOrComment:
		loc.scope = loc.frame.semantic
		loc.none("cursor is inside a comment")
		return nil
	case chainGap:
		loc.scope = loc.frame.semantic
		loc.none("cursor is not on a key")
		return nil
	}

	loc.mode = locKey
	loc.frame = descendLoc(loc, chain.pieces)
	loc.scope = loc.frame.semantic
	loc.pieces = chain.pieces
	loc.activeStart = chain.activeStart
	loc.activeEnd = chain.activeEnd
	loc.prefix = chain.prefix
	loc.quoted = chain.quoted

	loc.emitValue = eq < 0 || eq < chain.activeStart
	return nil
}

// classifyHeader handles "[a.b]" and "[[a.b]]".
func classifyHeader(full string, col, start int, loc *location) error {
	pos := start
	array := false
	if pos+1 < len(full) && full[pos+1] == '[' {
		array = true
		pos++
	}
	nameStart := skipSpaces(full, pos+1)

	if closePos := findHeaderClose(full, nameStart, array); closePos >= 0 && closePos < col {
		loc.scope = loc.frame.semantic
		loc.none("cursor is after the table header")
		return nil
	}

	chain, st := parseChain(full, col, nameStart)
	if st != chainActive {
		loc.scope = loc.frame.semantic
		loc.none("cursor is not on a table header key")
		return nil
	}

	loc.mode = locHeader
	loc.arrayHeader = array
	loc.frame = descendLocFrom(loc.rep.root(), loc, chain.pieces)
	loc.scope = loc.frame.semantic
	loc.pieces = chain.pieces
	loc.activeStart = chain.activeStart
	loc.activeEnd = chain.activeEnd
	loc.prefix = chain.prefix
	loc.quoted = chain.quoted

	if findHeaderClose(full, chain.activeEnd, array) < 0 {
		if array {
			loc.appendCloser = "]]"
		} else {
			loc.appendCloser = "]"
		}
	}
	return nil
}

// descendLoc walks committed dotted pieces from the current body frame.
func descendLoc(loc *location, pieces []string) *frame {
	return descendLocFrom(loc.frame, loc, pieces)
}

func descendLocFrom(f *frame, loc *location, pieces []string) *frame {
	for _, seg := range pieces {
		f = loc.rep.descend(f, seg)
	}
	return f
}

type chainStatus int

const (
	chainActive chainStatus = iota
	chainGap
	chainValueOrComment
)

type chainResult struct {
	pieces      []string
	activeStart int
	activeEnd   int
	prefix      string
	quoted      bool
}

// parseChain scans dotted key segments from start up to the cursor at col.
// The terminator ('=' for keys, ']' for headers) is only reached after col, so
// segments wholly before col are committed and the segment containing col is
// active.
func parseChain(full string, col, start int) (chainResult, chainStatus) {
	var pieces []string
	i := skipSpaces(full, start)

	for i < col {
		r, _ := utf8.DecodeRuneInString(full[i:])
		switch {
		case r == '#':
			return chainResult{}, chainValueOrComment
		case r == '=' || r == ']':
			return chainResult{}, chainGap
		case r == '.':
			i = skipSpaces(full, i+1)
			if i >= col {
				// Just after a dot: a fresh empty segment is being started.
				return chainResult{pieces: pieces, activeStart: col, activeEnd: col}, chainActive
			}
			continue
		case isWhitespace(r):
			// Whitespace inside the chain; skip ahead to see what follows.
			j := skipSpaces(full, i)
			if j >= col {
				return chainResult{}, chainGap
			}
			switch full[j] {
			case '.':
				i = skipSpaces(full, j+1)
				if i >= col {
					return chainResult{pieces: pieces, activeStart: col, activeEnd: col}, chainActive
				}
				continue
			case '=', ']', '#':
				return chainResult{}, chainGap
			}
			// Otherwise whitespace before an active segment; fall through.
			i = j
			continue
		}

		if r == '"' || r == '\'' {
			content, open, close, ok := readQuoted(full, i, byte(r))
			if !ok {
				prefix := content
				if open+1+len(prefix) > col {
					prefix = full[open+1 : col]
				}
				if r == '"' {
					prefix = decodeBasicString(prefix)
				}
				return chainResult{
					pieces:      pieces,
					activeStart: open,
					activeEnd:   col,
					prefix:      prefix,
					quoted:      true,
				}, chainActive
			}
			val := content
			if r == '"' {
				val = decodeBasicString(val)
			}
			if close <= col {
				pieces = append(pieces, val)
				i = close
				continue
			}
			return chainResult{}, chainGap
		}

		if isBareKeyChar(r) {
			bareStart := i
			end := i
			for end < len(full) {
				rr, w := utf8.DecodeRuneInString(full[end:])
				if !isBareKeyChar(rr) {
					break
				}
				end += w
			}
			prefix := full[bareStart:min(end, col)]
			return chainResult{
				pieces:      pieces,
				activeStart: bareStart,
				activeEnd:   end,
				prefix:      prefix,
			}, chainActive
		}

		return chainResult{}, chainGap
	}

	// Cursor is before any segment characters (start of line or after skipped
	// leading space): an empty active segment.
	return chainResult{pieces: pieces, activeStart: col, activeEnd: col}, chainActive
}

// readQuoted reads a quoted segment opened at open. close is the offset just
// past the closing quote; ok is false when the quote is not closed on the line.
func readQuoted(full string, open int, quote byte) (content string, qOpen, close int, ok bool) {
	i := open + 1
	if quote == '"' {
		for i < len(full) {
			c := full[i]
			if c == '\\' && i+1 < len(full) {
				i += 2
				continue
			}
			if c == '\n' {
				return full[open+1 : i], open, 0, false
			}
			if c == '"' {
				return full[open+1 : i], open, i + 1, true
			}
			i++
		}
		return full[open+1:], open, 0, false
	}
	for i < len(full) {
		if full[i] == '\n' {
			return full[open+1 : i], open, 0, false
		}
		if full[i] == '\'' {
			return full[open+1 : i], open, i + 1, true
		}
		i++
	}
	return full[open+1:], open, 0, false
}

func skipSpaces(full string, i int) int {
	for i < len(full) && (full[i] == ' ' || full[i] == '\t') {
		i++
	}
	return i
}

// findTopLevel returns the byte index of b outside of quoted strings.
func findTopLevel(full string, b byte) int {
	for i := 0; i < len(full); {
		c := full[i]
		switch c {
		case '#':
			return -1
		case '"':
			i = skipDoubleQuoted(full, i)
			continue
		case '\'':
			i = skipSingleQuoted(full, i)
			continue
		case b:
			return i
		}
		i++
	}
	return -1
}

func findHeaderClose(full string, from int, array bool) int {
	for i := from; i < len(full); {
		switch full[i] {
		case '#':
			return -1
		case '"':
			i = skipDoubleQuoted(full, i)
			continue
		case '\'':
			i = skipSingleQuoted(full, i)
			continue
		case ']':
			if array {
				if i+1 < len(full) && full[i+1] == ']' {
					return i
				}
			} else {
				return i
			}
		}
		i++
	}
	return -1
}

func skipDoubleQuoted(full string, i int) int {
	i++
	for i < len(full) {
		if full[i] == '\\' && i+1 < len(full) {
			i += 2
			continue
		}
		if full[i] == '"' || full[i] == '\n' {
			return i + 1
		}
		i++
	}
	return i
}

func skipSingleQuoted(full string, i int) int {
	i++
	for i < len(full) {
		if full[i] == '\'' || full[i] == '\n' {
			return i + 1
		}
		i++
	}
	return i
}

// decodeBasicString unescapes a basic TOML string. It matches typed prefixes
// and field names, so unsupported escapes are passed through.
func decodeBasicString(s string) string {
	if !containsByte(s, '\\') {
		return s
	}
	var b []byte
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' || i+1 >= len(s) {
			b = append(b, c)
			i++
			continue
		}
		switch s[i+1] {
		case 'b':
			b = append(b, '\b')
		case 't':
			b = append(b, '\t')
		case 'n':
			b = append(b, '\n')
		case 'f':
			b = append(b, '\f')
		case 'r':
			b = append(b, '\r')
		case '"':
			b = append(b, '"')
		case '\\':
			b = append(b, '\\')
		default:
			b = append(b, '\\', s[i+1])
		}
		i += 2
	}
	return string(b)
}

func containsByte(s string, c byte) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return true
		}
	}
	return false
}
