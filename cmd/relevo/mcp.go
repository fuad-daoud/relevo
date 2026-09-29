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

	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/mcp"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// minMCPInterval floors --interval, the same guard the daemon's --interval
// gets, so a misconfigured poll cannot spin the claim file or the store lock.
const minMCPInterval = 200 * time.Millisecond

// relevo mcp starts beside the plugin's SessionStart hook (§5.2), which may
// still be running when the server comes up, so ErrNoMasterMind is retried for
// up to mcpResolveTimeout before the server falls back to tools-only.
const (
	mcpResolveTimeout = 10 * time.Second
	mcpResolveRetry   = 500 * time.Millisecond
)

// cmdMCP runs relevo mcp: an MCP server over stdio a Claude Code mastermind
// spawns from its plugin manifest (docs/specs/2026-09-21-mastermind-channel-design.md,
// #303 §4.5). In channel mode it also
// claims its mastermind and drains its mailbox; in tools mode it only serves
// the verbs as tools.
func cmdMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	mastermindFlag := fs.String("mastermind", "", "mastermind id or name (default: $RELEVO_MASTERMIND, else this session's host)")
	modeFlag := fs.String("mode", "auto", "channel|tools|auto (default: detected from the parent process's argv)")
	kindFlag := fs.String("kind", "", "harness kind this server runs under: opencode resolves the MasterMind per tool call, tools only")
	interval := fs.Duration("interval", time.Second, "poll interval in channel mode (floored at 200ms)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	if *interval < minMCPInterval {
		*interval = minMCPInterval
	}

	kind, err := mcpResolveKind(*kindFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo mcp: %v\n", err)
		return exitCodeErr{code: 2}
	}

	mode, err := mcpResolveMode(kind, *modeFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo mcp: %v\n", err)
		return exitCodeErr{code: 2}
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	// §4.5: resolve this mastermind, retrying while the hook may still be
	// running. A bad --mastermind value is not a race: it is reported at once.
	// An opencode server has no identity to resolve here: its calls name their
	// session, and each tool call resolves then.
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
		fmt.Fprintf(os.Stderr, "relevo mcp: mastermind %s (%s) mode %s\n", rec.Name, rec.ID, mcpModeWord(mode))
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
		// The mode is known before initialize is answered, so the model is
		// told from its first turn which delivery it should expect (#303 §4.5).
		Mode: mode,
		Kind: kind,
		Log:  os.Stderr,
		// §4.10: this server runs for the session's whole life, so when the
		// daemon has re-exec'd onto a newer relevo it says so on every tool
		// result and the mastermind reconnects (/mcp). The read is cached for
		// 30 s and a read error is no notice at all.
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

	if mode == mcp.ModeChannel && haveRec {
		srv.OnInitialized = func() {
			startMCPChannel(ctx, rt, rec, version, srv, *interval)
		}
	}

	serveErr := srv.Serve(ctx, os.Stdin, os.Stdout)

	if mode == mcp.ModeChannel && haveRec && rt.Channels != nil {
		_ = rt.Channels.Remove(rec.ID, os.Getpid())
	}

	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return serveErr
	}
	return nil
}

// resolveMCPMasterMind is mastermind.Resolve as relevo mcp calls it: --mastermind,
// $RELEVO_MASTERMIND, the parent Claude process, then the session (§4.5).
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
		Now:       now,
	})
}

// mcpResolveMode turns --mode into an mcp.Mode: "channel" and "tools" are
// literal, "auto" reads the parent process's argv and falls back to
// ModeTools (with a reason) when it cannot (spec §7).
func resolveMCPMode(flagVal string) (mcp.Mode, error) {
	switch flagVal {
	case "channel":
		return mcp.ModeChannel, nil
	case "tools":
		return mcp.ModeTools, nil
	case "auto":
		argv, err := mcp.ParentArgv()
		if err != nil {
			fmt.Fprintf(os.Stderr, "relevo mcp: cannot read parent argv (%v); tools-only mode\n", err)
			return mcp.ModeTools, nil
		}
		return mcp.DetectMode(argv), nil
	default:
		return mcp.ModeTools, fmt.Errorf("--mode must be channel, tools, or auto, got %q", flagVal)
	}
}

// mcpResolveKind validates --kind: the empty kind is Claude Code, resolved once
// at startup; opencode resolves each tool call from its session.
func mcpResolveKind(kind string) (string, error) {
	switch kind {
	case "", "opencode":
		return kind, nil
	default:
		return "", fmt.Errorf("--kind must be empty or opencode, got %q", kind)
	}
}

// mcpResolveMode picks the mode for the kind: opencode has no Claude channel,
// so its server always serves tools and an explicit --mode channel is refused
// rather than ignored.
func mcpResolveMode(kind, flagVal string) (mcp.Mode, error) {
	if kind == "opencode" {
		if flagVal == "channel" {
			return mcp.ModeTools, fmt.Errorf("--kind opencode has no channel mode")
		}
		return mcp.ModeTools, nil
	}
	return resolveMCPMode(flagVal)
}

// mcpVerbs builds the tool verbs: an opencode server resolves the calling
// session per call, a Claude one uses the mastermind resolved at startup. An
// opencode server with no mastermind registry is refused rather than served
// with a resolver that could never resolve a session.
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
// plugin created for it. The not-found text is actionable: an unanswered
// repository is the reason a tool call usually arrives without a record. A nil
// registry is an error: the server cannot resolve any session, so it must not
// start.
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

func mcpModeWord(m mcp.Mode) string {
	if m == mcp.ModeChannel {
		return "channel"
	}
	return "tools"
}

// startMCPChannel writes this process's initial claim and, on success, starts
// the poll loop. A refused claim (ErrClaimHeld) exits the process: Claude Code
// shows the server as failed and the mastermind keeps pane delivery, exactly as if
// relevo mcp had never started (spec §3.2, §6).
func startMCPChannel(ctx context.Context, rt relevo.Runtime, rec mastermind.Record, version string, p delivery.Pusher, interval time.Duration) {
	if rt.Channels == nil {
		fmt.Fprintln(os.Stderr, "relevo mcp: no claim store configured; running tools-only")
		return
	}

	now := rt.Now()
	cwd, _ := os.Getwd()
	host := os.Getppid()
	claim := delivery.Claim{
		MasterMind:    rec.ID,
		PID:           os.Getpid(),
		HostPID:       host,
		HostStartedAt: mastermindHostStart(host),
		StartedAt:     now,
		SeenAt:        now,
		CWD:           cwd,
		Version:       version,
	}
	if err := rt.Channels.Write(claim, now); err != nil {
		fmt.Fprintf(os.Stderr, "relevo mcp: %v\n", err)
		if errors.Is(err, delivery.ErrClaimHeld) {
			os.Exit(1)
		}
		return
	}

	go pollMCPChannel(ctx, rt, rec.ID, claim, p, interval)
}

// pollMCPChannel is relevo mcp's channel-mode poll loop: re-read the mastermind
// record, refresh the claim, then drain that mastermind's mailbox (spec §3.4,
// #303 §4.5). It never exits on a drain error -- only a stolen claim
// (ErrClaimHeld on refresh) or a forgotten record stops it, leaving the
// tools still serving.
func pollMCPChannel(ctx context.Context, rt relevo.Runtime, mastermindID string, claim delivery.Claim, p delivery.Pusher, interval time.Duration) {
	st := &delivery.DrainState{MasterMind: mastermindID}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// §4.5: the record is re-read by id every poll, so a rename or a
			// session move a later `relevo mastermind init --hook` makes (after
			// /clear) is picked up without a restart. A record that is gone
			// ends the channel; the tools keep serving.
			if _, err := rt.MasterMinds.Get(mastermindID); err != nil {
				fmt.Fprintf(os.Stderr, "relevo mcp: mastermind %s is gone; channel stopped\n", mastermindID)
				return
			}

			claim.SeenAt = rt.Now()
			if err := rt.Channels.Write(claim, claim.SeenAt); err != nil {
				fmt.Fprintf(os.Stderr, "relevo mcp: refresh claim: %v\n", err)
				if errors.Is(err, delivery.ErrClaimHeld) {
					return
				}
				continue
			}

			res, err := delivery.Drain(ctx, delivery.Deps{Store: rt.Store, Now: rt.Now, Channels: rt.Channels, Deliverers: rt.Deliverers, MasterMinds: rt.MasterMinds}, st, p)
			if err != nil {
				fmt.Fprintf(os.Stderr, "relevo mcp: drain: %v\n", err)
				continue
			}
			if res.Pushed > 0 || res.States > 0 || len(res.Failed) > 0 {
				fmt.Fprintf(os.Stderr, "relevo mcp: drain pushed=%d states=%d failed=%v\n", res.Pushed, res.States, res.Failed)
			}
		}
	}
}
