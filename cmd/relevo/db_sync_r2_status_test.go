package main

// The status surface's R2 half. The rule is about what a document may carry, not
// about how the line reads: the key id and the secret have no place in either
// shape, because status is printed on request, pasted into issues and read
// aloud, and a credential has no reason in any of those.

import (
	"encoding/json"
	"strings"
	"testing"

	relevosync "github.com/fuad-daoud/relevo/internal/sync"
)

// The two values that must never reach a status document, kept distinct so a
// test can search for each on its own.
const (
	statusR2KeyID  = "AKIAFIXTUREKEYID000001"
	statusR2Secret = "FIXTURE-R2-SECRET-do-not-print-me-0123456789"
)

// statusR2Doc is a document for a machine that holds a complete bucket
// configuration, with the credential-shaped fields set to values the test can
// search for.
func statusR2Doc() dbSyncStatusDoc {
	return dbSyncStatusDoc{
		Enabled:      true,
		RemoteURL:    syncStatusRemote,
		TokenPresent: true,
		R2Configured: true,
		R2Endpoint:   "https://acct.r2.cloudflarestorage.com",
		R2Bucket:     "relevo-sync",
	}
}

// TestStatusNeverShowsR2Secret pins the whole surface in both shapes: the JSON
// document and the one-line rendering. The endpoint and the bucket are named,
// because they are what a reader needs to tell one machine's bucket from
// another's; the key id and the secret are not, because nothing on this surface
// needs them.
//
// The document is built from a struct that has no field for either, so the JSON
// check is over a marshalled document that a future field could still appear in:
// that is what searching the encoded bytes catches.
func TestStatusNeverShowsR2Secret(t *testing.T) {
	t.Parallel()
	doc := statusR2Doc()

	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal the status document: %v", err)
	}
	for _, secret := range []string{statusR2KeyID, statusR2Secret} {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("the status document carries a credential: %s", encoded)
		}
	}

	line := dbSyncStatusLine(doc)
	for _, secret := range []string{statusR2KeyID, statusR2Secret} {
		if strings.Contains(line, secret) {
			t.Errorf("the status line carries a credential: %s", line)
		}
	}
	// The two that may be shown must actually be: a status that named neither
	// would satisfy the check above while being useless.
	for _, want := range []string{"https://acct.r2.cloudflarestorage.com", "relevo-sync"} {
		if !strings.Contains(line, want) {
			t.Errorf("the status line %q is missing %q", line, want)
		}
	}
}

// TestStatusOmitsR2WhenItIsNotConfigured pins that a machine with no bucket
// renders no R2 clause at all. Bodies in a bucket are opt-in, so a machine that
// has not turned them on is in the ordinary state -- the same state as one whose
// status line names no backlog or no last exchange.
func TestStatusOmitsR2WhenItIsNotConfigured(t *testing.T) {
	t.Parallel()
	line := dbSyncStatusLine(dbSyncStatusDoc{Enabled: true, RemoteURL: syncStatusRemote})
	if strings.Contains(line, "r2") {
		t.Errorf("status line %q carries an R2 clause on an unconfigured machine", line)
	}
	// A half-configured machine omits it too: three of the four is not somewhere
	// bodies can go, and the status says what is true.
	half := statusR2Doc()
	half.R2Configured = false
	if line := dbSyncStatusLine(half); strings.Contains(line, "r2") {
		t.Errorf("a half-configured machine rendered %q", line)
	}
}

// TestStatusR2LineNeverEchoesASecret pins the renderer on its own, over a
// document whose endpoint and bucket fields were filled from values that look
// like credentials. The renderer copies only those two, so a field added to the
// document later cannot leak by default.
func TestStatusR2LineNeverEchoesASecret(t *testing.T) {
	t.Parallel()
	doc := dbSyncStatusDoc{R2Configured: true, R2Endpoint: "e", R2Bucket: "b"}
	got := dbSyncR2Line(doc)
	if got != "e/b" {
		t.Errorf("dbSyncR2Line = %q, want %q", got, "e/b")
	}
	if !strings.Contains(relevosync.SecretR2Secret, "r2.secret") {
		t.Fatal("the secret's name changed; this test's premise no longer holds")
	}
}
