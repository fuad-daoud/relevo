package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// mastermindEffective is the answer that governs the calling session: its own
// answer when it has one, otherwise cwd's repository answer. A nil handle (the
// timed open did not finish) reads both as unset, the safe answer -- the ask,
// never a silent registration. own is reported separately because the notice
// prints nothing outside a git repo that has no session answer.
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

// mastermindRegisterSession registers the calling session through the registry,
// reusing a database row's id for (kind, session) when relevo.db already has
// one.
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
// next prompt's notice compares against what the session was already told. A
// nil handle writes nothing: the notice then treats its first read as the
// baseline.
func mastermindToldBaseline(d *db.DB, rt relevo.Runtime, kind, session, token string) {
	if d == nil {
		return
	}
	_ = d.SetSessionTold(kind, session, token, rt.Now())
}

// mastermindInitHook runs `relevo mastermind init --hook claude`.
//
// It never returns an error and never exits non-zero: the hook runs at the
// start of a Claude Code session, and blocking that session because relevo could
// not read its own state would be a far worse failure than an unregistered
// mastermind. Every failure prints HookNote on stdout -- where the model reads
// its context -- and the error on stderr for the human.
func mastermindInitHook(nameFlag string) error {
	in, err := mastermind.ParseHookInput(os.Stdin)
	// A round's harness session is a runner: it must not be asked, briefed or
	// registered, and must not get the failure note either, so it answers the
	// empty object for any payload -- malformed included -- before the
	// parse-error branch and before any state is read.
	if mastermind.IsRunner(os.Getenv) {
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

	// One timed open serves the prior id, the repository answer and the
	// session's own answer, so the hook never waits on sqlite. A read that did
	// not arrive reads as unset -- the ask, never a silent registration.
	d, closeDB := mastermindOpenTimed(rt.Store.DBPath())
	defer closeDB()

	ref, own, effective := mastermindEffective(d, rt, in.CWD, "claude", in.SessionID)

	// A cwd the hook cannot resolve to a repository and a session with no
	// answer of its own leave the session alone: there is nothing to remember,
	// so there is nothing to ask.
	if !mastermindRepoKnown(ref) && own == mastermind.ConsentUnset {
		_, _ = os.Stdout.Write(mastermind.HookConsent(""))
		return nil
	}

	// A session `no` hides any record: no briefing and no registration. Its
	// baseline is the silent token, so the next prompt's notice stays quiet.
	if effective == mastermind.ConsentNo {
		_, _ = os.Stdout.Write(mastermind.HookConsent(""))
		mastermindToldBaseline(d, rt, "claude", in.SessionID, mastermind.StatusNone)
		return nil
	}

	// An unset answer injects the ask-note and registers nothing.
	if effective != mastermind.ConsentYes {
		_, _ = os.Stdout.Write(mastermind.HookConsent(mastermind.ConsentText(effective, nil)))
		mastermindToldBaseline(d, rt, "claude", in.SessionID, mastermind.StatusAsk)
		return nil
	}

	// The hook's parent is the Claude Code process, which is the same pid
	// CLAUDE_PID names in a Bash tool and `relevo mcp`'s parent.
	host := os.Getppid()

	rec, err := mastermindRegisterSession(d, rt, mastermind.InitInput{
		Kind:           "claude",
		SessionID:      in.SessionID,
		TranscriptPath: in.TranscriptPath,
		CWD:            in.CWD,
		Name:           nameFlag,
		Agent:          os.Getenv("CLAUDE_CODE_AGENT"),
		HostPID:        host,
		HostStartedAt:  mastermindHostStart(host),
		Now:            rt.Now(),
	})
	if err != nil {
		return mastermindInitHookFailure(err)
	}

	// The export line is how every later Bash call in the session learns its
	// mastermind. Appending, never truncating: Claude Code reads the whole
	// file, and it may already carry lines from other tools.
	//
	// With no $CLAUDE_ENV_FILE there is nowhere to export to, so the answer
	// says so in additionalContext instead of pretending the export happened:
	// relevo then resolves the session through the host process.
	out := mastermind.HookOutput(rec)
	if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" {
		if err := appendEnvLine(envFile, mastermind.EnvLine(rec.ID)); err != nil {
			return mastermindInitHookFailure(err)
		}
	} else {
		out = mastermind.HookOutputNoEnv(rec)
	}

	_, _ = os.Stdout.Write(out)
	mastermindToldBaseline(d, rt, "claude", in.SessionID, mastermind.StatusToken(effective, &rec))
	return nil
}

// mastermindInitHookFailure reports a hook failure and swallows it: the note goes
// to stdout as the hook's answer, the error to stderr for the human, and the
// command still succeeds.
func mastermindInitHookFailure(err error) error {
	fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
	_, _ = os.Stdout.Write(mastermind.HookNote(fmt.Sprintf("relevo mastermind init failed: %v", err)))
	return nil
}

// cmdMasterMindNotice runs `relevo mastermind notice --hook claude`, the
// UserPromptSubmit hook that tells a session its status changed since the
// baseline its SessionStart hook recorded.
func cmdMasterMindNotice(args []string) error {
	fs := flag.NewFlagSet("notice", flag.ContinueOnError)
	hook := fs.String("hook", "", "read a Claude Code UserPromptSubmit payload from stdin (only \"claude\")")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *hook != "claude" {
		fmt.Fprintf(os.Stderr, "relevo: relevo mastermind notice --hook supports only \"claude\", got %q\n", *hook)
		return exitCodeErr{code: 2}
	}
	return mastermindNoticeHook()
}

// mastermindNoticeHook is the UserPromptSubmit hook's work. Like the
// SessionStart hook it never returns an error and never exits non-zero: a
// notice must never block a prompt. Every failure prints the empty object on
// stdout and the error on stderr.
func mastermindNoticeHook() error {
	in, err := mastermind.ParseHookInput(os.Stdin)
	// A round's harness session is a runner and must stay silent: no notice,
	// no failure note, and no told baseline that would let a later prompt
	// speak. The empty object for any payload, before the parse-error branch.
	if mastermind.IsRunner(os.Getenv) {
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

	ref, own, effective := mastermindEffective(d, rt, in.CWD, "claude", in.SessionID)

	// Nothing is printed outside a git repo that has no session answer: there
	// is nothing to remember and nothing to say.
	if !mastermindRepoKnown(ref) && own == mastermind.ConsentUnset {
		_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
		return nil
	}

	// The current token. A `no` hides any record; otherwise the session's
	// record names it, and a granted session with no record gets one now, so a
	// later rename or revocation has something to compare against.
	token := mastermind.StatusNone
	var rec *mastermind.Record
	if effective != mastermind.ConsentNo {
		rec = mastermindExistingRecord(rt, "claude", in.SessionID)
		if rec == nil && effective == mastermind.ConsentYes {
			host := os.Getppid()
			r, err := mastermindRegisterSession(d, rt, mastermind.InitInput{
				Kind:           "claude",
				SessionID:      in.SessionID,
				TranscriptPath: in.TranscriptPath,
				CWD:            in.CWD,
				Agent:          os.Getenv("CLAUDE_CODE_AGENT"),
				HostPID:        host,
				HostStartedAt:  mastermindHostStart(host),
				Now:            rt.Now(),
			})
			if err != nil {
				return mastermindNoticeFailure(err)
			}
			rec = &r
		}
		token = mastermind.StatusToken(effective, rec)
	}

	prev, ok, err := d.SessionTold("claude", in.SessionID)
	if err != nil {
		return mastermindNoticeFailure(err)
	}

	// No baseline yet (a session from before this round, or a baseline write
	// that failed): write it and stay silent, so the session is not told a
	// status it already holds.
	if !ok {
		mastermindToldBaseline(d, rt, "claude", in.SessionID, token)
		_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
		return nil
	}

	text := mastermindNoticeText(prev, token, rec)
	if text == "" {
		_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
		return nil
	}

	_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, text))
	mastermindToldBaseline(d, rt, "claude", in.SessionID, token)
	return nil
}

// mastermindNoticeText is StatusNotice with a grant's wording replaced by the
// no-env version when this hook cannot rely on $CLAUDE_ENV_FILE: UserPromptSubmit
// is not guaranteed the variable SessionStart writes. A rename or a new
// mastermind keeps StatusNotice's own line.
func mastermindNoticeText(prev, next string, rec *mastermind.Record) string {
	text := mastermind.StatusNotice(prev, next, rec)
	if text == "" || rec == nil || os.Getenv("CLAUDE_ENV_FILE") != "" {
		return text
	}
	if _, _, prevOK := mastermind.StatusMasterMindParts(prev); prevOK {
		return text
	}
	if _, _, nextOK := mastermind.StatusMasterMindParts(next); nextOK {
		return mastermind.NoEnvText(*rec)
	}
	return text
}

// mastermindNoticeFailure reports a notice failure and swallows it: the empty
// object goes to stdout as the hook's answer, the error to stderr for the
// human, and the command still succeeds.
func mastermindNoticeFailure(err error) error {
	fmt.Fprintf(os.Stderr, "relevo: %v\n", err)
	_, _ = os.Stdout.Write(mastermind.HookConsentFor(mastermind.HookEventUserPromptSubmit, ""))
	return nil
}

// mastermindExistingRecord reads the session's record without creating one.
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
