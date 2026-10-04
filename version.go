package toml

import (
	"fmt"
	"sort"
)

// Version is a version of the TOML specification.
type Version int

const (
	// Version10 is TOML 1.0: https://toml.io/en/v1.0.0
	Version10 Version = iota + 1

	// Version11 is TOML 1.1: https://toml.io/en/v1.1.0
	Version11
)

// LatestVersion is the newest version of the TOML specification this library
// supports. Documents are parsed as this version unless a different version
// is requested with [Decoder.Version].
const LatestVersion = Version11

func (v Version) String() string {
	switch v {
	case Version10:
		return "1.0"
	case Version11:
		return "1.1"
	default:
		return fmt.Sprintf("unknown version %d", int(v))
	}
}

// Feature is a TOML construct that requires a specific version of the TOML
// specification, as reported by [CheckVersion].
type Feature struct {
	Name        string   // Short unique name of the construct, e.g. "escape-e".
	Description string   // Human-readable description of the construct.
	Since       Version  // First version of TOML supporting this construct.
	Position    Position // Source range of the construct.
}

func (f Feature) String() string {
	return fmt.Sprintf("%s at line %d, column %d (requires TOML %s)",
		f.Description, f.Position.Line, f.Position.Col, f.Since)
}

// Constructs introduced in TOML 1.1; recorded during lexing and parsing.
var (
	featEscapeE = Feature{
		Name: "escape-e", Since: Version11,
		Description: `the '\e' escape sequence`,
	}
	featEscapeX = Feature{
		Name: "escape-x", Since: Version11,
		Description: `the '\xHH' hexadecimal escape sequence`,
	}
	featInlineTableNewline = Feature{
		Name: "inline-table-newline", Since: Version11,
		Description: "a newline inside an inline table",
	}
	featInlineTableComment = Feature{
		Name: "inline-table-comment", Since: Version11,
		Description: "a comment inside an inline table",
	}
	featInlineTableTrailingComma = Feature{
		Name: "inline-table-trailing-comma", Since: Version11,
		Description: "a trailing comma in an inline table",
	}
	featTimeOptionalSeconds = Feature{
		Name: "time-optional-seconds", Since: Version11,
		Description: "a time without seconds",
	}
)

// CheckVersion parses data and returns the minimum TOML version required to
// parse it, along with every construct that requires a version newer than
// TOML 1.0, in source order.
//
// Documents valid in TOML 1.0 return [Version10] and no features; documents
// using TOML 1.1 constructs return [Version11] and the list of those
// constructs.
//
// An error is returned if data is invalid in every supported TOML version;
// such documents can't be fixed by merely using a newer version.
func CheckVersion(data string) (Version, []Feature, error) {
	data = stripBOM(data)
	p, err := parse(data, 128, LatestVersion)
	if err != nil {
		return 0, nil, err
	}

	features := p.features
	sort.Slice(features, func(i, j int) bool {
		return features[i].Position.Start < features[j].Position.Start
	})

	version := Version10
	for i := range features {
		features[i].Position.Line, features[i].Position.Col = lineCol(data, features[i].Position.Start)
		if features[i].Since > version {
			version = features[i].Since
		}
	}
	return version, features, nil
}

// lineCol computes the 1-based line and column for a byte offset in s.
func lineCol(s string, start int) (line, col int) {
	line, col = 1, 1
	for i := 0; i < start && i < len(s); i++ {
		if s[i] == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}
