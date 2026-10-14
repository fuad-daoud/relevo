package db

// The stamp issuer internal/db's own tests run against.
//
// The real issuer is the hybrid clock in internal/synclog, and that package
// imports this one, so it is installed from the side that can see both: the
// consent package, on start-up. A test of this package never loads that one, so
// a consent write here would otherwise refuse for want of an issuer and every
// existing test of the write paths would have to say so.
//
// This stands in for it: a counter, so a test that writes the same key twice
// gets two different stamps and the ordering rules can be exercised, and a
// refusal of its own so a test can tell a missing issuer from a present one. It
// is deliberately not the real clock -- what the real clock does is the
// consent package's test to pin, and a stand-in that reimplemented it would be
// a second thing to keep right.

import (
	"fmt"
	"sync/atomic"
)

// testStampSeq is the counter behind testStampIssuer.
var testStampSeq atomic.Uint64

// testStampIssuer issues the next counter value as a decimal stamp.
func testStampIssuer(tx *Tx) (string, error) {
	return fmt.Sprintf("%d", testStampSeq.Add(1)), nil
}

func init() { SetStampIssuer(testStampIssuer) }
