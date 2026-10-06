package sync

import (
	"context"
	"fmt"
)

// OpenRole says what kind of file an open names, which is what decides whether
// the open may create sync membership in it or must find it already there.
//
// The three roles exist because the driver's surface cannot tell the two apart.
// Opening a bare file is what makes it a sync member, and nothing the driver
// returns says so afterwards; so an open that named a file which was not yet a
// member had no way to notice it was about to create one. Against a remote
// holding nothing, the first pull then applied the remote's emptiness over the
// file and left a skeleton where a database had been. Naming the role is how a
// caller states which of the three situations it is in, and refusing the
// unnamed case is what keeps a caller that forgot from landing in the
// destructive one.
type OpenRole string

const (
	// OpenUnset is an open that named no role, and it refuses. It is the zero
	// value on purpose: a caller that forgets to say which situation it is in
	// must be stopped, not defaulted into the one that rewrites a file.
	OpenUnset OpenRole = ""

	// OpenScratch is a throwaway file the caller created for one question. The
	// driver may create membership in it freely, because what it holds is
	// nothing the caller wants to keep. This is the emptiness probe's role, and
	// the reason a probe never names a live file: a scratch open is only
	// harmless because the file behind it is already disposable.
	OpenScratch OpenRole = "scratch"

	// OpenSeed is the live file, opened by the enable that is turning it into a
	// member. It is the only role allowed to create membership, and it is the
	// enable's own open: the seed matrix has already decided what the first call
	// is, so this is the one place where creating membership is the point.
	OpenSeed OpenRole = "seed"

	// OpenMember is the live file, which must already be a member. Every open
	// after the enable -- a push, a pull, a turn-off's final push, the cockpit's
	// connection test -- is this role, and a file without the driver's marker
	// tables refuses rather than being turned into a member.
	OpenMember OpenRole = "member"
)

// checkOpenRole is the refusal every open passes before the driver is reached.
//
// It reads the file's marker tables for the member role and refuses a bare one.
// The read is what makes the refusal safe: it happens before the driver is
// given the path, so a file refused here was never opened by anything that
// writes.
func checkOpenRole(cfg OpenConfig) error {
	switch cfg.Role {
	case OpenScratch:
		// A throwaway is disposable by construction, so there is nothing here
		// to check: the marker question is answered by which file was named.
		return nil
	case OpenSeed:
		// The enable's own open is where membership is created.
		return nil
	case OpenMember:
		return RequireSyncMember(cfg.Path)
	default:
		return fmt.Errorf("sync: open: no role for %s, so the open cannot say whether it may create membership: %w",
			cfg.Path, ErrNotSynced)
	}
}

// OpenConfig is what an enable asks a remote to open. It is this package's own
// shape rather than the driver's own struct, for two reasons: the seed matrix
// decides BootstrapIfEmpty here and a test has to be able to read that decision
// without the driver in reach, and the token belongs in a field this package
// controls, so there is one place that decides the value never reaches a log.
//
// BootstrapIfEmpty is a plain bool because every seed decision is total: no
// case wants the driver's pointer left unset, so its default never decides for
// us, and the field cannot be forgotten because there is nothing to forget.
type OpenConfig struct {
	// Role is what kind of file Path names. An unset role refuses, so a caller
	// must say whether it may create sync membership or must find it.
	Role OpenRole
	// Path is the file to sync.
	Path string
	// RemoteURL is the remote to sync it with.
	RemoteURL string
	// Namespace is the remote's namespace, empty when the remote does not name
	// one.
	Namespace string
	// ClientName is what the remote is told this client is called.
	ClientName string
	// AuthToken is the bearer token. It is carried into the open and into
	// nothing else: no message, no log line and no failure in this package ever
	// formats it.
	AuthToken []byte
	// BootstrapIfEmpty is whether the open may take the remote's initial state.
	// It is false only where the seed matrix already decided the first call is a
	// push, and every such decision pulls explicitly afterwards.
	BootstrapIfEmpty bool
	// PullBytesThreshold is the floor in bytes one of the open's pull requests
	// carries. Zero is the driver's own default: the whole first transfer in a
	// single round trip. The seed path sets it because the first transfer of a
	// seed is the largest this tree ever asks for, and a driver call that
	// carries all of it at once is the shape whose failure cannot be caught from
	// here -- see OpenRemote for what that failure is.
	PullBytesThreshold int
	// PushOperationsThreshold is the number of local operations one of the
	// open's push requests carries. Zero is the driver's own default: the entire
	// change set in one batch, split only on transaction boundaries once the
	// batch has grown this large.
	PushOperationsThreshold int
}

// The bounds the seed path puts on one driver call. They are the driver's own
// knobs, named here so the seed decision is the one place that sets them and a
// later edit cannot leave one of them at the default by accident.
//
// The values are deliberately modest: a remote holding a freshly imported seed
// is tens or hundreds of megabytes, and a call that carries all of it at once is
// the one whose failure aborts the process rather than returning an error (see
// OpenRemote). Neither bound is a claim about where the driver breaks -- that is
// not observable from this side of the C ABI -- so they are a bound on the work
// one call may be asked to do, not a reproduction of a failure.
const (
	// SeedPullBytes is the floor in bytes for one pull request on the seed path.
	SeedPullBytes = 4 << 20
	// SeedPushOperations is how many local operations one push request carries
	// on the seed path.
	SeedPushOperations = 4096
	// ProbePullBytes is the floor in bytes for the emptiness probe's pull. The
	// probe only asks whether the remote holds anything, so one chunk answers
	// it; without a bound the probe pulls the whole remote, and against a
	// hundreds-of-megabytes database that outlasts every timeout on the path.
	ProbePullBytes = 1 << 20
)

// DriverVersion is the sync driver this tree is built against, named so a test
// can pin it and a reader can tell which behaviour a report is about.
//
// It is deliberately the version that is in go.mod, and deliberately not moved
// past it. The driver can abort the whole process from inside its WAL: a frame
// lookup asked for a position outside the live frame range is answered by a
// process-killing assertion rather than an error, so no Go recover can catch it
// and a first pull of a large seed is the call that triggers it. Upstream
// replaced that assertion with a returned error, but on a branch that has not
// shipped in a release, so no published version carries it. Until one does, this
// constant stays as it is and the seed path bounds how much one driver call is
// asked to carry. docs/sync-driver-panic.md holds the evidence and the shape
// that is still unknown.
const DriverVersion = "turso.tech/database/tursogo v0.8.1"

// Opener builds the handle one open config describes. It is a function rather
// than an interface because there is exactly one thing to do with it -- hand it
// to a client -- and a fake closure is the whole of what a test needs.
type Opener func(context.Context, OpenConfig) (SyncClient, error)
