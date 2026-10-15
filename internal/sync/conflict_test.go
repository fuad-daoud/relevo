package sync

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRemoteConflictNamesBothRemotes pins that a --url against a different
// stored remote is refused with both values in the message, and that a machine
// with no stored remote is not a conflict.
func TestRemoteConflictNamesBothRemotes(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0).UTC()
	shared, local := joinPair(t, "m1")
	if err := SetToken(local, []byte(joinToken), now); err != nil {
		t.Fatalf("SetToken: %v", err)
	}
	storeR2(t, local)
	if err := PutSettings(local, Settings{RemoteURL: "libsql://stored.invalid"}, now); err != nil {
		t.Fatalf("PutSettings: %v", err)
	}
	_, err := EnableRequest{Local: local, Shared: shared, URL: "libsql://flag.invalid"}.Preflight()
	if !errors.Is(err, ErrRemoteConflict) {
		t.Fatalf("Preflight = %v, want the remote conflict", err)
	}
	for _, want := range []string{"libsql://flag.invalid", "libsql://stored.invalid"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("conflict %q does not name %s", err, want)
		}
	}
	var conflict *RemoteConflictError
	if !errors.As(err, &conflict) || conflict.URL != "libsql://flag.invalid" {
		t.Errorf("conflict = %v, want the typed error carrying the flag", err)
	}
}
