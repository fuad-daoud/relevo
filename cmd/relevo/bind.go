package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/housekeep"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// bindFlags is the union of the flags bind and add each accept.
type bindFlags struct {
	name       string
	candidate  string
	mastermind string
	resume     bool
	rebind     bool
	timeout    time.Duration
	tier       string
	allowYolo  bool
	gate       string
	noGate     bool
	regate     *int
	feature    string
	noFeature  bool
	ticket     string
	role       string

	// The placement flags choose add's path.
	worktree bool
	cwd      string
	branch   string
	server   string
	base     string

	// local runs the builder on this machine whatever the actor's placement says.
	// It is not a route flag.
	local bool

	// asJSON selects the result document instead of the human lines.
	asJSON bool
}

type bindRoute int

const (
	routeBind bindRoute = iota
	routeAdd
)

// bindRouteFor chooses the path from the parsed flags and refuses the forbidden
// combinations. It is pure, so the rules are table tested without a runtime.
func bindRouteFor(f bindFlags) (bindRoute, error) {
	placement := f.worktree || f.cwd != "" || f.branch != "" || f.server != "" || f.base != ""
	switch {
	case f.server != "" && f.local:
		// --server and --local name two different machines.
		return routeBind, errors.New("--server and --local are exclusive: --server runs the builder on that server, --local on this machine")
	case f.resume || f.rebind:
		// --resume and --rebind are bind's alone.
		if placement {
			return routeBind, errors.New("--resume/--rebind cannot be combined with --worktree/--cwd/--branch/--server/--base")
		}
		return routeBind, nil
	case placement:
		return routeAdd, nil
	default:
		return routeBind, nil
	}
}

type bindFlagValues struct {
	name           *string
	candidate      *string
	mastermindFlag *string
	resume         *bool
	rebind         *bool
	timeout        *time.Duration
	tier           *string
	allowYolo      *bool
	gate           *string
	noGate         *bool
	regate         *int
	feature        *string
	noFeature      *bool
	ticket         *string
	actor          *string
	worktree       *bool
	cwd            *string
	branch         *string
	server         *string
	base           *string
	local          *bool
	asJSON         *bool
}

// bindFlagSet is separate from cmdBind so a test can inspect the flag surface
// without running a bind.
func bindFlagSet(fs *flag.FlagSet) *bindFlagValues {
	v := &bindFlagValues{}
	v.name = fs.String("name", "", "binding name (default: sanitized cwd basename)")
	v.candidate = fs.String("candidate", "", "candidate name or harness/provider/model token to run; omit to take the actor's first ungated candidate")
	v.mastermindFlag = fs.String("mastermind", "", "act as this mastermind (id or name; default: $RELEVO_MASTERMIND, else this session's host)")
	v.resume = fs.Bool("resume", false, "adopt an existing binding into this mastermind")
	v.rebind = fs.Bool("rebind", false,
		"with --resume: replace a gone runner, picking it by config policy order and the ledger (like bind with --candidate omitted)")
	v.timeout = fs.Duration("timeout", 0, "round budget before relevo flags the binding (default 24h)")
	v.tier = fs.String("tier", "", "permission tier: harness|read|edit|yolo (default: candidate tier, then the actor's tier, then harness)")
	v.allowYolo = fs.Bool("allow-yolo", false, "permit --tier yolo above policy max_tier for this command")
	v.gate = fs.String("gate", "", "acceptance command relevo runs on the round's completion marker (default: config policy gate.default)")
	v.noGate = fs.Bool("no-gate", false, "opt this binding out of config policy's gate.default")
	v.regate = fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: config policy gate.regate)")
	v.feature = fs.String("feature", "", "label grouping this binding with others; a fresh bind needs exactly one of --feature or --no-feature")
	v.noFeature = fs.Bool("no-feature", false, "record that this binding is not a feature; a fresh bind needs exactly one of --feature or --no-feature")
	v.ticket = fs.String("ticket", "", "the issue this binding serves: N, #N, owner/repo#N, an issue key like ABC-123, or an issue URL (allowed with --no-feature)")
	v.actor = fs.String("actor", "", "the actor this binding runs (default builder); a reader actor leaves artifacts and never changes the tree")
	v.worktree = fs.Bool("worktree", false, "attach an additional runner to this mastermind, on its own worktree")
	v.cwd = fs.String("cwd", "", "bind the peer to an existing directory instead of creating a git worktree")
	v.branch = fs.String("branch", "", "existing local or origin/ branch to check out instead of cutting relevo/<name>")
	v.server = fs.String("server", "", "run the builder on this configured remote server instead of a local process (relevo config server list)")
	v.base = fs.String("base", "", "commit or ref to branch from; defaults to HEAD")
	v.local = fs.Bool("local", false, "run the builder on this machine, whatever the actor's placement says")
	v.asJSON = fs.Bool("json", false, "print the binding as a JSON document")
	return v
}

// cmdBind binds a mastermind to a builder: the flags choose which of the two
// bodies runs, and each body stays an unexported helper so none of its logic
// is duplicated.
func cmdBind(args []string) error {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	v := bindFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// The label rules are refused on one line with exit 2 before any runtime,
	// state, worktree or network work.
	if err := relevo.RequireFeatureChoice(*v.feature, *v.noFeature, *v.resume); err != nil {
		return refuseFlag(err)
	}
	if *v.feature != "" {
		if err := store.ValidFeature(*v.feature); err != nil {
			return refuseFlag(err)
		}
	}
	if *v.ticket != "" {
		if _, err := store.ParseTicket(*v.ticket, ""); err != nil {
			return refuseFlag(err)
		}
	}

	regateOpt, err := regateFlag(fs, v.regate)
	if err != nil {
		return err
	}

	f := bindFlags{
		name: *v.name, candidate: *v.candidate, mastermind: *v.mastermindFlag,
		resume: *v.resume, rebind: *v.rebind, timeout: *v.timeout, tier: *v.tier,
		allowYolo: *v.allowYolo, gate: *v.gate, noGate: *v.noGate, regate: regateOpt,
		feature: *v.feature, noFeature: *v.noFeature, ticket: *v.ticket,
		role: *v.actor, worktree: *v.worktree, cwd: *v.cwd,
		branch: *v.branch, server: *v.server, base: *v.base,
		local:  *v.local,
		asJSON: *v.asJSON,
	}

	route, rerr := bindRouteFor(f)
	if rerr != nil {
		return refuseFlag(rerr)
	}

	switch route {
	case routeAdd:
		return runAdd(f)
	default:
		return runBind(f)
	}
}

// refuseFlag is the one-line refusal the label rules and the route check
// share. The message is part of the error, so --json stderr carries it once
// instead of a stray human line plus an envelope.
func refuseFlag(err error) error {
	return fail(codeRefused, "%v", err)
}

// boundLine is the one line a successful bind prints. It is pure, so the wording
// is pinned without a live server.
func boundLine(b store.Binding, mastermind, candidate string, placement resolve.PlacementResolution) string {
	if b.Builder.Remote() {
		return fmt.Sprintf("bound %s: mastermind %s -> builder %s on %s, round %d%s",
			b.Name, mastermind, candidate, b.Builder.Server, b.Round, relevo.PlacementText(placement))
	}
	return fmt.Sprintf("bound %s: mastermind %s -> builder %s (%s), round %d",
		b.Name, mastermind, builderWhere(b.Builder), candidate, b.Round)
}

// runBind is bind's own body after parsing: bind the current tree, or resume
// or rebind an existing binding.
func runBind(f bindFlags) error {
	// --resume takes the name from --name, so a positional name is ignored.
	if f.resume && f.name == "" {
		return fail(codeUsage, "relevo bind --resume needs --name NAME (a positional name is ignored)")
	}
	if f.rebind && !f.resume {
		return fail(codeUsage, "relevo bind --rebind only applies with --resume (it replaces a gone builder on an existing binding)")
	}
	if f.role != "" && f.resume {
		return fail(codeUsage, "relevo bind --resume keeps the binding's actor; drop --actor")
	}

	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return writeError(fmt.Errorf("resolve working directory: %w", err))
	}

	opts := relevo.BindOptions{
		Name:         f.name,
		MasterMindID: f.mastermind,
		CWD:          cwd,
		Resume:       f.resume,
		Rebind:       f.rebind,
		RoundTimeout: f.timeout,
		Tier:         f.tier,
		AllowYolo:    f.allowYolo,
		Gate:         f.gate,
		NoGate:       f.noGate,
		Regate:       f.regate,
		Feature:      f.feature,
		NoFeature:    f.noFeature,
		Ticket:       f.ticket,
		Role:         f.role,
		Local:        f.local,
	}
	opts.Candidate = f.candidate

	adopted := f.resume
	roleName := roleOrBuilder(f.role)
	specRole := roleName
	kind := ""
	switch {
	case f.rebind:
		// A rebind replaces the builder of an existing binding, so the
		// definitions come from the stored binding's actor. If the load fails,
		// roleName stands as the fallback.
		if f.name != "" {
			if existing, err := rt.Store.Load(f.name); err == nil {
				specRole = existing.WriterRole()
			}
		}
		kind = resolve.CandidateKindFor(relevo.ResolveDeps(rt), opts.Candidate, specRole)
	case adopted:
		if f.name != "" {
			if existing, err := rt.Store.Load(f.name); err == nil {
				kind = existing.Builder.Kind
				specRole = existing.WriterRole()
			}
		}
	default:
		kind = resolve.CandidateKindFor(relevo.ResolveDeps(rt), opts.Candidate, roleName)
	}

	// Preflight is advisory only: it never blocks the bind.
	if kind != "" {
		env := doctor.NewEnv(rt.Store)
		// A Spec error is data: defs stays nil and the preflight falls back to
		// the shipped builder definitions.
		var defs []string
		if spec, err := rt.RoleRegistry().Spec(specRole, kind); err == nil {
			defs = spec.Definitions
		}
		for _, line := range doctor.BindPreflightDefs(context.Background(), env, kind, adopted, defs) {
			fmt.Fprintln(os.Stderr, line)
		}
	}

	b, res, err := relevo.BindResolved(context.Background(), rt, opts)
	if err != nil {
		return writeError(err)
	}
	// A resume keeps the binding's stored role, so the pick note names it.
	roleName = b.WriterRole()
	notices := noticeWriter(f.asJSON)
	// A resume's binding carries no fresh resolution, so the gate line names the
	// stored command.
	gateCmd, gateSource := res.Gate.Command, res.Gate.Source
	if f.resume {
		gateCmd, gateSource = b.Gate, resolve.GateSourceBinding
	}
	gateText := relevo.GateLine(gateCmd, gateSource)
	if t := relevo.RestoreText(res); t != "" {
		fmt.Fprintln(notices, t)
	}
	if res.WasPaused {
		fmt.Fprintf(notices, "resumed %s after pause\n", b.Name)
	}

	if f.resume && (f.candidate != "" || f.rebind) {
		builderDesc := builderWhere(b.Builder)
		if b.BuilderCandidate != "" {
			builderDesc = fmt.Sprintf("%s (%s)", builderWhere(b.Builder), rt.Candidates.NameOf(b.BuilderCandidate))
		}
		if !f.asJSON {
			fmt.Printf("rebound %s: builder %s, still on round %d\n"+
				"hand it the round with:\n"+
				"  relevo send --name %s --file %s\n",
				b.Name, builderDesc, b.Round, b.Name, rt.Store.PromptPath(b.Name, b.Round))
		}
		fmt.Fprintln(notices, gateText)
		noteRegateNoGate(notices, b)
		notePick(rt, roleName, res)
		warnWaitingOnYou(rt, b.Name)
		if f.asJSON {
			return printDoc(bindDocOf(b, cwd, roleName, candidateLabel(rt, b.BuilderCandidate), true))
		}
		return nil
	}

	if !f.asJSON {
		fmt.Println(boundLine(b, mastermindLabel(rt, b.MasterMindID), rt.Candidates.NameOf(b.BuilderCandidate), res.Placement))
	}
	fmt.Fprintln(notices, gateText)
	noteRegateNoGate(notices, b)
	if n := availability.GatedNote(relevo.AvailabilityDeps(rt), b.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(rt, roleName, res)
	warnWaitingOnYou(rt, b.Name)
	if f.asJSON {
		return printDoc(bindDocOf(b, cwd, roleName, candidateLabel(rt, b.BuilderCandidate), f.resume))
	}
	return nil
}

// runAdd is add's body after parsing, reached through `bind --worktree`
// or one of the placement flags.
func runAdd(f bindFlags) error {
	name := f.name
	branch := f.branch
	cwd := f.cwd
	if branch != "" && cwd != "" {
		return fail(codeRefused, "relevo bind --branch and --cwd are exclusive")
	}
	if name == "" {
		if branch == "" {
			return fail(codeUsage, "relevo bind requires --name NAME")
		}
		derived, err := relevo.DefaultBindingName(branch)
		if err != nil {
			return fail(codeUsage, "relevo bind --branch %s: cannot derive a binding name (%v); pass --name", branch, err)
		}
		name = derived
	}

	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}

	repo, err := os.Getwd()
	if err != nil {
		return writeError(fmt.Errorf("resolve working directory: %w", err))
	}

	res, err := relevo.Add(context.Background(), rt, relevo.AddOptions{
		Name:         name,
		Candidate:    f.candidate,
		MasterMindID: f.mastermind,
		Repo:         repo,
		CWD:          cwd,
		Branch:       branch,
		Server:       f.server,
		Base:         f.base,
		Local:        f.local,
		Tier:         f.tier,
		AllowYolo:    f.allowYolo,
		Gate:         f.gate,
		NoGate:       f.noGate,
		Regate:       f.regate,
		Feature:      f.feature,
		Ticket:       f.ticket,
		Role:         f.role,
	})
	if err != nil {
		return writeError(err)
	}

	notices := noticeWriter(f.asJSON)
	if !f.asJSON {
		switch {
		case res.Binding.Builder.Remote():
			fmt.Printf("added %s: builder %s on %s\n", res.Binding.Name, rt.Candidates.NameOf(res.Binding.BuilderCandidate), res.Binding.Builder.Server)
			if res.Binding.Tier != "" {
				fmt.Printf("  tier %s (server)\n", res.Binding.Tier)
			} else {
				fmt.Printf("  tier server's choice (pre-tier server)\n")
			}
		case res.Binding.Builder.Headless():
			fmt.Printf("added %s: builder %s (headless)\n", res.Binding.Name, rt.Candidates.NameOf(res.Binding.BuilderCandidate))
		default:
			fmt.Printf("added %s: builder %s in %s\n",
				res.Binding.Name, rt.Candidates.NameOf(res.Binding.BuilderCandidate), builderWhere(res.Binding.Builder))
		}
	}
	fmt.Fprintln(notices, relevo.GateLine(res.Resolution.Gate.Command, res.Resolution.Gate.Source))
	noteRegateNoGate(notices, res.Binding)
	if n := availability.GatedNote(relevo.AvailabilityDeps(rt), res.Binding.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(rt, roleOrBuilder(f.role), res.Resolution)
	if !f.asJSON {
		for _, line := range addedTreeLines(res) {
			fmt.Println(line)
		}
		fmt.Printf("  relevo send --name %s --file <plan.md>\n", res.Binding.Name)
	}
	warnWaitingOnYou(rt, res.Binding.Name)

	if f.asJSON {
		return printDoc(bindDocOf(res.Binding, housekeep.RoundDir(res.Binding), res.Binding.WriterRole(), candidateLabel(rt, res.Binding.BuilderCandidate), false))
	}

	return nil
}

// addedTreeLines is runAdd's own round-directory lines.
func addedTreeLines(res relevo.AddResult) []string {
	var lines []string
	switch {
	case res.Binding.Builder.Remote() && res.Binding.ExistingBranch:
		lines = append(lines, fmt.Sprintf("  branch %s (existing, tip %s) on %s", res.Binding.Branch, res.Base, res.Binding.Builder.Server))
	case res.Binding.Builder.Remote():
		lines = append(lines, fmt.Sprintf("  branch %s (from %s)", res.Binding.Branch, res.Base))
	case res.Worktree != "" && res.Binding.ExistingBranch:
		lines = append(lines, fmt.Sprintf("  worktree %s on existing branch %s (tip %s)", res.Worktree, res.Branch, res.Base))
	case res.Worktree != "":
		lines = append(lines, fmt.Sprintf("  worktree %s on %s (from %s)", res.Worktree, res.Branch, res.Base))
	default:
		lines = append(lines, fmt.Sprintf("  tree %s", res.Binding.CWD))
	}
	// Only a provisioned tree can differ from where the round runs.
	if res.Binding.Subdir != "" {
		lines = append(lines, fmt.Sprintf("  round runs in %s", housekeep.RoundDir(res.Binding)))
	}
	return lines
}
