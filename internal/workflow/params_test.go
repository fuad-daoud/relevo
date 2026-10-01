package workflow

import (
	"strings"
	"testing"
)

func TestRenderParamsLeavesOtherReferences(t *testing.T) {
	def := Default()
	got := RenderParams(def, "run {{params.gate}} scan {{params.scan}} regate {{params.regate}} then {{task}} and {{params.unknown}}")
	want := "run make check scan true regate 1 then {{task}} and {{params.unknown}}"
	if got != want {
		t.Fatalf("RenderParams = %q, want %q", got, want)
	}
}

func TestWithParamsParsesToTheDefaultsKind(t *testing.T) {
	def, err := WithParams(Default(), map[string]string{"builder": "other", "scan": "false", "regate": "5"})
	if err != nil {
		t.Fatalf("WithParams: %v", err)
	}
	if want := (Param{Kind: ParamString, Str: "other"}); def.Params["builder"] != want {
		t.Fatalf("builder = %+v, want %+v", def.Params["builder"], want)
	}
	if want := (Param{Kind: ParamBool, Bool: false}); def.Params["scan"] != want {
		t.Fatalf("scan = %+v, want %+v", def.Params["scan"], want)
	}
	if want := (Param{Kind: ParamInt, Int: 5}); def.Params["regate"] != want {
		t.Fatalf("regate = %+v, want %+v", def.Params["regate"], want)
	}
	if want := (Param{Kind: ParamString, Str: "make check"}); def.Params["gate"] != want {
		t.Fatalf("gate changed to %+v", def.Params["gate"])
	}
}

func TestWithParamsRejectsAnUnknownParamListingTheKnownOnes(t *testing.T) {
	_, err := WithParams(Default(), map[string]string{"bogus": "1"})
	if err == nil {
		t.Fatal("unknown param parsed without error")
	}
	for _, name := range []string{"builder", "gate", "max_corrections", "planner", "regate", "reviewer", "scan", "security"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("err = %v, want it to list %q", err, name)
		}
	}
}

func TestWithParamsRejectsAValueOfTheWrongKind(t *testing.T) {
	if _, err := WithParams(Default(), map[string]string{"regate": "many"}); err == nil {
		t.Fatal("a non-int value for an int param parsed without error")
	}
}

func TestValidName(t *testing.T) {
	for _, name := range []string{"a", "build-fix", "yes-no", "a1-b2-c3"} {
		if err := ValidName(name); err != nil {
			t.Fatalf("ValidName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{"", "Build", "1build", "has space", "-lead", strings.Repeat("a", 33)} {
		if err := ValidName(name); err == nil {
			t.Fatalf("ValidName(%q) = nil, want an error", name)
		}
	}
}
