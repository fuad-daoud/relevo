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

// mastermindPriorIDFunc reuses a database row's id for (kind, session). A nil
// handle means "no prior id" and Init mints one.
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

// mastermindRepoKnown reports whether ref names a repository at all.
func mastermindRepoKnown(ref db.Repo) bool {
	return ref.OriginURL != nil || ref.CommonDir != nil
}

// mastermindCaller fills the calling session's identity: an explicit
// --kind/--session pair wins, else detection. An opencode session carries no
// id in the environment, so detection fills it from opencode's own database.
func mastermindCaller(rt relevo.Runtime, cwd, kindFlag, sessionFlag string) (mastermind.InitInput, error) {
	if kindFlag != "" || sessionFlag != "" {
		if kindFlag == "" || sessionFlag == "" {
			return mastermind.InitInput{}, fail(codeUsage, "relevo mastermind: needs both --kind and --session, or neither")
		}
		return mastermind.InitInput{Kind: kindFlag, SessionID: sessionFlag}, nil
	}

	ident, ok := mastermind.Detect(os.Getenv, os.Getppid())
	if !ok {
		return mastermind.InitInput{}, fail(codeRefused, "not in a detectable mastermind session: pass --kind and --session")
	}
	in := mastermind.InitInput{
		Kind:      ident.Kind,
		SessionID: ident.SessionID,
		Agent:     os.Getenv("CLAUDE_CODE_AGENT"),
		HostPID:   ident.HostPID,
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

// mastermindEnableFlagValues holds the pointers `mastermind enable` parses
// into.
type mastermindEnableFlagValues struct {
	repo    *bool
	kind    *string
	session *string
	asJSON  *bool
}

// mastermindEnableFlagSet defines those flags on fs and returns what they
// parse into.
func mastermindEnableFlagSet(fs *flag.FlagSet) *mastermindEnableFlagValues {
	v := &mastermindEnableFlagValues{}
	v.repo = fs.Bool("repo", false, "remember yes for this repository, not just this session")
	v.kind = fs.String("kind", "", "harness kind for an explicit registration (with --session)")
	v.session = fs.String("session", "", "harness session id for an explicit registration (with --kind)")
	v.asJSON = fs.Bool("json", false, "print the document the consent produced")
	return v
}

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

	if *repo {
		if err := mastermindWriteConsent(rt, cwd, db.ConsentYes); err != nil {
			return err
		}
	}

	caller, err := mastermindCaller(rt, cwd, *kind, *session)
	if err != nil {
		return err
	}
	caller.CWD = cwd
	caller.Now = rt.Now()

	// The answer is the session's own, unless --repo answered for the
	// repository: then the session's earlier "this session only" is cleared so
	// the repository's answer governs the session.
	answer := db.ConsentYes
	if *repo {
		answer = db.ConsentUnset
	}
	if err := mastermindWriteSessionConsent(rt, caller.Kind, caller.SessionID, answer); err != nil {
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

	// The env-file append is a side effect both modes keep; only the stdout
	// export line is a notice that moves off stdout under --json.
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

// cmdMasterMindDisable answers no for the repository, or for this session: the
// session's own answer is written whether or not it has a record, and an
// existing record is forgotten.
// mastermindDisableFlagValues holds the pointers `mastermind disable` parses
// into.
type mastermindDisableFlagValues struct {
	repo    *bool
	kind    *string
	session *string
	asJSON  *bool
}

// mastermindDisableFlagSet defines those flags on fs and returns what they
// parse into.
func mastermindDisableFlagSet(fs *flag.FlagSet) *mastermindDisableFlagValues {
	v := &mastermindDisableFlagValues{}
	v.repo = fs.Bool("repo", false, "remember no for this repository; without it, answer no for this session")
	v.kind = fs.String("kind", "", "harness kind for an explicit session (with --session)")
	v.session = fs.String("session", "", "harness session id for an explicit session (with --kind)")
	v.asJSON = fs.Bool("json", false, "print the document the consent produced")
	return v
}

func cmdMasterMindDisable(args []string) error {
	return outcomeError(cmdMasterMindDisableRun(args))
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

	if *repo {
		if err := mastermindWriteConsent(rt, cwd, db.ConsentNo); err != nil {
			return err
		}
		// A session's own yes would outrank the repository's no, so the
		// caller's answer is cleared when the caller is known. A caller relevo
		// cannot detect (a human in a plain terminal) is left alone; a
		// half-given explicit pair is still an error.
		if caller, err := mastermindCaller(rt, cwd, *kind, *session); err == nil {
			if err := mastermindWriteSessionConsent(rt, caller.Kind, caller.SessionID, db.ConsentUnset); err != nil {
				return err
			}
		} else if *kind != "" || *session != "" {
			return err
		}
		if *asJSON {
			return printDoc(mastermindDocOf("", "", "disabled", mastermindRepoLabel(rt, cwd)))
		}
		fmt.Println("relevo will not register this repository's sessions (relevo mastermind enable --repo to change)")
		return nil
	}

	// An explicit (kind, session) names the session a harness cannot detect
	// itself; a plugin passes it, and the rest falls back to detection.
	var kindV, sessionV string
	if *kind != "" || *session != "" {
		caller, err := mastermindCaller(rt, cwd, *kind, *session)
		if err != nil {
			return err
		}
		kindV, sessionV = caller.Kind, caller.SessionID
	} else {
		rec, err := gcResolver(rt)("")
		if err != nil {
			return fail(codeRefused, "not in a detectable mastermind session: pass --repo to answer for the repository, --kind/--session to name one, or run relevo mastermind forget <id|name>")
		}
		kindV, sessionV = rec.HarnessKind, rec.SessionID
	}

	// The answer takes effect whether or not a record exists: a session that
	// never registered can still answer no.
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

// cmdMasterMindReset clears the current repository's answer and the calling
// session's own answer and told baseline, so the next session -- or the next
// prompt -- asks the consent question again.
// mastermindResetFlagValues holds the pointers `mastermind reset` parses into.
type mastermindResetFlagValues struct {
	kind    *string
	session *string
	asJSON  *bool
}

// mastermindResetFlagSet defines those flags on fs and returns what they parse
// into.
func mastermindResetFlagSet(fs *flag.FlagSet) *mastermindResetFlagValues {
	v := &mastermindResetFlagValues{}
	v.kind = fs.String("kind", "", "harness kind for an explicit session (with --session)")
	v.session = fs.String("session", "", "harness session id for an explicit session (with --kind)")
	v.asJSON = fs.Bool("json", false, "print the document the reset produced")
	return v
}

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

// cmdMasterMindGuide renders the model-facing text for a location's consent
// answer: the guide when the repository answered yes, the ask-note when it has
// not, and nothing when it answered no or is not a repository. The opencode
// plugin calls it once per session and pushes the text into the session's
// system instructions.
// mastermindGuideFlagValues holds the pointers `mastermind guide` parses into.
type mastermindGuideFlagValues struct {
	cwd     *string
	kind    *string
	session *string
	asJSON  *bool
}

// mastermindGuideFlagSet defines those flags on fs and returns what they parse
// into.
func mastermindGuideFlagSet(fs *flag.FlagSet) *mastermindGuideFlagValues {
	v := &mastermindGuideFlagValues{}
	v.cwd = fs.String("cwd", "", "the session's working directory (default: the process cwd)")
	v.kind = fs.String("kind", "", "harness kind (with --session): an enabled session's record is created here")
	v.session = fs.String("session", "", "harness session id (with --kind)")
	v.asJSON = fs.Bool("json", false, "print {\"state\",\"text\",\"repo\",\"id\",\"name\"} instead of the text")
	return v
}

func cmdMasterMindGuide(args []string) error {
	fs := flag.NewFlagSet("guide", flag.ContinueOnError)
	v := mastermindGuideFlagSet(fs)
	cwdFlag, kind, session, asJSON := v.cwd, v.kind, v.session, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// A process relevo started for a round is a runner, not a MasterMind
	// session: it is never asked, briefed or registered, so the guide answers
	// and returns before anything opens the database or reads a repository.
	// The disabled state is the existing non-repository answer, which the
	// plugin is already silent on.
	if mastermind.IsRunner(os.Getenv) {
		if *asJSON {
			fmt.Println(`{"state":"disabled","text":""}`)
		}
		return nil
	}

	rt, err := newRuntime()
	if err != nil {
		return fail(codeInternal, "%v", err)
	}

	// The plugin knows the session but not its repository, so an opencode
	// session id resolves the directory opencode recorded. An explicit --cwd,
	// or anything else, falls back to the process's own directory.
	cwd := *cwdFlag
	if cwd == "" && *kind == "opencode" && *session != "" && rt.OpencodeSessionDir != nil {
		if dir, derr := rt.OpencodeSessionDir(*session); derr == nil {
			cwd = dir
		}
	}
	if cwd == "" {
		if cwd, err = os.Getwd(); err != nil {
			return fail(codeInternal, "resolve working directory: %v", err)
		}
	}

	// No repository means no answer to give and none to ask for; the hook
	// reads the same rule.
	repoState := db.ConsentNo
	repoLabel := ""
	ref := mastermindRepoOf(context.Background(), rt, cwd)
	d, _ := rt.Store.DB()
	if mastermindRepoKnown(ref) {
		if ref.OriginURL != nil {
			repoLabel = *ref.OriginURL
		} else {
			repoLabel = *ref.CommonDir
		}
		repoState = db.ConsentUnset
		if d != nil {
			if c, err := d.RepoConsent(ref); err == nil {
				repoState = c
			}
		}
	}

	// The session's own answer outranks the repository's: a session `no` stays
	// disabled inside a `yes` repository, and a session `yes` enables even
	// where the repository is unset or says no.
	sessionState := db.ConsentUnset
	if *kind != "" && *session != "" && d != nil {
		if c, err := d.SessionConsent(*kind, *session); err == nil {
			sessionState = c
		}
	}
	state := mastermind.EffectiveConsent(sessionState, repoState)

	// The session's own record, when it has one, is reported for every state
	// but no: a session that answered for itself (`relevo mastermind enable`)
	// is briefed whatever the repository says, and one that answered no is not
	// briefed at all. An enabled session creates the record here, so the
	// identity sentence names the MasterMind the TUI shows; a registration
	// failure leaves the text without the sentence rather than failing the
	// session.
	var rec *mastermind.Record
	if *kind != "" && *session != "" && state != db.ConsentNo {
		if reg, err := mastermindRegistry(rt); err == nil {
			if r, err := reg.BySession(*kind, *session); err == nil {
				rec = &r
			}
			if state == db.ConsentYes && rec == nil {
				in := mastermind.InitInput{Kind: *kind, SessionID: *session, CWD: cwd, Now: rt.Now()}
				in.PriorID = mastermindPriorIDFunc(d)
				if r, _, err := mastermind.Init(reg, in); err == nil {
					rec = &r
				} else {
					fmt.Fprintf(os.Stderr, "relevo mastermind guide: register %s: %v\n", *session, err)
				}
			}
		}
	}

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
