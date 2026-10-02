package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
)

// planSlice collects the repeatable --plan flag in the order it was given: a
// chain's plans are ordered, so the order the flags appear is the order they
// run.
type planSlice []string

func (p *planSlice) String() string { return strings.Join(*p, ",") }

func (p *planSlice) Set(val string) error {
	*p = append(*p, val)
	return nil
}

// paramSlice collects the repeatable --param k=v flag; parseChainParams turns
// it into the map a workflow takes.
type paramSlice []string

func (p *paramSlice) String() string { return strings.Join(*p, ",") }

func (p *paramSlice) Set(val string) error {
	*p = append(*p, val)
	return nil
}

// chainFlagValues holds the pointers `relevo chain` parses into.
type chainFlagValues struct {
	name           *string
	plans          *planSlice
	feature        *string
	noFeature      *bool
	resume         *bool
	ticket         *string
	base           *string
	gate           *string
	noGate         *bool
	regate         *int
	maxCorrections *int
	reviewerActor  *string
	plannerActor   *string
	securityActor  *string
	security       *bool
	noSecurity     *bool
	mastermind     *string
	asJSON         *bool
	server         *string
	workflow       *string
	params         *paramSlice
	task           *string
	taskFile       *string
	dryRun         *bool
	from           *string
}

// chainFlagSet defines chain's flags on fs and returns what they parse into,
// so the registry's parity test finds exactly one installer per verb.
func chainFlagSet(fs *flag.FlagSet) *chainFlagValues {
	v := &chainFlagValues{}
	v.plans = &planSlice{}
	v.name = fs.String("name", "", "chain name (also its builder member's name); at most 27 characters")
	fs.Var(v.plans, "plan", "a plan file the chain runs, in order; repeatable, at least one")
	v.feature = fs.String("feature", "", "label grouping every member with others; a chain needs exactly one of --feature or --no-feature")
	v.noFeature = fs.Bool("no-feature", false, "record that this chain is not a feature; a chain needs exactly one of --feature or --no-feature")
	v.resume = fs.Bool("resume", false, "continue a halted or stopped chain: re-run the step it stopped on, applying any settings flags")
	v.ticket = fs.String("ticket", "", "the issue this chain serves: N, #N, owner/repo#N, or a .../issues/N URL")
	v.base = fs.String("base", "", "commit or ref to cut the builder's worktree from; defaults to HEAD")
	v.gate = fs.String("gate", "", "acceptance command the builder runs on the round's completion marker (default: config policy gate.default)")
	v.noGate = fs.Bool("no-gate", false, "opt the builder out of config policy's gate.default")
	v.regate = fs.Int("regate", -1, "after a failing gate, open up to N automatic repair rounds; 0 disables (default: config policy gate.regate)")
	v.maxCorrections = fs.Int("max-corrections", -1, "correction rounds allowed per plan before NEEDS YOU; 0 halts on the first changes (default: config policy chain.max_corrections)")
	v.reviewerActor = fs.String("reviewer-actor", "", "the actor the reviewer member runs (default: config policy chain.reviewer_actor)")
	v.plannerActor = fs.String("planner-actor", "", "the actor that writes correction and fix plans (default: config policy chain.planner_actor)")
	v.securityActor = fs.String("security-actor", "", "the actor the security member runs (default: config policy chain.security_actor)")
	v.security = fs.Bool("security", false, "run the chain's security phase")
	v.noSecurity = fs.Bool("no-security", false, "do not run the chain's security phase")
	v.mastermind = fs.String("mastermind", "", "act as this mastermind (id or name; default: $RELEVO_MASTERMIND, else this session's host)")
	v.asJSON = fs.Bool("json", false, "print the chain as a JSON document")
	v.server = fs.String("server", "", "run the whole chain on this server; it continues when this machine is off")
	v.workflow = fs.String("workflow", "", "run this workflow: a saved or shipped name, or a YAML/JSON file")
	v.params = &paramSlice{}
	fs.Var(v.params, "param", "fill a workflow param as key=value; repeatable")
	v.task = fs.String("task", "", "the chain's task input text")
	v.taskFile = fs.String("task-file", "", "read the chain's task input from this file")
	v.dryRun = fs.Bool("dry-run", false, "resolve and validate the workflow and print its graph; start nothing")
	v.from = fs.String("from", "", "re-enter a halted chain at this step; with --resume")
	return v
}

// ChainDoc is `relevo chain --json`: the chain it started, its members, how
// many plans it holds, and the state it begins in.
type ChainDoc struct {
	Name    string           `json:"name"`
	Members []ChainMemberDoc `json:"members"`
	Plans   int              `json:"plans"`
	Status  string           `json:"status"`
	Step    string           `json:"step"`
	// Check is the builder member's resolved acceptance command, rendered
	// "none" when it ran no check.
	Check string `json:"check"`
}

// ChainMemberDoc is one member in the document: its binding name, the actor it
// runs and where it runs.
type ChainMemberDoc struct {
	Name  string `json:"name"`
	Actor string `json:"actor"`
	// Placement names where the member runs: "local", or the server a remote
	// member's builder endpoint names.
	Placement string `json:"placement"`
}

// cmdChain starts a chain, or resumes a halted or stopped one. Every refusal it
// can make on its own flags runs before newRuntime, so a bad invocation touches
// no state directory.
func cmdChain(args []string) error {
	fs := flag.NewFlagSet("relevo chain", flag.ContinueOnError)
	v := chainFlagSet(fs)
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	// --resume takes the name (a positional is not a name) and only the
	// settings flags; a start takes the plans and the label too.
	if *v.resume {
		opts, err := chainResumeOptions(fs, v)
		if err != nil {
			return err
		}
		rt, err := newRuntime()
		if err != nil {
			return writeError(err)
		}
		res, err := relevo.ChainResume(context.Background(), rt, opts)
		if err != nil {
			return writeError(err)
		}
		if *v.asJSON {
			return printDoc(chainDocOf(res))
		}
		chainResumedText(res)
		return nil
	}

	opts, err := chainOptions(fs, v)
	if err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return writeError(err)
	}
	if opts.DryRun {
		return chainDryRun(context.Background(), rt, opts)
	}
	res, err := relevo.ChainStart(context.Background(), rt, opts)
	if err != nil {
		return writeError(err)
	}

	if *v.asJSON {
		return printDoc(chainDocOf(res))
	}
	chainStartedText(rt, res)
	return nil
}

// chainOptions turns the parsed flags into a start request, refusing the
// combinations the verb's contract makes invalid: exactly one feature flag, a
// named chain (a positional is not a name), at least one non-empty plan, and
// one security flag. A dry run skips the start-input requirements, because the
// workflow's own inputs decide what it needs and the dry run validates them.
func chainOptions(fs *flag.FlagSet, v *chainFlagValues) (relevo.ChainOptions, error) {
	if *v.name == "" {
		return relevo.ChainOptions{}, fail(codeUsage, "relevo chain needs --name NAME")
	}
	if *v.security && *v.noSecurity {
		return relevo.ChainOptions{}, fail(codeUsage, "--security and --no-security are exclusive")
	}
	if *v.task != "" && *v.taskFile != "" {
		return relevo.ChainOptions{}, fail(codeUsage, "--task and --task-file are exclusive")
	}
	params, err := parseChainParams(v.params)
	if err != nil {
		return relevo.ChainOptions{}, err
	}
	if *v.server != "" && (*v.workflow != "" || *v.task != "" || *v.taskFile != "" || *v.from != "" || len(params) > 0) {
		return relevo.ChainOptions{}, fail(codeUsage, "relevo chain --server does not take --workflow, --param, --task or --from: the server's workflow feature is not available yet")
	}
	if *v.from != "" {
		return relevo.ChainOptions{}, fail(codeUsage, "relevo chain --from applies with --resume: it re-enters a halted chain at another step")
	}
	maxCorrections, err := chainIntFlag(fs, "max-corrections", *v.maxCorrections)
	if err != nil {
		return relevo.ChainOptions{}, err
	}
	regate, err := regateFlag(fs, v.regate)
	if err != nil {
		return relevo.ChainOptions{}, err
	}
	task, err := chainTaskBody(v)
	if err != nil {
		return relevo.ChainOptions{}, err
	}

	opts := relevo.ChainOptions{
		Name:           *v.name,
		Plans:          append([]string(nil), (*v.plans)...),
		Feature:        *v.feature,
		NoFeature:      *v.noFeature,
		Ticket:         *v.ticket,
		Base:           *v.base,
		Gate:           *v.gate,
		NoGate:         *v.noGate,
		Regate:         regate,
		MaxCorrections: maxCorrections,
		ReviewerActor:  *v.reviewerActor,
		PlannerActor:   *v.plannerActor,
		SecurityActor:  *v.securityActor,
		MasterMindID:   *v.mastermind,
		Server:         *v.server,
		Workflow:       *v.workflow,
		Params:         params,
		Task:           task,
		DryRun:         *v.dryRun,
	}
	// --security/--no-security are exclusive (refused above), so an explicit
	// value is whichever flag was given; neither leaves the policy's own.
	if chainFlagGiven(fs, "security") || chainFlagGiven(fs, "no-security") {
		opts.Security = v.security
	}
	if opts.DryRun {
		return opts, nil
	}
	if err := relevo.RequireFeatureChoice(*v.feature, *v.noFeature, false); err != nil {
		return relevo.ChainOptions{}, refuseFlag(err)
	}
	if *v.feature != "" {
		if err := store.ValidFeature(*v.feature); err != nil {
			return relevo.ChainOptions{}, refuseFlag(err)
		}
	}
	if *v.ticket != "" {
		if _, err := store.ParseTicket(*v.ticket, ""); err != nil {
			return relevo.ChainOptions{}, refuseFlag(err)
		}
	}
	if len(*v.plans) == 0 {
		return relevo.ChainOptions{}, fail(codeUsage, "relevo chain needs at least one --plan <file>")
	}
	for _, path := range *v.plans {
		if strings.TrimSpace(path) == "" {
			return relevo.ChainOptions{}, fail(codeUsage, "--plan needs a file path")
		}
	}
	return opts, nil
}

// parseChainParams reads the repeatable --param k=v flag into the map a
// workflow takes. An entry with no key or no equals sign is a usage error.
func parseChainParams(params *paramSlice) (map[string]string, error) {
	if params == nil || len(*params) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(*params))
	for _, entry := range *params {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fail(codeUsage, "--param wants key=value, got %q", entry)
		}
		out[strings.TrimSpace(key)] = value
	}
	return out, nil
}

// chainTaskBody is the chain's task input: --task's own text, or the contents
// of --task-file when it named one. The two are refused together before either
// is read.
func chainTaskBody(v *chainFlagValues) (string, error) {
	if *v.taskFile == "" {
		return *v.task, nil
	}
	body, err := os.ReadFile(*v.taskFile)
	if err != nil {
		return "", fail(codeUsage, "read task file %s: %v", *v.taskFile, err)
	}
	return string(body), nil
}

// chainResumeOptions turns the parsed flags into a resume request: the chain's
// name, and only the settings whose flags were given, so every unflagged
// setting keeps the value the chain was started with. A resume names no plan
// and no label of its own -- the chain already holds both -- so the
// feature-choice rule is the resume one.
func chainResumeOptions(fs *flag.FlagSet, v *chainFlagValues) (relevo.ResumeOptions, error) {
	if *v.name == "" {
		return relevo.ResumeOptions{}, fail(codeUsage, "relevo chain --resume needs --name NAME")
	}
	if *v.server != "" {
		return relevo.ResumeOptions{}, fail(codeUsage, "relevo chain --resume does not take --server: a resume finds its server on the chain")
	}
	if err := relevo.RequireFeatureChoice(*v.feature, *v.noFeature, true); err != nil {
		return relevo.ResumeOptions{}, refuseFlag(err)
	}
	if *v.security && *v.noSecurity {
		return relevo.ResumeOptions{}, fail(codeUsage, "--security and --no-security are exclusive")
	}
	// A start's own flags: --resume continues a chain that already holds its
	// plans, its label, its workflow and its mastermind, so these have nothing
	// to act on. Silently ignoring one would leave the caller believing it took
	// effect, so each is refused by name -- before any runtime is built (round
	// 6's review; --mastermind is the same dead flag the sweep found).
	for _, name := range []string{"plan", "base", "ticket", "feature", "no-feature", "mastermind", "workflow", "task", "task-file", "dry-run"} {
		if chainFlagGiven(fs, name) {
			return relevo.ResumeOptions{}, fail(codeUsage, "relevo chain --resume does not take --%s: a resume continues the chain it names, which already holds its plans, label, workflow and mastermind", name)
		}
	}
	params, err := parseChainParams(v.params)
	if err != nil {
		return relevo.ResumeOptions{}, err
	}
	maxCorrections, err := chainIntFlag(fs, "max-corrections", *v.maxCorrections)
	if err != nil {
		return relevo.ResumeOptions{}, err
	}
	regate, err := regateFlag(fs, v.regate)
	if err != nil {
		return relevo.ResumeOptions{}, err
	}

	opts := relevo.ResumeOptions{
		Name:           *v.name,
		MaxCorrections: maxCorrections,
		ReviewerActor:  *v.reviewerActor,
		PlannerActor:   *v.plannerActor,
		SecurityActor:  *v.securityActor,
		Gate:           *v.gate,
		NoGate:         *v.noGate,
		Regate:         regate,
		From:           *v.from,
		Params:         params,
	}
	// --security/--no-security are exclusive (refused above), so an explicit
	// value is whichever flag was given; neither leaves the stored setting.
	if chainFlagGiven(fs, "security") || chainFlagGiven(fs, "no-security") {
		opts.Security = v.security
	}
	return opts, nil
}

// chainIntFlag turns a chain integer flag into the *int the chain takes: its
// -1 default means "not given", which becomes nil so the policy stands; any
// value the human typed must be >= 0.
func chainIntFlag(fs *flag.FlagSet, name string, value int) (*int, error) {
	if !chainFlagGiven(fs, name) {
		return nil, nil
	}
	if value < 0 {
		return nil, fail(codeUsage, "--%s must be >= 0, got %d", name, value)
	}
	return &value, nil
}

// chainFlagGiven reports whether the named flag was set on the command line.
func chainFlagGiven(fs *flag.FlagSet, name string) bool {
	given := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			given = true
		}
	})
	return given
}

// chainDocOf is chain's document.
func chainDocOf(res relevo.ChainResult) ChainDoc {
	doc := ChainDoc{
		Name:   res.Chain.Name,
		Plans:  res.Plans,
		Status: res.Chain.Status,
		Step:   res.Chain.Step,
		Check:  chainCheckText(res.Check),
	}
	for _, m := range res.Members {
		doc.Members = append(doc.Members, ChainMemberDoc{
			Name:      m.Name,
			Actor:     relevo.BindingRole(m),
			Placement: chainMemberPlacement(m),
		})
	}
	return doc
}

// chainMemberPlacement names where a member runs: the server a remote member's
// builder endpoint names, or "local" for every member that runs here.
func chainMemberPlacement(m store.Binding) string {
	if m.Builder.Server != "" {
		return m.Builder.Server
	}
	return "local"
}

// chainPartOf names the part a member fills, from the chain row's columns.
func chainPartOf(row db.ChainRow, name string) string {
	switch name {
	case row.Builder:
		return "builder"
	case row.Reviewer:
		return "reviewer"
	case row.Planner:
		return "planner"
	case row.Security:
		return "security"
	}
	return ""
}

// chainStartedText is what a start prints for a human: the chain, its members
// with where each runs and the command that watches it.
func chainStartedText(rt relevo.Runtime, res relevo.ChainResult) {
	fmt.Printf("started chain %s: %d plan(s), status %s\n",
		res.Chain.Name, res.Plans, res.Chain.Status)
	builderPlacement := ""
	for _, m := range res.Members {
		actor := relevo.BindingRole(m)
		if m.BuilderCandidate != "" {
			actor = fmt.Sprintf("%s (%s)", actor, candidateLabel(rt, m.BuilderCandidate))
		}
		placement := chainMemberPlacement(m)
		if m.Name == res.Chain.Builder {
			builderPlacement = placement
		}
		fmt.Printf("  %-8s %-16s %-8s %s\n", chainPartOf(res.Chain, m.Name), m.Name, placement, actor)
	}
	fmt.Printf("  check: %s\n", chainCheckText(res.Check))
	if res.Chain.Worktree != "" {
		fmt.Printf("  worktree %s on %s (from %s)\n", res.Chain.Worktree, res.Chain.Branch, res.Chain.Base)
	} else {
		fmt.Printf("  builder %s on %s · branch %s · from %s\n",
			res.Chain.Builder, builderPlacement, res.Chain.Branch, res.Chain.Base)
	}
	fmt.Printf("  relevo wait --name %s\n", res.Chain.Name)
}

// chainCheckText renders a resolved check for a human and the document: "none"
// when the builder ran no check, the command itself otherwise.
func chainCheckText(check string) string {
	if check == "" {
		return "none"
	}
	return check
}

// chainResumedText is what a resume prints for a human: the chain's state now,
// the round it awaits, where each member runs and the command that watches it.
func chainResumedText(res relevo.ChainResult) {
	c := res.Chain
	fmt.Printf("resumed chain %s: status %s, step %s\n",
		c.Name, c.Status, c.Step)
	for _, m := range res.Members {
		fmt.Printf("  %-8s %-16s %-8s %s\n", chainPartOf(c, m.Name), m.Name, chainMemberPlacement(m), relevo.BindingRole(m))
	}
	fmt.Printf("  awaiting %s round %d\n", c.AwaitingMember, c.AwaitingRound)
	fmt.Printf("  relevo wait --name %s\n", c.Name)
}
