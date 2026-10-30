package housekeep

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/store"
)

// RefOutcome records what happened to one git ref during cleanup or sweep.
type RefOutcome struct {
	Ref         string `json:"ref"`
	Deleted     bool   `json:"deleted,omitempty"`
	WouldDelete bool   `json:"would_delete,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

// SweepResult is the result of a ref sweep over a repository directory.
type SweepResult struct {
	Dir  string       `json:"dir"`
	Refs []RefOutcome `json:"refs"`
}

// firstLine reduces an error to one line, for a reason or note field where a
// multi-line git message would break the line's shape.
func firstLine(err error) string {
	if err == nil {
		return ""
	}
	s := strings.TrimSpace(err.Error())
	if idx := strings.Index(s, "\n"); idx != -1 {
		s = strings.TrimSpace(s[:idx])
	}
	return s
}

func repoDirOf(b store.Binding) string {
	if b.Repo != "" {
		return b.Repo
	}
	if b.RepoRef != nil && b.RepoRef.CommonDir != "" {
		return b.RepoRef.CommonDir
	}
	return ""
}

func bindingRefCandidates(ctx context.Context, deps Deps, dir string, b store.Binding) ([]string, error) {
	var candidates []string
	seen := make(map[string]bool)

	add := func(ref string) {
		if !seen[ref] {
			seen[ref] = true
			candidates = append(candidates, ref)
		}
	}

	if (!b.ExistingBranch && b.Branch == "relevo/"+b.Name) || (b.Builder.Remote() && b.Branch != "relevo/"+b.Name) {
		head := "refs/heads/relevo/" + b.Name
		// Propose the head ref only when the repo actually has it: a
		// candidate that does not exist makes cleanRefs report "check
		// failed: … malformed object name …" for every remote binding. A
		// RefSHA error still proposes it, so the failure is reported rather
		// than swallowed.
		if _, ok, err := deps.Refs.RefSHA(ctx, dir, head); err != nil || ok {
			add(head)
		}
	}

	refs, err := deps.Refs.ListRefs(ctx, dir, "refs/relevo/"+b.Name+"/")
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		add(r)
	}

	return candidates, nil
}

func cleanRefs(ctx context.Context, deps Deps, dir string, refs []string, dryRun bool) []RefOutcome {
	var outcomes []RefOutcome
	for _, ref := range refs {
		on, err := deps.Refs.RefOnRemote(ctx, dir, ref)
		if err != nil {
			outcomes = append(outcomes, RefOutcome{
				Ref:    ref,
				Reason: "check failed: " + firstLine(err),
			})
			continue
		}
		if !on {
			outcomes = append(outcomes, RefOutcome{
				Ref:    ref,
				Reason: "not on any remote-tracking ref",
			})
			continue
		}
		if dryRun {
			outcomes = append(outcomes, RefOutcome{
				Ref:         ref,
				WouldDelete: true,
			})
			continue
		}
		if strings.HasPrefix(ref, "refs/heads/") {
			err = deps.Refs.DeleteBranch(ctx, dir, ref)
		} else {
			err = deps.Refs.DeleteRef(ctx, dir, ref)
		}
		if err != nil {
			outcomes = append(outcomes, RefOutcome{
				Ref:    ref,
				Reason: "delete failed: " + firstLine(err),
			})
			continue
		}
		outcomes = append(outcomes, RefOutcome{
			Ref:     ref,
			Deleted: true,
		})
	}
	return outcomes
}

// SweepRefs applies the ref-cleanup rule to every candidate ref in dir whose
// binding has no live record.
func SweepRefs(ctx context.Context, deps Deps, dir string, dryRun bool) (SweepResult, error) {
	if deps.Refs == nil {
		return SweepResult{}, errors.New("git is unavailable; the sweep needs it")
	}

	var res SweepResult
	res.Dir = dir

	err := deps.Store.WithLock(func(tx *store.Tx) error {
		bindings, err := tx.List()
		if err != nil {
			return err
		}
		live := liveBindingNames(bindings)

		heads, others, err := listSweepRefs(ctx, deps, dir)
		if err != nil {
			return err
		}

		candidates, remaining := splitSweepRefs(append(heads, others...), live)
		res.Refs = joinSweepOutcomes(candidates, cleanRefs(ctx, deps, dir, remaining, dryRun))

		return nil
	})
	if err != nil {
		return SweepResult{}, err
	}
	return res, nil
}

// liveBindingNames is the set of binding names the sweep must not touch.
func liveBindingNames(bindings []store.Binding) map[string]bool {
	live := make(map[string]bool, len(bindings))
	for _, b := range bindings {
		live[b.Name] = true
	}
	return live
}

// listSweepRefs lists the relevo branch heads and client refs the sweep judges.
func listSweepRefs(ctx context.Context, deps Deps, dir string) ([]string, []string, error) {
	heads, err := deps.Refs.ListRefs(ctx, dir, "refs/heads/relevo/")
	if err != nil {
		return nil, nil, err
	}
	others, err := deps.Refs.ListRefs(ctx, dir, "refs/relevo/")
	if err != nil {
		return nil, nil, err
	}
	return heads, others, nil
}

// sweepCandidate is one ref the sweep judged, with whether its binding is live.
type sweepCandidate struct {
	ref    string
	isLive bool
}

// splitSweepRefs sorts refs into judged candidates and the dead ones cleanRefs
// removes: a ref whose name parses to a live binding is kept, one with no live
// binding is removed, and one that names no binding at all is skipped.
func splitSweepRefs(allRefs []string, live map[string]bool) ([]sweepCandidate, []string) {
	var candidates []sweepCandidate
	var remaining []string

	for _, ref := range allRefs {
		var name string
		switch {
		case strings.HasPrefix(ref, "refs/heads/relevo/"):
			rest := strings.TrimPrefix(ref, "refs/heads/relevo/")
			if rest == "" {
				continue
			}
			name = rest
		case strings.HasPrefix(ref, "refs/relevo/"):
			sub := strings.TrimPrefix(ref, "refs/relevo/")
			seg, rest, found := strings.Cut(sub, "/")
			if !found || rest == "" {
				continue
			}
			name = seg
		default:
			continue
		}

		if err := store.ValidName(name); err != nil {
			continue
		}

		if live[name] {
			candidates = append(candidates, sweepCandidate{ref: ref, isLive: true})
		} else {
			candidates = append(candidates, sweepCandidate{ref: ref, isLive: false})
			remaining = append(remaining, ref)
		}
	}

	return candidates, remaining
}

// joinSweepOutcomes lines the cleaned outcomes back up with the candidates: a
// live binding reads as kept, a dead one reads whatever cleanRefs decided.
func joinSweepOutcomes(candidates []sweepCandidate, cleaned []RefOutcome) []RefOutcome {
	var refs []RefOutcome
	cleanedIdx := 0
	for _, c := range candidates {
		if c.isLive {
			refs = append(refs, RefOutcome{
				Ref:    c.ref,
				Reason: "live binding",
			})
		} else {
			refs = append(refs, cleaned[cleanedIdx])
			cleanedIdx++
		}
	}
	return refs
}

// RefLines renders outcomes as lines for CLI output.
func RefLines(refs []RefOutcome) []string {
	var lines []string
	for _, r := range refs {
		switch {
		case r.Deleted:
			lines = append(lines, fmt.Sprintf("deleted      %s", r.Ref))
		case r.WouldDelete:
			lines = append(lines, fmt.Sprintf("would delete %s", r.Ref))
		default:
			lines = append(lines, fmt.Sprintf("kept         %s (%s)", r.Ref, r.Reason))
		}
	}
	return lines
}
