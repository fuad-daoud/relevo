package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/mcp"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// relevo mcp starts beside the plugin's SessionStart hook, so ErrNoMasterMind is
// retried for up to mcpResolveTimeout before the server falls back to tools-only.
const (
	mcpResolveTimeout = 10 * time.Second
	mcpResolveRetry   = 500 * time.Millisecond
)

type mcpFlagValues struct {
	mastermind *string
	kind       *string
}

func mcpFlagSet(fs *flag.FlagSet) *mcpFlagValues {
	v := &mcpFlagValues{}
	v.mastermind = fs.String("mastermind", "", "mastermind id or name (default: $RELEVO_MASTERMIND, else this session's host)")
	v.kind = fs.String("kind", "", "harness kind this server runs under: opencode resolves the MasterMind per tool call, tools only")
	return v
}

// cmdMCP runs relevo mcp: an MCP server over stdio serving the verbs as tools.
// This server pushes nothing itself.
func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	v := mcpFlagSet(fs)
	mastermindFlag, kindFlag := v.mastermind, v.kind
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	kind, err := mcpResolveKind(*kindFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo mcp: %v\n", err)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// Resolve this mastermind, retrying while the hook may still be running. An
	// opencode server has no identity to resolve here: each tool call resolves
	// then.
	var (
		rec     mastermind.Record
		haveRec bool
	)
	if kind != "opencode" {
		deadline := time.Now().Add(mcpResolveTimeout)
		for {
			r, _, rerr := resolveMCPMasterMind(rt, *mastermindFlag)
			if rerr == nil {
				rec, haveRec = r, true
				break
			}
			if !errors.Is(rerr, mastermind.ErrNoMasterMind) {
				fmt.Fprintf(os.Stderr, "relevo mcp: %v\n", rerr)
				return exitCodeErr{code: 2}
			}
			if !time.Now().Before(deadline) {
				break
			}
			time.Sleep(mcpResolveRetry)
		}
	}

	version := buildVersion()
	switch {
	case haveRec:
		fmt.Fprintf(os.Stderr, "relevo mcp: mastermind %s (%s) tools\n", rec.Name, rec.ID)
	case kind == "opencode":
		fmt.Fprintln(os.Stderr, "relevo mcp: opencode tools server; each tool call resolves its MasterMind")
	default:
		// No registration and no host match: the verbs still serve, so a
		// mastermind whose hook never ran can still use the tools.
		fmt.Fprintln(os.Stderr, `relevo mcp: no relevo mastermind for this session; tools-only (run "relevo mastermind init")`)
	}

	verbs, err := mcpVerbs(rt, kind, rec.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo mcp: %v\n", err)
		return exitCodeErr{code: 2}
	}

	srv := &mcp.Server{
		Verbs:   verbs,
		Version: version,
		Mode:    mcp.ModeTools,
		Kind:    kind,
		Log:     os.Stderr,
		// The daemon may re-exec onto a newer relevo mid-session, so every tool
		// result carries the notice; the read is cached and a read error is no
		// notice at all.
		Notice: cachedString(mcpNoticeTTL, time.Now, func() string {
			info, ok, err := rt.Store.ReadDaemonInfo()
			if err != nil {
				return ""
			}
			return mcpNotice(version, info, ok)
		}),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := srv.Serve(ctx, os.Stdin, os.Stdout)

	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return serveErr
	}
	return nil
}

// resolveMCPMasterMind is mastermind.Resolve as relevo mcp calls it.
func resolveMCPMasterMind(rt relevo.Runtime, flagVal string) (mastermind.Record, mastermind.Resolution, error) {
	var now time.Time
	if rt.Now != nil {
		now = rt.Now()
	}
	return mastermind.Resolve(rt.MasterMinds, mastermind.ResolveInput{
		Flag:      flagVal,
		Env:       os.Getenv,
		PPID:      os.Getppid(),
		ProcStart: rt.ProcStart,
		MuseHost:  rt.MuseHost,
		Now:       now,
		Heal:      true,
	})
}

// mcpResolveKind validates --kind: empty is Claude Code, resolved once at
// startup; opencode resolves each tool call from its session.
func mcpResolveKind(kind string) (string, error) {
	switch kind {
	case "", "opencode":
		return kind, nil
	default:
		return "", fmt.Errorf("--kind must be empty or opencode, got %q", kind)
	}
}

// mcpVerbs builds the tool verbs. An opencode server with no mastermind registry
// is refused rather than served with a resolver that could never resolve.
func mcpVerbs(rt relevo.Runtime, kind, masterMindID string) (*mcp.RelevoVerbs, error) {
	v := &mcp.RelevoVerbs{RT: rt, MasterMind: masterMindID}
	if kind == "opencode" {
		resolve, err := opencodeSessionMasterMind(rt)
		if err != nil {
			return nil, err
		}
		v.ResolveSession = resolve
	}
	return v, nil
}

// opencodeSessionMasterMind maps one tool call's session to the record the
// plugin created for it. A nil registry is an error: the server must not start
// when it cannot resolve any session.
func opencodeSessionMasterMind(rt relevo.Runtime) (func(session string) (string, error), error) {
	if rt.MasterMinds == nil {
		return nil, errors.New("opencode tools server has no mastermind registry; cannot resolve tool-call sessions")
	}
	return func(session string) (string, error) {
		rec, err := rt.MasterMinds.BySession("opencode", session)
		switch {
		case err == nil:
			return rec.ID, nil
		case errors.Is(err, mastermind.ErrNotFound):
			return "", fmt.Errorf("no relevo MasterMind for opencode session %s: answer relevo's consent question in the repository, then run relevo mastermind enable --repo", session)
		default:
			return "", err
		}
	}, nil
}
