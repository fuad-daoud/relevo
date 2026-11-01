package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// mastermindOpenTimed opens relevo.db on a goroutine and gives up after
// mastermindPriorIDTimeout, so a hook never waits on sqlite. The caller must
// call the returned close function; a nil *db.DB means the open did not finish
// in time, and the safe answers (no prior id, unset consent) follow from it.
func mastermindOpenTimed(path string) (*db.DB, func()) {
	opened := make(chan *db.DB, 1)
	go func() {
		d, err := openDB(path)
		if err != nil {
			opened <- nil
			return
		}
		opened <- d
	}()

	select {
	case d := <-opened:
		if d == nil {
			return nil, func() {}
		}
		return d, func() { _ = d.Close() }
	case <-time.After(mastermindPriorIDTimeout):
		// The open is still running; close whatever it eventually produces so
		// a slow database does not leak a connection for the process's life.
		go func() {
			if d := <-opened; d != nil {
				_ = d.Close()
			}
		}()
		return nil, func() {}
	}
}

// mastermindPriorIDFunc reuses a database row's id for (kind, session); a nil
// handle means "no prior id".
func mastermindPriorIDFunc(d *db.DB) func(kind, session string) (string, bool) {
	if d == nil {
		return nil
	}
	return func(kind, session string) (string, bool) {
		p, ok, err := d.MasterMindBySession(kind, session)
		if err != nil || !ok {
			return "", false
		}
		return p.ID, true
	}
}

func mastermindRepoKnown(ref db.Repo) bool {
	return ref.OriginURL != nil || ref.CommonDir != nil
}

// mastermindCaller fills the calling session's identity: an explicit
// --kind/--session pair wins, else detection. An opencode session carries no id
// in the environment, so detection fills it from opencode's own database.
func mastermindCaller(rt relevo.Runtime, cwd, kindFlag, sessionFlag string) (mastermind.InitInput, error) {
	if kindFlag != "" || sessionFlag != "" {
		if kindFlag == "" || sessionFlag == "" {
			return mastermind.InitInput{}, fail(codeUsage, "relevo mastermind: needs both --kind and --session, or neither")
		}
		return mastermind.InitInput{Kind: kindFlag, SessionID: sessionFlag}, nil
	}

	ident, ok := mastermind.Detect(os.Getenv, os.Getppid())
	if !ok {
		return mastermind.InitInput{}, fail(codeRefused, "not in a detectable mastermind session: run it from the MasterMind's own session (Claude Code or muse with the relevo plugin, or opencode), or pass --kind and --session")
	}
	in := mastermind.InitInput{
		Kind:      ident.Kind,
		SessionID: ident.SessionID,
		Agent:     os.Getenv("CLAUDE_CODE_AGENT"),
		HostPID:   ident.HostPID,
	}
	// A muse host is the nearest muse ancestor, not the direct parent, so
	// the record points at the muse process before it is registered.
	if in.Kind == "muse" && rt.MuseHost != nil {
		if pid, ok := rt.MuseHost(os.Getppid()); ok {
			in.HostPID = pid
		}
	}
	if in.HostPID > 0 {
		in.HostStartedAt = mastermindHostStart(in.HostPID)
	}
	if in.SessionID == "" && in.Kind == "opencode" && rt.OpencodeSession != nil {
		var now time.Time
		if rt.Now != nil {
			now = rt.Now()
		}
		id, err := rt.OpencodeSession(cwd, now)
		if err != nil {
			return mastermind.InitInput{}, fmt.Errorf("resolve the opencode session: %w", err)
		}
		in.SessionID = id
	}
	return in, nil
}

type mastermindEnableFlagValues struct {
	repo    *bool
	kind    *string
	session *string
	asJSON  *bool
}

func mastermindEnableFlagSet(fs *flag.FlagSet) *mastermindEnableFlagValues {
	v := &mastermindEnableFlagValues{}
	v.repo = fs.Bool("repo", false, "remember yes for this repository, not just this session")
	v.kind = fs.String("kind", "", "harness kind for an explicit registration (with --session)")
	v.session = fs.String("session", "", "harness session id for an explicit registration (with --kind)")
	v.asJSON = fs.Bool("json", false, "print the document the consent produced")
	return v
}

// cmdMasterMindEnable answers yes for this session, and with --repo for the
// repository, then registers the calling session.
func cmdMasterMindEnable(args []string) error {
	return outcomeError(cmdMasterMindEnableRun(args))
}

func cmdMasterMindEnableRun(args []string) error {
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	v := mastermindEnableFlagSet(fs)
	repo, kind, session, asJSON := v.repo, v.kind, v.session, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	caller, err := prepareMasterMindEnable(rt, cwd, kind, session, *repo)
	if err != nil {
		return err
	}
	d, closeDB := mastermindOpenTimed(rt.Store.DBPath())
	defer closeDB()
	caller.PriorID = mastermindPriorIDFunc(d)

	reg, err := mastermindRegistry(rt)
	if err != nil {
		return err
	}
	rec, res, err := mastermind.Init(reg, caller)
	if err != nil {
		return err
	}

	// The env-file append stays in both modes; only the stdout export line moves
	// off stdout under --json.
	exported := false
	if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" {
		if err := appendEnvLine(envFile, mastermind.EnvLine(rec.ID)); err != nil {
			return err
		}
		exported = true
	}
	docRepo := ""
	if *repo {
		docRepo = mastermindRepoLabel(rt, cwd)
	}

	if *asJSON {
		if !exported {
			fmt.Fprintf(os.Stderr, "export RELEVO_MASTERMIND=%s\n", rec.ID)
		}
		if *repo {
			fmt.Fprintln(os.Stderr, "relevo will register this repository's sessions (relevo mastermind disable --repo to stop)")
		}
		return printDoc(mastermindDocOf(rec.ID, rec.Name, string(res), docRepo))
	}

	fmt.Printf("MasterMind %s (%s) %s\n", rec.Name, rec.ID, res)
	if !exported {
		fmt.Printf("export RELEVO_MASTERMIND=%s\n", rec.ID)
	}
	if *repo {
		fmt.Println("relevo will register this repository's sessions (relevo mastermind disable --repo to stop)")
	}
	return nil
}

type mastermindDisableFlagValues struct {
	repo    *bool
	kind    *string
	session *string
	asJSON  *bool
}

func mastermindDisableFlagSet(fs *flag.FlagSet) *mastermindDisableFlagValues {
	v := &mastermindDisableFlagValues{}
	v.repo = fs.Bool("repo", false, "remember no for this repository; without it, answer no for this session")
	v.kind = fs.String("kind", "", "harness kind for an explicit session (with --session)")
	v.session = fs.String("session", "", "harness session id for an explicit session (with --kind)")
	v.asJSON = fs.Bool("json", false, "print the document the consent produced")
	return v
}

// prepareMasterMindEnable records the consent answers and resolves the calling
// session's identity: the input mastermind.Init registers.
func prepareMasterMindEnable(rt relevo.Runtime, cwd string, kind, session *string, repo bool) (mastermind.InitInput, error) {
	var caller mastermind.InitInput
	if repo {
		if err := mastermindWriteConsent(rt, cwd, db.ConsentYes); err != nil {
			return caller, err
		}
	}
	c, err := mastermindCaller(rt, cwd, *kind, *session)
	if err != nil {
		return caller, err
	}
	c.CWD = cwd
	c.Now = rt.Now()

	// With --repo the session's earlier "this session only" is cleared, so the
	// repository's answer governs the session.
	answer := db.ConsentYes
	if repo {
		answer = db.ConsentUnset
	}
	if err := mastermindWriteSessionConsent(rt, c.Kind, c.SessionID, answer); err != nil {
		return caller, err
	}
	return c, nil
}

// cmdMasterMindDisable answers no for the repository, or for this session, and
// forgets an existing record.
func cmdMasterMindDisable(args []string) error {
	return outcomeError(cmdMasterMindDisableRun(args))
}

// disableMasterMindRepo answers no for the repository and clears the caller's
// own yes so the repository's answer governs; it reports whether it handled
// the invocation.
func disableMasterMindRepo(rt relevo.Runtime, cwd string, kind, session *string, repo, asJSON bool) (bool, error) {
	if !repo {
		return false, nil
	}
	if err := mastermindWriteConsent(rt, cwd, db.ConsentNo); err != nil {
		return true, err
	}
	// A session's own yes would outrank the repository's no, so the caller's
	// answer is cleared when the caller is known.
	if caller, err := mastermindCaller(rt, cwd, *kind, *session); err == nil {
		if err := mastermindWriteSessionConsent(rt, caller.Kind, caller.SessionID, db.ConsentUnset); err != nil {
			return true, err
		}
	} else if *kind != "" || *session != "" {
		return true, err
	}
	if asJSON {
		return true, printDoc(mastermindDocOf("", "", "disabled", mastermindRepoLabel(rt, cwd)))
	}
	fmt.Println("relevo will not register this repository's sessions (relevo mastermind enable --repo to change)")
	return true, nil
}

// resolveMasterMindDisableTarget names the session the disable answers for:
// the explicit pair when given, else the calling session's own record.
func resolveMasterMindDisableTarget(rt relevo.Runtime, cwd string, kind, session *string) (kindV, sessionV string, err error) {
	// An explicit (kind, session) names the session a harness cannot detect
	// itself; the rest falls back to detection.
	if *kind != "" || *session != "" {
		caller, err := mastermindCaller(rt, cwd, *kind, *session)
		if err != nil {
			return "", "", err
		}
		return caller.Kind, caller.SessionID, nil
	}
	rec, err := gcResolver(rt)("")
	if err != nil {
		return "", "", fail(codeRefused, "not in a detectable mastermind session: pass --repo to answer for the repository, --kind/--session to name one, or run relevo mastermind forget <id|name>")
	}
	return rec.HarnessKind, rec.SessionID, nil
}

func cmdMasterMindDisableRun(args []string) error {
	fs := flag.NewFlagSet("disable", flag.ContinueOnError)
	v := mastermindDisableFlagSet(fs)
	repo, kind, session, asJSON := v.repo, v.kind, v.session, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}

	handled, err := disableMasterMindRepo(rt, cwd, kind, session, *repo, *asJSON)
	if err != nil {
		return err
	}
	if handled {
		return nil
	}

	kindV, sessionV, err := resolveMasterMindDisableTarget(rt, cwd, kind, session)
	if err != nil {
		return err
	}

	// The answer takes effect whether or not a record exists.
	if err := mastermindWriteSessionConsent(rt, kindV, sessionV, db.ConsentNo); err != nil {
		return err
	}

	reg, err := mastermindRegistry(rt)
	if err != nil {
		return err
	}
	rec, err := reg.BySession(kindV, sessionV)
	if errors.Is(err, mastermind.ErrNotFound) {
		if *asJSON {
			return printDoc(mastermindDocOf("", "", "no_record", ""))
		}
		fmt.Printf("relevo will not register %s session %s (it had no record to forget)\n", kindV, sessionV)
		return nil
	}
	if err != nil {
		return err
	}
	if err := reg.Forget(rec.ID, mastermindInUse(rt)); err != nil {
		return outcomeError(fmt.Errorf("relevo mastermind disable: the session answered no, but %w", err))
	}
	if *asJSON {
		return printDoc(mastermindDocOf(rec.ID, rec.Name, "forgotten", ""))
	}
	fmt.Printf("forgot mastermind %s (%s)\n", rec.Name, rec.ID)
	return nil
}

type mastermindResetFlagValues struct {
	kind    *string
	session *string
	asJSON  *bool
}

func mastermindResetFlagSet(fs *flag.FlagSet) *mastermindResetFlagValues {
	v := &mastermindResetFlagValues{}
	v.kind = fs.String("kind", "", "harness kind for an explicit session (with --session)")
	v.session = fs.String("session", "", "harness session id for an explicit session (with --kind)")
	v.asJSON = fs.Bool("json", false, "print the document the reset produced")
	return v
}

// cmdMasterMindReset clears the current repository's answer and the calling
// session's own answer and told baseline, so the consent question is asked again.
func cmdMasterMindReset(args []string) error {
	return outcomeError(cmdMasterMindResetRun(args))
}

func cmdMasterMindResetRun(args []string) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	v := mastermindResetFlagSet(fs)
	kind, session, asJSON := v.kind, v.session, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fail(codeUsage, "mastermind reset wants [--kind K --session S], got %v", fs.Args())
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	ref := mastermindRepoOf(context.Background(), rt, cwd)
	repoKnown := mastermindRepoKnown(ref)

	caller, callerErr := mastermindCaller(rt, cwd, *kind, *session)
	if callerErr != nil && (*kind != "" || *session != "") {
		return callerErr
	}
	if !repoKnown && callerErr != nil {
		return fail(codeRefused, "relevo mastermind reset: %s is not inside a git repository and no session was named; pass --kind/--session to clear one session's answer", cwd)
	}

	if repoKnown {
		if err := mastermindWriteConsent(rt, cwd, db.ConsentUnset); err != nil {
			return err
		}
	}
	if callerErr == nil {
		if err := mastermindClearSessionConsent(rt, caller.Kind, caller.SessionID); err != nil {
			return err
		}
	}

	docRepo := ""
	if repoKnown {
		docRepo = mastermindRepoLabel(rt, cwd)
	}

	if *asJSON {
		return printDoc(mastermindDocOf("", "", "reset", docRepo))
	}
	if repoKnown {
		fmt.Println("relevo will ask about this repository again (relevo mastermind enable --repo to answer yes now)")
	} else {
		fmt.Println("relevo will ask this session again (relevo mastermind enable to answer yes now)")
	}
	return nil
}

type mastermindGuideFlagValues struct {
	cwd     *string
	kind    *string
	session *string
	asJSON  *bool
}

func mastermindGuideFlagSet(fs *flag.FlagSet) *mastermindGuideFlagValues {
	v := &mastermindGuideFlagValues{}
	v.cwd = fs.String("cwd", "", "the session's working directory (default: the process cwd)")
	v.kind = fs.String("kind", "", "harness kind (with --session): an enabled session's record is created here")
	v.session = fs.String("session", "", "harness session id (with --kind)")
	v.asJSON = fs.Bool("json", false, "print {\"state\",\"text\",\"repo\",\"id\",\"name\"} instead of the text")
	return v
}

// cmdMasterMindGuide renders the model-facing text for a location's consent
// answer: the guide when the repository answered yes, the ask-note when it has
// not, and nothing when it answered no or is not a repository.
func cmdMasterMindGuide(args []string) error {
	fs := flag.NewFlagSet("guide", flag.ContinueOnError)
	v := mastermindGuideFlagSet(fs)
	cwdFlag, kind, session, asJSON := v.cwd, v.kind, v.session, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// A process relevo started for a round is a runner, never asked, briefed or
	// registered, so the guide answers before anything opens the database.
	// The disabled state is the existing non-repository answer, which the
	// plugin is already silent on.
	if isRunnerHost() {
		if *asJSON {
			fmt.Println(`{"state":"disabled","text":""}`)
		}
		return nil
	}

	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	cwd, err := guideCWD(rt, *cwdFlag, *kind, *session)
	if err != nil {
		return err
	}

	d, _ := rt.Store.DB()
	repoState, repoLabel := guideRepoAnswer(rt, cwd, d)

	// The session's own answer outranks the repository's.
	sessionState := db.ConsentUnset
	if *kind != "" && *session != "" && d != nil {
		if c, err := d.SessionConsent(*kind, *session); err == nil {
			sessionState = c
		}
	}
	state := mastermind.EffectiveConsent(sessionState, repoState)

	rec := guideRecord(rt, cwd, *kind, *session, state, d)

	text := mastermind.ConsentText(state, rec)
	if !*asJSON {
		if text != "" {
			fmt.Println(text)
		}
		return nil
	}

	word := "disabled"
	switch state {
	case db.ConsentYes:
		word = "enabled"
	case db.ConsentUnset:
		word = "ask"
	}
	out := struct {
		State string `json:"state"`
		Text  string `json:"text"`
		Repo  string `json:"repo,omitempty"`
		ID    string `json:"id,omitempty"`
		Name  string `json:"name,omitempty"`
	}{State: word, Text: text, Repo: repoLabel}
	if rec != nil {
		out.ID, out.Name = rec.ID, rec.Name
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return err
	}
	fmt.Println(string(raw))
	return nil
}

// guideCWD resolves the directory the guide answers for: an explicit --cwd,
// else the directory opencode recorded for the session, else the process's
// directory.
func guideCWD(rt relevo.Runtime, cwdFlag, kind, session string) (string, error) {
	cwd := cwdFlag
	if cwd == "" && kind == "opencode" && session != "" && rt.OpencodeSessionDir != nil {
		if dir, derr := rt.OpencodeSessionDir(session); derr == nil {
			cwd = dir
		}
	}
	if cwd == "" {
		dir, err := os.Getwd()
		if err != nil {
			return "", fail(codeInternal, "resolve working directory: %v", err)
		}
		return dir, nil
	}
	return cwd, nil
}

// guideRepoAnswer reads the repository's consent answer for cwd: no repository
// means no answer to give and none to ask for, which the guide renders as
// disabled.
func guideRepoAnswer(rt relevo.Runtime, cwd string, d *db.DB) (db.Consent, string) {
	ref := mastermindRepoOf(context.Background(), rt, cwd)
	if !mastermindRepoKnown(ref) {
		return db.ConsentNo, ""
	}
	label := ""
	if ref.OriginURL != nil {
		label = *ref.OriginURL
	} else {
		label = *ref.CommonDir
	}
	state := db.ConsentUnset
	if d != nil {
		if c, err := d.RepoConsent(ref); err == nil {
			state = c
		}
	}
	return state, label
}

// guideRecord returns the enabled session's record, registering it when the
// answer is yes; a registration failure leaves the text without the identity
// sentence rather than failing the session.
func guideRecord(rt relevo.Runtime, cwd, kind, session string, state db.Consent, d *db.DB) *mastermind.Record {
	if kind == "" || session == "" || state == db.ConsentNo {
		return nil
	}
	reg, err := mastermindRegistry(rt)
	if err != nil {
		return nil
	}
	if r, err := reg.BySession(kind, session); err == nil {
		return &r
	}
	if state != db.ConsentYes {
		return nil
	}
	in := mastermind.InitInput{Kind: kind, SessionID: session, CWD: cwd, Now: rt.Now()}
	in.PriorID = mastermindPriorIDFunc(d)
	r, _, err := mastermind.Init(reg, in)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo mastermind guide: register %s: %v\n", session, err)
		return nil
	}
	return &r
}
