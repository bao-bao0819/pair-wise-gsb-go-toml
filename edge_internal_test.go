package toml

import "testing"

func TestNumberCanonical(t *testing.T) {
	cases := []struct {
		lex  string
		want string
	}{
		{"1e10", "1e10"},
		{"0E0", "0e0"},
		{"+0.0", "0.0"},
		{"-0.0", "-0.0"},
		{"1.00", "1.00"},
		{"+inf", "inf"},
		{"inf", "inf"},
		{"-inf", "-inf"},
		{"nan", "nan"},
		{"-nan", "-nan"},
		{"42", "42"},
		{"-0", "0"},
		{"1_000", "1000"},
	}
	for _, c := range cases {
		var n Number
		if c.lex == "inf" || c.lex == "+inf" || c.lex == "-inf" || c.lex == "nan" || c.lex == "-nan" {
			_, sp, neg, err := parseFloatLexeme(c.lex)
			if err != nil {
				t.Fatalf("%q: %v", c.lex, err)
			}
			n = newFloatNumber(c.lex, sp, neg)
		} else if containsAny(c.lex, ".eE") {
			_, sp, neg, err := parseFloatLexeme(c.lex)
			if err != nil {
				t.Fatalf("%q: %v", c.lex, err)
			}
			n = newFloatNumber(c.lex, sp, neg)
		} else {
			n = newIntegerNumber(c.lex)
		}
		if got := n.canonical(); got != c.want {
			t.Errorf("canonical(%q) = %q, want %q", c.lex, got, c.want)
		}
	}
}

func containsAny(s string, chars string) bool {
	for _, c := range s {
		for _, m := range chars {
			if c == m {
				return true
			}
		}
	}
	return false
}
