package resolve

import (
	"errors"
	"testing"

	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

// uiBuilderRow is the custom writer row this file's tests add: shape writer,
// a claude definition of my-ui, and its own candidate list.
func uiBuilderRow(candidates ...string) roles.Row {
	shape := "writer"
	return roles.Row{
		Shape:       &shape,
		Candidates:  candidates,
		Definitions: map[string]roles.DefRow{"claude": {Agent: "my-ui"}},
	}
}

// TestWriterRoleAndNormRole pins one accessor naming the writer role a binding runs. An
// empty stored role and the literal "builder" both mean builder, and NormRole
// is the inverse for the stored form.
func TestWriterRoleAndNormRole(t *testing.T) {
	t.Parallel()

	if got := (store.Binding{}).WriterRole(); got != "builder" {
		t.Errorf("WriterRole(Role \"\") = %q, want builder", got)
	}
	if got := (store.Binding{Role: "builder"}).WriterRole(); got != "builder" {
		t.Errorf("WriterRole(Role builder) = %q, want builder", got)
	}
	if got := (store.Binding{Role: "ui-builder"}).WriterRole(); got != "ui-builder" {
		t.Errorf("WriterRole(Role ui-builder) = %q, want ui-builder", got)
	}

	if got := NormRole("builder"); got != "" {
		t.Errorf("NormRole(builder) = %q, want empty", got)
	}
	if got := NormRole(""); got != "" {
		t.Errorf("NormRole(\"\") = %q, want empty", got)
	}
	if got := NormRole("ui-builder"); got != "ui-builder" {
		t.Errorf("NormRole(ui-builder) = %q, want ui-builder", got)
	}
}

// TestActorShape pins a writer role (or the builder default) reading as a
// writer, a reader as a reader, and an unknown name refused with
// ErrUnknownRole.
func TestActorShape(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, testCandidatesJSON)
	reg := fileRegistry(t, set, map[string]roles.Row{
		"builder":    {Candidates: []string{testClaudeRef}},
		"ui-builder": uiBuilderRow(testClaudeRef),
	})

	for _, role := range []string{"builder", "", "ui-builder"} {
		shape, err := ActorShape(reg, role)
		if err != nil {
			t.Errorf("ActorShape(%q) = %v, want nil", role, err)
			continue
		}
		if shape != store.ShapeWriter {
			t.Errorf("ActorShape(%q) = %q, want writer", role, shape)
		}
	}

	shape, err := ActorShape(reg, "reviewer")
	if err != nil {
		t.Fatalf("ActorShape(reviewer) err = %v, want nil", err)
	}
	if shape != store.ShapeReader {
		t.Errorf("ActorShape(reviewer) = %q, want reader", shape)
	}

	if _, err := ActorShape(reg, "nope"); !errors.Is(err, ErrUnknownRole) {
		t.Fatalf("ActorShape(nope) err = %v, want ErrUnknownRole", err)
	}
}
