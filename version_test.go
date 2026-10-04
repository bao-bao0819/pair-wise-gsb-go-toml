package toml

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Decode data as TOML 1.0; expects a version error mentioning the feature.
func decode10(t *testing.T, data string) error {
	t.Helper()
	var v any
	dec := NewDecoder(strings.NewReader(data))
	dec.Version(Version10)
	_, err := dec.Decode(&v)
	return err
}

func TestVersionEscape(t *testing.T) {
	t.Run("escape-e", func(t *testing.T) {
		var v struct{ S string }
		if _, err := Decode(`s = "a\eb"`, &v); err != nil {
			t.Fatal(err)
		}
		if v.S != "a\x1bb" {
			t.Fatalf("have %q; want %q", v.S, "a\x1bb")
		}

		err := decode10(t, `s = "a\eb"`)
		if err == nil || !strings.Contains(err.Error(), `escape sequence '\e' requires TOML 1.1`) {
			t.Fatalf("expected TOML 1.1 version error; got: %v", err)
		}
		var pErr ParseError
		if !errors.As(err, &pErr) || !strings.Contains(pErr.ErrorWithUsage(), "TOML 1.1") {
			t.Fatalf("expected usage guidance in error; got:\n%s", pErr.ErrorWithUsage())
		}
	})

	t.Run("escape-x", func(t *testing.T) {
		var v struct{ S string }
		if _, err := Decode(`s = "Jos\xE9"`, &v); err != nil {
			t.Fatal(err)
		}
		if v.S != "José" {
			t.Fatalf("have %q; want %q", v.S, "José")
		}

		err := decode10(t, `s = "Jos\xE9"`)
		if err == nil || !strings.Contains(err.Error(), `escape sequence '\xHH' requires TOML 1.1`) {
			t.Fatalf("expected TOML 1.1 version error; got: %v", err)
		}
	})

	// Feature-like text in literal strings, comments, or after an escaped
	// backslash must not be reported as a TOML 1.1 feature.
	t.Run("no false positives", func(t *testing.T) {
		for _, doc := range []string{
			`s = '\e \x41'`,              // literal string
			"s = '''\n" + `\e` + "\n'''", // multiline literal string
			`# \e \x41 in comment`,       // comment
			`s = "\\e \\x41"`,            // escaped backslash
			`s = "\u001B"`,               // \u escape: valid TOML 1.0
		} {
			v, features, err := CheckVersion(doc)
			if err != nil {
				t.Fatalf("%s: %s", doc, err)
			}
			if v != Version10 || len(features) != 0 {
				t.Fatalf("%s: have version %s, features %v; want TOML 1.0 with no features",
					doc, v, features)
			}
		}
	})

	// An invalid escape is a syntax error, not a "version upgrade".
	t.Run("invalid escape", func(t *testing.T) {
		if _, _, err := CheckVersion(`s = "\xZZ"`); err == nil {
			t.Fatal(`expected error for "\xZZ"`)
		}
		if _, _, err := CheckVersion(`s = "\q"`); err == nil {
			t.Fatal(`expected error for "\q"`)
		}
	})
}

func TestVersionDateTime(t *testing.T) {
	valid11 := []struct{ doc, format, want string }{
		{`t = 07:32`, "15:04", "07:32"},
		{`t = 1979-05-27T07:32`, "2006-01-02T15:04", "1979-05-27T07:32"},
		{`t = 1979-05-27 07:32Z`, "2006-01-02T15:04Z07:00", "1979-05-27T07:32Z"},
	}
	for _, tt := range valid11 {
		t.Run(tt.doc, func(t *testing.T) {
			var v struct{ T time.Time }
			if _, err := Decode(tt.doc, &v); err != nil {
				t.Fatal(err)
			}
			if got := v.T.Format(tt.format); got != tt.want {
				t.Fatalf("have %q; want %q", got, tt.want)
			}

			ver, features, err := CheckVersion(tt.doc)
			if err != nil {
				t.Fatal(err)
			}
			if ver != Version11 || len(features) != 1 || features[0].Kind != FeatureDateTimeNoSeconds {
				t.Fatalf("have version %s, features %v; want TOML 1.1 with %s",
					ver, features, FeatureDateTimeNoSeconds)
			}

			err = decode10(t, tt.doc)
			if err == nil || !strings.Contains(err.Error(), "without seconds requires TOML 1.1") {
				t.Fatalf("expected TOML 1.1 version error; got: %v", err)
			}
		})
	}

	t.Run("with seconds is TOML 1.0", func(t *testing.T) {
		for _, doc := range []string{`t = 07:32:00`, `t = 1979-05-27T07:32:00Z`} {
			ver, features, err := CheckVersion(doc)
			if err != nil {
				t.Fatal(err)
			}
			if ver != Version10 || len(features) != 0 {
				t.Fatalf("%s: have version %s, features %v; want TOML 1.0", doc, ver, features)
			}
			if err := decode10(t, doc); err != nil {
				t.Fatalf("%s: %s", doc, err)
			}
		}
	})

	// Invalid times stay invalid in both versions and must not be reported
	// as "just needs a newer version".
	t.Run("invalid", func(t *testing.T) {
		for _, doc := range []string{`t = 25:32`, `t = 07:32:`, `t = 1979-05-27T25:32`} {
			if _, _, err := CheckVersion(doc); err == nil {
				t.Fatalf("%s: expected error", doc)
			}
			if err := decode10(t, doc); err == nil {
				t.Fatalf("%s: expected error", doc)
			}
		}
	})
}

func TestVersionInlineTable(t *testing.T) {
	t.Run("newline", func(t *testing.T) {
		doc := "t = {a = 1,\n b = 2}"
		var v struct {
			T struct{ A, B int }
		}
		if _, err := Decode(doc, &v); err != nil {
			t.Fatal(err)
		}
		if v.T.A != 1 || v.T.B != 2 {
			t.Fatalf("have %+v", v.T)
		}

		err := decode10(t, doc)
		if err == nil || !strings.Contains(err.Error(), "newline in inline table requires TOML 1.1") {
			t.Fatalf("expected TOML 1.1 version error; got: %v", err)
		}
	})

	t.Run("trailing comma", func(t *testing.T) {
		doc := `t = {a = 1,}`
		var v struct {
			T struct{ A int }
		}
		if _, err := Decode(doc, &v); err != nil {
			t.Fatal(err)
		}
		if v.T.A != 1 {
			t.Fatalf("have %+v", v.T)
		}

		ver, features, err := CheckVersion(doc)
		if err != nil {
			t.Fatal(err)
		}
		if ver != Version11 || len(features) != 1 || features[0].Kind != FeatureInlineTableTrailingComma {
			t.Fatalf("have version %s, features %v; want TOML 1.1 with %s",
				ver, features, FeatureInlineTableTrailingComma)
		}

		err = decode10(t, doc)
		if err == nil || !strings.Contains(err.Error(), "trailing comma in inline table requires TOML 1.1") {
			t.Fatalf("expected TOML 1.1 version error; got: %v", err)
		}
	})

	t.Run("trailing comma after newline", func(t *testing.T) {
		_, features, err := CheckVersion("t = {a = 1,\n}")
		if err != nil {
			t.Fatal(err)
		}
		if len(features) != 2 ||
			features[0].Kind != FeatureInlineTableTrailingComma ||
			features[1].Kind != FeatureInlineTableNewline {
			t.Fatalf("have %v; want trailing comma + newline features", features)
		}
	})

	t.Run("single line is TOML 1.0", func(t *testing.T) {
		doc := `t = {a = 1, b = 2}`
		ver, features, err := CheckVersion(doc)
		if err != nil {
			t.Fatal(err)
		}
		if ver != Version10 || len(features) != 0 {
			t.Fatalf("have version %s, features %v; want TOML 1.0", ver, features)
		}
		if err := decode10(t, doc); err != nil {
			t.Fatal(err)
		}
	})

	// Invalid inline tables are syntax errors in both versions.
	t.Run("invalid", func(t *testing.T) {
		for _, doc := range []string{`t = {a = 1`, `t = {a = 1,, b = 2}`, `t = {,}`} {
			if _, _, err := CheckVersion(doc); err == nil {
				t.Fatalf("%s: expected error", doc)
			}
			if err := decode10(t, doc); err == nil {
				t.Fatalf("%s: expected error", doc)
			}
		}
	})
}

func TestCheckVersion(t *testing.T) {
	t.Run("lists all features in order", func(t *testing.T) {
		doc := "# \\e in a comment is not a feature\n" +
			"s = \"a\\eb\"\n" +
			"t = 07:32\n" +
			"tbl = {a = 1,\n b = 2}\n"
		ver, features, err := CheckVersion(doc)
		if err != nil {
			t.Fatal(err)
		}
		if ver != Version11 {
			t.Fatalf("have version %s; want %s", ver, Version11)
		}
		want := []FeatureKind{
			FeatureEscapeE,
			FeatureDateTimeNoSeconds,
			FeatureInlineTableNewline,
		}
		if len(features) != len(want) {
			t.Fatalf("have %d features %v; want %d", len(features), features, len(want))
		}
		for i, k := range want {
			if features[i].Kind != k {
				t.Errorf("feature %d: have %s; want %s", i, features[i].Kind, k)
			}
			if features[i].Position.Line != i+2 {
				t.Errorf("feature %d: have line %d; want %d", i, features[i].Position.Line, i+2)
			}
		}
		if features[0].Position.Col != 7 { // The '\' in `s = "a\eb"`.
			t.Errorf("escape feature: have column %d; want 7", features[0].Position.Col)
		}
	})

	t.Run("invalid document is an error", func(t *testing.T) {
		for _, doc := range []string{
			"key = ",
			"[table",
			"k = 1\nk = 2",
			// Uses a 1.1 feature *and* has a syntax error: still an error.
			"t = 07:32\nkey = ",
		} {
			ver, features, err := CheckVersion(doc)
			if err == nil {
				t.Fatalf("%q: expected error; got version %s, features %v", doc, ver, features)
			}
		}
	})

	t.Run("empty document", func(t *testing.T) {
		ver, features, err := CheckVersion("")
		if err != nil {
			t.Fatal(err)
		}
		if ver != Version10 || len(features) != 0 {
			t.Fatalf("have version %s, features %v; want TOML 1.0", ver, features)
		}
	})
}

// Content valid in both versions must decode to the same values as before.
func TestVersionDecodeIdentical(t *testing.T) {
	doc := `
str   = "value"
int   = 42
float = 1.5
bool  = true
time  = 07:32:00
date  = 1979-05-27
tbl   = {a = 1, b = 2}
arr   = [1, 2, 3]
`
	var def, v10 map[string]any
	if _, err := Decode(doc, &def); err != nil {
		t.Fatal(err)
	}
	dec10 := NewDecoder(strings.NewReader(doc))
	dec10.Version(Version10)
	if _, err := dec10.Decode(&v10); err != nil {
		t.Fatal(err)
	}
	if !mapsEqual(def, v10) {
		t.Fatalf("default: %v\nTOML 1.0: %v", def, v10)
	}

	// The zero Version means "latest supported", like the default.
	var zero map[string]any
	dec0 := NewDecoder(strings.NewReader(doc))
	dec0.Version(0)
	if _, err := dec0.Decode(&zero); err != nil {
		t.Fatal(err)
	}
	if !mapsEqual(def, zero) {
		t.Fatalf("default: %v\nzero version: %v", def, zero)
	}
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		switch va := va.(type) {
		case map[string]any:
			vb, ok := vb.(map[string]any)
			if !ok || !mapsEqual(va, vb) {
				return false
			}
		case []any:
			vb, ok := vb.([]any)
			if !ok || len(va) != len(vb) {
				return false
			}
			for i := range va {
				if va[i] != vb[i] {
					return false
				}
			}
		default:
			if va != vb {
				return false
			}
		}
	}
	return true
}
