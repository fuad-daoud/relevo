package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/setup"
)

// cmdInit seeds the candidates, policy and actors sections from the harness
// binaries on PATH, then installs the agent definitions for those harnesses.
//
// initFlagValues holds the pointers init's flags parse into. initFlagSet
// defines them on fs; cmdInit and TestRemovedFlagsAreUnknown read the same
// surface (A4-1a).
type initFlagValues struct {
	force    *bool
	noAgents *bool
	asJSON   *bool
}

// initFlagSet defines init's flags on fs and returns the values they parse
// into, so a test can inspect the flag surface without writing any config.
func initFlagSet(fs *flag.FlagSet) *initFlagValues {
	v := &initFlagValues{}
	v.force = fs.Bool("force", false, "overwrite the existing candidates / policy sections")
	v.noAgents = fs.Bool("no-agents", false, "do not install agent definitions")
	v.asJSON = fs.Bool("json", false, "print the document the init produced")
	return v
}

func cmdInit(args []string) error {
	return outcomeError(cmdInitRun(args))
}

func cmdInitRun(args []string) error {
	fs := flag.NewFlagSet("relevo config init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	v := initFlagSet(fs)
	force, noAgents, asJSON := v.force, v.noAgents, v.asJSON
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	rt, err := newRuntime()
	if err != nil {
		return err
	}

	env, err := agentInstallEnv()
	if err != nil {
		return err
	}

	files, tier, err := initProbeFiles(env, rt, *force)
	if err != nil {
		return err
	}

	// One PutDoc writes all three sections as one revision (A2 round 2 R5).
	doc := map[config.Section]json.RawMessage{
		config.Candidates: files.Candidates,
		config.Policy:     files.Policy,
		config.Actors:     files.Actors,
	}
	warnings, err := rt.Config.As("init", "config init").PutDoc(doc)
	if err != nil {
		return err
	}

	// The human lines keep stdout in the default mode and move to stderr under
	// --json, where stdout carries the document alone.
	w := noticeWriter(*asJSON)

	_, _ = fmt.Fprintf(w, "wrote candidates (%d: %s)\n", len(files.CandidateNames), strings.Join(files.CandidateNames, ", "))
	summary, err := actorSummary(files.Actors, files.ActorOrder)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(w, "wrote actors (%s)\n", summary)
	_, _ = fmt.Fprintln(w, tierLine(tier))

	if err := installInitAgents(env, files, w, *noAgents); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(w, "next: edit the model names, then run: relevo doctor\n")

	if *asJSON {
		version, err := rt.Config.Version()
		if err != nil {
			return err
		}
		return printDoc(configImportDocOf(importSections(doc), warnings, version))
	}
	return nil
}

// initProbeFiles probes the harnesses and builds the seed files, refusing a
// configured store without --force: the seed would overwrite it.
func initProbeFiles(env setup.SetupEnv, rt relevo.Runtime, force bool) (setup.Files, string, error) {
	inv := setup.Probe(env)
	rec := setup.RecommendedAnswers(inv, nil)
	files, err := setup.Build(inv, rec)
	if err != nil {
		return setup.Files{}, "", initBuildError(err)
	}
	tier := ""
	if t := rec["tier"]; len(t) == 1 {
		tier = t[0]
	}

	hasCandidates, err := rt.Config.Has(config.Candidates)
	if err != nil {
		return setup.Files{}, "", err
	}
	hasPolicy, err := rt.Config.Has(config.Policy)
	if err != nil {
		return setup.Files{}, "", err
	}
	hasActors, err := rt.Config.Has(config.Actors)
	if err != nil {
		return setup.Files{}, "", err
	}
	if !force && (hasCandidates || hasPolicy || hasActors) {
		return setup.Files{}, "", fail(codeConflict, "candidates, policy or actors already configured; pass --force to overwrite")
	}
	return files, tier, nil
}

// installInitAgents installs the probed kinds' agent definitions after a
// config init, reporting each result line. A failed install fails the init.
func installInitAgents(env setup.SetupEnv, files setup.Files, w io.Writer, noAgents bool) error {
	if noAgents {
		return nil
	}
	failed := false
	for _, kind := range files.Kinds {
		results, err := harness.Install(env, harness.InstallOptions{Kind: kind})
		if err != nil {
			return err
		}
		for _, r := range results {
			_, _ = fmt.Fprintln(w, r.Line())
			if r.Outcome == harness.OutcomeError {
				failed = true
			}
		}
	}
	if failed {
		return fail(codeInternal, "one or more agent definitions failed to install")
	}
	return nil
}

// initBuildError classifies what Build refused: nothing is logged in is the
// machine's own state, so it is not_available with the login commands the
// message already names, and the wizard as the way back in.
func initBuildError(err error) error {
	var na *setup.NotAvailableError
	if errors.As(err, &na) {
		return failNext(codeNotAvailable, "relevo setup", "%s", na.Message)
	}
	return err
}

// tierLine is init's tier notice: what the tier it seeded lets a builder do,
// followed by the way to restrict it.
func tierLine(tier string) string {
	why := "builders run commands without asking"
	if tier == "edit" {
		why = "only codex builders can bind; claude, agy and opencode builders are refused at edit"
	}
	return fmt.Sprintf("tier: %s (%s) -- relevo config set policy.max_tier edit to restrict", tier, why)
}

// actorSummary renders every actor in order as the text inside the `wrote
// actors (...)` line: `builder: a, b; planner: c`. An actor with no candidates
// renders as `builder: none`. Each actor's names come from parsing the actors
// section Plan wrote.
func actorSummary(actors []byte, order []string) (string, error) {
	set, _, err := roles.ParseActors(actors)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		a, ok := set[name]
		if !ok {
			continue
		}
		names := make([]string, 0, len(a.Candidates))
		for _, e := range a.Candidates {
			names = append(names, e.Candidate)
		}
		rendered := strings.Join(names, ", ")
		if len(names) == 0 {
			rendered = "none"
		}
		parts = append(parts, name+": "+rendered)
	}
	return strings.Join(parts, "; "), nil
}
