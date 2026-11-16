package sync

import (
	"errors"
	"time"
)

// ErrMigrateWaiting reports a remote that is not migrated yet: another machine
// must run the migrate verb before this one can sync. It is not a permanent
// refusal -- those latch, and this one is waited out -- because the fix runs on
// another machine and every retry until then is cheap. The sentence is fixed
// text written in this tree, never the remote's, so a remote cannot choose what
// a reader is told.
var ErrMigrateWaiting = errors.New("sync: the remote is not migrated yet; run `relevo db sync migrate` on one machine once every machine runs this release")

// MigrateRetryEvery is the gap between attempts while the remote waits for a
// migration. Each retry is one hello plus one pull of an unchanged remote, a
// few hundred bytes, so ten minutes is 144 retries a day: often enough to pick
// up a migration soon, cheap enough to wait indefinitely.
const MigrateRetryEvery = 10 * time.Minute
