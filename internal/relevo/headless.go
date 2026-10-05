package relevo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/consult"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/reporttail"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
	"github.com/fuad-daoud/relevo/internal/view"
)

// ErrBuilderBusy reports a send against a headless binding whose previous
// round's process is still running (headless spec §5.2). One process per
// round is the model; two at once in one tree would race each other's
// edits. Being a refusalSentinel, anything wrapping it also unwraps to
// ErrRefused, so the CLI classifies the busy send as refused (exit 2) rather
// than internal without each call site naming both sentinels.
var ErrBuilderBusy = refusalSentinel("the previous process is still running; wait for the round to close, or relevo done")

// ErrReportPending reports a send refused because the current round already
// has its completion marker or report on disk, but the daemon has not yet
// ingested the close. The round is over: restaging its plan and starting a
// second builder would make the daemon close on the stale marker and deliver
// the old report. The caller retries once the report is delivered.
var ErrReportPending = refusalSentinel("the round's output is on disk but not yet delivered; relevo wait delivers it, then send the next round")

// ErrScopeActive reports a send refused because this round's systemd scope
// unit is still loaded: a builder for the round is already alive, most
// likely started by an earlier send whose bookkeeping failed (#445). Nothing
// was spawned and nothing was saved.
var ErrScopeActive = refusalSentinel("this round's builder scope is still running")

// handleOf is the endpoint's stored process fields as the Runner's handle.
// StartedAt is Unix seconds on the endpoint (store spec §3.1, amended).
func handleOf(e store.Endpoint) spawn.ProcHandle {
	return spawn.ProcHandle{PID: e.PID, StartedAt: time.Unix(e.StartedAt, 0)}
}

// scopeKind is what a scoped spawn is: the word between "relevo-" and the
// owner in its unit name (#313).
type scopeKind string

const (
	scopeRound   scopeKind = "round"
	scopeGate    scopeKind = "gate"
	scopeConsult scopeKind = "consult"
	scopeVerify  scopeKind = "verify"
)

// scopeOwner8 is the owner component of a unit name: "local" for a binding
// with no owner; the first 8 hex characters of the owning client's id when
// Owner parses as a ClientID; "unknown" otherwise (which never happens for
// what the server itself wrote, but must never block a spawn).
func scopeOwner8(owner string) string {
	if owner == "" {
		return "local"
	}
	if dir, ok := remote.ClientID(owner).Dir(); ok {
		return dir[:8]
	}
	return "unknown"
}

// scopeUnitNameFor is the systemd scope unit's base name (the runner appends
// ".scope") for any scoped spawn (#244, #216, #313): "relevo-<kind>-<owner8>-
// <name>-<round>", followed by "-<id>" when id is non-empty. owner8 and the
// sanitising are shared with the round path, so relevo-round-* names are
// byte-identical to before.
func scopeUnitNameFor(kind scopeKind, owner, name string, round int, id string) string {
	unit := "relevo-" + string(kind) + "-" + scopeOwner8(owner) + "-" + safeUnitPart(name) + "-" + strconv.Itoa(round)
	if id != "" {
		unit += "-" + safeUnitPart(id)
	}
	return unit
}

// scopeUnitName is the per-round systemd scope unit's base name (the
// runner appends ".scope"): "relevo-round-<owner8>-<name>-<round>" (#244,
// #216).
func scopeUnitName(b store.Binding) string {
	return scopeUnitNameFor(scopeRound, b.Owner, b.Name, b.Round, "")
}

// scopeFor builds a spawn's ScopeSpec from the runtime's template (#313, #314):
// nil when the template is nil (scopes off), else a copy of the template with
// Unit set, GateCPUQuota zeroed, and AllowedCPUs set to cpus when cpus is
// non-empty. When kind is scopeGate and the template sets GateCPUQuota, the
// gate's spec uses it as CPUQuota; every other kind keeps the template's
// CPUQuota. It never mutates rt.Scope.
func scopeFor(rt Runtime, kind scopeKind, unit, cpus string) *spawn.ScopeSpec {
	if rt.Scope == nil {
		return nil
	}
	s := *rt.Scope
	s.Unit = unit
	s.GateCPUQuota = ""
	if cpus != "" {
		s.AllowedCPUs = cpus
	}
	if kind == scopeGate && rt.Scope.GateCPUQuota != "" {
		s.CPUQuota = rt.Scope.GateCPUQuota
	}
	return &s
}

// safeUnitPart replaces every rune outside [A-Za-z0-9:_.-] with '-', for a
// string destined for a systemd unit name.
func safeUnitPart(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == ':', r == '_', r == '.', r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteByte('-')
		}
	}
	return sb.String()
}

// defaultRoundBudget mirrors the store's default round timeout, for a
// binding that was never saved. Store.Save fills RoundTimeoutMS on every
// real binding, so this is a guard, not a policy.
const defaultRoundBudget = 24 * time.Hour

// roundBudget is the binding's round budget as a duration: what agy's
// --print-timeout gets, so the harness's own default (5m) never cuts a
// round short (headless spec §3.5).
func roundBudget(b store.Binding) time.Duration {
	if b.RoundTimeoutMS <= 0 {
		return defaultRoundBudget
	}
	return time.Duration(b.RoundTimeoutMS) * time.Millisecond
}

// builderEnv is the environment a binding's headless builder runs with: the
// client's git identity, so a commit the builder makes is authored and
// committed as the client rather than as the server's OS user (#335). Pure.
//
// nil when the binding carries no identity -- a local binding, an old
// client's, or one created before #335 -- which leaves the builder with
// whatever identity the server's environment already resolved.
func builderEnv(b store.Binding) []string {
	if b.Serve == nil || b.Serve.AuthorName == "" || b.Serve.AuthorEmail == "" {
		return nil
	}
	name, email := b.Serve.AuthorName, b.Serve.AuthorEmail
	return []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
}

// roundEnv is the environment a round's process runs with: builderEnv's git
// identity plus the marker naming the binding this process is a runner for. A
// harness session relevo spawns for a round must not read itself as a
// MasterMind or planner, so the marker is what its consent hooks go silent on.
// The spawn appends the marker after the deny filter, so a stale RELEVO_RUNNER
// in the daemon's own environment cannot shadow it. Pure.
func roundEnv(b store.Binding) []string {
	return append(builderEnv(b), mastermind.RunnerEnvEntry(b.Name))
}

// roundEnvFor is roundEnv with the round's builder account pinned: the same git
// identity and runner marker, plus the account home entry last. Appending it
// after the marker puts it after proc.ChildEnv's deny filter, so the account's
// value is the later of two entries of the same name and a stale parent value
// cannot win. A nil account is exactly roundEnv's output, so a host with no
// accounts configured spawns the environment it always did.
func roundEnvFor(b store.Binding, a *account.Account) []string {
	return append(roundEnv(b), accountEnvEntry(a)...)
}

// accountEnvEntry is the spawn environment entry that pins a round's harness to
// the account's per-process home (CLAUDE_CONFIG_DIR, CODEX_HOME). nil for a nil
// account, a kind with no per-process home (opencode), and an account whose
// selector is empty: each would pin nothing.
func accountEnvEntry(a *account.Account) []string {
	if a == nil {
		return nil
	}
	name, ok := harness.HomeEnv(string(a.Harness))
	if !ok {
		return nil
	}
	home, ok := harness.AccountHome(*a)
	if !ok {
		return nil
	}
	return []string{name + "=" + home}
}

// accountFor returns the account a round on candidate c runs as: the first
// account of c's harness serving c's provider, or nil when accounts are
// unconfigured or no account serves the provider. The pool's config order is
// the failover order the pick walks, so the home this pins is the one the
// round's recorded account will name.
func accountFor(rt Runtime, c candidate.Candidate) *account.Account {
	if len(rt.Accounts) == 0 {
		return nil
	}
	pool := rt.Accounts.Pool(account.Kind(c.Harness), c.Provider)
	if len(pool) == 0 {
		return nil
	}
	a, ok, err := account.Select(pool, nil, account.Failover)
	if err != nil || !ok {
		return nil
	}
	return &a
}

// startRound starts the round's process for a headless binding and records
// its handle on the endpoint (headless spec §4.3). The caller holds the
// state lock, has staged the plan, and saves what comes back.
//
// It is the round's prologue -- CPU assignment, candidate lookup, argv -- and
// the spawn itself is startProcess, which resumeRound shares (#370).
//
// Preconditions:  b.Builder.Headless(); no live process on the endpoint
// (Send checks with Runner.Alive first); rt.Runner non-nil; the stored tier is
// at or below max_tier unless allowYolo.
// Postconditions: on success PID, StartedAt and LogPath describe the new
// process. On failure the endpoint is returned as it was (cursor moved to this
// round), PID 0, and the candidate's spawn_failed is in the ledger -- the same
// record a pane spawn failure leaves, because it is the same failure: the
// candidate could not be launched. The caller decides the binding's state.
func startRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, prompt string, allowYolo bool) (store.Binding, error) {
	if rt.Runner == nil {
		return b, spawn.ErrRunnerUnavailable
	}
	b = assignRoundCPU(rt, tx, b)
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	role, err := bindingSpec(rt, b, c.Harness)
	if err != nil {
		return b, fmt.Errorf("binding %q builder: %w", b.Name, err)
	}
	tier, err := launchTier(b, rt.Policy, allowYolo)
	if err != nil {
		return b, err
	}
	argv, err := spawn.HeadlessLaunch(c, role, tier, roundBudget(b), prompt, roundTree(rt, b), rt.Store.OutDir(b.Name))
	if err != nil {
		return b, err
	}
	return startProcess(ctx, rt, tx, b, argv, c)
}

// spawnFailure marks an error that came from Runner.Start refusing to launch
// the process. Its text is its wrapped error's, so startRound's error reads
// exactly as it always did; the type is what lets the lost builder's resume
// branch tell "this harness would not start" -- worth one fresh attempt
// (#370) -- from "this candidate cannot be rendered", which a fresh attempt
// would fail the same way.
type spawnFailure struct{ err error }

func (e spawnFailure) Error() string { return e.err.Error() }
func (e spawnFailure) Unwrap() error { return e.err }

// legacyLog reports whether a round has a builder log on disk (builder-log
// spec §4.4): a round started by an older relevo has one, because that relevo
// opened it for stderr at spawn, and nothing creates one any more. Every
// decision in builder-log round 2 uses this one rule. Pure apart from one
// os.Stat.
func legacyLog(rt Runtime, name string, round int) bool {
	_, err := os.Stat(rt.Store.BuilderLogPath(name, round))
	return err == nil
}

// startProcess is the spawn half of a headless round, shared by startRound
// and resumeRound (#370): argv is the complete command line, already built by
// the caller, and c is the candidate it was built for. It does the
// bookkeeping a new process needs -- the stream cursor, the ProcSpec with its
// scope, Runner.Start, the spawn-failure ledger record, the Watched mark --
// and returns the endpoint carrying the new PID, StartedAt and LogPath.
//
// Preconditions: rt.Runner non-nil (both callers check it before building
// argv); argv is a complete command line. Postconditions: as startRound's.
func startProcess(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, argv []string, c candidate.Candidate) (store.Binding, error) {
	// The harness's writable root is <binding>/out; create it before the
	// process starts so the report, done marker and artifacts all land there.
	if err := rt.Store.EnsureOutDir(b.Name); err != nil {
		return b, fmt.Errorf("ensure out dir for %q: %w", b.Name, err)
	}
	if b.Builder.StreamRound != b.Round {
		// A new round is a new stream file; a mid-round switch (same
		// round) keeps rendering the file both processes append to.
		b.Builder.StreamRound, b.Builder.StreamOffset = b.Round, 0
		b.Builder.StreamSegments = nil
	}
	if fi, err := os.Stat(rt.Store.StreamPath(b.Name, b.Round)); err == nil {
		b.Builder.StreamStart = fi.Size()
	} else {
		b.Builder.StreamStart = 0
	}
	// Record which harness wrote from here on, so the drain renders each line
	// with its own process's kind. A retried spawn at the same offset replaces
	// its failed predecessor's entry rather than stacking a second segment
	// there.
	seg := store.StreamSegment{Start: b.Builder.StreamStart, Kind: b.Builder.Kind}
	if n := len(b.Builder.StreamSegments); n > 0 && b.Builder.StreamSegments[n-1].Start == seg.Start {
		b.Builder.StreamSegments[n-1] = seg
	} else {
		b.Builder.StreamSegments = append(b.Builder.StreamSegments, seg)
	}
	// Record the round's segment list as a round file, so a later round can
	// still render this round's stream with the right harness per process
	// (builder-log spec §4.6). A failure here is never a spawn failure: tests
	// with no saved record hit ErrNotFound, which is fine.
	if tx != nil {
		body, err := json.Marshal(b.Builder.StreamSegments)
		if err == nil {
			err = tx.PutRoundFile(b.Name, b.Round, rt.Store.BuilderSegmentsPath(b.Name, b.Round), body)
		}
		if err != nil {
			slog.Warn("record stream segments", "binding", b.Name, "round", b.Round, "err", err)
		}
	}
	// A new process announces its own session on its own stream (#147);
	// drainStream fills this in again from the first line it writes.
	b.Builder.StreamSessionID = ""
	// stderr joins the round's stream, as it does for consults and gates
	// (#420): LogPath == StreamPath for a new round. A round that already had
	// a NNN-builder.log when the process started -- history, or a round in
	// flight across the upgrade -- keeps writing stderr to that log instead.
	logPath := rt.Store.StreamPath(b.Name, b.Round)
	if legacyLog(rt, b.Name, b.Round) {
		logPath = rt.Store.BuilderLogPath(b.Name, b.Round)
	}
	// A reader round always runs in its scratch worktree (A5 R4a §2). The
	// round's start (Send or Admit) created it from the captured baseline; a
	// mid-round switch, a nudge or a relaunch reuses it. If a crash took it
	// away, recreate it from the round's own baseline before launching: a
	// missing scratch fails the round rather than running in the binding's
	// tree, and never falls back to b.CWD.
	if b.Shape == store.ShapeReader {
		if _, err := os.Stat(roundTree(rt, b)); err != nil {
			if _, err := CreateScratchFrom(ctx, rt, b, b.Round, b.RoundBaselineHead, b.RoundBaselineTree); err != nil {
				return b, err
			}
		}
	}
	spec := spawn.ProcSpec{
		Dir: roundTree(rt, b), Argv: argv,
		Env:        roundEnvFor(b, accountFor(rt, c)),
		LogPath:    logPath,
		StreamPath: rt.Store.StreamPath(b.Name, b.Round),
	}
	spec.Scope = scopeFor(rt, scopeRound, scopeUnitName(b), cpuPinText(b))
	h, err := rt.Runner.Start(ctx, spec)
	if err != nil {
		// A tenant-boundary refusal is the operator's to fix, not the
		// candidate's: it neither gates the candidate nor is a spawnFailure
		// (which the lost-builder path would answer with a fresh relaunch).
		if errors.Is(err, spawn.ErrBoundarySetup) {
			return b, fmt.Errorf("start headless builder for %q (%s): %w", b.Name, c.Ref().String(), err)
		}
		availability.RecordSpawnFailureLocked(AvailabilityDeps(rt), c.Ref().String(), b.Name, err)
		return b, spawnFailure{fmt.Errorf("start headless builder for %q (%s): %w", b.Name, c.Ref().String(), err)}
	}
	// This daemon has now seen the process alive (#370): every successful
	// Start made from a reconcile path marks its handle, so a later tick
	// never judges it lost to a restart.
	rt.Watched.Mark(h.PID, h.StartedAt.Unix())
	b.Builder.PID = h.PID
	b.Builder.StartedAt = h.StartedAt.Unix()
	b.Builder.LogPath = logPath
	return b, nil
}

// resumeRound continues the round's builder in the session it announced
// before it was lost to a daemon restart (#370, spec §4.10): it resolves the
// candidate exactly as startRound does, rebuilds the round's Launch for the
// same candidate and tier, renders its argv through ResumeBuild -- which
// appends the harness's resume selector -- and hands that to startProcess.
//
// It has startRound's pre- and postconditions. ErrResumeUnsupported comes
// back wrapped and unchanged, so the caller can fall back to a fresh
// relaunch.
func resumeRound(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, sessionID, prompt string, allowYolo bool) (store.Binding, error) {
	if rt.Runner == nil {
		return b, spawn.ErrRunnerUnavailable
	}
	ref, err := candidate.ParseRef(b.BuilderCandidate)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	c, err := rt.Candidates.Lookup(ref)
	if err != nil {
		return b, fmt.Errorf("binding %q builder candidate: %w", b.Name, err)
	}
	// The session only resolves under the login that wrote it, so a row a
	// human moved meanwhile is put back before the harness resumes.
	if b.BuilderAccount != "" {
		pick := pickFor(rt)
		if rec, ok := accountByName(pick.Set, b.BuilderAccount); ok {
			ensureOpencodeActive(ctx, rt, rec, pick.Gates)
		}
	}
	h, ok := harness.Lookup(c.Harness)
	if !ok {
		return b, fmt.Errorf("unknown harness kind %q", c.Harness)
	}
	role, err := bindingSpec(rt, b, c.Harness)
	if err != nil {
		return b, fmt.Errorf("binding %q builder: %w", b.Name, err)
	}
	tier, err := launchTier(b, rt.Policy, allowYolo)
	if err != nil {
		return b, err
	}
	l, err := h.Launch(c.Provider, c.Model, c.ExtraArgs, role, tier)
	if err != nil {
		return b, err
	}
	if l.PromptAt < 0 {
		return b, fmt.Errorf("harness %q has no print form", c.Harness)
	}
	sel, err := h.ResumeBuild(sessionID, l, prompt, roundBudget(b), roundTree(rt, b), rt.Store.OutDir(b.Name))
	if err != nil {
		return b, err
	}
	argv := append([]string{h.Binary}, sel...)
	return startProcess(ctx, rt, tx, b, argv, c)
}

// segmentKind is the harness kind that wrote the stream byte at off: the Kind
// of the last segment whose Start is at or before off, or fallback when no
// segment covers off (segs empty, or off before the first Start). The drain
// asks it per line, so a mid-round switch renders the old process's lines with
// the kind that wrote them. Pure.
func segmentKind(segs []store.StreamSegment, off int64, fallback string) string {
	kind := fallback
	for _, s := range segs {
		if s.Start <= off {
			kind = s.Kind
		}
	}
	return kind
}

// carryStream moves the round's stream cursor and its segment list from the
// outgoing endpoint onto its replacement (from -> to) and returns to, so a
// mid-round switch keeps rendering the same round's file from where it left
// off instead of re-rendering it from byte 0 with the new harness's kind. The
// fields it does not name -- Kind, AgentName, Mode, PID -- come from to. Pure:
// from is not changed.
func carryStream(from, to store.Endpoint) store.Endpoint {
	to.StreamRound = from.StreamRound
	to.StreamOffset = from.StreamOffset
	to.StreamStart = from.StreamStart
	to.StreamSegments = append([]store.StreamSegment(nil), from.StreamSegments...)
	return to
}

// drainStream brings a legacy round's builder log up to date with its stream
// (transcript spec §4.2) and, for a round with no log, only advances the
// cursor and captures the session id: every complete line of the stream file
// past the endpoint's cursor is rendered with one transcript renderer for the
// pass and, when the round has a log on disk (legacyLog), appended to it in one
// write. The cursor moves past the last newline consumed either way.
// A trailing partial line waits for the next tick. The cursor is keyed on
// StreamRound, not b.Round, so a round that closed on its marker while the
// builder was still flushing keeps draining until the next round starts.
//
// The pass owns one renderer, so a claude call and its result drained in the
// same range keep their duration; the two events falling either side of a tick
// boundary lose that one duration, and nothing else.
//
// The first drained line that names the harness's own session records it on
// the endpoint (#147); later lines cannot change it, so a sub-agent's session
// appearing mid-round is ignored. A line that names none is not an error.
//
// It never fails the tick: every problem is a slog.Warn and an unchanged
// binding, and the cursor advances only after the append succeeded, so a
// failed write renders the same lines again next tick rather than dropping
// them. Nothing here is read for meaning (headless spec §1).
func drainStream(rt Runtime, b store.Binding) store.Binding {
	round := b.Builder.StreamRound
	if round == 0 {
		return b
	}
	logPath := ""
	if legacyLog(rt, b.Name, round) {
		logPath = rt.Store.BuilderLogPath(b.Name, round)
	}
	r := transcript.NewRenderer()
	b.Builder.StreamOffset = drainFile(
		logPath,
		rt.Store.StreamPath(b.Name, round),
		b.Builder.StreamOffset,
		func(off int64, line []byte) []string {
			kind := segmentKind(b.Builder.StreamSegments, off, b.Builder.Kind)
			if b.Builder.StreamSessionID == "" && off >= b.Builder.StreamStart {
				if id := transcript.SessionID(kind, line); id != "" {
					b.Builder.StreamSessionID = id
				}
			}
			return r.Render(kind, line)
		},
		"stream", "binding", b.Name, "round", round,
	)
	return b
}

// drainFile appends render(off, line) for every complete line of src past off
// to logPath -- nothing when logPath is "", which still advances the offset --
// and returns the new offset. off is the line's own absolute byte
// offset in src, so a caller rendering per byte range knows where each line
// came from. It is the shared body of
// drainStream and drainSession (#184): it never fails the tick -- every
// problem is a slog.Warn (with what) and the offset unchanged, except an
// offset past EOF, which resets to 0. what names the source in warnings
// ("stream", "session").
func drainFile(logPath, src string, off int64, render func(off int64, line []byte) []string, what string, fields ...any) int64 {
	info, err := os.Stat(src)
	if err != nil {
		return off // not started yet, or gone with the round: nothing to drain
	}
	if off > info.Size() {
		slog.Warn("builder "+what+": cursor past end of "+what+"; rendering from the start",
			append(append([]any{}, fields...), "offset", off, "size", info.Size())...)
		off = 0
	}
	if off == info.Size() {
		return off
	}
	data, err := readFrom(src, off)
	if err != nil {
		slog.Warn("builder "+what, append(append([]any{}, fields...), "err", err)...)
		return off
	}
	end := bytes.LastIndexByte(data, '\n')
	if end < 0 {
		return off
	}
	var out []string
	lineOff := off
	for _, line := range bytes.Split(data[:end], []byte{'\n'}) {
		out = append(out, render(lineOff, line)...)
		lineOff += int64(len(line)) + 1
	}
	if logPath != "" && len(out) > 0 {
		if err := appendLines(logPath, out); err != nil {
			slog.Warn("builder "+what, append(append([]any{}, fields...), "err", err)...)
			return off
		}
	}
	return off + int64(end) + 1
}

// readFrom is the file's bytes from off to its end.
func readFrom(path string, off int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// appendLines appends lines, each newline-terminated, to the file at path
// in one write: O_APPEND writes of one buffer interleave with the
// supervisor's stderr at line boundaries.
func appendLines(path string, lines []string) error {
	f, err := openAppend(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(strings.Join(lines, "\n") + "\n")
	return err
}

// logTailLines is how much of a builder log the exit entry carries: enough
// to see why it died, not enough to flood the round log (spec §3.8).
const logTailLines = 20

// logTail is the last n lines of the file at path, without a trailing
// newline, or "" when the file is absent or empty. It is text for humans --
// the exit entry's payload, the status snippet -- and is never parsed
// (spec §1: relevo reads no builder output for meaning).
func logTail(path string, n int) string {
	data, err := os.ReadFile(path)
	if err != nil || n <= 0 {
		return ""
	}
	s := strings.TrimRight(string(data), "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// clearProcess is the endpoint between rounds: no pid, no start time, no
// log. Identity -- name, kind, mode -- is untouched.
func clearProcess(e store.Endpoint) store.Endpoint {
	e.PID, e.StartedAt, e.LogPath = 0, 0, ""
	return e
}

// exitEntry is the log record of a headless builder that exited without a
// report (spec §3.8): relevo → log only, Confirmed, never a pending payload.
// codeText is the exit code, or "unknown" when the supervisor's trailer is
// missing (killed, or the log unreadable). payload is the builder's last
// logTailLines of evidence, computed by the caller with builderTail, for the
// human; relevo reads nothing out of it. shape picks the word for the missing
// artifact: a reader's is an output.
func exitEntry(now time.Time, round int, logPath, codeText, suffix, payload, shape string) store.LogEntry {
	return store.LogEntry{
		TS:        now,
		Round:     round,
		Direction: store.DirToMasterMind,
		Kind:      store.KindExit,
		Path:      logPath,
		Note:      fmt.Sprintf("builder exited (code %s) %s%s", codeText, withoutArtifact(shape), suffix),
		Payload:   payload,
		Confirmed: true,
	}
}

// streamLastActivity is when a live headless builder's stream last moved: the
// later of the stream file's mtime and the round's start (#252). A missing or
// unreadable stream is not an error -- a round that has not produced a line
// yet counts from its start, and a stream that never appears for stall_after_ms
// on a live process is exactly a stall.
func streamLastActivity(rt Runtime, b store.Binding) time.Time {
	st, err := os.Stat(rt.Store.StreamPath(b.Name, b.Round))
	if err != nil {
		return b.RoundStartedAt
	}
	last := st.ModTime()
	if b.RoundStartedAt.After(last) {
		return b.RoundStartedAt
	}
	return last
}

// stallLimitGateRecorded reports whether this stalled round has already
// recorded its rate-limit gate (#905 follow-up). gateOnLimit writes the ledger
// entry before it looks for the round's report, so a stalled live builder whose
// round already produced a report was re-recording the same gate on every tick
// for as long as the stall held -- and with a reset the scan reads relative to
// now ("resets in 23m", or no reset at all and the fallback), each record moved
// Until forward, so every other binding on the same candidate or account stayed
// gated indefinitely. A repeat tick carries no new fact about the provider, so
// it must not record again: one entry per round is the whole observation, and
// the tick falls through to deliverAndSettle exactly as a tail that matched
// nothing does.
//
// Scoped to the current round by RoundStartedAt, which a switch restarts, so a
// new round gates afresh. The report is required to be on disk because that is
// the only shape that reaches here without having switched: a match with no
// report ends the tick in gateOnLimit -> switchBuilder, and the replacement
// starts with a fresh RoundStartedAt. Without the report a gate-on-limit tick
// is the existing single-tick switch path, unchanged.
func stallLimitGateRecorded(rt Runtime, b store.Binding) bool {
	if rt.Gates == nil {
		return false
	}
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.ReportPath(b.Name, b.Round)); !ok {
		return false
	}
	l, err := availability.LoadLedger(rt.Gates)
	if err != nil {
		// An unreadable ledger is not evidence the gate was already recorded,
		// the same rule gateOnLimit writes under: it records and reports.
		return false
	}
	for _, e := range l.Entries {
		if e.Kind != availability.RateLimited || e.Binding != b.Name {
			continue
		}
		if e.At.Before(b.RoundStartedAt) {
			continue
		}
		return true
	}
	return false
}

// reconcileHeadless is one tick of a headless binding (spec §5.1). Reconcile
// hands off here right after the DONE gate; the pane path never runs for a
// headless endpoint and this never runs for a pane one.
//
// Order: halt on the round cap, then decide. A report file finishes the round
// whatever the process did (the report is the contract). Otherwise: a gated
// candidate is switched with its process killed; a live process is left alone,
// its budget the only thing that can halt it; a process that exited is logged
// with its code and log tail and the builder is switched, up to max_switches.
// No round open means idle -- never BROKEN -- and a process lingering with no
// round is a stray relevo stops.
func reconcileHeadless(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding) (store.Binding, error) {
	if b.Round > b.RoundCap {
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: %s of %d", b.Name, ErrRoundCap, b.RoundCap))
	}

	// Render what the builder has streamed since the last tick before
	// anything below reads the log: the exit entry's tail, the limit scan
	// and the status snippet all see a log that is current (transcript
	// spec §4.3).
	b = drainStream(rt, b)

	entries, err := tx.ReadLog(b.Name)
	if err != nil {
		return b, err
	}
	roundOpen := store.RoundOpen(entries, b.Round)

	if !roundOpen {
		// Idle is normal (spec §5.1): between rounds there is no process.
		// One with no round is a stray -- a send whose round closed some
		// other way -- and relevo stops it rather than let it edit a tree
		// nobody is watching.
		if b.Builder.PID != 0 {
			if rt.Runner != nil {
				// No round is open, nothing reads this handle's exit, and
				// b.Round may be 0.
				if err := rt.Runner.Kill(ctx, handleOf(b.Builder), ""); err != nil {
					slog.Warn("stray headless process not killed", "binding", b.Name, "pid", b.Builder.PID, "err", err)
				} else {
					slog.Warn("killed stray headless process", "binding", b.Name, "pid", b.Builder.PID)
					b = abandonSession(b)
				}
			}
			b.Builder = clearProcess(b.Builder)
			b.StalledSince = time.Time{}
		}
		if b.State == store.StateBroken {
			b.State = store.StateActive
			// The fault that broke the binding is over, so the halt that
			// recorded it goes with it. Left in place it reads as the reason the
			// round is still stopped: the next send is refused with 409
			// round_halted naming a switch that has already been replaced, and
			// the round classifies as halted for as long as the binding lives.
			b = clearHaltFields(b)
		}
		return deliverAndSettle(ctx, rt, tx, b)
	}

	// A queued round (#285) has no process and no clocks: nothing to
	// reconcile until relevo.Admit starts it. This must precede the PID == 0
	// "spawn failed earlier" branch below, or a queued round would be
	// mistaken for a failed spawn.
	if !b.QueuedAt.IsZero() {
		return b, nil
	}

	// The marker is the contract (completion-marker spec §4.4): the process
	// is done with whatever it exits as, and it exits on its own. Never Kill
	// here. A report without a marker is a process still working.
	//
	// The escape check runs before closeOnMarker, not inside it: queueReport
	// clears RoundBaselineTree, and the snapshot must be compared against it
	// while it is still on b (#192). It runs only when the marker is there --
	// the close is the only reader of its answer -- so a live round pays two
	// git subprocesses once, at its close, not every tick under the state
	// lock. That per-tick pair was the largest cost of a live headless tick
	// and it warned every tick for a repository git could not read.
	markerNote := ""
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.DonePath(b.Name, b.Round)); ok {
		if escapeCheck(ctx, rt, b, true) == EscapeNote {
			markerNote = escapeNote
		}
	}
	// queueReport's reset block clears RoundVerify: read the round's verify
	// flag before the close consumes it (#144), as the pane path does.
	wantVerify := b.RoundVerify
	next, closed, gating, err := markerClose(ctx, rt, tx, b, entries, markerNote, wantVerify)
	if err != nil {
		return b, err
	}
	if gating || closed {
		return next, nil
	}

	if b.Builder.PID == 0 {
		// Round open, no process, no report: a send whose Start failed.
		// Send already put the binding in NEEDS YOU and the ledger has the
		// spawn_failed; nothing to observe until the human acts.
		return b, nil
	}
	if rt.Runner == nil {
		slog.Warn("headless binding but no Runner configured", "binding", b.Name)
		return b, nil
	}

	switchable := b.BuilderCandidate != "" && !b.RoundStartedAt.IsZero()
	if switchable {
		if g, gated := gatedBuilder(rt, b); gated {
			reason := "rate-limited"
			if g.Note != "" {
				reason = "rate-limited: " + g.Note
			}
			return switchBuilder(ctx, rt, tx, b, reason, true, false)
		}
	}

	alive, err := rt.Runner.Alive(ctx, handleOf(b.Builder))
	if err != nil {
		// An OS hiccup is not evidence the builder stopped: treat it as
		// alive this tick and say so (spec §6).
		slog.Warn("headless liveness check failed; treating as alive", "binding", b.Name, "pid", b.Builder.PID, "err", err)
		alive = true
	}
	if err == nil && alive {
		// A sighting: this daemon now knows the process is alive (#370), so
		// no later tick classifies it as lost to a restart.
		rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt)
	}
	if alive {
		now := rt.Now().UTC()
		next, halted, err := checkRoundTimeout(ctx, rt, tx, b)
		if halted {
			return next, err // the halt queued its entry and settled delivery itself
		}
		if err != nil {
			return b, err
		}
		// The process is alive; the progress clock (#135) replaces #252's
		// stream-only rule: the tree and the stream are both signals, and a
		// stall is quiet on both. The read is gated on the interval (#135
		// follow-up): a tick inside it samples nothing.
		if progressDue(next, now, rt.Policy.ProgressInterval()) {
			next = progressStep(rt, next, now, sampleSignals(ctx, rt, next))
		}
		// A builder that went quiet on both signals is not necessarily
		// working (#905): it may be sitting on a provider's rate-limit
		// answer it never got to report, because it never got to exit. The
		// exit path already gates on the same tail, but only once the
		// process is gone -- a stalled one can sit there until the round's
		// budget runs out. So the stall is the gate: scan the current
		// process's own harness lines (limitText) and, on a match, take the
		// existing gateOnLimit -> switchBuilder path, uncounted and with
		// closeOld, so the replacement starts on the same round.
		//
		// Nothing else about this tick changes: only a stalled binding is
		// scanned, so an alive tick inside stall_after_ms still pays no
		// stream read, and a stall with no limit line in its tail falls
		// through to deliverAndSettle exactly as before. Escape and denial
		// text keep their precedence at exit -- this scan is limit patterns
		// only, and never reads anything the exit path would have handled.
		//
		// A repeat tick on the same stall reads the same unchanged tail, so
		// it must not record the same gate again: stallLimitGateRecorded
		// skips the whole block once this round has recorded one.
		if !next.StalledSince.IsZero() && !stallLimitGateRecorded(rt, next) {
			gated, _, handled, gerr := gateOnLimit(ctx, rt, tx, next, limitText(ctx, rt, next), true)
			if gerr != nil {
				return next, gerr
			}
			if handled {
				// A switch tick does not deliver, like every other switch:
				// replacing the builder is the whole of what this tick did.
				return gated, nil
			}
		}
		return deliverAndSettle(ctx, rt, tx, next)
	}

	// The runner exited, so whatever the round's scope still holds is an
	// abandoned straggler: end it before anything below relaunches, switches
	// or nudges under the same unit name. Best effort -- the round's close
	// must not fail because a scope would not die.
	endRoundScope(ctx, rt, b, b.Round)

	// Exited. The exit code is read once, from the stream's trailer.
	codeText := "unknown"
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); ok {
		codeText = strconv.Itoa(code)
	}

	// The marker can be written between the closeOnMarker read above and this
	// liveness observation: a builder that writes its marker and exits inside
	// one tick must close as marked, not as unmarked (#328). Re-check the
	// marker now and close through the marker path if it is there.
	if _, _, ok, _ := rt.Store.StatFile(rt.Store.DonePath(b.Name, b.Round)); ok {
		slog.Debug("marker appeared before exit was observed", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID)
		markerNote := ""
		if escapeCheck(ctx, rt, b, true) == EscapeNote {
			markerNote = escapeNote
		}
		next, _, _, err := markerClose(ctx, rt, tx, b, entries, markerNote, wantVerify)
		if err != nil {
			return b, err
		}
		return next, nil
	}

	// Exited after writing a report but without the marker: an exited
	// process cannot be mid-write, so the report is trusted and the
	// omission noted (spec §4.4). A reader whose deliverable is absent
	// skips this and falls through to the exit-without-report path.
	if b.Shape != store.ShapeReader || readerDeliverablePresent(rt, b) {
		reportPath, serr := writeReaderSummary(rt, b)
		if serr != nil {
			slog.Warn("reader output not written", "binding", b.Name, "round", b.Round, "err", serr)
		}
		if _, _, ok, _ := rt.Store.StatFile(reportPath); ok {
			_, m, _, err := gateOnLimit(ctx, rt, tx, b, limitText(ctx, rt, b), false)
			if err != nil {
				return b, err
			}
			slog.Warn("headless builder exited with a report but no marker", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText, "note", "unmarked")
			payload := fmt.Sprintf(
				"Builder exited (code %s) after writing its %s but never confirmed completion (no %s). %s.",
				codeText, artifactNoun(rt, b), filepath.Base(rt.Store.DonePath(b.Name, b.Round)), closeClause(rt, b, b.Round))
			if m.Line != "" {
				payload += fmt.Sprintf(" Provider rate-limited: %s; gated until %s.", m.Line, availability.GateTimeText(m.Until))
			}
			note := "unmarked"
			if escapeCheck(ctx, rt, b, true) == EscapeNote {
				note = joinNotes(note, escapeNote)
			}
			next, err := queueReport(ctx, rt, tx, b, entries, reportPath, payload, note, nil, nil, nil, nil, "", false, scopeVerdict{})
			if err != nil {
				return b, err
			}
			if next.Owner != "" {
				// An unmarked close is still a close: record the served round's
				// facts as markerClose does, or the owner sees "idle" and never
				// fetches the report.
				next = closeServedRound(ctx, rt, next)
			}
			next.Builder = clearProcess(next.Builder)
			next.StalledSince = time.Time{}
			return deliverAndSettle(ctx, rt, tx, next)
		}
	}

	// Exited without a report.
	now := rt.Now().UTC()
	h, _ := harness.Lookup(b.Builder.Kind)
	var c candidate.Candidate
	if ref, err := candidate.ParseRef(b.BuilderCandidate); err == nil && rt.Candidates != nil {
		if cand, err := rt.Candidates.Lookup(ref); err == nil {
			c = cand
		}
	}
	denialLine, isDenial := matchDenial(currentBuilderTail(rt, b, availability.LimitScanLines), denialPatterns(c, h))
	suffix := ""
	if isDenial {
		suffix = "; permission-blocked: " + denialLine
	}

	// Detect the oom kill before the exit entry, so the entry says it. A
	// requested stop still wins and is checked below. The kernel reports a
	// cgroup kill either as a process that left no trailer (unknown) or as the
	// supervisor's own 128+SIGKILL (137), so both are probed.
	var oomPeak int64
	oom := false
	if codeText == "unknown" || codeText == "137" {
		if peak, killed := oomKilled(ctx, rt, b); killed {
			oom, oomPeak = true, peak
			suffix += "; " + oomWords(peak)
		}
	}

	if err := tx.AppendLog(b.Name, exitEntry(now, b.Round, b.Builder.LogPath, codeText, suffix, builderTail(rt, b, logTailLines), b.Shape)); err != nil {
		return b, err
	}
	slog.Info("headless builder exited without a report", "binding", b.Name, "round", b.Round, "pid", b.Builder.PID, "code", codeText)

	// A stop requested for a round that then exited without a report (#138):
	// close it without a report and without a switch, because a stop is not a
	// failure. Belt-and-braces only -- Stop on a headless round closes
	// synchronously and never sets the request -- for a process killed
	// between the request and the close.
	if !b.StopRequestedAt.IsZero() {
		next, err := closeStopped(ctx, rt, tx, b, "killed")
		if err != nil {
			return next, err
		}
		if next.Owner != "" {
			// Same as Stop and markerClose: a served round that closed here
			// must record its facts, or the owner sees "idle" and never
			// collects it.
			next = closeServedRound(ctx, rt, next)
		}
		return next, nil
	}

	// An oom kill is not the candidate's failure, so re-queue on the same
	// candidate with no switch and no exclusion.
	if oom {
		return requeueOOM(ctx, rt, tx, b, oomPeak, now)
	}

	// A cgroup/group kill of the daemon (systemd restart, kill -9 of the
	// process tree) takes the supervisor with it before it can write the
	// relevo-exit: trailer, which reads exactly like a real builder death:
	// code unknown. The daemon can tell the two apart because it knows its
	// own start time and, since #370, which processes it has seen alive: a
	// process that started before this daemon and that this daemon never
	// saw is the one this daemon's restart must have killed. Recovery keeps
	// the same candidate on the same round, uncounted, so a daemon restart
	// does not spend the round's switch budget (#244). A lost local builder
	// first resumes its own harness session (claude --resume, agy
	// --conversation, opencode --session --fork), and falls back to a fresh
	// relaunch when the harness cannot resume (codex), announced no session,
	// or failed to spawn with the resume selector. Both carry the
	// interrupted note, stay uncounted and keep RoundStartedAt.
	lost := codeText == "unknown" && lostToRestart(rt, b.Builder.PID, b.Builder.StartedAt)

	b.Builder.PID, b.Builder.StartedAt = 0, 0 // LogPath stays: status and the entry point at it
	b.StalledSince = time.Time{}              // the process is gone: not stalled any more

	if lost && switchable {
		if b.Owner != "" {
			// On a server, a box reboot must not relaunch every builder past
			// the cap: re-queue at the head of the queue instead of
			// relaunching (#285, #244). The local daemon keeps the relaunch
			// below.
			b.QueuedAt = b.RoundStartedAt
			b.RoundStartedAt = time.Time{}
			b = abandonSession(b)
			b.Builder = clearProcess(b.Builder)
			if err := tx.AppendLog(b.Name, store.LogEntry{
				TS: now, Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindQueue, Confirmed: true,
				Note: "re-queued (builder lost to a restart)",
			}); err != nil {
				return b, err
			}
			slog.Info("headless builder re-queued after daemon restart", "binding", b.Name, "round", b.Round)
			return b, nil
		}

		// A candidate the configured set no longer holds cannot be resumed
		// or relaunched: pick again the way a switch does. Accepted cost:
		// switchBuilder restarts RoundStartedAt, unlike the same-candidate
		// relaunch below.
		if staleBuilder(rt, b) {
			return switchBuilder(ctx, rt, tx, b,
				"lost to a daemon restart; candidate "+b.BuilderCandidate+" is no longer configured", false, false)
		}

		text := roundPrompt(rt, tx, b, rt.Store.PromptPath(b.Name, b.Round), rt.Store.ReportPath(b.Name, b.Round), rt.Store.DonePath(b.Name, b.Round)) +
			"\n\n" + interruptedNote(rt.StartedAt)
		keep := b.RoundStartedAt
		// Read the round's session before anything clears it (#370): the
		// old process announced it on its stream, and startProcess -- which
		// the fresh relaunch calls -- clears StreamSessionID, because a new
		// process begins a new session.
		sess := b.Builder.StreamSessionID
		oldKind := b.Builder.Kind
		prior := peekUsage(ctx, rt, b, now)
		var (
			next store.Binding
			err  error
			how  = "relaunched"
		)
		if sess != "" {
			next, err = resumeRound(ctx, rt, tx, b, sess, text, false)
			switch {
			case err == nil:
				how = "resumed session " + sess
			case errors.Is(err, harness.ErrResumeUnsupported):
				// codex, and any kind relevo cannot resume: the fresh
				// relaunch is round 1's behaviour and needs no note.
				next, err = startRound(ctx, rt, tx, b, text, false)
			default:
				var sf spawnFailure
				if errors.As(err, &sf) {
					// The harness would not start with the resume
					// selector. One fresh attempt, then the halt below if
					// that fails too (spec §6).
					slog.Warn("resume failed; relaunching fresh", "binding", b.Name, "round", b.Round, "session", sess, "err", err)
					next, err = startRound(ctx, rt, tx, b, text, false)
				}
			}
		} else {
			next, err = startRound(ctx, rt, tx, b, text, false)
		}
		if err != nil {
			b = abandonSessionID(b, oldKind, sess)
			return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: builder lost to a daemon restart and could not be relaunched: %v", b.Name, err))
		}
		b = next
		// The new process has StreamSessionID "": reapable waits until it
		// announces its own session, because the fork reads the old one.
		b = abandonSessionID(b, oldKind, sess)
		// The round's budget clock survives the restart (#370, spec §4.3):
		// the interruption is relevo's, so it must not buy the round more
		// time than it had.
		b.RoundStartedAt = keep
		if err := tx.AppendLog(b.Name, store.LogEntry{
			TS: now, Round: b.Round, Direction: store.DirToMasterMind, Kind: store.KindSwitch, Confirmed: true,
			Usage: prior,
			Note: fmt.Sprintf("%s builder (lost to a daemon restart at %s): picked %s for builder: same candidate, not counted",
				how, rt.StartedAt.UTC().Format(time.RFC3339), b.BuilderCandidate),
		}); err != nil {
			return b, err
		}
		b.State = store.StateActive
		slog.Info("headless builder relaunched after daemon restart", "binding", b.Name, "round", b.Round, "candidate", b.BuilderCandidate)
		return b, nil
	}

	// The escape halt comes before gateOnLimit: a limit line in the log of
	// an escaped round must not turn a halt into a switch (#192).
	if escapeCheck(ctx, rt, b, false) == EscapeHalt {
		return haltAndSettle(ctx, rt, tx, b, escapeDiagnosis(b, codeText))
	}

	next, _, handled, err := gateOnLimit(ctx, rt, tx, b, limitText(ctx, rt, b), false)
	if handled {
		return next, err
	}

	if isDenial {
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf(
			"%s: builder exited (code %s) %s after a permission denial (%q); not switched -- re-send with a higher tier (relevo send --name %s --file <plan> --tier edit|yolo [--allow-yolo]) or extend the harness's allow list; log: %s",
			b.Name, codeText, withoutArtifact(b.Shape), denialLine, b.Name, showCommand(b.Name, b.Round, "log")))
	}

	// A builder that ended its turn cleanly without a report has left a
	// session nothing will wake: resume it once with a nudge and hand the
	// round back, rather than spend a switch on work that is nearly done.
	if next, resumed, err := nudgeResume(ctx, rt, tx, b, entries, codeText, now); err != nil {
		return next, err
	} else if resumed {
		slog.Info("headless builder nudged to finish", "binding", b.Name, "round", b.Round, "session", b.Builder.StreamSessionID)
		return next, nil
	}

	if b.Shape == store.ShapeReader && codeText == "0" {
		return haltAndSettle(ctx, rt, tx, b, readerUndeliveredReason(rt, b, nudgesSincePlan(entries, b.Round)))
	}

	if !switchable {
		return haltAndSettle(ctx, rt, tx, b, fmt.Sprintf("%s: builder exited (code %s) %s; see %s", b.Name, codeText, withoutArtifact(b.Shape), showCommand(b.Name, b.Round, "log")))
	}
	// A provider-side outage behind this exit is a provider gate, not a
	// builder failure (#931), so it is offered the timed gate rather than the
	// until-cleared exclusion below: one rate_limited entry whose note keeps
	// the cause, and the builder switched UNCUNTED, so the candidate is
	// eligible again as soon as the provider recovers instead of staying
	// locked out of the round until a human clears it.
	//
	// It sits at the exclusion decision and nowhere earlier, so a stop, an
	// oom kill, a loss to a restart, an escape, a denial, a nudge, an
	// undelivered reader and a !switchable halt all keep their existing order
	// and outcomes -- and gateOnLimit above, which is the quota-shaped case,
	// still runs first. A tail that names no provider fault falls through
	// untouched, so a genuine crash keeps every part of #191.
	//
	// No repeat-record guard is needed here, unlike the stalled-live scan's:
	// this tick is on the exited path, and the switch restarts RoundStartedAt
	// and the stream cursor, so the next decision point scans a different
	// process's own bytes.
	outaged, gated, gerr := gateOnOutage(ctx, rt, tx, b, limitText(ctx, rt, b))
	if gerr != nil {
		return outaged, gerr
	}
	if gated {
		return outaged, nil
	}
	// The exclusion is appended to the b that switchBuilder receives so the
	// replacement inherits it and the field is persisted with the switch
	// (#191): a headless builder that exited without a report is excluded
	// from the pick for the rest of this round.
	b.RoundExcluded = appendUnique(b.RoundExcluded, b.BuilderCandidate)
	return switchBuilder(ctx, rt, tx, b, fmt.Sprintf("exited (code %s) %s", codeText, withoutArtifact(b.Shape)), false, true)
}

// readerFinalMessageGrace is how long a reader round whose marker is present
// waits for its runner to exit before relevo gives up on it: a runner writes
// its final message just after the marker and then exits, so two minutes is
// generous, and a hung one must not hold the round forever.
const readerFinalMessageGrace = 2 * time.Minute

// readerSummaryEarlyNote is the note a reader round closes with when its
// runner outlived readerFinalMessageGrace: the summary is the stream as it was,
// not a completed final message.
const readerSummaryEarlyNote = "runner still running after its marker; summary taken early"

// holdReaderOnMarker reads a reader round's marker once and reports what the
// close must do with it. A reader's marker is not the end of its stream: the
// runner writes its final message just after the marker and then exits, and
// that message is the round's summary. So the round waits for the exit -- held
// is true while the process is alive inside readerFinalMessageGrace -- and a
// runner still alive after the grace is stopped the way `relevo stop` stops
// one, with early true so the caller closes on the stream as it is.
//
// present is false when the marker is not on disk this tick: the round is
// still live, and the caller must close nothing at all. That is what keeps the
// reader's marker read and its close on one observation: a marker written
// between this read and closeOnMarker's own would otherwise close the round
// the instant it appeared, before the runner printed the final message.
//
// A marker whose round has no report yet is held inside the grace even when
// its process is gone: the message can land after the marker while the
// process exits, so an empty stream must not close as noreport the moment the
// marker is seen. Past the grace a genuinely empty round is left to the
// ordinary close. An unreadable liveness check holds this tick, as the
// unmarked path treats it as alive.
func holdReaderOnMarker(ctx context.Context, rt Runtime, b store.Binding) (present, held, early bool, err error) {
	fi, serr := os.Stat(rt.Store.DonePath(b.Name, b.Round))
	if serr != nil {
		return false, false, false, nil // no marker yet: the round is live, close nothing
	}
	present = true
	withinGrace := rt.Now().Sub(fi.ModTime()) < readerFinalMessageGrace

	// A marker can appear before the runner prints its final message and
	// before it exits: while the round has no report and the grace has not
	// run out, hold the close so a message still coming is not lost to a
	// noreport entry. A live process is recorded so a later tick never
	// classifies it as lost to a restart.
	if withinGrace && !readerHasReport(rt, b) {
		if b.Builder.PID != 0 && rt.Runner != nil {
			alive, aerr := rt.Runner.Alive(ctx, handleOf(b.Builder))
			if aerr != nil {
				slog.Warn("reader liveness check failed; holding the round", "binding", b.Name, "pid", b.Builder.PID, "err", aerr)
			} else if alive {
				rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt)
			}
		}
		return present, true, false, nil
	}

	if b.Builder.PID == 0 || rt.Runner == nil {
		return present, false, false, nil
	}
	alive, aerr := rt.Runner.Alive(ctx, handleOf(b.Builder))
	if aerr != nil {
		slog.Warn("reader liveness check failed; holding the round", "binding", b.Name, "pid", b.Builder.PID, "err", aerr)
		return present, true, false, nil
	}
	if !alive {
		return present, false, false, nil
	}
	// A sighting: this daemon now knows the process is alive, so a later tick
	// never classifies it as lost to a restart.
	rt.Watched.Mark(b.Builder.PID, b.Builder.StartedAt)
	if withinGrace {
		return present, true, false, nil
	}
	if _, err := stopProcess(ctx, rt, b, "stop"); err != nil {
		return present, false, false, err
	}
	return present, false, true, nil
}

// markerClose is the marker branch of reconcileHeadless: it calls
// closeOnMarker and, when the marker is present and the gate is done, runs the
// post-close sequence (served-round close, process clear, verify consult,
// delivery, repair round). The normal read and the re-check in the
// exited branch share it, so a marker that appears inside one tick is handled
// by one implementation instead of a copy of the post-close block.
//
// closed and gating are reported so the caller returns exactly what the marker
// branch does; a marker-absent read comes back unchanged with both false.
func markerClose(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, entries []store.LogEntry, markerNote string, wantVerify bool) (store.Binding, bool, bool, error) {
	// A reader round closes on its runner's exit, not on the marker: the runner
	// may have written its output near the end, so the summary is only complete
	// once the process has gone. While the runner is alive the round stays
	// open; past the grace the runner is stopped and the round closes with the
	// summary taken early.
	//
	// The reader's marker is read exactly here, and a marker that is absent
	// this tick closes nothing: the round is still live and the next tick
	// re-reads it. Reading it here and again in closeOnMarker would let a
	// marker written between the two reads close the round the instant it
	// appeared, before the runner printed the final message the summary is
	// taken from.
	if b.Shape == store.ShapeReader {
		present, held, early, err := holdReaderOnMarker(ctx, rt, b)
		if err != nil {
			return b, false, false, err
		}
		if !present || held {
			return b, false, false, nil
		}
		if early {
			markerNote = joinNotes(markerNote, readerSummaryEarlyNote)
		}
	}
	base := b.RoundBaselineTree
	next, closed, gating, rec, err := closeOnMarker(ctx, rt, tx, b, entries, markerNote)
	if err != nil {
		return b, false, false, err
	}
	if gating || !closed {
		return next, closed, gating, nil
	}

	closedRound := b.Round
	if next.Owner != "" {
		next = closeServedRound(ctx, rt, next)
	}
	next.Builder = clearProcess(next.Builder)
	// A runner that wrote its marker and then died inside the same tick is
	// gone; end its scope now, best effort. A live runner is left alone:
	// its own supervisor reaps the scope as it exits. The scope check leads,
	// so a scopes-off close pays for no liveness read.
	if rt.Scope != nil && !roundRunnerAlive(ctx, rt, b) {
		endRoundScope(ctx, rt, b, closedRound)
	}
	next.StalledSince = time.Time{}
	// The gate result -> report queued -> verify consult started ->
	// delivery (#144), exactly as the pane path orders it: the reviewer
	// sees the gate's output, so it starts after the gate and before the
	// mastermind is told.
	if wantVerify && next.Shape != store.ShapeReader {
		gateLogPath := ""
		if rec != nil {
			gateLogPath = rec.LogPath
		}
		next, err = consult.StartVerify(ctx, consultDeps(rt), tx, next, closedRound, consult.VerifyDiffCommand(base, next.RoundClosedTree), gateLogPath)
		if err != nil {
			return next, true, false, err
		}
	}
	next, err = deliverAndSettle(ctx, rt, tx, next)
	if err != nil {
		return next, true, false, err
	}
	// The report is queued; a failing gate may now open round N+1, as a
	// fresh process, exactly as Send would (#132 part 2). The failed
	// round's own report, diff and gate=fail stand.
	//
	// A chain member's red gate is the chain's to spend: the wiring already
	// decided in this tick whether a repair can run, on this binding and this
	// gate log, so the two agree. It opens the round only when that decision
	// allowed one; when it did not, the red event went to the chain's reviewer
	// and the member must be left alone rather than halted.
	//
	// Only a done round may buy a repair: a chain member that reported halted,
	// blocked or unstructured halts the chain instead, whatever its gate says
	// (the gate runs on the done marker, not on the report).
	if rec != nil && rec.Result == "fail" && next.Regate > 0 && next.State != store.StateNeedsYou {
		repair := true
		if chainOwnsMember(tx, b.Name) {
			repair, _ = repairDecision(next, gateSignature(rt.Store.ReadFile, rec.LogPath))
			repair = repair && closedReportOutcome(tx, b.Name, closedRound) == reporttail.OutcomeDone
		}
		if repair {
			next, err = startRepairRound(ctx, rt, tx, next, *rec, closedRound)
			if err != nil {
				return next, true, false, err
			}
		}
	}
	return next, true, false, nil
}

// closedReportOutcome is the outcome the just-closed round's report entry
// recorded, or "" when the log holds no such entry. The chain wiring reads it
// for the same round the close wrote, so the repair decision and the close
// agree on what the builder actually reported.
func closedReportOutcome(tx *store.Tx, name string, round int) string {
	entries, err := tx.ReadLog(name)
	if err != nil {
		return ""
	}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Round == round && e.Direction == store.DirToMasterMind && e.Kind == store.KindReport {
			return e.Outcome
		}
	}
	return ""
}

// appendUnique returns s with v appended, unless it is already present.
func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

// interruptedNoteFormat is the relaunch prompt's note (#370, spec §4.3): a
// constant text with the interruption time interpolated. It tells the
// builder three things: the round was interrupted by a relevo daemon restart
// at T; the working tree may already hold partial edits from an earlier
// attempt at this same plan, and those edits are its own; and it should run
// git status and git diff first, keep what is correct and finish the plan.
const interruptedNoteFormat = `This round was interrupted at %s by a relevo daemon restart. The working tree may already hold partial edits from an earlier attempt at this same plan, and those edits are your own work, not someone else's. Run "git status" and "git diff" first, keep whatever is correct, and finish the plan.`

// interruptedNote renders interruptedNoteFormat for the daemon restart at t
// (#370, spec §4.3). t is rendered as UTC RFC 3339, the same shape the
// relaunch log entry uses.
func interruptedNote(t time.Time) string {
	return fmt.Sprintf(interruptedNoteFormat, t.UTC().Format(time.RFC3339))
}

// ErrStopFailed reports that relevo marked a binding done or unbound but
// could not stop its headless process (spec §4.6). The state change stands;
// the pid stays on the endpoint (done) or in the result (unbind) so the
// human can find the process.
var ErrStopFailed = errors.New("could not stop the builder process")

// stopProcess kills a headless endpoint's live process, if it has one. It
// returns the pid it addressed -- 0 when there was nothing to stop -- and
// Kill's error. A binding with no headless process, or one between rounds, is
// a no-op. Runner nil with a pid recorded is an error: relevo cannot say the
// process is stopped.
func stopProcess(ctx context.Context, rt Runtime, b store.Binding, why string) (int, error) {
	if !b.Builder.Headless() || b.Builder.PID == 0 {
		return 0, nil
	}
	if rt.Runner == nil {
		return b.Builder.PID, spawn.ErrRunnerUnavailable
	}
	if err := rt.Runner.Kill(ctx, handleOf(b.Builder), rt.Store.StreamPath(b.Name, b.Round)); err != nil {
		return b.Builder.PID, err
	}
	return b.Builder.PID, nil
}

// statusTailLines is how much of the log `relevo status` shows under a
// headless builder line (spec §4.8).
const statusTailLines = 3

// headlessStatus is what `relevo status` says about a headless endpoint: the
// status word and the process details. Idle between rounds; otherwise a live
// Alive check, then, for an exited process, the trailer's code. No Runner
// means relevo cannot say. A live process whose stream has gone quiet
// (binding.StalledSince, #252) reads "stalled <age>" in place of "working".
func headlessStatus(ctx context.Context, rt Runtime, b store.Binding) (string, *view.HeadlessInfo) {
	e := b.Builder
	info := &view.HeadlessInfo{PID: e.PID, LogPath: e.LogPath}
	if e.StartedAt != 0 {
		info.StartedAt = time.Unix(e.StartedAt, 0)
	}
	if e.LogPath != "" {
		if tail := builderTail(rt, b, statusTailLines); tail != "" {
			info.Tail = strings.Split(tail, "\n")
		}
	}
	if e.PID == 0 {
		return "idle", info
	}
	if rt.Runner == nil {
		return "unknown", info
	}
	alive, err := rt.Runner.Alive(ctx, handleOf(e))
	if err != nil {
		return "unknown", info
	}
	if alive {
		// #252's label is now one of #135's progress labels, so a live
		// headless builder can also read "exploring <age>".
		if working, _ := labelsOf(b, rt.Now()); working != "" {
			return working, info
		}
		return "working", info
	}
	if code, ok := rt.Runner.ExitCode(ctx, handleOf(e), rt.Store.StreamPath(b.Name, b.Round)); ok {
		info.ExitCode = strconv.Itoa(code)
		return "exited " + info.ExitCode, info
	}
	info.ExitCode = "unknown"
	return "exited", info
}
