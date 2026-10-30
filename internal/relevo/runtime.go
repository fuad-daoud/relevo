// Package relevo implements the handoff policy: which text moves between a
// mastermind and a builder, when, and when to stop. It holds no intelligence --
// every judgement stays with the mastermind agent.
package relevo

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
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
	"github.com/fuad-daoud/relevo/internal/pathscope"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// Git is the slice of the git CLI relevo needs. *git.Client satisfies it.
type Git interface {
	SnapshotTree(ctx context.Context, dir string) (string, error)
	// MaterializeTree writes tree into dir's working files without committing,
	// leaving every difference unstaged and files outside HEAD untracked.
	MaterializeTree(ctx context.Context, dir, tree string) error
	DiffTrees(ctx context.Context, dir, from, to string) (git.Diff, error)
	// ChangedFiles lists the paths that differ between two trees with their
	// status, modes and blob ids, for the scope check (#801).
	ChangedFiles(ctx context.Context, dir, from, to string) ([]pathscope.Change, error)
	// ReadBlob reads one blob's contents by object id, for the scope check's
	// comment judge (#801).
	ReadBlob(ctx context.Context, dir, oid string) ([]byte, error)
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
	Remotes(ctx context.Context, dir string) ([]string, error)
	RemoteURL(ctx context.Context, dir, remote string) (string, error)
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
	// BlobAt resolves path at ref to its blob id; ok is false when ref has no
	// such regular file.
	BlobAt(ctx context.Context, dir, ref, path string) (oid string, ok bool, err error)
	// ShowPrefix reports dir's path relative to its repository's root, as
	// `rev-parse --show-prefix`; empty at the root. housekeep.ResolveSubdir's one call
	// site: it is what a bind started below the root tells its round apart
	// with.
	ShowPrefix(ctx context.Context, dir string) (string, error)
	// DirAt reports whether ref names a directory at path; ok is false when
	// the entry there is a file or there is no entry at all, so a bind can
	// refuse a subdirectory the base commit does not have.
	DirAt(ctx context.Context, dir, ref, path string) (bool, error)
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
	// InitBare initializes a bare repository at path; an existing bare repo is
	// left as it is. It is on the runtime so a user-mode server creates the
	// owner's bare repo through the owner's credentialed git client.
	InitBare(ctx context.Context, path string) error // CommitAll stages the whole working tree (git add -A) and commits it
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
	// MergeKeep is Merge that leaves a conflict in the tree: it returns the
	// unmerged paths and keeps the merge in progress, so the builder a fork
	// routes to can resolve the markers in the tree it is handed.
	MergeKeep(ctx context.Context, dir, ref string) ([]string, error)
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
	// SkillExists reports whether a skill file is on disk; nil means os.Stat.
	SkillExists func(string) bool
	// Runner starts and stops headless builder processes (#99). cmd/relevo
	// wires proc.New(); tests wire fakeRunner. Nil means no headless path
	// can run, and reports ErrRunnerUnavailable.
	Runner spawn.Runner
	// CPUTicks reads a builder process tree's CPU; nil reads /proc. Tests
	// set it because a fake builder has no process to read.
	CPUTicks   func(pid int) (uint64, bool)
	Store      *store.Store
	Candidates *candidate.Set

	// TmpRoot is the state directory this runtime's rounds and checks create
	// their per-process entries under, through the store's tmp-entry helpers.
	// Empty means no per-process dirs: every test, and container mode, whose
	// builders see the container's own /tmp instead of any host path.
	TmpRoot string

	// DiskFree reads the free bytes under a path for the disk preflight; nil
	// reads the filesystem. Tests set it so no disk is ever filled.
	DiskFree func(path string) (uint64, error)

	// Accounts is the configured login pool: the logins a pick may draw a
	// round from. Empty on every host with no accounts, where a pick records
	// no account and every other path is byte-identical to before.
	Accounts account.Set

	// OpencodeAuth is the install-global opencode credential row: reading
	// which login is active and flipping it. Nil means this host has no
	// opencode seam -- every test that does not set one, and any host with no
	// opencode accounts -- so no flip is attempted.
	OpencodeAuth OpencodeAuth

	// Gates is where the availability ledger and the availability history live
	// (P3b plan §4.5): the store root's database. A nil Gates means no gates
	// store is configured -- gates read as empty, and every write is dropped
	// with an error.
	//
	// The ledger map -- which writer lands in which row and which reader
	// reads it -- is documented at the union that joins the two rows, in
	// gateunion.go.
	Gates db.KV

	// GatesDB is the machine-local file Gates is bound to, as a handle that can
	// open a transaction. It is what the gates document records its write
	// stamps and its export signal in, so both belong to the same transaction
	// as the ledger write that caused them. Nil whenever Gates is not that
	// file -- the serve-wide ledger under its own prefix, a test double -- and
	// then a gate write is stamped nowhere and signals nothing.
	GatesDB *db.DB

	// GatesLocal is the host's machine-local ledger row "ledger", set only on
	// the server's runtimes (internal/serve runtimeAt) beside a Gates bound to
	// the `serve.`-prefixed row. When it is set, AvailabilityDeps binds
	// Deps.Gates to the union of the two rows: gates recorded by `relevo gate`
	// on the server host, and gates the synced gates document applied there,
	// steer served picks and switches exactly as they steer local ones.
	// Reading the union never double-counts a subject live in both rows, and
	// writing through it keeps every entry in its own row: a served write
	// lands in "serve.ledger", never in the host's own.
	GatesLocal db.KV

	// SyncNow asks for a sync attempt now rather than at the end of the export
	// debounce, which is what a gate a person just recorded is worth. The daemon
	// installs its own; nil on a CLI one-shot with no daemon to ask, on a
	// runtime built before the daemon existed, and in every test, and then the
	// next ordinary trigger exports the gate.
	SyncNow func()

	// InFlight counts the live rounds per candidate token across every binding
	// the resolver knows, leaving out the binding named by its argument. Nil
	// means a walk over Store; the server sets its machine-wide walk.
	InFlight func(except string) map[string]int

	// DB is relevo's sqlite database.
	// Nil means no database: this round opens it only in `relevo db *`, never
	// in the shared runtime constructor, so nothing else reads it yet and
	// every call site that will (later rounds) must treat nil the same as a
	// machine with no db.
	DB *db.DB

	// History is the shared file the availability_event and latency_sample rows
	// are read from and written into, when one is open. It is the shared file
	// rather than the machine-local one Gates and Latency bind, because a history
	// row travels through the outbox like every other shared row while a kv
	// document travels as a document; buildRuntime sets it beside the local
	// handle it binds those two to.
	//
	// It is separate from DB rather than the same field because the two answer
	// different questions. DB is the handle a verb owns and writes whole records
	// through, and only a few verbs open one; History is needed by every surface
	// that renders a gate or a latency figure, so a runtime with no DB still has
	// one. historyDB resolves the pair for AvailabilityDeps, so a test or a
	// caller that sets either alone still gets the file.
	History *db.DB

	// Config is the database-backed config store newRuntime imports into and
	// loads from. It is nil
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

	// Sync is the machine-local sync seam: the bounded push-then-pull the
	// daemon drives after a seal that wrote files and again on the idle
	// window, with each outcome recorded in the local markers the statusline
	// reads. Nil means this machine has no sync wired at all, so both
	// triggers skip and the statusline reads markers nobody writes.
	Sync *sync.Runner

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
	// means the local census, which reads only the caller's tx. The server sets
	// it to a closure over its owner root, so internal/relevo stays unaware of
	// owners.
	HeldCPUs func(tx *store.Tx, self string) ([]int, error)

	// Channels arbitrates a mastermind's mailbox between the daemon and a live
	// `relevo push` holder.
	// Nil means no claims exist, so DeliverPending leaves the entry pending
	// for `relevo wait`; cmd/relevo wires delivery.KVClaims.
	Channels delivery.ClaimStore

	// Waits is where `relevo wait` records that it is polling, so a status
	// read can tell a pull payload a live wait is about to collect from one
	// nobody is coming for. Wait writes and refreshes a registration per
	// binding on every poll and removes it on the way out; statusRow only
	// reads. Nil means no registrations exist and every wait reads as not
	// live, which is the safe direction: a payload nobody is waiting on
	// still escalates. cmd/relevo wires delivery.KVWaitClaims.
	Waits delivery.WaitClaimStore

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

	// MuseHost is the nearest ancestor of the caller's parent pid whose
	// command name is `muse`, the host a muse record resolves and heals
	// through. Nil falls back to the parent pid. cmd/relevo wires the
	// /proc reader.
	MuseHost func(ppid int) (pid int, ok bool)

	// OpencodeSession finds an opencode session id for the working directory (#393).
	// Nil when sqlite3 is not on PATH or not configured.
	OpencodeSession func(cwd string, now time.Time) (string, error)

	// OpencodeSessionDir returns the working directory opencode recorded for
	// one session, so a caller holding only the session id can resolve its
	// repository. Nil when sqlite3 is not on PATH or not configured.
	OpencodeSessionDir func(sessionID string) (string, error)

	// Deliverers routes a mastermind-bound payload to that mastermind kind's own
	// push path. A kind
	// with no entry, and a nil map, leave the entry pending for `relevo wait`.
	Deliverers map[string]delivery.MasterMindDeliverer

	// SessionReaper deletes harness sessions relevo abandoned, so a harness
	// that resumes its own sessions cannot restart a round relevo wrote off.
	// Nil means deletes are skipped and the abandoned entries stay on the
	// binding; cmd/relevo wires the real one.
	SessionReaper SessionDeleter

	// CloseServedRound records an owned binding's closed round, through the
	// server package that owns the served close. Nil keeps the binding
	// unchanged, which is what a hook-less test runtime gets; both production
	// builders wire the serve implementation.
	CloseServedRound func(ctx context.Context, rt Runtime, b store.Binding) store.Binding
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
// true nil interfaces), and Now is time.Now.
func IngestDeps(rt Runtime) ingest.Deps {
	return ingest.Deps{
		Git: rt.Git,
		Now: time.Now,
	}
}

// AvailabilityDeps builds internal/availability's Deps from rt. Now carries
// through nil-safe as time.Now, since a zero Runtime (a test) has no clock; the
// roles registry is passed as the lazy builder rt.RoleRegistry, so availability
// never builds it eagerly.
//
// History is historyDB(rt) -- the shared file, whose outbox is the one the sync
// drains -- while Gates is rt.Gates, the machine-local kv view the ledger lives
// in. A row that travels and a machine-local record that does not are not the
// same thing, so they are not the same handle. A served runtime sets
// GatesLocal beside a prefixed Gates, and Gates then binds to their union
// (gateunion.go): the server-wide row and the host's own, one gate per
// subject, every write landing in its own row.
func AvailabilityDeps(rt Runtime) availability.Deps {
	now := rt.Now
	if now == nil {
		now = time.Now
	}
	gates := rt.Gates
	if gates != nil && rt.GatesLocal != nil {
		gates = gateUnionKV{primary: gates, host: rt.GatesLocal, now: now}
	}
	return availability.Deps{
		Store:        rt.Store,
		Candidates:   rt.Candidates,
		Accounts:     rt.Accounts,
		Gates:        gates,
		History:      historyDB(rt),
		Now:          now,
		Roles:        rt.Roles,
		RoleRegistry: rt.RoleRegistry,
		Local:        rt.GatesDB,
		SyncNow:      rt.SyncNow,
		InFlight:     inFlightOf(rt),
	}
}

// historyDB is the shared file the two history tables are reached through:
// History where a runtime sets one, else DB. The fallback is the daemon's shape,
// which sets both to the same handle, and the shape a test uses when it sets only
// DB; a runtime that sets neither is a machine with no database and reads empty.
func historyDB(rt Runtime) *db.DB {
	if rt.History != nil {
		return rt.History
	}
	return rt.DB
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
			res, err := resolve.ResolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), "", "reviewer")
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
	// Actor reads a server's answer for one actor, the same answer a create
	// naming it would get, so a placement probe can decide before anything is
	// created. A server that predates the placement feature answers 404.
	Actor(ctx context.Context, server, actor, candidate string) (remote.ActorView, error)
	Candidates(ctx context.Context, server string) (remote.CandidatesResponse, error)
	CreateBinding(ctx context.Context, server string, req remote.CreateBindingRequest) (remote.BindingView, error)
	GetBinding(ctx context.Context, server, name string) (remote.BindingView, error)
	// StartRound's retryOnUnreachable is the caller's answer to whether the
	// server advertised remote.FeatureIdempotentSend: only a server that
	// dedupes a repeated send may be sent the same round twice (#373 §4.4).
	// force carries SendOptions.Force to the server's own seed-cap check, and
	// is gated on remote.FeatureForce before the call (#702).
	StartRound(ctx context.Context, server, name string, round int, plan []byte, bundle io.Reader, tier, candidate string, force bool, tags []remote.TagRef, retryOnUnreachable bool, verify *bool) (remote.BindingView, error)
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
	Done(ctx context.Context, server, name string) (remote.BindingView, error)
	Unbind(ctx context.Context, server, name string, force bool) error
	Resume(ctx context.Context, server, name string) (remote.BindingView, error)
	Stop(ctx context.Context, server, name string) (remote.BindingView, error)
	// CreateChain's retry is safe only because the server dedupes an
	// identical create: a repeated request answers 200 with the chain's
	// current view rather than creating a second chain.
	CreateChain(ctx context.Context, server string, req remote.CreateChainRequest, bundle io.Reader) (remote.ChainView, error)
	GetChain(ctx context.Context, server, name string) (remote.ChainView, error)
	ChainStop(ctx context.Context, server, name string) (remote.ChainStopResponse, error)
	ChainResume(ctx context.Context, server, name string, req remote.ChainResumeRequest) (remote.ChainView, error)
	ChainDone(ctx context.Context, server, name string) (remote.ChainView, error)
	CreateCheck(ctx context.Context, server, name string, req remote.CreateCheckRequest) (remote.CheckView, error)
	GetCheck(ctx context.Context, server, name, id string) (remote.CheckView, error)
	SetGate(ctx context.Context, server, name string, req remote.SetGateRequest) error
}
