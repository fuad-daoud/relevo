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
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// bindFlags is the union of the flags today's bind and add each accept.
// bindRouteFor chooses which of the two bodies runs; keeping the flag set in
// one place is what lets each old flag keep its name, default and help text
// (§4.1).
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

	// local runs the builder on this machine whatever the actor's placement
	// says. It is not a route flag: a plain `bind --local` stays bind, and
	// --server with --local is refused below.
	local bool

	// asJSON selects the result document instead of the human lines; stdout
	// then carries the document alone and the notices move to stderr (§2.1).
	asJSON bool
}

// bindRoute names which of the three merged paths cmdBind runs.
type bindRoute int

const (
	routeBind bindRoute = iota
	routeAdd
)

// bindRouteFor chooses the path from the parsed flags and refuses the
// combinations §4.1 forbids. It is a pure function, so the rules are table
// tested without a runtime, a state directory or a harness.
func bindRouteFor(f bindFlags) (bindRoute, error) {
	placement := f.worktree || f.cwd != "" || f.branch != "" || f.server != "" || f.base != ""
	switch {
	case f.server != "" && f.local:
		// --server and --local name two different machines; the two together
		// are refused on one line like every other route conflict.
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

// cmdBind binds a mastermind to a builder. It is the one entry point the old
// bind and add merged into (§4.1): the flags choose which of the two
// bodies runs, and each body stays an unexported helper so none of its logic
// is duplicated.
// bindFlagValues holds the pointers bind's flags parse into. bindFlagSet
// defines them on fs; cmdBind and TestBindFlagsHaveActorNotRole read the same
// surface, so the flag names can never drift from what a test pins (A2 round 3
// S1).
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

// bindFlagSet defines bind's flags on fs and returns the values they parse
// into. It is separate from cmdBind so a test can inspect the flag surface
// without running a bind (A2 round 3 S1).
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
	v.ticket = fs.String("ticket", "", "the issue this binding serves: N, #N, owner/repo#N, or a .../issues/N URL (allowed with --no-feature)")
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

func cmdBind(args []string) error {
	fs := flag.NewFlagSet("bind", flag.ContinueOnError)
	v := bindFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// #637: the label rules are CLI rules, and they are refused on one line
	// with exit 2 before any runtime, state, worktree or network work -- the
	// same shape as the route refusal below.
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
		// One line, exit 2, before any runtime is built (§4.1).
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
// share: a refused code, which the frame renders with exit 2 and no next hint.
// The message is part of the error, so --json stderr carries it once instead
// of a stray human line plus an envelope.
func refuseFlag(err error) error {
	return fail(codeRefused, "%v", err)
}

// runBind is bind's own body after parsing: bind the current tree, or resume
// or rebind an existing binding (the old cmdBind).
func runBind(f bindFlags) error {
	// --resume takes the name from --name, so a positional one is dropped on
	// the floor and the binding lookup then fails on the empty name.
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
		// definitions come from the stored binding's actor -- --actor is
		// refused with --resume, so roleName is "builder" here (#382 round 3).
		// If the load fails, today's behaviour (roleName) stands.
		if f.name != "" {
			if existing, err := rt.Store.Load(f.name); err == nil {
				specRole = relevo.BindingRole(existing)
			}
		}
		kind = relevo.CandidateKindFor(rt, opts.Candidate, specRole)
	case adopted:
		if f.name != "" {
			if existing, err := rt.Store.Load(f.name); err == nil {
				kind = existing.Builder.Kind
				specRole = relevo.BindingRole(existing)
			}
		}
	default:
		kind = relevo.CandidateKindFor(rt, opts.Candidate, roleName)
	}

	// Preflight is advisory only: it never blocks the bind, and any probe
	// failure is dropped rather than printed. See bindPreflight.
	if kind != "" {
		env := doctor.NewEnv(rt.Store)
		// The binding's role's definitions for this kind come from the
		// registry, so a config roles that names a custom builder executor
		// preflights that file (#374 §3.4). A Spec error is data: defs stays
		// nil and the preflight falls back to the shipped builder definitions.
		var defs []string
		if spec, err := rt.RoleRegistry().Spec(specRole, kind); err == nil {
			defs = spec.Definitions
		}
		for _, line := range bindPreflightDefs(context.Background(), env, kind, adopted, defs) {
			fmt.Fprintln(os.Stderr, line)
		}
	}

	b, res, err := relevo.BindResolved(context.Background(), rt, opts)
	if err != nil {
		return writeError(err)
	}
	// A resume keeps the binding's stored role, so the pick note names it.
	roleName = relevo.BindingRole(b)
	// Every supplementary line goes to the human default's stdout, or to
	// stderr under --json, where stdout must carry the document alone (§2.1).
	notices := noticeWriter(f.asJSON)
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
		noteRegateNoGate(notices, b)
		notePick(rt, roleName, res)
		warnWaitingOnYou(rt, b.Name)
		if f.asJSON {
			return printDoc(bindDocOf(b, cwd, roleName, candidateLabel(rt, b.BuilderCandidate), true))
		}
		return nil
	}

	if !f.asJSON {
		fmt.Printf("bound %s: mastermind %s -> builder %s (%s), round %d\n",
			b.Name, b.MasterMind.PaneID, builderWhere(b.Builder), rt.Candidates.NameOf(b.BuilderCandidate), b.Round)
	}
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

// runAdd is add's body after parsing (the old cmdAdd), reached through
// `bind --worktree` or one of the placement flags (§4.1).
func runAdd(f bindFlags) error {
	name := f.name
	branch := f.branch
	cwd := f.cwd

	// Before newRuntime, in this order: the flag pair, then a name that is
	// either given or derivable from the branch.
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
	noteRegateNoGate(notices, res.Binding)
	if n := availability.GatedNote(relevo.AvailabilityDeps(rt), res.Binding.BuilderCandidate); n != "" {
		fmt.Fprintln(os.Stderr, n)
	}
	notePick(rt, roleOrBuilder(f.role), res.Resolution)
	if !f.asJSON {
		switch {
		case res.Binding.Builder.Remote() && res.Binding.ExistingBranch:
			fmt.Printf("  branch %s (existing, tip %s) on %s\n", res.Binding.Branch, res.Base, res.Binding.Builder.Server)
		case res.Binding.Builder.Remote():
			fmt.Printf("  branch %s (from %s)\n", res.Binding.Branch, res.Base)
		case res.Worktree != "" && res.Binding.ExistingBranch:
			fmt.Printf("  worktree %s on existing branch %s (tip %s)\n", res.Worktree, res.Branch, res.Base)
		case res.Worktree != "":
			fmt.Printf("  worktree %s on %s (from %s)\n", res.Worktree, res.Branch, res.Base)
		default:
			fmt.Printf("  tree %s\n", res.Binding.CWD)
		}
		fmt.Printf("  relevo send --name %s --file <plan.md>\n", res.Binding.Name)
	}
	warnWaitingOnYou(rt, res.Binding.Name)

	if f.asJSON {
		dir := res.Worktree
		if dir == "" {
			dir = res.Binding.CWD
		}
		return printDoc(bindDocOf(res.Binding, dir, relevo.BindingRole(res.Binding), candidateLabel(rt, res.Binding.BuilderCandidate), false))
	}

	return nil
}
