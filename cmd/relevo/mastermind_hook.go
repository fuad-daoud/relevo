package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// mastermindEffective is the answer that governs the calling session: its own
// answer when it has one, otherwise cwd's repository answer. A nil handle reads
// both as unset, the safe answer; own is reported separately because the notice
// prints nothing outside a git repo with no session answer.
func mastermindEffective(d *db.DB, rt relevo.Runtime, cwd, kind, session string) (ref db.Repo, own, effective mastermind.Consent) {
	ref = mastermindRepoOf(context.Background(), rt, cwd)

	repo := mastermind.ConsentUnset
	own = mastermind.ConsentUnset
	if d != nil {
		if mastermindRepoKnown(ref) {
			if c, err := d.RepoConsent(ref); err == nil {
				repo = c
			}
		}
		if c, err := d.SessionConsent(kind, session); err == nil {
			own = c
		}
	}
	return ref, own, mastermind.EffectiveConsent(own, repo)
}

// mastermindToldBaseline records the status token a hook just injected, so the
// next prompt's notice compares against what the session was already told. A nil
// handle writes nothing: the notice then treats its first read as the baseline.
func mastermindToldBaseline(d *db.DB, rt relevo.Runtime, kind, session, token string) {
	if d == nil {
		return
	}
	_ = d.SetSessionTold(kind, session, token, rt.Now())
}

// readHookInput reads a hook's payload from stdin. A muse hook whose stdin
// arrived empty rebuilds it from muse's environment instead, since the user's
// login shell may have read stdin before the hook command ran.
func readHookInput(hookKind, event string) (mastermind.HookInput, error) {
	in, err := mastermind.ParseHookInput(os.Stdin)
	if err == nil || hookKind != "muse" || !errors.Is(err, io.EOF) {
		return in, err
	}
	cwd, _ := os.Getwd()
	if rebuilt, ok := mastermind.MuseHookInput(event, os.Getenv, cwd); ok {
		return rebuilt, nil
	}
	return in, err
}

// mastermindInitTimeout bounds the whole SessionStart hook, from the parsed
// input to the stdout write: the hook blocks the session's start, so a missed
// budget answers empty, writes no baseline, and leaves the session to the next
// prompt's notice. It is wider than the notice's budget because this hook is
// the one that registers the session and exports its id: a miss leaves the
// session without a MasterMind until it is started again. Five seconds still
// keeps the start far below Claude's 30 s hook cutoff.
var mastermindInitTimeout = 5 * time.Second

// hookWorkers counts the hook workers still running, the ones a deadline
// abandoned included. A process exits past them, but a test runs the next
// command in the same process, so it joins them first: an abandoned worker
// otherwise reads the globals that command's run rewrites.
var hookWorkers sync.WaitGroup

// mastermindInitHook runs `relevo mastermind init --hook claude|muse`. It never
// returns an error and never exits non-zero: blocking a session because relevo
// could not read its own state is worse than an unregistered mastermind.
func mastermindInitHook(nameFlag, hookKind string) error {
	in, err := readHookInput(hookKind, mastermind.HookEventSessionStart)
	// A round's harness session is a runner: it answers the empty object for any
	// payload, before the parse-error branch and before any state is read, so it
	// gets no failure note and no told baseline.
	if isRunnerHost() {
		_, _ = os.Stdout.Write(mastermind.HookConsent(""))
		return nil
	}
	if err != nil {
		return mastermindInitHookFailure(err)
	}

	// The whole run answers under one deadline, in the same goroutine+select
	// shape as the notice hook: a stuck database gets the empty answer, never
	// the session's wait. The worker returns its stdout instead of writing it,
	// so a late answer cannot land in the session; the channel holds one, so
	// the loser never blocks sending it.
	done := make(chan mastermindNoticeResult, 1)
	hookWorkers.Add(1)
	go func() {
		defer hookWorkers.Done()
		stdout, err := mastermindInitRun(nameFlag, hookKind, in)
		done <- mastermindNoticeResult{stdout: stdout, err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return mastermindInitHookFailure(r.err)
		}
		_, _ = os.Stdout.Write(r.stdout)
		return nil
	case <-time.After(mastermindInitTimeout):
		// No baseline is written here: the session was told nothing, so the
		// next prompt's notice establishes the baseline from the live state
		// instead of reading this silence as told.
		_, _ = os.Stdout.Write(mastermind.HookConsent(""))
		return nil
	}
}

// mastermindInitRun does the init hook's work and returns its stdout. It runs
// on a goroutine under the hook's deadline and writes no output itself.
func mastermindInitRun(nameFlag, hookKind string, in mastermind.HookInput) ([]byte, error) {
	empty := mastermind.HookConsent("")

	rt, err := newRuntime()
	if err != nil {
		return nil, err
	}

	// One bounded handle serves every database touch below. An open error
	// fails the run, and the wrapper answers the failure note: there is no
	// nil handle for the reads to dereference.
	d, closeDB, err := mastermindOpenHook(rt.Store.DBPath())
	if err != nil {
		return nil, err
	}
	defer closeDB()

	ref, own, effective := mastermindEffective(d, rt, in.CWD, hookKind, in.SessionID)

	// With no repository and no session answer of its own, the session is left
	// alone: there is nothing to remember, so nothing to ask.
	if !mastermindRepoKnown(ref) && own == mastermind.ConsentUnset {
		return empty, nil
	}

	// A session `no` hides any record: no briefing and no registration. Its
	// baseline is the silent token, so the next prompt's notice stays quiet.
	if effective == mastermind.ConsentNo {
		mastermindToldBaseline(d, rt, hookKind, in.SessionID, mastermind.StatusNone)
		return empty, nil
	}

	// An unset answer injects the ask-note and registers nothing.
	if effective != mastermind.ConsentYes {
		mastermindToldBaseline(d, rt, hookKind, in.SessionID, mastermind.StatusAsk)
		return mastermind.HookConsent(mastermind.ConsentText(effective, nil)), nil
	}

	// The hook's parent is the harness process itself, so the session's host.
	host := os.Getppid()

	reg := mastermindHookRegistry(d, rt)
	initIn := mastermind.InitInput{
		Kind:          hookKind,
		SessionID:     in.SessionID,
		CWD:           in.CWD,
		Name:          nameFlag,
		Agent:         os.Getenv("CLAUDE_CODE_AGENT"),
		HostPID:       host,
		HostStartedAt: mastermindHostStart(host),
		Now:           rt.Now(),
	}
	initIn.PriorID = mastermindPriorIDFunc(d)
	rec, _, err := mastermind.Init(reg, initIn)
	if err != nil {
		return nil, err
	}

	// The export line is how every later Bash call in the session learns its
	// mastermind. Appending, never truncating: Claude Code reads the whole
	// file, which may already carry other tools' lines. Muse sets no
	// $CLAUDE_ENV_FILE, so the muse answer always carries the no-env wording
	// and never touches the file, even when the variable is set. With no
	// $CLAUDE_ENV_FILE there is nowhere to export to, so the answer says so
	// instead of pretending the export happened.
	var out []byte
	if hookKind == "muse" {
		out = mastermind.HookOutputMuse(rec)
	} else {
		out = mastermind.HookOutput(rec)
		if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" {
			if err := appendEnvLine(envFile, mastermind.EnvLine(rec.ID)); err != nil {
				return nil, err
			}
		} else {
			out = mastermind.HookOutputNoEnv(rec)
		}
	}

	mastermindToldBaseline(d, rt, hookKind, in.SessionID, mastermind.StatusToken(effective, &rec))
	return out, nil
}

// mastermindInitHookFailure reports a hook failure and swallows it: the note goes
// to stdout as the hook's answer, the error to stderr, and the command still
// succeeds.
func mastermindInitHookFailure(err error) error {
	fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
	_, _ = os.Stdout.Write(mastermind.HookNote(fmt.Sprintf("relevo mastermind init failed: %v", err)))
	return nil
}

type mastermindNoticeFlagValues struct {
	hook *string
}

func mastermindNoticeFlagSet(fs *flag.FlagSet) *mastermindNoticeFlagValues {
	v := &mastermindNoticeFlagValues{}
	v.hook = fs.String("hook", "", "read a Claude-shaped UserPromptSubmit payload from stdin (only \"claude\" or \"muse\")")
	return v
}

// cmdMasterMindNotice runs `relevo mastermind notice --hook claude|muse`, the
// UserPromptSubmit hook that tells a session its status changed.
func cmdMasterMindNotice(args []string) error {
	fs := flag.NewFlagSet("notice", flag.ContinueOnError)
	v := mastermindNoticeFlagSet(fs)
	hook := v.hook
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *hook != "claude" && *hook != "muse" {
		fmt.Fprintf(os.Stderr, "relevo: relevo mastermind notice --hook supports only \"claude\" and \"muse\", got %q\n", *hook)
		return exitCodeErr{code: 2}
	}
	return mastermindNoticeHook(*hook)
}

// mastermindNoticeHook is the UserPromptSubmit hook's work. Like the SessionStart
// hook it never returns an error: a notice must never block a prompt.
func mastermindNoticeHook(hookKind string) error {
	in, err := readHookInput(hookKind, mastermind.HookEventUserPromptSubmit)
	// A round's harness session is a runner and must stay silent: the empty
	// object for any payload, before the parse-error branch, so it gets no
	// failure note and no told baseline.
	if isRunnerHost() {
		_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
		return nil
	}
	if err != nil {
		return mastermindNoticeFailure(err)
	}

	// The whole run answers under one deadline, in the same goroutine+select
	// shape as the timed open: a stuck database gets the empty answer, never
	// the prompt's wait. The worker returns its stdout instead of writing it,
	// so a late answer cannot land in the next prompt; the channel holds one,
	// so the loser never blocks sending it.
	done := make(chan mastermindNoticeResult, 1)
	hookWorkers.Add(1)
	go func() {
		defer hookWorkers.Done()
		stdout, err := mastermindNoticeRun(hookKind, in)
		done <- mastermindNoticeResult{stdout: stdout, err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return mastermindNoticeFailure(r.err)
		}
		_, _ = os.Stdout.Write(r.stdout)
		return nil
	case <-time.After(mastermindNoticeTimeout):
		// No baseline is written here, so the next prompt retries the notice
		// instead of reading this silence as told.
		_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
		return nil
	}
}

// mastermindNoticeResult is one worker round's answer: the stdout bytes, or the
// failure the empty answer carries on stdout with its note on stderr.
type mastermindNoticeResult struct {
	stdout []byte
	err    error
}

// mastermindHookRegistry binds the notice hook's registry to its own bounded
// handle. The store would open a second handle with the default waits, which
// is the unbounded tail the deadline cuts: reads waiting out busy_timeout per
// statement and a registration retrying a busy begin for thirty seconds.
func mastermindHookRegistry(d *db.DB, rt relevo.Runtime) *mastermind.DBRegistry {
	return &mastermind.DBRegistry{KV: db.TxKV{DB: d.LocalOrSelf()}, Now: rt.Now}
}

// mastermindOpenHook opens the machine database for the notice hook with short
// busy waits, so work the hook's deadline abandons still fails fast on a lock
// instead of lingering the defaults. The owner route takes no options, so a
// dialled handle keeps them and the deadline is its only bound. An error
// answers empty upstream, never a nil handle.
func mastermindOpenHook(path string) (*db.DB, func(), error) {
	if d, ok, err := openDBRoute(path); ok {
		if err != nil {
			return nil, func() {}, err
		}
		return d, func() { _ = d.Close() }, nil
	}
	d, err := openDBDirectWith(path, db.Options{BusyTimeout: mastermindHookBusyTimeout, BeginRetry: mastermindHookBeginRetry})
	if err != nil {
		return nil, func() {}, err
	}
	return d, func() { _ = d.Close() }, nil
}

// mastermindNoticeRun does the notice hook's work and returns its stdout. It
// runs on a goroutine under the hook's deadline and writes no output itself.
func mastermindNoticeRun(hookKind string, in mastermind.HookInput) ([]byte, error) {
	empty := mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, "")

	rt, err := newRuntime()
	if err != nil {
		return nil, err
	}

	// One bounded handle serves every database touch below, and an open error
	// answers empty: there is no nil handle for the reads to dereference.
	d, closeDB, err := mastermindOpenHook(rt.Store.DBPath())
	if err != nil {
		return nil, err
	}
	defer closeDB()

	ref, own, effective := mastermindEffective(d, rt, in.CWD, hookKind, in.SessionID)

	// Outside a git repo with no session answer, nothing is printed.
	if !mastermindRepoKnown(ref) && own == mastermind.ConsentUnset {
		return empty, nil
	}

	// A `no` hides any record; otherwise a granted session with no record gets
	// one now.
	token := mastermind.StatusNone
	var rec *mastermind.Record
	if effective != mastermind.ConsentNo {
		reg := mastermindHookRegistry(d, rt)
		if r, rerr := reg.BySession(hookKind, in.SessionID); rerr == nil {
			rec = &r
		}
		if rec == nil && effective == mastermind.ConsentYes {
			host := os.Getppid()
			initIn := mastermind.InitInput{
				Kind:          hookKind,
				SessionID:     in.SessionID,
				CWD:           in.CWD,
				Agent:         os.Getenv("CLAUDE_CODE_AGENT"),
				HostPID:       host,
				HostStartedAt: mastermindHostStart(host),
				Now:           rt.Now(),
			}
			initIn.PriorID = mastermindPriorIDFunc(d)
			r, _, err := mastermind.Init(reg, initIn)
			if err != nil {
				return nil, err
			}
			rec = &r
		}
		token = mastermind.StatusToken(effective, rec)
	}

	prev, ok, err := d.SessionTold(hookKind, in.SessionID)
	if err != nil {
		return nil, err
	}

	// No baseline yet (a session from before this round, or a baseline write
	// that failed): write it and stay silent, so the session is not told a
	// status it already holds.
	if !ok {
		mastermindToldBaseline(d, rt, hookKind, in.SessionID, token)
		return empty, nil
	}

	text := mastermindNoticeText(hookKind, prev, token, rec)
	if text == "" {
		return empty, nil
	}

	mastermindToldBaseline(d, rt, hookKind, in.SessionID, token)
	return mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, text), nil
}

// mastermindNoticeText is StatusNotice with a grant's wording replaced by the
// no-env version when this hook cannot rely on $CLAUDE_ENV_FILE: UserPromptSubmit
// is not guaranteed the variable that SessionStart writes, and muse sets none.
func mastermindNoticeText(hookKind, prev, next string, rec *mastermind.Record) string {
	text := mastermind.StatusNotice(prev, next, rec)
	if text == "" || rec == nil {
		return text
	}
	// A muse grant always carries the muse no-env wording; the claude branch
	// reaches its own wording only when the variable is unset.
	if hookKind == "claude" && os.Getenv("CLAUDE_ENV_FILE") != "" {
		return text
	}
	if _, _, prevOK := mastermind.StatusMasterMindParts(prev); prevOK {
		return text
	}
	if _, _, nextOK := mastermind.StatusMasterMindParts(next); nextOK {
		if hookKind == "muse" {
			return mastermind.NoEnvTextMuse(*rec)
		}
		return mastermind.NoEnvText(*rec)
	}
	return text
}

// mastermindNoticeFailure reports a notice failure and swallows it: the empty
// object goes to stdout, the error to stderr, and the command still succeeds.
func mastermindNoticeFailure(err error) error {
	fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
	_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
	return nil
}
