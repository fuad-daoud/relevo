package relevo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/workflow"
)

// chainRenderSeed renders one step's seed into the prompt a runner gets. Every
// reference becomes a path through chainSeedInput, or the path-free miss clause
// when it cannot be produced; a task, a param, the chain's base and branch and
// the chain's own diff render inline. A seed that is exactly one file reference
// hands that file over as bytes, the way a plan is handed over today.
func chainRenderSeed(rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, st workflow.State, seed string) (string, error) {
	if name, ok := strings.CutPrefix(seed, "shipped:"); ok {
		return chainRenderShippedSeed(rt, tx, c, def, st, name)
	}
	refs, err := workflow.Refs(seed)
	if err != nil {
		return "", err
	}
	if workflow.IsSingleRef(seed) && len(refs) == 1 {
		text, isFile, rerr := chainSeedRef(rt, tx, c, def, st, refs[0])
		if rerr != nil {
			return chainSeedMissing(rerr), nil
		}
		if !isFile {
			return text, nil
		}
		body, oerr := os.ReadFile(text)
		if oerr != nil {
			return chainSeedMissing(oerr), nil
		}
		return string(body), nil
	}

	var b strings.Builder
	rest := seed
	for _, ref := range refs {
		start := strings.Index(rest, "{{")
		if start < 0 {
			break
		}
		end := strings.Index(rest[start+2:], "}}")
		if end < 0 {
			break
		}
		b.WriteString(rest[:start])
		text, _, rerr := chainSeedRef(rt, tx, c, def, st, ref)
		if rerr != nil {
			b.WriteString(chainSeedMissing(rerr))
		} else {
			b.WriteString(text)
		}
		rest = rest[start+2+end+2:]
	}
	b.WriteString(rest)
	return b.String(), nil
}

// chainSeedRef resolves one reference to the text a seed names. isFile is true
// when the text is a path whose bytes a single-reference seed hands over.
func chainSeedRef(rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, st workflow.State, ref workflow.Ref) (string, bool, error) {
	switch ref.Root {
	case "task":
		body, err := rt.Store.ReadFile(rt.Store.ChainTaskPath(c.Name))
		if err != nil {
			return "", false, err
		}
		return string(body), false, nil
	case "chain":
		return chainChainRef(rt, tx, c, ref)
	}
	target, err := workflow.ResolveRef(def, st, ref)
	if err != nil {
		return "", false, err
	}
	switch {
	case target.Key != "":
		return chainSeedInput(rt, c, target.Key), true, nil
	case target.List != nil:
		path, err := chainSeedListFile(rt, c, ref, target.List)
		if err != nil {
			return "", false, err
		}
		return path, true, nil
	default:
		return target.Inline, false, nil
	}
}

// chainChainRef resolves a chain reference: the base and the branch render
// inline, and the whole-branch diff is captured and named as a copy.
func chainChainRef(rt Runtime, tx *store.Tx, c db.ChainRow, ref workflow.Ref) (string, bool, error) {
	switch ref.Attr {
	case "base":
		return c.Base, false, nil
	case "branch":
		return c.Branch, false, nil
	case "diff":
		key := chainBranchDiff(rt, tx, c, c.Builder, memberNewestClosedRound(tx, c.Builder))
		if key == "" {
			return "", false, errors.New("no chain diff captured")
		}
		return chainSeedInput(rt, c, key), true, nil
	}
	return "", false, fmt.Errorf("chain.%s is not chain state", ref.Attr)
}

// chainSeedListFile writes every path a list reference names under the chain's
// input directory and returns that file's path, so a seed names one openable
// file rather than an inline list.
func chainSeedListFile(rt Runtime, c db.ChainRow, ref workflow.Ref, items []string) (string, error) {
	dir := rt.Store.ChainInputDir(c.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, ref.Root+"-"+ref.Attr+".list")
	if err := os.WriteFile(path, []byte(strings.Join(items, "\n")+"\n"), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// shippedSeedKinds names the shipped seed templates that render from a
// member-round view, keyed by the name a workflow's "shipped:<name>" names.
var shippedSeedKinds = map[string]chain.SeedKind{
	"review":  chain.SeedReviewer,
	"correct": chain.SeedCorrection,
	"scan":    chain.SeedSecurity,
	"fix":     chain.SeedFixes,
}

// chainRenderShippedSeed renders one of the shipped seed templates for an
// engine send. The member-round seeds render from the same view the fixed state
// machine builds, so the shipped default hands a member the same prompt
// whichever engine drives the chain. The repair seed renders from the failed
// check's own record.
func chainRenderShippedSeed(rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, st workflow.State, name string) (string, error) {
	if name == "repair" {
		return chainRenderRepairSeed(rt, tx, c, def, st)
	}
	kind, ok := shippedSeedKinds[name]
	if !ok {
		return "", fmt.Errorf("chain %s: unknown shipped seed %q", c.Name, name)
	}
	lv := workflow.LegacyView(def, st)
	s := chain.State{Plan: lv.Plan, Plans: lv.Plans, Corrections: lv.Corrections, Phase: chain.Phase(lv.Phase)}
	var closedRound int
	switch kind {
	case chain.SeedReviewer:
		closedRound = memberNewestClosedRound(tx, c.Builder)
	case chain.SeedCorrection:
		closedRound = memberNewestClosedRound(tx, c.Reviewer)
	case chain.SeedFixes:
		closedRound = memberNewestClosedRound(tx, c.Security)
	}
	v, err := chainSeedView(rt, tx, c, s, chain.Action{Seed: kind}, closedRound)
	if err != nil {
		return "", err
	}
	// An engine chain runs its checks as steps, so the check the seed names
	// comes from the engine's recorded result, not from a member-gate record.
	if v.GateLogPath == "" {
		if result, log := flowCheckResult(st); log != "" {
			v.GateResult = result
			v.GateLogPath = chainSeedInput(rt, c, log)
		}
	}
	return workflow.RenderShipped(name, v)
}

// flowCheckResult names the newest check the engine recorded: the result word
// and the sealed log a seed can name. Nothing is returned when the engine ran
// no check, which the seed words as "No check ran".
func flowCheckResult(st workflow.State) (string, string) {
	result, log, round := "", "", -1
	for _, r := range st.Results {
		if r.Status != chainCheckGreen && r.Status != chainCheckRed {
			continue
		}
		logs := r.Artifacts["log"]
		if len(logs) == 0 || logs[0] == "" {
			continue
		}
		if r.Round >= round {
			round, result, log = r.Round, r.Status, logs[0]
		}
	}
	return result, log
}

// chainRenderRepairSeed renders the repair seed a workflow's repair step hands
// its writer: the round the send opens, the round whose check failed, the check
// command, the failed round's own prompt and the tail of the failed check's log.
// An engine chain runs its checks as steps, so the log comes from the engine's
// recorded result -- the newest check flowCheckResult names -- not from a
// member-gate record a chain writer no longer carries.
func chainRenderRepairSeed(rt Runtime, tx *store.Tx, c db.ChainRow, def workflow.Definition, st workflow.State) (string, error) {
	failedRound := memberNewestClosedRound(tx, c.Builder)
	b, err := tx.Load(c.Builder)
	if err != nil {
		return "", err
	}
	logPath := ""
	if _, log := flowCheckResult(st); log != "" {
		logPath = chainSeedInput(rt, c, log)
	}
	return workflow.RenderShipped("repair", workflow.SeedView{
		Name:        c.Builder,
		RepairRound: b.Round,
		FailedRound: failedRound,
		Gate:        workflow.RenderParams(def, "{{params.gate}}"),
		PlanPath:    rt.Store.PromptPath(c.Builder, failedRound),
		GateLogPath: logPath,
		Tail:        tailLines(rt.Store.ReadFile, logPath, repairTailLines),
	})
}
