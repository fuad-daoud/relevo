package remote

import "testing"

func TestContainsDotDotRelFindsAnyParentElement(t *testing.T) {
	cases := map[string]bool{
		"..":      true,
		"../x":    true,
		"a/../b":  true,
		`a\..\b`:  true,
		"a/b/..":  true,
		"a/b":     false,
		"..a/b..": false,
		"a/.../b": false,
		"":        false,
		"./a":     false,
	}
	for rel, want := range cases {
		if got := ContainsDotDotRel(rel); got != want {
			t.Errorf("ContainsDotDotRel(%q) = %v, want %v", rel, got, want)
		}
	}
}
