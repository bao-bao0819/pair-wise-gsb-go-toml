package toml_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/BurntSushi/toml/internal/tag"
	tomltest "github.com/BurntSushi/toml/internal/toml-test"
)

// testDecoder10 decodes as TOML 1.0.
type testDecoder10 struct{}

func (p testDecoder10) Cmd() []string { return nil }

func (p testDecoder10) Run(ctx context.Context, input string) (pid int, output string, outputIsError bool, retErr error) {
	var d any
	dec := toml.NewDecoder(strings.NewReader(input))
	dec.Version(toml.Version10)
	if _, err := dec.Decode(&d); err != nil {
		return 0, err.Error(), true, nil
	}

	j, err := json.MarshalIndent(tag.Add("", d), "", "  ")
	if err != nil {
		return 0, "", false, err
	}
	return 0, string(j), false, nil
}

// Run the toml-test suite as TOML 1.0: all TOML 1.0 documents must parse,
// and all documents using TOML 1.1 features must fail.
func TestToml10(t *testing.T) {
	r := tomltest.Runner{
		Version: "1.0.0",
		Files:   tomltest.TestCases(),
		Decoder: testDecoder10{},

		// Same pre-existing parser bugs as the TOML 1.1 run in
		// toml_test.go; not related to the version handling.
		SkipTests: []string{
			"invalid/datetime/offset-overflow-minute",
			"invalid/array/extend-defined-aot",
			"invalid/inline-table/duplicate-key-03",
			"invalid/inline-table/overwrite-02",
			"invalid/inline-table/overwrite-08",
			"invalid/spec-1.0.0/inline-table-2-0",
			"invalid/spec-1.0.0/table-9-1",
			"invalid/table/append-to-array-with-dotted-keys",
			"invalid/table/append-with-dotted-keys-01",
			"invalid/table/append-with-dotted-keys-02",
			"invalid/table/append-with-dotted-keys-03",
			"invalid/table/append-with-dotted-keys-05",
			"invalid/table/append-with-dotted-keys-08",
			"invalid/table/duplicate-key-04",
			"invalid/table/duplicate-key-05",
			"invalid/table/duplicate-key-dotted-table-01",
			"invalid/table/duplicate-key-dotted-table-02",
			"invalid/table/redefine-02",
			"invalid/table/redefine-03",
		},
	}

	tests, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range tests.Tests {
		if test.Failed() {
			t.Errorf("%s:\n%s\n\nInput:\n%s\nOutput:\n%s\n",
				test.Path, test.Failure, test.Input, test.Output)
		}
	}
}
