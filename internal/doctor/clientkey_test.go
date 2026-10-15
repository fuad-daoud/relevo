package doctor

// The client-id row, pinned as a pure function of the one fact it reads. What
// matters about it is that it names the fix in the order the fix has to be run
// and that it never claims a machine is broken when nothing replaced its key.

import (
	"strings"
	"testing"
)

func TestNoClientKeyRowWithoutASwap(t *testing.T) {
	t.Parallel()
	if _, ok := ClientKeyCheck(ClientKeyReplacedInput{}); ok {
		t.Error("a machine that never had its key replaced got a row")
	}
	if _, ok := ClientKeyCheck(ClientKeyReplacedInput{Swapped: true}); ok {
		t.Error("a swap recorded without an identity got a row naming no identity")
	}
}

func TestClientKeyRowNamesBothIdentitiesAndTheFixInOrder(t *testing.T) {
	t.Parallel()
	check, ok := ClientKeyCheck(ClientKeyReplacedInput{Swapped: true, From: "old-id", To: "new-id"})
	if !ok {
		t.Fatal("a replaced client key produced no row")
	}
	if check.Severity != SevWarn {
		t.Errorf("severity = %v, want a warning: a machine that cannot be recognised still runs", check.Severity)
	}
	for _, want := range []string{"new-id", "old-id", "enrol"} {
		if !strings.Contains(check.Detail, want) {
			t.Errorf("the row does not name %q: %s", want, check.Detail)
		}
	}
	enrollLine := strings.Index(check.Fix, "--enroll-line")
	enroll := strings.Index(check.Fix, "serve enroll")
	if enrollLine < 0 || enroll < 0 {
		t.Fatalf("the fix does not carry both halves: %s", check.Fix)
	}
	if enrollLine > enroll {
		t.Errorf("the fix runs the enrolment before it has the line: %s", check.Fix)
	}
}

// A row must not change what the verdict is made of. The doctor counts failures
// and warnings to decide whether this machine can run at all, and a replaced
// client id is something an operator fixes while the machine carries on.
func TestAClientKeyRowDoesNotTouchUsableBuilder(t *testing.T) {
	t.Parallel()
	rep := Report{UsableBuilder: true}
	rep.Checks = append(rep.Checks, Check{
		Name: "binary", Severity: SevOK, Detail: "claude",
	})
	if check, ok := ClientKeyCheck(ClientKeyReplacedInput{Swapped: true, To: "new-id"}); ok {
		rep.Checks = append(rep.Checks, check)
	}
	if !rep.UsableBuilder {
		t.Error("the client id row cleared UsableBuilder")
	}
	if rep.Failures() != 0 {
		t.Errorf("Failures = %d, want 0: a replaced client id is a warning", rep.Failures())
	}
}
