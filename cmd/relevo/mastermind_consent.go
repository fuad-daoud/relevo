package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

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

// mastermindRepoOf reads cwd's repository identity through the runtime's git
// client, in the same shape internal/ingest stores: the origin URL normalised,
// the common dir canonical. A Runtime with no git client, or a cwd outside a
// repository, yields an empty ref.
func mastermindRepoOf(ctx context.Context, rt relevo.Runtime, cwd string) db.Repo {
	if rt.Git == nil {
		return db.Repo{}
	}
	originURL, commonDir, err := rt.Git.RepoFacts(ctx, cwd)
	if err != nil {
		return db.Repo{}
	}
	out := db.Repo{}
	if origin := git.NormalizeOriginURL(originURL); origin != "" {
		out.OriginURL = &origin
	}
	if commonDir != "" {
		dir := commonDir
		out.CommonDir = &dir
	}
	return out
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
			return mastermind.InitInput{}, fmt.Errorf("relevo mastermind: needs both --kind and --session, or neither")
		}
		return mastermind.InitInput{Kind: kindFlag, SessionID: sessionFlag}, nil
	}

	ident, ok := mastermind.Detect(os.Getenv, os.Getppid())
	if !ok {
		return mastermind.InitInput{}, fmt.Errorf("not in a detectable mastermind session: pass --kind and --session")
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

// appendEnvLine appends one line to the file $CLAUDE_ENV_FILE names.

func cmdMasterMindEnable(args []string) error {
	fs := flag.NewFlagSet("enable", flag.ContinueOnError)
	repo := fs.Bool("repo", false, "remember yes for this repository, not just this session")
	kind := fs.String("kind", "", "harness kind for an explicit registration (with --session)")
	session := fs.String("session", "", "harness session id for an explicit registration (with --kind)")
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

	fmt.Printf("MasterMind %s (%s) %s\n", rec.Name, rec.ID, res)
	if envFile := os.Getenv("CLAUDE_ENV_FILE"); envFile != "" {
		if err := appendEnvLine(envFile, mastermind.EnvLine(rec.ID)); err != nil {
			return err
		}
	} else {
		fmt.Printf("export RELEVO_MASTERMIND=%s\n", rec.ID)
	}
	if *repo {
		fmt.Println("relevo will register this repository's sessions (relevo mastermind disable --repo to stop)")
	}
	return nil
}

// cmdMasterMindDisable answers no for the repository, or forgets this
// session's record when --repo is absent.
func cmdMasterMindDisable(args []string) error {
	fs := flag.NewFlagSet("disable", flag.ContinueOnError)
	repo := fs.Bool("repo", false, "remember no for this repository; without it, forget this session's record")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	if *repo {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolve working directory: %w", err)
		}
		if err := mastermindWriteConsent(rt, cwd, db.ConsentNo); err != nil {
			return err
		}
		fmt.Println("relevo will not register this repository's sessions (relevo mastermind enable --repo to change)")
		return nil
	}

	rec, ok := mastermindFilter(rt)
	if !ok {
		return fmt.Errorf("not in a detectable mastermind session: pass --repo to answer for the repository, or run relevo mastermind forget <id|name>")
	}
	reg, err := mastermindRegistry(rt)
	if err != nil {
		return err
	}
	if err := reg.Forget(rec.ID, mastermindInUse(rt)); err != nil {
		return err
	}
	fmt.Printf("forgot mastermind %s (%s)\n", rec.Name, rec.ID)
	return nil
}

// cmdMasterMindReset clears the current repository's answer, so the next
// session asks the consent question again.
func cmdMasterMindReset(args []string) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("usage: relevo mastermind reset")
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve working directory: %w", err)
	}
	if err := mastermindWriteConsent(rt, cwd, db.ConsentUnset); err != nil {
		return err
	}
	fmt.Println("relevo will ask about this repository again (relevo mastermind enable --repo to answer yes now)")
	return nil
}

// mastermindWriteConsent writes one answer for cwd's repository. A cwd outside
// a git repository has nothing to remember, so the answer commands refuse.
func mastermindWriteConsent(rt relevo.Runtime, cwd string, c db.Consent) error {
	ref := mastermindRepoOf(context.Background(), rt, cwd)
	if !mastermindRepoKnown(ref) {
		return fmt.Errorf("relevo mastermind enable|disable|reset: %s is not inside a git repository, so there is no repository answer to change", cwd)
	}
	d, err := rt.Store.DB()
	if err != nil {
		return err
	}
	if _, err := d.SetRepoConsent(ref, c, rt.Now()); err != nil {
		return err
	}
	return nil
}

// cmdMasterMindGuide renders the model-facing text for a location's consent
// answer: the guide when the repository answered yes, the ask-note when it has
// not, and nothing when it answered no or is not a repository. The opencode
// plugin calls it once per session and pushes the text into the session's
// system instructions.
func cmdMasterMindGuide(args []string) error {
	fs := flag.NewFlagSet("guide", flag.ContinueOnError)
	cwdFlag := fs.String("cwd", "", "the session's working directory (default: the process cwd)")
	kind := fs.String("kind", "", "harness kind (with --session): an enabled session's record is created here")
	session := fs.String("session", "", "harness session id (with --kind)")
	asJSON := fs.Bool("json", false, "print {\"state\",\"text\",\"repo\",\"id\",\"name\"} instead of the text")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
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
			return fmt.Errorf("resolve working directory: %w", err)
		}
	}

	// No repository means no answer to give and none to ask for; the hook
	// reads the same rule.
	state := db.ConsentNo
	repoLabel := ""
	ref := mastermindRepoOf(context.Background(), rt, cwd)
	if mastermindRepoKnown(ref) {
		if ref.OriginURL != nil {
			repoLabel = *ref.OriginURL
		} else {
			repoLabel = *ref.CommonDir
		}
		state = db.ConsentUnset
		if d, err := rt.Store.DB(); err == nil {
			if c, err := d.RepoConsent(ref); err == nil {
				state = c
			}
		}
	}

	// An enabled opencode session gets its record here, so the identity
	// sentence names the MasterMind the TUI shows. A registration failure
	// leaves the text without the sentence rather than failing the session.
	var rec *mastermind.Record
	if state == db.ConsentYes && *kind == "opencode" && *session != "" {
		in := mastermind.InitInput{Kind: *kind, SessionID: *session, CWD: cwd, Now: rt.Now()}
		if d, err := rt.Store.DB(); err == nil {
			in.PriorID = mastermindPriorIDFunc(d)
		}
		if reg, err := mastermindRegistry(rt); err == nil {
			if r, _, err := mastermind.Init(reg, in); err == nil {
				rec = &r
			} else {
				fmt.Fprintf(os.Stderr, "relevo mastermind guide: register %s: %v\n", *session, err)
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
