package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/setup"
)

type initFlagValues struct {
	force    *bool
	noAgents *bool
	asJSON   *bool
}

// initFlagSet defines init's flags, so cmdInit and the tests read one flag
// surface.
func initFlagSet(fs *flag.FlagSet) *initFlagValues {
	v := &initFlagValues{}
	v.force = fs.Bool("force", false, "overwrite the existing candidates / policy sections")
	v.noAgents = fs.Bool("no-agents", false, "do not install agent definitions")
	v.asJSON = fs.Bool("json", false, "print the document the init produced")
	return v
}

// cmdInit seeds the candidates, policy and actors sections from the harness
// binaries on PATH, then installs the agent definitions.
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

	inv := setup.Probe(env)
	rec := setup.RecommendedAnswers(inv, nil)
	files, err := setup.Build(inv, rec)
	if err != nil {
		return initBuildError(err)
	}
	tier := ""
	if t := rec["tier"]; len(t) == 1 {
		tier = t[0]
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

	// One PutDoc writes all three sections as one revision.
	doc := map[config.Section]json.RawMessage{
		config.Candidates: files.Candidates,
		config.Policy:     files.Policy,
		config.Actors:     files.Actors,
	}
	warnings, err := rt.Config.As("init", "config init").PutDoc(doc)
	if err != nil {
		return err
	}

	// Human lines move to stderr under --json, where stdout carries the document
	// alone.
	w := noticeWriter(*asJSON)

	fmt.Fprintf(w, "wrote candidates (%d: %s)\n", len(files.CandidateNames), strings.Join(files.CandidateNames, ", "))
	summary, err := actorSummary(files.Actors, files.ActorOrder)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "wrote actors (%s)\n", summary)
	fmt.Fprintln(w, tierLine(tier))

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

// initBuildError classifies what Build refused: nothing is logged in is the
// machine's own state, so it is not_available with the wizard as the way back
// in.
func initBuildError(err error) error {
	var na *setup.NotAvailableError
	if errors.As(err, &na) {
		return failNext(codeNotAvailable, "relevo setup", "%s", na.Message)
	}
	return err
}

// tierLine is init's tier notice: what the seeded tier lets a builder do.
func tierLine(tier string) string {
	why := "builders run commands without asking"
	if tier == "edit" {
		why = "only codex builders can bind; claude, agy and opencode builders are refused at edit"
	}
	return fmt.Sprintf("tier: %s (%s) -- relevo config set policy.max_tier edit to restrict", tier, why)
}

// actorSummary renders every actor in order as the text inside the `wrote
// actors (...)` line. An actor with no candidates renders as `builder: none`.
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
