package workflow

import (
	"reflect"
	"testing"
)

func TestRefsFindsEveryReference(t *testing.T) {
	refs, err := Refs("a {{task}} b {{correct.plan}} c {{ params.x }}")
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	want := []Ref{{Root: "task"}, {Root: "correct", Attr: "plan"}, {Root: "params", Attr: "x"}}
	if !reflect.DeepEqual(refs, want) {
		t.Fatalf("refs = %+v, want %+v", refs, want)
	}
}

func TestRefsRejectsAnUnterminatedReference(t *testing.T) {
	if _, err := Refs("a {{task"); err == nil {
		t.Fatal("unterminated reference parsed without error")
	}
	if _, err := Refs("a {{ }}"); err == nil {
		t.Fatal("empty reference parsed without error")
	}
}

func TestIsSingleRef(t *testing.T) {
	for _, template := range []string{"{{plans.current}}", "  {{ correct.plan }}  "} {
		if !IsSingleRef(template) {
			t.Fatalf("IsSingleRef(%q) = false, want true", template)
		}
	}
	for _, template := range []string{"", "x {{a}}", "{{a}}{{b}}", "{{a}} x", "{{a"} {
		if IsSingleRef(template) {
			t.Fatalf("IsSingleRef(%q) = true, want false", template)
		}
	}
}
