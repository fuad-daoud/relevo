// Package relevo implements the handoff policy: which text moves between a
// mastermind and a builder, when, and when to stop. It holds no intelligence --
// every judgement stays with the mastermind agent.
package relevo

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/classify"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/ingest"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Git is the slice of the git CLI relevo needs. *git.Client satisfies it.
type Git interface {
	SnapshotTree(ctx context.Context, dir string) (string, error)
	// MaterializeTree writes tree into dir's working files without committing,
	// leaving every difference unstaged and files outside HEAD untracked.
	MaterializeTree(ctx context.Context, dir, tree string) error
	DiffTrees(ctx context.Context, dir, from, to string) (git.Diff, error)
	// DiffWorktreeStat compares tree against dir's current working tree and
	// returns just the stat, no patch (#143): the live "+N/-M in F" a status
	// row shows while a round is open, cheaper than DiffTrees because it
	// never reads the patch body.
	DiffWorktreeStat(ctx context.Context, dir, tree string) (git.Stat, error)
	HeadCommit(ctx context.Context, dir string) (string, error)
	// TreeFingerprint hashes dir's HEAD and porcelain status into one short
	// string that changes when the tree does (#135). It never reads a diff or
	// writes a snapshot, so a binding's progress sample is cheap.
	TreeFingerprint(ctx context.Context, dir string) (string, error)
	RevListCount(ctx context.Context, dir, from, to string) (int, error)
	BranchExists(ctx context.Context, dir, branch string) (bool, error)
	CreateBranch(ctx context.Context, dir, branch, commit string) error
	// CreateTrackingBranch creates branch in dir tracking upstream, the
	// existing-branch form of creation: add --branch uses it when only
	// origin/<name> exists, before CheckoutWorktree.
	CreateTrackingBranch(ctx context.Context, dir, branch, upstream string) error
	DeleteBranch(ctx context.Context, dir, branch string) error
	AddWorktree(ctx context.Context, dir, path, branch, commit string) error
	// AddDetachedWorktree is AddWorktree without a branch: a throwaway tree
	// at commit with a detached HEAD (#144).
	AddDetachedWorktree(ctx context.Context, dir, path, commit string) error
	// CheckoutWorktree is the existing-branch form of git worktree add; AddWorktree creates the branch, this one checks it out.
	CheckoutWorktree(ctx context.Context, dir, path, branch string) error
	RemoveWorktree(ctx context.Context, dir, path string, force bool) error
	Dirty(ctx context.Context, dir string) (bool, error)
	RefSHA(ctx context.Context, dir, ref string) (string, bool, error)
	UpdateRef(ctx context.Context, dir, ref, newSHA, oldSHA string) error
	// DeleteRef removes ref; a missing ref is success, so the server's
	// settled-binding cleanup deletes the refs it listed without racing.
	DeleteRef(ctx context.Context, dir, ref string) error
	// ListRefs returns every ref in dir beginning with prefix, the server
	// cleanup's worklist of a binding's refs/relevo/<name>/* refs.
	ListRefs(ctx context.Context, dir, prefix string) ([]string, error)
	// RefOnRemote is the ref cleanup's safety check (nothing unpushed is deleted).
	RefOnRemote(ctx context.Context, dir, ref string) (bool, error)
	CommitTree(ctx context.Context, dir, tree, parent, message string) (string, error)
	// CommitAll stages the whole working tree (git add -A) and commits it
	// with relevo's fixed identity, returning the new HEAD sha, or ("", nil)
	// when there was nothing to commit (#137).
	CommitAll(ctx context.Context, dir, message string) (string, error)
	MergeFF(ctx context.Context, dir, ref string) error
	RootCommit(ctx context.Context, dir string) (string, error)
	// ListTags maps each of dir's tags (short name) to the commit it points
	// at, annotated tags peeled (#242).
	ListTags(ctx context.Context, dir string) (map[string]string, error)
	// RepoFacts reports dir's repository identity -- origin remote URL
	// (raw, unnormalised) and the main worktree's absolute .git directory --
	// for the coming history database (#172; captureRepo is the caller).
	RepoFacts(ctx context.Context, dir string) (originURL, commonDir string, err error)
	// Identity reports dir's effective git identity -- the user.name and
	// user.email `git config --get` resolves, global and system config
	// included -- so a remote builder can commit as the client (#335). An
	// unset key is ("", nil), not an error.
	Identity(ctx context.Context, dir string) (name, email string, err error)
	// CurrentBranch names the branch dir has checked out, or "" when HEAD
	// is detached. add/fork record it as a binding's BaseRef, the branch
	// `relevo land` rebases onto (#136).
	CurrentBranch(ctx context.Context, dir string) (string, error)
	// Fetch fetches ref from remote, so origin/<ref> resolves afterwards.
	Fetch(ctx context.Context, dir, remote, ref string) error
	// Rebase rewrites dir's branch onto onto. On a conflict it returns the
	// unmerged paths and aborts the rebase, leaving the worktree as it was.
	Rebase(ctx context.Context, dir, onto string) ([]string, error)
	// Merge integrates ref into dir's branch without rewriting it (--merge's
	// escape hatch). On a conflict it returns the unmerged paths and aborts
	// the merge, leaving the worktree as it was.
	Merge(ctx context.Context, dir, ref string) ([]string, error)
	// Push pushes branch to remote, force-with-lease when the rebase rewrote
	// a branch that already exists there.
	Push(ctx context.Context, dir, remote, branch string, forceWithLease bool) error
	// RemoteBranchExists reports whether remote already has branch, which
	// decides whether a push needs the lease.
	RemoteBranchExists(ctx context.Context, dir, remote, branch string) (bool, error)
}

// Runtime carries relevo's dependencies explicitly, so every command and the
// daemon can be driven by a fake in tests.
type Runtime struct {
	Git Git
	// Runner starts and stops headless builder processes (#99). cmd/relevo
	// wires proc.New(); tests wire fakeRunner. Nil means no headless path
	// can run, and reports ErrRunnerUnavailable.
	Runner     spawn.Runner
	Store      *store.Store
	Candidates *candidate.Set

	// Gates is where the availability ledger and the availability history live
	// (P3b plan §4.5): the store root's database. A nil Gates means no gates
	// store is configured -- gates read as empty, and every write is dropped
	// with an error.
	Gates db.KV

	// GatesDir is the legacy directory holding ledger.json, availability.json
	// and history.json, which LoadKV imports on the first read of their kv
	// rows. It is normally the store root. "" skips the import.
	GatesDir string

	// Latency is where the per-candidate latency history lives (#324 part 1;
	// P3b plan §4.5): time to first output per candidate, recorded by `relevo
	// config --probe` and read back for the p50 on a plain listing. Nil means
	// no store is configured, so nothing is recorded (tests, and any caller
	// that never set one).
	Latency db.KV

	// DB is relevo's sqlite database (docs/specs/2026-09-20-persistence-design.md).
	// Nil means no database: this round opens it only in `relevo db *`, never
	// in the shared runtime constructor, so nothing else reads it yet and
	// every call site that will (later rounds) must treat nil the same as a
	// machine with no db.
	DB *db.DB

	// Config is the database-backed config store newRuntime imports into and
	// loads from (docs/specs/2026-09-24-db-as-record-design.md). It is nil
	// only for a runtime built by newRuntimePeek, which opens no database.
	Config *config.Store

	// Policy is ~/.config/relevo/policy.json: the mastermind's candidate order
	// per role (#61 step 2). The zero value means nothing is ordered, so
	// tests that do not set it behave as a machine with no policy file.
	Policy policy.Policy

	// Registry is the roles registry: roles.json merged over the built-ins,
	// or the legacy derivation. nil means "derive the legacy registry from
	// Candidates and Policy on demand" (#374), which is what every existing
	// test gets, since tests don't set it.
	Registry *roles.Registry

	// ConfigWarnings collects the unknown-key and skipped-candidate warnings
	// the last config load produced (candidates.json then policy.json). They
	// never fail a command: `relevo doctor` renders them in a `config` row and
	// the daemon logs each once (#372 §4.4). newRuntime fills it without
	// printing; the daemon's ConfigWatcher refills it on reload.
	ConfigWarnings []string

	// Usage reads what a round consumed from the harness's own record
	// (#142). Nil means every round records Basis unknown, note
	// "no reader"; tests that do not set it behave as a machine with no
	// reader, and rounds close exactly as before.
	Usage usage.Reader

	// Sessions locates a session record so a round's transcript can be
	// recorded (#184). mastermindLocator (bind.go) also calls it at bind time
	// to fill MasterMind.TranscriptLocator (#172), the same file path, for the
	// coming history database.
	Sessions SessionLocator

	// Classify judges report and dialog paragraphs for instruction-shaped
	// content beside the regex scan (#211). Nil means no classifier is
	// configured and the regex result stands alone; cmd/relevo wires
	// classify.Resolve, tests wire *classify.Fake.
	Classify classify.Classifier

	// Prices is ~/.config/relevo/prices.json over the embedded default. The
	// zero value prices nothing, so every estimate is unknown.
	Prices usage.Prices

	// Fetcher reads the newest published release tag for the day-cached
	// staleness check (#293). Nil means no check runs at all: a served
	// daemon, an air-gapped build and every test that does not set it tick
	// exactly as before. cmd/relevo wires release.NewHTTPFetcher.
	Fetcher release.Fetcher

	Now       func() time.Time
	Hooks     hooks.Dispatcher
	Remote    RemoteClient
	Transport remote.TreeTransport

	// NewID mints a consult id. Nil means a crypto/rand id, so no production
	// call site has to set it and tests can make ids deterministic.
	NewID func() string

	// StartedAt is when this daemon process started; zero means unknown
	// (CLI one-shots, tests), which disables the daemon-restart-relaunch
	// check (#244): a builder can never be "lost to a daemon restart" if
	// the daemon does not know when it itself started.
	StartedAt time.Time

	// Watched is the daemon's in-memory record of the processes it has seen
	// alive (#370). Only `relevo daemon` sets one (relevo.NewWatched); a nil
	// *Watched has seen nothing, which is exactly the #244 rule, so every
	// CLI one-shot keeps its old behaviour.
	Watched *Watched

	// AuthGrace is the daemon's in-memory record of when each binding's server
	// first answered a transient auth error (#373 §3). Only `relevo daemon`
	// sets one (relevo.NewAuthGrace); a nil *AuthGrace never expires, so every
	// CLI one-shot keeps its old behaviour and never halts on a transient 401.
	AuthGrace *AuthGrace

	// Roles checks whether a harness kind's shipped role files are present
	// on disk, so a candidate whose harness has none installed is gated
	// before it is picked (#238). Nil means no check, so tests that do not
	// set it behave as before; cmd/relevo wires harness.OSRoleChecker().
	Roles harness.RoleChecker

	// Scope is the template a served headless round's ProcSpec.Scope is
	// filled from (#244, #216); its Unit is always empty here, since
	// startRound fills in the per-round unit name. Nil means no scopes
	// (the local daemon, CI, or a server whose scope probe failed).
	Scope *spawn.ScopeSpec

	// HeldCPUs returns the cores held by live rounds other than the binding
	// named self, in every store that shares this host's pool (#314). Nil
	// means localHeldCPUs, which reads only the caller's tx. The server sets
	// it to a closure over its owner root, so internal/relevo stays unaware of
	// owners.
	HeldCPUs func(tx *store.Tx, self string) ([]int, error)

	// Channels arbitrates a mastermind's mailbox between the daemon and a live
	// `relevo mcp` channel (docs/specs/2026-09-21-mastermind-channel-design.md).
	// Nil means no claims exist, so DeliverPending leaves the entry pending
	// for `relevo wait`; cmd/relevo wires delivery.KVClaims.
	Channels delivery.ClaimStore

	// MasterMinds is the mastermind registry (#303 step 1a). bind, add, fork and
	// ask resolve their mastermind through it, and the daemon back-fills a
	// binding written before MasterMindID existed. Nil means no registry is
	// configured -- tests, and any embedded caller that predates it -- and
	// resolution then fails with ErrNoMasterMind.
	MasterMinds mastermind.Registry

	// ProcStart reads a process's start time in Unix seconds, the pid-reuse
	// defence mastermind.Resolve's host step needs. Nil means the host step
	// cannot run, and resolution falls through to the session.
	ProcStart func(pid int) (int64, error)

	// OpencodeSession finds an opencode session id for the working directory (#393).
	// Nil when sqlite3 is not on PATH or not configured.
	OpencodeSession func(cwd string, now time.Time) (string, error)

	// OpencodeSessionDir returns the working directory opencode recorded for
	// one session, so a caller holding only the session id can resolve its
	// repository. Nil when sqlite3 is not on PATH or not configured.
	OpencodeSessionDir func(sessionID string) (string, error)

	// Deliverers routes a mastermind-bound payload to that mastermind kind's own
	// push path (docs/specs/2026-09-22-opencode-delivery-design.md). A kind
	// with no entry, and a nil map, leave the entry pending for `relevo wait`.
	Deliverers map[string]delivery.MasterMindDeliverer

	// SessionReaper deletes harness sessions relevo abandoned, so a harness
	// that resumes its own sessions cannot restart a round relevo wrote off.
	// Nil means deletes are skipped and the abandoned entries stay on the
	// binding; cmd/relevo wires the real one.
	SessionReaper SessionDeleter
}

// legacyRegistry is the registry derived from candidates.json and
// policy.json, the behaviour every runtime path had before roles.json (#374).
// It is roles.Build with no file, and that never errors in legacy mode, so
// the error is discarded.
func legacyRegistry(set *candidate.Set, pol policy.Policy) *roles.Registry {
	reg, _ := roles.Build(nil, set, pol)
	return reg
}

// RoleRegistry returns the runtime's roles registry: the one loaded from
// roles.json when it was set, else the legacy derivation of Candidates and
// Policy, built on demand (#374). It never returns nil.
func (rt Runtime) RoleRegistry() *roles.Registry {
	if rt.Registry != nil {
		return rt.Registry
	}
	return legacyRegistry(rt.Candidates, rt.Policy)
}

// IngestDeps builds internal/ingest's Deps from rt: Git carries through
// nil-safe (a nil rt.Git converts to a nil ingest.GitFacts, since both are
// true nil interfaces), Sessions is converted to ingest's own
// SessionLocator type at this boundary (internal/ingest cannot import this
// package -- it is ingest's caller -- so it declares an identical function
// type rather than reusing SessionLocator directly), and Now is time.Now.
func IngestDeps(rt Runtime) ingest.Deps {
	return ingest.Deps{
		Git:      rt.Git,
		Sessions: ingest.SessionLocator(rt.Sessions),
		Now:      time.Now,
	}
}

// AvailabilityDeps builds internal/availability's Deps from rt. Now carries
// through nil-safe as time.Now, since a zero Runtime (a test) has no clock; the
// roles registry is passed as the lazy builder rt.RoleRegistry, so availability
// never builds it eagerly.
func AvailabilityDeps(rt Runtime) availability.Deps {
	now := rt.Now
	if now == nil {
		now = time.Now
	}
	return availability.Deps{
		Store:        rt.Store,
		Candidates:   rt.Candidates,
		Gates:        rt.Gates,
		GatesDir:     rt.GatesDir,
		Latency:      rt.Latency,
		Now:          now,
		Roles:        rt.Roles,
		RoleRegistry: rt.RoleRegistry,
	}
}

// captureDeps builds internal/capture's Deps from rt. Git carries through
// nil-safe: a nil rt.Git converts to a nil capture.Git, since both are true
// nil interfaces at the assignment.
func captureDeps(rt Runtime) capture.Deps {
	return capture.Deps{Git: rt.Git, Store: rt.Store}
}

// consultDeps builds internal/consult's Deps from rt. Git carries through
// nil-safe (both are true nil interfaces at the assignment); Seen and
// LostToRestart close over the daemon's watch set, so a runtime with none reads
// as "never seen" exactly as lostToRestart's nil rule does.
func consultDeps(rt Runtime) consult.Deps {
	now := rt.Now
	if now == nil {
		now = time.Now
	}
	return consult.Deps{
		Store:  rt.Store,
		Runner: rt.Runner,
		Git:    rt.Git,
		Now:    now,
		NewID:  rt.NewID,
		Seen: func(pid int, startedAt int64) {
			rt.Watched.Mark(pid, startedAt)
		},
		LostToRestart: func(pid int, startedAt int64) bool {
			return lostToRestart(rt, pid, startedAt)
		},
		Scope: func(kind, owner, name string, round int, id string) *spawn.ScopeSpec {
			k := scopeKind(kind)
			return scopeFor(rt, k, scopeUnitNameFor(k, owner, name, round, id), "")
		},
		Usage: func(ctx context.Context, b store.Binding, c store.Consult, end time.Time) *usage.Usage {
			return recordUsage(ctx, rt, consultSource(rt, b, c, end))
		},
		Delivery: deliveryDeps(rt),
		ResolveReviewer: func() (candidate.Candidate, harness.RoleSpec, harness.Tier, error) {
			res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), "", "reviewer")
			if err != nil {
				return candidate.Candidate{}, harness.RoleSpec{}, "", err
			}
			c := res.Candidate
			role, err := rt.RoleRegistry().Spec("reviewer", c.Harness)
			if err != nil {
				return candidate.Candidate{}, harness.RoleSpec{}, "", err
			}
			return c, role, verifyTier(rt, c), nil
		},
	}
}

// deliveryDeps builds internal/delivery's Deps from rt. The Channels interface
// carries through nil-safe: a nil rt.Channels stays a nil delivery.ClaimStore,
// since the assignment is between identical interface types.
func deliveryDeps(rt Runtime) delivery.Deps {
	return delivery.Deps{
		Store:       rt.Store,
		Now:         rt.Now,
		Channels:    rt.Channels,
		Deliverers:  rt.Deliverers,
		MasterMinds: rt.MasterMinds,
	}
}

// ErrRemoteUnavailable is returned when a remote operation is attempted without a configured remote client.
var ErrRemoteUnavailable = errors.New("no remote client configured; run relevo config server key and relevo config server add")

// RemoteClient is the client for communicating with remote relevo servers.
type RemoteClient interface {
	WhoAmI(ctx context.Context, server string) (remote.WhoAmI, error)
	Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error)
	CreateBinding(ctx context.Context, server string, req remote.CreateBindingRequest) (remote.BindingView, error)
	GetBinding(ctx context.Context, server, name string) (remote.BindingView, error)
	// StartRound's retryOnUnreachable is the caller's answer to whether the
	// server advertised remote.FeatureIdempotentSend: only a server that
	// dedupes a repeated send may be sent the same round twice (#373 §4.4).
	StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier, candidate string, tags []remote.TagRef, retryOnUnreachable bool) (remote.BindingView, error)
	RoundFile(ctx context.Context, server, name string, round int, kind string) (io.ReadCloser, error)
	RoundFileFrom(ctx context.Context, server, name string, round int, kind string, from int64) (io.ReadCloser, remote.FileRange, error)
	// RoundArtifacts and RoundArtifact read a closed reader round's artifacts;
	// a server that does not advertise remote.FeatureReaders answers 404.
	RoundArtifacts(ctx context.Context, server, name string, round int) (remote.ArtifactList, error)
	RoundArtifact(ctx context.Context, server, name string, round int, rel string) (io.ReadCloser, error)
	RoundBundle(ctx context.Context, server, name string, round int, since string) (io.ReadCloser, error)
	Ack(ctx context.Context, server, name string, round int) (remote.BindingView, error)
	Unavailable(ctx context.Context, server, name, token, reason string) error
	Available(ctx context.Context, server, subject string) (remote.AvailableResponse, error)
	Done(ctx context.Context, server, name string) error
	Unbind(ctx context.Context, server, name string) error
	Resume(ctx context.Context, server, name string) (remote.BindingView, error)
	Stop(ctx context.Context, server, name string) (remote.BindingView, error)
}
