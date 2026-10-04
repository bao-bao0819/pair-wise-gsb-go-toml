package toml

import (
	"fmt"
	"sort"
	"strings"
)

// Version is a version of the TOML specification.
//
// The zero value is not a valid version; where a Version is accepted (such as
// [Decoder.Version]) the zero value means "the latest version this library
// supports", which is currently [Version11].
type Version int

const (
	// Version10 is TOML v1.0.0: https://toml.io/en/v1.0.0
	Version10 Version = iota + 1
	// Version11 is TOML v1.1.0: https://toml.io/en/v1.1.0
	Version11

	// VersionLatest is the latest TOML version this library supports.
	VersionLatest = Version11
)

func (v Version) String() string {
	switch v {
	case Version10:
		return "1.0"
	case Version11:
		return "1.1"
	}
	return "unknown"
}

// FeatureKind is a TOML syntax feature that requires a version newer than
// TOML 1.0.
type FeatureKind int

const (
	// FeatureEscapeE is the '\e' (escape, U+001B) escape sequence in basic
	// strings.
	FeatureEscapeE FeatureKind = iota + 1
	// FeatureEscapeX is the '\xHH' escape sequence in basic strings.
	FeatureEscapeX
	// FeatureDateTimeNoSeconds is a time or datetime with the seconds
	// omitted, such as "07:32" or "1979-05-27T07:32".
	FeatureDateTimeNoSeconds
	// FeatureInlineTableNewline is a newline (or comment) inside an inline
	// table.
	FeatureInlineTableNewline
	// FeatureInlineTableTrailingComma is a trailing comma after the last
	// key/value pair in an inline table.
	FeatureInlineTableTrailingComma
)

func (k FeatureKind) String() string {
	switch k {
	case FeatureEscapeE:
		return `escape sequence '\e'`
	case FeatureEscapeX:
		return `escape sequence '\xHH'`
	case FeatureDateTimeNoSeconds:
		return "time or datetime without seconds"
	case FeatureInlineTableNewline:
		return "newline in inline table"
	case FeatureInlineTableTrailingComma:
		return "trailing comma in inline table"
	}
	return "unknown feature"
}

// Since returns the first TOML version that supports this feature.
func (k FeatureKind) Since() Version { return Version11 }

// A Feature is a single use of a TOML syntax feature that requires a version
// newer than TOML 1.0, found in a document by [CheckVersion].
type Feature struct {
	Kind     FeatureKind // The feature that was used.
	Position Position    // Where in the document the feature was used.
}

func (f Feature) String() string {
	return fmt.Sprintf("%s at line %d, column %d", f.Kind, f.Position.Line, f.Position.Col)
}

// CheckVersion parses a TOML document and reports the minimum TOML version
// required to parse it, together with every use of a TOML 1.1 feature (the
// "upgrade points"), in order of appearance.
//
// If the returned features are empty the document is valid TOML 1.0. If the
// document is not valid TOML at all an error is returned instead: an invalid
// document is never reported as "only needing a newer version".
//
// Features are detected by the parser itself, so feature-like text inside
// comments, literal strings, or escaped characters is never reported.
func CheckVersion(data string) (Version, []Feature, error) {
	// Strip the BOM exactly like parse() does, so that feature positions line
	// up with the data the parser saw.
	if strings.HasPrefix(data, "\xff\xfe") || strings.HasPrefix(data, "\xfe\xff") {
		data = data[2:]
	} else if strings.HasPrefix(data, "\xef\xbb\xbf") {
		data = data[3:]
	}

	p, err := parse(data, 128, VersionLatest)
	if err != nil {
		return 0, nil, err
	}

	features := p.lx.features
	if len(features) == 0 {
		return Version10, nil, nil
	}

	sort.SliceStable(features, func(i, j int) bool {
		return features[i].Position.Start < features[j].Position.Start
	})
	for i := range features {
		features[i].Position = features[i].Position.withCol(data)
	}
	return Version11, features, nil
}
