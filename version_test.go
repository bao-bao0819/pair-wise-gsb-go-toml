package toml_test

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/BurntSushi/toml/internal/tag"
	tomltest "github.com/BurntSushi/toml/internal/toml-test"
)

func decodeAs(v toml.Version, doc string, into any) (toml.MetaData, error) {
	dec := toml.NewDecoder(strings.NewReader(doc))
	dec.Version(v)
	return dec.Decode(into)
}

func TestCheckVersion(t *testing.T) {
	tests := []struct {
		doc      string
		version  toml.Version
		features []string
	}{
		// Plain TOML 1.0 documents.
		{`a = 1`, toml.Version10, nil},
		{`s = "hello"`, toml.Version10, nil},
		{"s = \"escapes: \\b \\t \\n \\f \\r \\\" \\\\ \\u00e9\"", toml.Version10, nil},
		{`t = 07:32:00`, toml.Version10, nil},
		{`d = 1987-07-05T17:45:00Z`, toml.Version10, nil},
		{`d = 1987-07-05 17:45:00`, toml.Version10, nil},
		{`t = {a = 1, b = 2}`, toml.Version10, nil},

		// New escapes.
		{`s = "\e"`, toml.Version11, []string{"escape-e"}},
		{"s = \"\"\"\n\\e\n\"\"\"", toml.Version11, []string{"escape-e"}},
		{`s = "\x41"`, toml.Version11, []string{"escape-x"}},
		{`s = "\e\x41"`, toml.Version11, []string{"escape-e", "escape-x"}},

		// ... but not in literal strings, comments, or escaped backslashes.
		{`s = '\e \x41'`, toml.Version10, nil},
		{"s = '''\n\\e\n'''", toml.Version10, nil},
		{`s = "\\e \\x41"`, toml.Version10, nil},
		{`# \e \x41 07:32 {a = 1,}`, toml.Version10, nil},
		{`s = "# not a comment: \e"`, toml.Version11, []string{"escape-e"}},

		// Optional seconds in times.
		{`t = 07:32`, toml.Version11, []string{"time-optional-seconds"}},
		{`d = 1987-07-05T17:45Z`, toml.Version11, []string{"time-optional-seconds"}},
		{`d = 1987-07-05T17:45+07:00`, toml.Version11, []string{"time-optional-seconds"}},
		{`d = 1987-07-05 17:45`, toml.Version11, []string{"time-optional-seconds"}},

		// Inline tables: newlines, comments, trailing commas.
		{"t = {\n}", toml.Version11, []string{"inline-table-newline"}},
		{"t = {a = 1,\n b = 2}", toml.Version11, []string{"inline-table-newline"}},
		{"t = {a = 1 # c\n}", toml.Version11, []string{"inline-table-comment", "inline-table-newline"}},
		{`t = {a = 1,}`, toml.Version11, []string{"inline-table-trailing-comma"}},
		{`t = {a = 1, }`, toml.Version11, []string{"inline-table-trailing-comma"}},
		{"t = {a = 1,\n}", toml.Version11, []string{"inline-table-trailing-comma", "inline-table-newline"}},
		{"t = {a = 1, # c\n}", toml.Version11,
			[]string{"inline-table-trailing-comma", "inline-table-comment", "inline-table-newline"}},

		// ... but newlines inside values (arrays, multiline strings) were
		// always allowed.
		{"t = {a = [\n  1,\n  2,\n]}", toml.Version10, nil},
		{"t = {a = \"\"\"\nx\n\"\"\"}", toml.Version10, nil},
		{"t = [{a = 1}, {b = 2}]", toml.Version10, nil},

		// Multiple features are reported in source order.
		{"s = \"\\e\"\nt = 07:32\ni = {a = 1,}\n", toml.Version11,
			[]string{"escape-e", "time-optional-seconds", "inline-table-trailing-comma"}},

		// CRLF line endings count as a single newline.
		{"t = {a = 1,\r\n}", toml.Version11, []string{"inline-table-trailing-comma", "inline-table-newline"}},

		// Nested inline tables and inline tables in arrays.
		{"t = {a = {b = 1,},}", toml.Version11,
			[]string{"inline-table-trailing-comma", "inline-table-trailing-comma"}},
		{"t = [{a = 1,\n}]", toml.Version11,
			[]string{"inline-table-trailing-comma", "inline-table-newline"}},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			version, features, err := toml.CheckVersion(tt.doc)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if version != tt.version {
				t.Errorf("version: have %s, want %s", version, tt.version)
			}
			var names []string
			for _, f := range features {
				names = append(names, f.Name)
				if f.Since != toml.Version11 {
					t.Errorf("feature %q: Since = %s, want 1.1", f.Name, f.Since)
				}
			}
			if !reflect.DeepEqual(names, tt.features) {
				t.Errorf("features: have %v, want %v", names, tt.features)
			}
		})
	}
}

func TestCheckVersionPosition(t *testing.T) {
	tests := []struct {
		doc  string
		want []toml.Position
	}{
		{`s = "\e"`, []toml.Position{{Line: 1, Col: 6, Start: 5, Len: 2}}},
		{`s = "\x41"`, []toml.Position{{Line: 1, Col: 6, Start: 5, Len: 4}}},
		{`t = 07:32`, []toml.Position{{Line: 1, Col: 5, Start: 4, Len: 5}}},
		{`i = {a = 1,}`, []toml.Position{{Line: 1, Col: 11, Start: 10, Len: 1}}},
		{"i = {\n a = 1\n}", []toml.Position{
			{Line: 1, Col: 6, Start: 5, Len: 1},
			{Line: 2, Col: 7, Start: 12, Len: 1},
		}},
		{"\n\ns = \"\\e\"", []toml.Position{{Line: 3, Col: 6, Start: 7, Len: 2}}},
		{"\xef\xbb\xbfs = \"\\e\"", []toml.Position{{Line: 1, Col: 6, Start: 5, Len: 2}}},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			_, features, err := toml.CheckVersion(tt.doc)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			var have []toml.Position
			for _, f := range features {
				have = append(have, f.Position)
			}
			if !reflect.DeepEqual(have, tt.want) {
				t.Errorf("positions:\nhave: %+v\nwant: %+v", have, tt.want)
			}
		})
	}
}

// Invalid documents can't be fixed by upgrading the TOML version, and must be
// reported as an error rather than a version requirement.
func TestCheckVersionInvalid(t *testing.T) {
	docs := []string{
		`a = `,
		`a = {`,
		`a = [1,`,
		"\"a\" = 1\n\"a\" = 2",
		`s = "\e" oops`,        // 1.1 feature, but invalid everywhere.
		"s = \"\\e\"\ninvalid", // 1.1 feature followed by a syntax error.
		`t = 07:32:`,           // Not a valid time in any version.
		`s = "\x4"`,            // Malformed \x escape.
	}

	for _, doc := range docs {
		t.Run("", func(t *testing.T) {
			version, features, err := toml.CheckVersion(doc)
			if err == nil {
				t.Fatalf("expected an error; got version %s, features %v", version, features)
			}
			if features != nil {
				t.Errorf("expected no features on error; got %v", features)
			}
		})
	}
}

func TestDecodeVersion10(t *testing.T) {
	tests := []struct {
		doc     string
		wantErr string // Empty: valid in TOML 1.0.
	}{
		// 1.1 constructs: explainable version errors.
		{`s = "\e"`, `the '\e' escape sequence requires TOML 1.1, but this document is being parsed as TOML 1.0`},
		{`s = "\x41"`, `'\xHH' hexadecimal escape sequence requires TOML 1.1`},
		{`t = 07:32`, `a time without seconds requires TOML 1.1`},
		{`d = 1987-07-05T17:45Z`, `a time without seconds requires TOML 1.1`},
		{"t = {\n}", `a newline inside an inline table requires TOML 1.1`},
		{"t = {a = 1 # c\n}", `a comment inside an inline table requires TOML 1.1`},
		{`t = {a = 1,}`, `a trailing comma in an inline table requires TOML 1.1`},
		{"t = {a = 1,\n}", `requires TOML 1.1`},

		// Plain invalid syntax stays a syntax error, also in 1.0 mode.
		{`a = @`, `expected value but found '@'`},
		{`s = "\q"`, `invalid escape in string`},
		{`t = 07:32:`, `invalid datetime`},
		{`a = {b = 1`, `inline table terminator`},

		// Valid in both versions.
		{`s = "\n"`, ``},
		{`t = 07:32:00`, ``},
		{`t = {a = 1}`, ``},
		{"t = {a = [\n1\n]}", ``},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			var v any
			_, err := decodeAs(toml.Version10, tt.doc, &v)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %s", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q; got none", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error:\nhave: %s\nwant substring: %s", err, tt.wantErr)
			}
		})
	}
}

// Documents valid in both TOML 1.0 and 1.1 decode to identical values.
func TestDecodeVersionEqual(t *testing.T) {
	docs := []string{
		`a = 1`,
		`s = "escapes: \b \t \n \f \r \" \\ é \U0001F600"`,
		`s = '\e is not an escape here'`,
		"t = 07:32:00\nd = 1987-07-05T17:45:00Z\nl = 1987-07-05 17:45:00",
		`t = {a = 1, b = {c = 2}, d = [1, 2]}`,
		"t = {a = [\n  1,\n  2,\n]}",
		"arr = [\n  {a = 1},\n  {b = 2},\n]",
		"# comment\n[tbl]\nkey = \"value\" # trailing comment\n",
	}

	for _, doc := range docs {
		t.Run("", func(t *testing.T) {
			var v10, v11 any
			if _, err := decodeAs(toml.Version10, doc, &v10); err != nil {
				t.Fatalf("TOML 1.0: unexpected error: %s", err)
			}
			if _, err := decodeAs(toml.Version11, doc, &v11); err != nil {
				t.Fatalf("TOML 1.1: unexpected error: %s", err)
			}
			if !reflect.DeepEqual(v10, v11) {
				t.Errorf("values differ:\n1.0: %#v\n1.1: %#v", v10, v11)
			}
		})
	}
}

// Equivalent 1.0/1.1 spellings decode to identical values.
func TestDecodeVersion11Values(t *testing.T) {
	tests := []struct {
		doc10, doc11 string
	}{
		{`t = 07:32:00`, `t = 07:32`},
		{`d = 1987-07-05T17:45:00Z`, `d = 1987-07-05T17:45Z`},
		{`d = 1987-07-05 17:45:00`, `d = 1987-07-05 17:45`},
		{"s = \"A\"", `s = "\x41"`},
		{"t = {\n a = 1,\n b = 2,\n}", `t = {a = 1, b = 2}`},
	}

	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			var v10, v11 any
			if _, err := toml.Decode(tt.doc10, &v10); err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if _, err := toml.Decode(tt.doc11, &v11); err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if !reflect.DeepEqual(v10, v11) {
				t.Errorf("values differ:\n%q: %#v\n%q: %#v", tt.doc10, v10, tt.doc11, v11)
			}
		})
	}
}

// Run the toml-test suite as TOML 1.0: 1.1 constructs must be rejected and
// everything else must keep working.
func TestToml10(t *testing.T) {
	r := tomltest.Runner{
		Version:       "1.0.0",
		Files:         tomltest.TestCases(),
		SkipMustError: true,
		Decoder:       testDecoder10{},
		Encoder:       testEncoder{},
		SkipTests:     tomlTestSkips(),
	}

	tests, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range tests.Tests {
		t.Run(test.Path, func(t *testing.T) {
			if test.Failed() {
				t.Fatalf("\nError:\n%s\n\nInput:\n%s\nOutput:\n%s\nWant:\n%s\n",
					test.Failure, test.Input, test.Output, test.Want)
			}
		})
	}

	t.Logf("  valid: passed %d; failed %d", tests.PassedValid, tests.FailedValid)
	t.Logf("invalid: passed %d; failed %d", tests.PassedInvalid, tests.FailedInvalid)
	t.Logf("skipped: %d", tests.Skipped)
}

type testDecoder10 struct{}

func (p testDecoder10) Cmd() []string { return nil }

func (p testDecoder10) Run(ctx context.Context, input string) (pid int, output string, outputIsError bool, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			switch rr := r.(type) {
			case error:
				retErr = rr
			default:
				retErr = fmt.Errorf("%s", rr)
			}
		}
	}()

	var d any
	_, err := decodeAs(toml.Version10, input, &d)
	if err != nil {
		return 0, err.Error(), true, retErr
	}

	j, err := json.MarshalIndent(tag.Add("", d), "", "  ")
	if err != nil {
		return 0, "", false, err
	}
	return 0, string(j), false, retErr
}
