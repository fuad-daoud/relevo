package serve

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestAddSetsUnixUserOnActiveKey pins the enrolment rule: a new key stores the
// declared user, an already-enrolled active key given a user has it updated
// instead of answering ErrAlreadyEnrolled, and a call with no user at all is
// still the already-enrolled refusal.
func TestAddSetsUnixUserOnActiveKey(t *testing.T) {
	kp, err := remote.Generate()
	if err != nil {
		t.Fatal(err)
	}
	c := &Clients{}
	pubLine := remote.MarshalPublic(kp.Public, "alice")

	if _, err := c.Add("alice", pubLine, "alice", time.Now()); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	cl, err := c.Add("alice", pubLine, "alice2", time.Now())
	if err != nil {
		t.Fatalf("re-enrolling an active key with --user must update it, got %v", err)
	}
	if cl.UnixUser != "alice2" {
		t.Errorf("UnixUser = %q, want alice2", cl.UnixUser)
	}
	if _, err := c.Add("alice", pubLine, "", time.Now()); !errors.Is(err, ErrAlreadyEnrolled) {
		t.Errorf("Add without --user = %v, want ErrAlreadyEnrolled", err)
	}
}

// TestCheckUnixUserRefusesUnknownWithUseradd pins the refusal text: an unknown
// login name is refused with the exact command that creates it, and a known one
// resolves to its tenant.
func TestCheckUnixUserRefusesUnknownWithUseradd(t *testing.T) {
	_, err := CheckUnixUser(func(string) (isolate.Tenant, error) {
		return isolate.Tenant{}, errors.New("unknown user")
	}, "relevo-no-such-user-xyz")
	if err == nil {
		t.Fatal("CheckUnixUser = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "useradd --create-home relevo-no-such-user-xyz") {
		t.Errorf("error = %q, want it to name useradd --create-home <user>", err)
	}

	if _, err := CheckUnixUser(func(string) (isolate.Tenant, error) { return isolate.Tenant{}, nil }, ""); err == nil {
		t.Error(`CheckUnixUser("") = nil, want a refusal`)
	}

	tok, err := CheckUnixUser(func(name string) (isolate.Tenant, error) {
		return isolate.Tenant{User: name, UID: 1001, GID: 1002, Home: "/home/" + name}, nil
	}, "alice")
	if err != nil || tok.User != "alice" || tok.UID != 1001 || tok.GID != 1002 {
		t.Errorf("CheckUnixUser(alice) = (%+v, %v), want the resolved tenant", tok, err)
	}
}
