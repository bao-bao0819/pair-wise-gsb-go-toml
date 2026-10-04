package toml

import (
	"fmt"
	"testing"
)

func TestDbg(t *testing.T) {
	for _, s := range []string{"base.ena", "servers.ena"} {
		loc, err := analyze(s, len(s))
		fmt.Printf("%q mode=%d scope=%v pieces=%v prefix=%q frameSem=%v frameID=%v emit=%v err=%v\n",
			s, loc.mode, loc.scope, loc.pieces, loc.prefix,
			safeSem(loc), safeID(loc), loc.emitValue, err)
	}
}

func safeSem(loc *location) []string {
	if loc.frame == nil {
		return nil
	}
	return loc.frame.semantic
}
func safeID(loc *location) []string {
	if loc.frame == nil {
		return nil
	}
	return loc.frame.id
}
