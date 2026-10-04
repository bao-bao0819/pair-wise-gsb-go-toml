package toml

import (
	"strings"
	"testing"
)

func FuzzDecodeStrict(f *testing.F) {
	for _, s := range []string{
		"x = 0.1\n", "x = -0.0\n", "x = 1e400\n", "x = 0x10\n",
		"x = 9007199254740993\n", "x = inf\n", "x = -nan\n",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		var m map[string]any
		_, err := NewDecoder(strings.NewReader(s)).SetStrictNumbers(true).Decode(&m)
		if err == nil {
			// Round-trip whatever we accepted without panicking.
			_, _ = Marshal(m)
		}
	})
}
