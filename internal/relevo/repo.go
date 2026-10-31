package relevo

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/git"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/resolve"
	"github.com/fuad-daoud/relevo/internal/store"
)

// captureRepo best-effort captures cwd's git identity for the coming history
// database. It never fails the caller: a nil Git, or any error RepoFacts
// returns, is logged at Debug and reported as a nil *store.RepoRef.
func captureRepo(ctx context.Context, rt Runtime, cwd string) *store.RepoRef {
	if rt.Git == nil {
		return nil
	}
	originURL, commonDir, err := rt.Git.RepoFacts(ctx, cwd)
	if err != nil {
		slog.Debug("repo facts", "cwd", cwd, "err", err)
		return nil
	}
	return &store.RepoRef{
		OriginURL: git.NormalizeOriginURL(originURL),
		CommonDir: commonDir,
	}
}

// The per-repository settings: the `repos` config section, keyed by the
// repository's identity -- the lower-case hex sha256 of its root commit, the
// same hash `bind --server` already travels on the wire as its RepoID -- and
// the gate rule that reads one entry's `gate` as the tier between the flag and
// the policy default.

// RepoSettings is one repository's stored settings as the gate, the board and
// the go-flags paths read them. "" means the repository sets nothing, which
// every caller reads as the next tier or the default.
type RepoSettings struct {
	Key        string
	Name       string
	Gate       string
	BoardTheme string
	Trimpath   string
}

// TrimpathOn is the effective trimpath setting. ParseRepos leaves Trimpath
// empty, "on" or "off" and nothing else, so those three are the whole
// vocabulary here.
func (s RepoSettings) TrimpathOn() bool {
	return s.Trimpath != "off"
}

// resolveGateFor applies the gate resolution rule. The repository tier sits
// exactly where the policy tier sat, behind the role rule: the role rule
// decides whether any check runs at all, not which tier supplies it, so a
// reader round still takes no gate even when its repository sets one.
func resolveGateFor(gate string, noGate bool, pol policy.Policy, roleChecks bool, repo RepoSettings) resolve.GateResolution {
	if noGate {
		return resolve.GateResolution{}
	}
	if gate != "" {
		return resolve.GateResolution{Command: gate, Source: resolve.GateSourceFlag}
	}
	if !roleChecks {
		return resolve.GateResolution{}
	}
	if repo.Gate != "" {
		return resolve.GateResolution{Command: repo.Gate, Source: resolve.GateSourceRepo}
	}
	cmd := pol.GateDefault()
	if cmd == "" {
		return resolve.GateResolution{}
	}
	return resolve.GateResolution{Command: cmd, Source: resolve.GateSourcePolicy}
}

// RepoKey returns dir's repository identity: remote.RepoID over the root
// commit `bind --server` identifies a repository with. ok is false -- with no
// error -- when dir has no settings to key: a repository with no commits
// (git.ErrRefMissing), a directory that is not a repository (git.ErrNotRepo)
// and a git that cannot answer (git.ErrGitUnavailable) all mean "no settings",
// so a bind must not start failing because git cannot identify the tree.
func RepoKey(ctx context.Context, g Git, dir string) (string, bool, error) {
	if g == nil {
		return "", false, nil
	}
	root, err := g.RootCommit(ctx, dir)
	if err != nil {
		if errors.Is(err, git.ErrRefMissing) || errors.Is(err, git.ErrNotRepo) || errors.Is(err, git.ErrGitUnavailable) {
			return "", false, nil
		}
		return "", false, err
	}
	id, err := remote.RepoID(root)
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// RepoSettingsFor reads dir's own settings out of rt's config, or the zero
// value when there is nothing to key. The section is read before the git
// identity is taken, so a machine whose section is empty reads no git for
// this at all.
func RepoSettingsFor(ctx context.Context, rt Runtime, dir string) (RepoSettings, error) {
	out := RepoSettings{}
	if rt.Config == nil {
		return out, nil
	}
	body, present, err := rt.Config.Body(config.Repos)
	if err != nil {
		return out, err
	}
	if !present {
		return out, nil
	}
	entries, err := config.ParseRepos(body)
	if err != nil {
		return out, err
	}
	if len(entries) == 0 {
		return out, nil
	}
	key, ok, err := RepoKey(ctx, rt.Git, dir)
	if err != nil {
		return out, err
	}
	if !ok {
		return out, nil
	}
	entry, ok := entries[key]
	if !ok {
		return out, nil
	}
	return RepoSettings{Key: key, Name: entry.Name, Gate: entry.Gate, BoardTheme: entry.BoardTheme, Trimpath: entry.Trimpath}, nil
}

// GateLine renders the gate line a bind or an add prints through the notice
// writer: "gate: <cmd> (<source>)", or "gate: none" when no command resolved.
// A source with no word renders the command bare, which is the shape a caller
// that knows only the stored command prints.
func GateLine(cmd, source string) string { return resolutionLine("gate", cmd, source) }

// CheckLine is the gate line's own rule under the "check" prefix.
func CheckLine(cmd, source string) string { return resolutionLine("check", cmd, source) }

func resolutionLine(prefix, cmd, source string) string {
	if cmd == "" {
		return prefix + ": none"
	}
	if source == "" {
		return prefix + ": " + cmd
	}
	return fmt.Sprintf("%s: %s (%s)", prefix, cmd, source)
}
