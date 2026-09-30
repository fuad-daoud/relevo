package bugreport

import (
	"errors"
	"strings"
	"testing"
)

// TestCollectOmitsFailingSource pins that one dead source costs one line: the
// failing section becomes an omission and every other section still renders.
func TestCollectOmitsFailingSource(t *testing.T) {
	boom := errors.New("read hooks: boom")
	ok := func(name string) Source {
		return Source{Name: name, Build: func() (Section, error) {
			return Section{Lines: []string{name + " is fine"}}, nil
		}}
	}
	b := Collect("t", "v", fixedNow, []Source{
		ok(SectionStatus),
		{Name: SectionHooks, Build: func() (Section, error) { return Section{}, boom }},
		ok(SectionGates),
	})

	if len(b.Sections) != 2 {
		t.Fatalf("Sections = %d, want the two that built", len(b.Sections))
	}
	if len(b.Omissions) != 1 {
		t.Fatalf("Omissions = %v, want one", b.Omissions)
	}
	if b.Omissions[0].Section != SectionHooks || b.Omissions[0].Reason != boom.Error() {
		t.Errorf("Omissions[0] = %+v, want hooks and the reason", b.Omissions[0])
	}
	md := Markdown(b)
	if !strings.Contains(md, "hooks: omitted: read hooks: boom") {
		t.Errorf("markdown does not carry the omission line:\n%s", md)
	}
	for _, name := range []string{SectionStatus, SectionGates} {
		if !strings.Contains(md, name+" is fine") {
			t.Errorf("section %s is missing from:\n%s", name, md)
		}
	}
}

// TestCollectRecoversPanickingSource pins that a panic is one omission too: a
// source that panics never takes the bundle, or the run, down with it.
func TestCollectRecoversPanickingSource(t *testing.T) {
	b := Collect("t", "v", fixedNow, []Source{
		{Name: SectionStatus, Build: func() (Section, error) {
			return Section{Lines: []string{"status is fine"}}, nil
		}},
		{Name: SectionJournal, Build: func() (Section, error) { panic("journal read: nil store") }},
	})

	if len(b.Sections) != 1 || len(b.Omissions) != 1 {
		t.Fatalf("Sections = %d, Omissions = %v, want one of each", len(b.Sections), b.Omissions)
	}
	want := "panic: journal read: nil store"
	if b.Omissions[0].Reason != want {
		t.Errorf("omission reason = %q, want %q", b.Omissions[0].Reason, want)
	}
	md := Markdown(b)
	if !strings.Contains(md, SectionJournal+": omitted: "+want) {
		t.Errorf("markdown does not carry the omission line:\n%s", md)
	}
	if !strings.Contains(md, "status is fine") {
		t.Errorf("the surviving section is missing from:\n%s", md)
	}
}

// TestCollectNamesSectionsFromTheSource pins that a projection may leave the
// name out: the source's own name is the identity then.
func TestCollectNamesSectionsFromTheSource(t *testing.T) {
	b := Collect("t", "v", fixedNow, []Source{{Name: "custom", Build: func() (Section, error) {
		return Section{Lines: []string{"x"}}, nil
	}}})
	if len(b.Sections) != 1 || b.Sections[0].Name != "custom" {
		t.Fatalf("Sections = %+v, want one named custom", b.Sections)
	}
}
