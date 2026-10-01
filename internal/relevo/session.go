package relevo

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/store"
)

// SessionLocator finds a pane harness's own session record for a builder,
// or reports that there is none: ok is false for a kind that keeps no
// record relevo can read (agy, opencode this round), for an empty
// sessionID, and when the file is not (yet) on disk. Pure apart from the
// filesystem; cmd/relevo wires HomeSessionLocator, tests wire a map.
type SessionLocator func(kind, sessionID string) (path string, ok bool)

// HomeSessionLocator locates claude's record under home, the default
// ~/.claude: the account-aware AccountSessionLocator is this locator with every
// configured account home added.
func HomeSessionLocator(home string) SessionLocator {
	return ClaudeSessionLocator(filepath.Join(home, ".claude"))
}

// ClaudeSessionLocator locates claude's record under configDir, the directory a
// round's CLAUDE_CONFIG_DIR names (the default ~/.claude when no account pins
// one): filepath.Glob(configDir/projects/*/<sessionID>.jsonl); when several
// match (a session copied between slugs) the newest by mtime wins. A sessionID
// containing a path separator or a glob metacharacter is refused (ok false): a
// session id is used in a path.
func ClaudeSessionLocator(configDir string) SessionLocator {
	return func(kind, sessionID string) (string, bool) {
		if kind != "claude" || sessionID == "" {
			return "", false
		}
		if strings.ContainsAny(sessionID, `/\*?[]`) {
			return "", false
		}
		matches, err := filepath.Glob(filepath.Join(configDir, "projects", "*", sessionID+".jsonl"))
		if err != nil || len(matches) == 0 {
			return "", false
		}
		best := matches[0]
		bestTime := mtimeOf(best)
		for _, m := range matches[1:] {
			if t := mtimeOf(m); t.After(bestTime) {
				best, bestTime = m, t
			}
		}
		return best, true
	}
}

// AccountSessionLocator searches the default home and every claude account's
// own config dir: a session id only resolves under the home that wrote it, and
// a pane builder may have run under any configured account. A kind other than
// claude, and a session id that is empty or carries a path separator or glob
// metacharacter, locate nothing, exactly as HomeSessionLocator refuses them.
func AccountSessionLocator(home string, accounts account.Set) SessionLocator {
	dirs := []string{filepath.Join(home, ".claude")}
	seen := map[string]bool{dirs[0]: true}
	for _, a := range accounts {
		dir, ok := claudeConfigDir(a)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	locators := make([]SessionLocator, 0, len(dirs))
	for _, dir := range dirs {
		locators = append(locators, ClaudeSessionLocator(dir))
	}
	return func(kind, sessionID string) (string, bool) {
		for _, locate := range locators {
			if path, ok := locate(kind, sessionID); ok {
				return path, true
			}
		}
		return "", false
	}
}

// claudeConfigDir is a claude account's config dir, and false for any other
// kind: codex and opencode keep their sessions elsewhere, so their accounts
// never add a search root.
func claudeConfigDir(a account.Account) (string, bool) {
	if a.Harness != account.Claude || a.ConfigDir == "" {
		return "", false
	}
	return a.ConfigDir, true
}

// mtimeOf is path's modification time, or the zero time when it cannot be
// stat'd -- HomeSessionLocator's newest-wins tie-break never fails a glob
// match over a stat error.
func mtimeOf(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// builderSessionOf names the harness session that built the binding's closed
// round (#147), for the report entry: a headless builder's stream id when the
// round's stream announced one. Remote builders and any binding with no
// headless session answer nil -- never guessed.
func builderSessionOf(b store.Binding) *store.BuilderSession {
	if !b.Builder.Headless() || b.Builder.StreamSessionID == "" {
		return nil
	}
	return &store.BuilderSession{Kind: b.Builder.Kind, ID: b.Builder.StreamSessionID}
}

// roundSession names the harness session that built a closed round (#147
// part 2), read off the round's own report entry: the newest KindReport entry
// for that round carrying a BuilderSession. ok is false when the round has no
// report entry at all, or when its report names no session -- relevo never
// guesses, and a resumed session must be the one that built the round.
func roundSession(entries []store.LogEntry, round int) (*store.BuilderSession, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Round == round && e.Kind == store.KindReport && e.BuilderSession != nil {
			return e.BuilderSession, true
		}
	}
	return nil, false
}
