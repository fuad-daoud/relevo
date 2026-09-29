package relevo

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

func repoDirOf(b store.Binding) string {
	if b.Repo != "" {
		return b.Repo
	}
	if b.RepoRef != nil && b.RepoRef.CommonDir != "" {
		return b.RepoRef.CommonDir
	}
	return ""
}

func bindingRefCandidates(ctx context.Context, rt Runtime, dir string, b store.Binding) ([]string, error) {
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
		// Propose the head ref only when the repo actually has it (#452): a
		// candidate that does not exist makes cleanRefs report "check
		// failed: … malformed object name …" for every remote binding. A
		// RefSHA error still proposes it, so the failure is reported rather
		// than swallowed.
		if _, ok, err := rt.Git.RefSHA(ctx, dir, head); err != nil || ok {
			add(head)
		}
	}

	refs, err := rt.Git.ListRefs(ctx, dir, "refs/relevo/"+b.Name+"/")
	if err != nil {
		return nil, err
	}
	for _, r := range refs {
		add(r)
	}

	return candidates, nil
}

func cleanRefs(ctx context.Context, rt Runtime, dir string, refs []string, dryRun bool) []RefOutcome {
	var outcomes []RefOutcome
	for _, ref := range refs {
		on, err := rt.Git.RefOnRemote(ctx, dir, ref)
		if err != nil {
			outcomes = append(outcomes, RefOutcome{
				Ref:    ref,
				Reason: "check failed: " + brief(err),
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
			err = rt.Git.DeleteBranch(ctx, dir, ref)
		} else {
			err = rt.Git.DeleteRef(ctx, dir, ref)
		}
		if err != nil {
			outcomes = append(outcomes, RefOutcome{
				Ref:    ref,
				Reason: "delete failed: " + brief(err),
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
func SweepRefs(ctx context.Context, rt Runtime, dir string, dryRun bool) (SweepResult, error) {
	if rt.Git == nil {
		return SweepResult{}, errors.New("git is unavailable; the sweep needs it")
	}

	var res SweepResult
	res.Dir = dir

	err := rt.Store.WithLock(func(tx *store.Tx) error {
		bindings, err := tx.List()
		if err != nil {
			return err
		}
		live := make(map[string]bool, len(bindings))
		for _, b := range bindings {
			live[b.Name] = true
		}

		heads, err := rt.Git.ListRefs(ctx, dir, "refs/heads/relevo/")
		if err != nil {
			return err
		}
		others, err := rt.Git.ListRefs(ctx, dir, "refs/relevo/")
		if err != nil {
			return err
		}

		allRefs := append(heads, others...)

		type candidate struct {
			ref    string
			isLive bool
		}
		var candidates []candidate
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
				candidates = append(candidates, candidate{ref: ref, isLive: true})
			} else {
				candidates = append(candidates, candidate{ref: ref, isLive: false})
				remaining = append(remaining, ref)
			}
		}

		cleaned := cleanRefs(ctx, rt, dir, remaining, dryRun)
		cleanedIdx := 0
		for _, c := range candidates {
			if c.isLive {
				res.Refs = append(res.Refs, RefOutcome{
					Ref:    c.ref,
					Reason: "live binding",
				})
			} else {
				res.Refs = append(res.Refs, cleaned[cleanedIdx])
				cleanedIdx++
			}
		}

		return nil
	})
	if err != nil {
		return SweepResult{}, err
	}
	return res, nil
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
