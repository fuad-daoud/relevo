package mastermind

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OpencodeSession is one row from OpenCode's session table.
type OpencodeSession struct {
	ID        string
	Directory string
	ParentID  string
	Title     string
	Updated   time.Time
	Archived  bool
}

// ErrNoOpencodeSession is returned when no OpenCode session matches the
// working directory.
var ErrNoOpencodeSession = errors.New("no matching OpenCode session")

// OpencodeActiveWindow is how recently an OpenCode session must have worked
// for MatchOpencodeSession to treat it as the one a shell command is running
// in: a session idle longer than this is not it.
const OpencodeActiveWindow = 10 * time.Minute

// ErrAmbiguousOpencodeSession is returned when multiple active OpenCode
// sessions match the directory.
type ErrAmbiguousOpencodeSession struct {
	Dir    string
	Titles []string
}

func (e ErrAmbiguousOpencodeSession) Error() string {
	return fmt.Sprintf("two OpenCode sessions are active in %s: %s; pass --mastermind", e.Dir, strings.Join(e.Titles, ", "))
}

func (e ErrAmbiguousOpencodeSession) Is(target error) bool {
	other, ok := target.(ErrAmbiguousOpencodeSession)
	if !ok {
		return false
	}
	if e.Dir != other.Dir || len(e.Titles) != len(other.Titles) {
		return false
	}
	for i := range e.Titles {
		if e.Titles[i] != other.Titles[i] {
			return false
		}
	}
	return true
}

// MatchOpencodeSession matches cwd against sessions. Pure. cwd must be
// absolute (clean it with filepath.Clean). In order:
//  1. Keep sessions with ParentID == "", !Archived, and Directory equal to
//     cwd or an ancestor of it (path-segment aware).
//  2. Discard those idle longer than OpencodeActiveWindow. None left ->
//     ErrNoOpencodeSession.
//  3. Of those, keep only the ones with the longest Directory.
//  4. Sort by Updated descending. If there are >= 2 and the second's Updated
//     is within 60s of now -> ErrAmbiguousOpencodeSession naming both titles.
//  5. Else the first's ID.
func MatchOpencodeSession(cwd string, sessions []OpencodeSession, now time.Time) (string, error) {
	cleanCWD := filepath.Clean(cwd)

	var matched []OpencodeSession
	for _, s := range sessions {
		if s.ParentID != "" || s.Archived {
			continue
		}
		s.Directory = filepath.Clean(s.Directory)
		if isAncestorOrEqual(s.Directory, cleanCWD) {
			matched = append(matched, s)
		}
	}

	var active []OpencodeSession
	for _, s := range matched {
		if now.Sub(s.Updated) <= OpencodeActiveWindow {
			active = append(active, s)
		}
	}
	if len(active) == 0 {
		return "", ErrNoOpencodeSession
	}

	maxLen := -1
	for _, s := range active {
		if l := len(s.Directory); l > maxLen {
			maxLen = l
		}
	}

	var longest []OpencodeSession
	for _, s := range active {
		if len(s.Directory) == maxLen {
			longest = append(longest, s)
		}
	}
	if len(longest) == 0 {
		return "", ErrNoOpencodeSession
	}

	sort.SliceStable(longest, func(i, j int) bool {
		return longest[i].Updated.After(longest[j].Updated)
	})

	if len(longest) >= 2 && now.Sub(longest[1].Updated) <= 60*time.Second {
		return "", ErrAmbiguousOpencodeSession{
			Dir:    longest[0].Directory,
			Titles: []string{longest[0].Title, longest[1].Title},
		}
	}

	return longest[0].ID, nil
}

func isAncestorOrEqual(dir, cwd string) bool {
	if dir == cwd || dir == "/" {
		return true
	}
	return strings.HasPrefix(cwd, dir+"/")
}
