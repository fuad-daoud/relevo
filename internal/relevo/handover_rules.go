package relevo

import (
	"fmt"
	"strings"

	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/refusal"
)

// MinHandoverSchema is the schema a hand-over target must have written at least
// once: older builds do not know the writer column and would apply a hand-over
// as a plain row.
const MinHandoverSchema = 31

// InstallationFact is one installation the remote knows, as the hand-over
// resolves --to against.
type InstallationFact struct {
	ID    string
	Label string
	// NewestSchema is the highest schema version any entry of the installation
	// carried; zero when none was seen.
	NewestSchema int
}

// HandoverFacts is everything the hand-over refusals read, gathered by the verb
// so the rules stay a pure function.
type HandoverFacts struct {
	Name string
	To   string
	// This is this installation's id.
	This string

	SyncEnabled bool
	Locked      bool

	// Round is the binding's current round and RoundOpen whether it is open.
	Round     int
	RoundOpen bool
	// ChainErr is the running-chain refusal for the binding, or nil.
	ChainErr error
	// Dirty is an unclean worktree; Unpushable is why the branch cannot travel
	// ("" when it can).
	Dirty      bool
	Unpushable string
	// Remote is the remote the branch is pushed to. The URLs are empty when the
	// remote is origin or could not be read.
	Remote    string
	RemoteURL string
	OriginURL string
	// Server is a binding with a Link or a remote builder.
	Server bool

	Installations []InstallationFact
	// HandedTo is the installation the binding already went to, or "".
	HandedTo string
}

// HandoverTarget is the installation a hand-over resolved --to to, and whether
// the binding is already there.
type HandoverTarget struct {
	InstallationFact
	Already bool
}

// HandoverRefusal decides whether a hand-over may go ahead and to whom. The
// first refusal wins; each is one sentence the owner can act on.
func HandoverRefusal(f HandoverFacts) (HandoverTarget, error) {
	if !f.SyncEnabled {
		return HandoverTarget{}, refusal.New("sync is off on this machine: relevo db sync enable first; a hand-over travels through the sync log")
	}
	if f.Locked {
		return HandoverTarget{}, refusal.New("sync is locked on this machine: relevo db sync unlock first; a hand-over is signed and a locked machine cannot sign")
	}
	target, err := resolveHandoverTarget(f)
	if err != nil {
		return HandoverTarget{}, err
	}
	if f.HandedTo != "" {
		if f.HandedTo != target.ID {
			return HandoverTarget{}, refusal.New("binding %q is already handed over to %s; relevo takeover %s --force takes it back first", f.Name, f.HandedTo, f.Name)
		}
		return HandoverTarget{InstallationFact: target, Already: true}, nil
	}
	if err := bindingStateRefusal(f); err != nil {
		return HandoverTarget{}, err
	}
	if target.NewestSchema < MinHandoverSchema {
		return HandoverTarget{}, refusal.New("%s (%s) has not written at schema %d or newer yet (newest seen: %d): upgrade it, let it sync once, then hand over",
			label(target), target.ID, MinHandoverSchema, target.NewestSchema)
	}
	return HandoverTarget{InstallationFact: target}, nil
}

// bindingStateRefusal is the part of the rules that reads the binding itself.
func bindingStateRefusal(f HandoverFacts) error {
	switch {
	case f.Server:
		return refusal.New("binding %q runs on a server: its runner, queue and round state live there under this machine's client key, so the copy cannot change hands; finish or unbind it on the server first", f.Name)
	case f.RoundOpen:
		return refusal.New("binding %q has round %d open: relevo stop %s ends it, or wait for it to close, then hand over", f.Name, f.Round, f.Name)
	case f.ChainErr != nil:
		return f.ChainErr
	case f.Dirty:
		return refusal.New("binding %q has uncommitted changes: they do not travel and releasing the tree would strand them; commit or discard them first", f.Name)
	case f.Unpushable != "":
		return refusal.New("binding %q has commits the push cannot carry: %s", f.Name, f.Unpushable)
	}
	return RemoteRefusal(f)
}

// RemoteRefusal refuses a push remote whose URL is not origin's, because the
// target's bind --resume always fetches origin.
func RemoteRefusal(f HandoverFacts) error {
	if f.Remote == "" || f.Remote == "origin" || SameRepo(f.RemoteURL, f.OriginURL) {
		return nil
	}
	return refusal.New("binding %q cannot go through remote %q: its URL is not origin's, and the target's bind --resume fetches origin, so the branch would be stranded; run relevo handover %s --to %s without --remote, or set %s's URL to origin's",
		f.Name, f.Remote, f.Name, f.To, f.Remote)
}

// resolveHandoverTarget turns --to into one installation other than this one. A
// label is not unique (two machines can report the same hostname), so an id is
// always accepted and an ambiguous label is refused with the ids it matches.
func resolveHandoverTarget(f HandoverFacts) (InstallationFact, error) {
	if f.To == "" {
		return InstallationFact{}, refusal.New("relevo handover needs --to <installation id or label>")
	}
	for _, in := range f.Installations {
		if in.ID == f.To {
			if in.ID == f.This {
				return InstallationFact{}, refusal.New("--to %s names this machine: a binding cannot be handed to itself", f.To)
			}
			return in, nil
		}
	}
	var matches []InstallationFact
	for _, in := range f.Installations {
		if in.Label == f.To {
			matches = append(matches, in)
		}
	}
	switch len(matches) {
	case 0:
		return InstallationFact{}, refusal.New("--to %q matches no installation the remote knows: relevo db sync pull, then check relevo db query 'SELECT id, label FROM installation'", f.To)
	case 1:
		if matches[0].ID == f.This {
			return InstallationFact{}, refusal.New("--to %s names this machine: a binding cannot be handed to itself", f.To)
		}
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for n, m := range matches {
		ids[n] = m.ID
	}
	return InstallationFact{}, refusal.New("--to %q matches %d installations (%s): pass one of the ids", f.To, len(matches), strings.Join(ids, ", "))
}

func label(in InstallationFact) string {
	if in.Label != "" {
		return in.Label
	}
	return in.ID
}

// ResumeFacts is what bind --resume reads about a record before touching it.
type ResumeFacts struct {
	Name string
	// Mine is true when this installation writes a live record of the name.
	Mine bool
	// LeftTo and LeftKind name where this installation sent the binding.
	LeftTo   string
	LeftKind string
	// ForeignWriter is the installation that writes a record this one holds
	// but does not write, or "".
	ForeignWriter string
}

// ResumeRefusal refuses a resume that would write a record this machine does
// not write. It returns nil when the record is this machine's or does not exist
// at all, which the ordinary lookup then reports.
func ResumeRefusal(f ResumeFacts) error {
	switch {
	case f.Mine:
		return nil
	case f.LeftTo != "":
		return refusal.New("binding %q is handed over to %s: resume it there, or relevo takeover %s --force here", f.Name, f.LeftTo, f.Name)
	case f.ForeignWriter != "":
		return refusal.New("binding %q is not handed to this machine yet (it is written by %s): run relevo db sync, then resume again", f.Name, f.ForeignWriter)
	}
	return nil
}

// SameRepo reports whether two origin URLs name the same repository once
// normalised; an empty URL names none.
func SameRepo(recorded, local string) bool {
	a, b := git.NormalizeOriginURL(recorded), git.NormalizeOriginURL(local)
	return a != "" && a == b
}

// ResumeCheckoutRefusal is the sentence for a resume run from a directory that
// is not a checkout of the binding's repository.
func ResumeCheckoutRefusal(name, recorded, local string) error {
	if local == "" {
		return refusal.New("binding %q was cut from %s, and this directory has no origin remote: run relevo bind --resume from a checkout of it", name, recorded)
	}
	return refusal.New("binding %q was cut from %s, and this directory's origin is %s: run relevo bind --resume from a checkout of the first", name, recorded, local)
}

// ResumeBranchRefusal is the sentence for a handed-over branch the checkout
// cannot fetch.
func ResumeBranchRefusal(name, branch string) error {
	return refusal.New("branch %s of binding %q is not on origin: ask the old machine to run relevo handover %s again", branch, name, name)
}

// HandoverText renders a hand-over's result for the old machine.
func HandoverText(name string, r HandoverResult) string {
	var b strings.Builder
	if r.Already {
		fmt.Fprintf(&b, "binding %s is already handed over to %s; nothing to do\n", name, r.Target)
		return b.String()
	}
	fmt.Fprintf(&b, "handed over %s to %s: branch %s pushed", name, r.Target, r.Branch)
	switch {
	case r.WorktreeRemoved != "":
		fmt.Fprintf(&b, ", worktree %s released", r.WorktreeRemoved)
	case r.WorktreeKept != "":
		fmt.Fprintf(&b, ", worktree %s kept (%s)", r.WorktreeKept, r.KeptReason)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "on %s: relevo db sync, then relevo bind --resume --name %s from a checkout of the repository\n", r.Target, name)
	return b.String()
}
