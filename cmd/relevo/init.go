package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
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

	files, err := setup.Plan(env)
	if err != nil {
		return err
	}

	hasCandidates, err := rt.Config.Has(config.Candidates)
	if err != nil {
		return err
	}
	hasPolicy, err := rt.Config.Has(config.Policy)
	if err != nil {
		return err
	}
	hasActors, err := rt.Config.Has(config.Actors)
	if err != nil {
		return err
	}
	if !*force && (hasCandidates || hasPolicy || hasActors) {
		return fail(codeConflict, "candidates, policy or actors already configured; pass --force to overwrite")
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

	fmt.Fprintf(w, "wrote candidates (%d: %s)\n", len(files.CandidateNames), strings.Join(files.CandidateNames, ", "))
	summary, err := actorSummary(files.Actors, files.ActorOrder)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote actors (%s)\n", summary)
	actorSet, _, err := roles.ParseActors(files.Actors)
	if err != nil {
		return err
	}
	if len(actorSet["builder"].Candidates) == 0 {
		fmt.Fprintf(w, "note: no builder candidate (claude only plans); add one from a builder harness (%s): relevo config set actors.builder.candidates '[\"<name>\"]'\n", strings.Join(setup.BuilderKinds(), ", "))
	}

	if !*noAgents {
		failed := false
		for _, kind := range files.Kinds {
			results, err := harness.Install(env, harness.InstallOptions{Kind: kind})
			if err != nil {
				return err
			}
			for _, r := range results {
				fmt.Fprintln(w, r.Line())
				if r.Outcome == harness.OutcomeError {
					failed = true
				}
			}
		}
		if failed {
			return fail(codeInternal, "one or more agent definitions failed to install")
		}
	}

	fmt.Fprintf(w, "next: edit the model names, then run: relevo doctor\n")

	if *asJSON {
		version, err := rt.Config.Version()
		if err != nil {
			return err
		}
		return printDoc(configImportDocOf(importSections(doc), warnings, version))
	}
	return nil
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
