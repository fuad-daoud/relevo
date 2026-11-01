package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

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

func mastermindRegisterSession(d *db.DB, rt relevo.Runtime, in mastermind.InitInput) (mastermind.Record, error) {
	reg, err := mastermindRegistry(rt)
	if err != nil {
		return mastermind.Record{}, err
	}
	in.PriorID = mastermindPriorIDFunc(d)
	rec, _, err := mastermind.Init(reg, in)
	return rec, err
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

	rt, err := newRuntime()
	if err != nil {
		return mastermindInitHookFailure(err)
	}

	// One timed open serves the prior id and both answers, so the hook never
	// waits on sqlite.
	d, closeDB := mastermindOpenTimed(rt.Store.DBPath())
	defer closeDB()

	ref, own, effective := mastermindEffective(d, rt, in.CWD, hookKind, in.SessionID)

	// With no repository and no session answer of its own, the session is left
	// alone: there is nothing to remember, so nothing to ask.
	if !mastermindRepoKnown(ref) && own == mastermind.ConsentUnset {
		_, _ = os.Stdout.Write(mastermind.HookConsent(""))
		return nil
	}

	// A session `no` hides any record: no briefing and no registration. Its
	// baseline is the silent token, so the next prompt's notice stays quiet.
	if effective == mastermind.ConsentNo {
		_, _ = os.Stdout.Write(mastermind.HookConsent(""))
		mastermindToldBaseline(d, rt, hookKind, in.SessionID, mastermind.StatusNone)
		return nil
	}

	// An unset answer injects the ask-note and registers nothing.
	if effective != mastermind.ConsentYes {
		_, _ = os.Stdout.Write(mastermind.HookConsent(mastermind.ConsentText(effective, nil)))
		mastermindToldBaseline(d, rt, hookKind, in.SessionID, mastermind.StatusAsk)
		return nil
	}

	// The hook's parent is the harness process itself, so the session's host.
	host := os.Getppid()

	rec, err := mastermindRegisterSession(d, rt, mastermind.InitInput{
		Kind:          hookKind,
		SessionID:     in.SessionID,
		CWD:           in.CWD,
		Name:          nameFlag,
		Agent:         os.Getenv("CLAUDE_CODE_AGENT"),
		HostPID:       host,
		HostStartedAt: mastermindHostStart(host),
		Now:           rt.Now(),
	})
	if err != nil {
		return mastermindInitHookFailure(err)
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
				return mastermindInitHookFailure(err)
			}
		} else {
			out = mastermind.HookOutputNoEnv(rec)
		}
	}

	_, _ = os.Stdout.Write(out)
	mastermindToldBaseline(d, rt, hookKind, in.SessionID, mastermind.StatusToken(effective, &rec))
	return nil
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

	rt, err := newRuntime()
	if err != nil {
		return mastermindNoticeFailure(err)
	}

	d, closeDB := mastermindOpenTimed(rt.Store.DBPath())
	defer closeDB()

	ref, own, effective := mastermindEffective(d, rt, in.CWD, hookKind, in.SessionID)

	// Outside a git repo with no session answer, nothing is printed.
	if !mastermindRepoKnown(ref) && own == mastermind.ConsentUnset {
		_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
		return nil
	}

	// A `no` hides any record; otherwise a granted session with no record gets
	// one now.
	token := mastermind.StatusNone
	var rec *mastermind.Record
	if effective != mastermind.ConsentNo {
		rec = mastermindExistingRecord(rt, hookKind, in.SessionID)
		if rec == nil && effective == mastermind.ConsentYes {
			host := os.Getppid()
			r, err := mastermindRegisterSession(d, rt, mastermind.InitInput{
				Kind:          hookKind,
				SessionID:     in.SessionID,
				CWD:           in.CWD,
				Agent:         os.Getenv("CLAUDE_CODE_AGENT"),
				HostPID:       host,
				HostStartedAt: mastermindHostStart(host),
				Now:           rt.Now(),
			})
			if err != nil {
				return mastermindNoticeFailure(err)
			}
			rec = &r
		}
		token = mastermind.StatusToken(effective, rec)
	}

	prev, ok, err := d.SessionTold(hookKind, in.SessionID)
	if err != nil {
		return mastermindNoticeFailure(err)
	}

	// No baseline yet (a session from before this round, or a baseline write
	// that failed): write it and stay silent, so the session is not told a
	// status it already holds.
	if !ok {
		mastermindToldBaseline(d, rt, hookKind, in.SessionID, token)
		_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
		return nil
	}

	text := mastermindNoticeText(hookKind, prev, token, rec)
	if text == "" {
		_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
		return nil
	}

	_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, text))
	mastermindToldBaseline(d, rt, hookKind, in.SessionID, token)
	return nil
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

func mastermindExistingRecord(rt relevo.Runtime, kind, session string) *mastermind.Record {
	reg, err := mastermindRegistry(rt)
	if err != nil {
		return nil
	}
	rec, err := reg.BySession(kind, session)
	if err != nil {
		return nil
	}
	return &rec
}
