package store

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Dir is the state directory for one binding.
func (s *Store) Dir(name string) string { return filepath.Join(s.root, name) }

func (s *Store) roundFile(name string, round int, suffix, ext string) string {
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.Join(s.Dir(name), fmt.Sprintf("%03d-%s%s", round, suffix, ext))
}

// actorNameRe is the rule an actor name must satisfy to become a directory
// name: the cockpit spec's agent key, with no slash and no leading dot.
var actorNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ArtifactDir is a round's artifact directory, <state>/<binding>/NNN-<actor>/.
// It may hold any files, in subdirectories too; summary.md in it is the
// runner's final message.
//
// An actor that cannot be a directory name is a programming error, not input:
// the actor came from validated config, and every other helper here is total,
// so this panics rather than returning an error.
func (s *Store) ArtifactDir(name string, round int, actor string) string {
	mustActor(actor)
	return filepath.Join(s.Dir(name), fmt.Sprintf("%03d-%s", round, actor))
}

// SummaryPath is the artifact directory's final message. R4 writes it; A5 only
// names it.
func (s *Store) SummaryPath(name string, round int, actor string) string {
	return filepath.Join(s.ArtifactDir(name, round, actor), "summary.md")
}

// ArtifactRel is the round_file name of a file inside a round's artifact
// directory: "NNN-<actor>/<rel>", always with forward slashes, whatever the OS.
func ArtifactRel(round int, actor, rel string) string {
	mustActor(actor)
	return fmt.Sprintf("%03d-%s/%s", round, actor, filepath.ToSlash(rel))
}

// mustActor panics on an actor that cannot name a directory.
func mustActor(actor string) {
	if !actorNameRe.MatchString(actor) {
		panic(fmt.Sprintf("store: actor %q does not match %s", actor, actorNameRe))
	}
}

// roundFileRel resolves path, which must live inside dir, into the round_file
// name it belongs to: the flat base of a path directly in dir, or
// "NNN-<actor>/<rel>" with forward slashes for a path inside a top-level round
// directory. ok is false for a path outside dir, a name that is not a round
// file, and any path containing "..".
func roundFileRel(dir, path string) (string, bool) {
	if containsDotDot(path) {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) == 1 {
		if !roundBaseRe.MatchString(parts[0]) {
			return "", false
		}
		return parts[0], true
	}
	if !roundBaseRe.MatchString(parts[0]) {
		return "", false
	}
	for _, part := range parts {
		if part == "" || part == "." {
			return "", false
		}
	}
	return strings.Join(parts, "/"), true
}

// bindingRelOf resolves path, under s.root, into the binding it names and the
// round_file name of the file it points at. A nil Store never resolves: a read
// of an unrelated path must not need the state dir.
func (s *Store) bindingRelOf(path string) (binding, name string, ok bool) {
	if s == nil || containsDotDot(path) {
		return "", "", false
	}
	rel, err := filepath.Rel(filepath.Clean(s.root), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", false
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 2)
	if len(parts) != 2 {
		return "", "", false
	}
	name, ok = roundFileRel(s.Dir(parts[0]), path)
	if !ok {
		return "", "", false
	}
	return parts[0], name, true
}

// containsDotDot reports whether any element of path is "..", before any
// cleaning: it is how a path that escapes its directory is refused.
func containsDotDot(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == filepath.Separator
	}) {
		if part == ".." {
			return true
		}
	}
	return false
}

func (s *Store) PlanPath(name string, round int) string {
	return s.roundFile(name, round, "plan", ".md")
}

func (s *Store) ReportPath(name string, round int) string {
	return s.roundFile(name, round, "report", ".md")
}

// DonePath is the builder's completion marker for a round: an empty file it
// creates as its last action. relevo only ever stats it.
func (s *Store) DonePath(name string, round int) string {
	return s.roundFile(name, round, "done", "")
}

// BuilderLogPath is the builder log of a round from before stderr was folded
// into the stream; kept for old rounds and for a round in flight across an
// upgrade.
func (s *Store) BuilderLogPath(name string, round int) string {
	return s.roundFile(name, round, "builder", ".log")
}

// RunnerStreamPath is the harness's streamed JSON, one event per line, plus
// the supervisor's relevo-exit trailer, for rounds started after the rename.
// New rounds always write to this name. Postcondition: always ends NNN-runner.jsonl.
func (s *Store) RunnerStreamPath(name string, round int) string {
	return s.roundFile(name, round, "runner", ".jsonl")
}

// BuilderStreamPath is the pre-rename stream name that rounds started before
// the rename carry on disk or as sealed round_file rows. Frozen: nothing new
// may call this to write or open a stream unless it has already resolved to
// this name via StreamPath.
func (s *Store) BuilderStreamPath(name string, round int) string {
	return s.roundFile(name, round, "builder", ".jsonl")
}

// StreamPath returns the path of a round's stream file: the new name when that
// file exists (on disk or as a sealed row), the old name when only that exists,
// or the new name when neither exists (the name a fresh round starts on).
// Total: errors and misses are both treated as not-found; the resolver never
// returns an error.
func (s *Store) StreamPath(name string, round int) string {
	newPath := s.RunnerStreamPath(name, round)
	if _, _, ok, _ := s.StatFile(newPath); ok {
		return newPath
	}
	oldPath := s.BuilderStreamPath(name, round)
	if _, _, ok, _ := s.StatFile(oldPath); ok {
		return oldPath
	}
	return newPath
}

// BuilderSegmentsPath is the round's []StreamSegment as JSON, written straight
// into round_file and never to disk.
func (s *Store) BuilderSegmentsPath(name string, round int) string {
	return s.roundFile(name, round, "builder-segments", ".json")
}

func (s *Store) GateLogPath(name string, round int) string {
	return s.roundFile(name, round, "gate", ".log")
}

func (s *Store) QuestionPath(name string, round int) string {
	return s.roundFile(name, round, "question", ".md")
}

// DiffPath is a round_file key, stored with Tx.PutRoundFile and never written
// to disk; read it with Store.ReadFile.
func (s *Store) DiffPath(name string, round int) string {
	return s.roundFile(name, round, "diff", ".patch")
}

func (s *Store) DriftPath(name string, round int) string {
	return s.roundFile(name, round, "drift", ".patch")
}

// ViewedPath is where the "viewed" sidecar lived before viewed_at became a
// record column; nothing writes it now.
func (s *Store) ViewedPath(name string) string {
	return filepath.Join(s.Dir(name), ".viewed")
}

// MarkViewed is a no-op when the binding has no record: a read verb must not
// fail because a stamp could not be written.
func (s *Store) MarkViewed(name string, at time.Time) error {
	if err := s.importPresent(name); err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	_, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return d.RecordSetViewed(s.owner, name, at)
}

func (s *Store) ViewedAt(name string) (time.Time, bool) {
	if err := s.importPresent(name); err != nil {
		return time.Time{}, false
	}
	d, err := s.dbForRead()
	if err != nil || d == nil {
		return time.Time{}, false
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil || !ok || rec.ViewedAt == nil {
		return time.Time{}, false
	}
	return *rec.ViewedAt, true
}

// AskPath is not NNN-question.md: that name belongs to QuestionPath, the
// blocked-dialog capture, and a consult being asked a question is a different
// event from a builder being blocked on one.
func (s *Store) AskPath(name string, round int, id string) string {
	return s.consultFile(name, round, id, "ask", ".md")
}

// FindingsPath is a round_file key, stored with Tx.PutRoundFile and never
// written to disk.
func (s *Store) FindingsPath(name string, round int, id string) string {
	return s.consultFile(name, round, id, "findings", ".md")
}

// ConsultStreamPath is a headless consult's own stream, exactly as a headless
// builder round has one.
func (s *Store) ConsultStreamPath(name string, round int, id string) string {
	return s.consultFile(name, round, id, "consult", ".jsonl")
}

// consultFile is roundFile with a consult id folded in: widening roundFile
// would touch five call sites that will never have an id.
func (s *Store) consultFile(name string, round int, id, suffix, ext string) string {
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.Join(s.Dir(name), fmt.Sprintf("%03d-%s-%s%s", round, id, suffix, ext))
}

func (s *Store) bindingPath(name string) string {
	return filepath.Join(s.Dir(name), "bind.json")
}

// ArchiveDir is the legacy directory a pre-database relevo kept archived
// bindings in; nothing writes it now, it is the one-time import's input.
func (s *Store) ArchiveDir() string { return filepath.Join(s.root, archiveDirName) }

// archiveStampLayout is the stamp a legacy tarball's file name carries, and
// the archived_at the import gives the record it makes from it.
const archiveStampLayout = "20060102-150405"

func (s *Store) DBPath() string { return filepath.Join(s.root, "relevo.db") }

// ChannelsDir is where relevo mcp's claim files lived before claims became kv
// rows; the claim import reads them from it and removes it.
func (s *Store) ChannelsDir() string { return filepath.Join(s.root, "channels") }

// PlannersDir is where planner records lived before they became kv rows.
func (s *Store) PlannersDir() string { return filepath.Join(s.root, "planners") }

// AgyCredsDir is where captured agy credentials lived before they became
// secrets. Dot-prefixed so a directory scan never reads it as a record.
func (s *Store) AgyCredsDir() string { return filepath.Join(s.PlannersDir(), ".agy") }

// WorktreeDir is dot-prefixed, which is what keeps list() from walking into it
// and trying to read a working tree as a binding.
func (s *Store) WorktreeDir() string {
	return filepath.Join(s.root, ".worktrees")
}

func (s *Store) WorktreePath(name string) string {
	return filepath.Join(s.WorktreeDir(), name)
}

// VerifyWorktreePath lives under its own dot-prefixed subdirectory so a human
// can see at a glance which trees are leftovers from a crashed verify, and so
// nothing mistakes one for a binding's worktree (Store.WorktreePath is the
// only path relevo ever removes).
func (s *Store) VerifyWorktreePath(name string, round int) string {
	return filepath.Join(s.WorktreeDir(), ".verify", fmt.Sprintf("%s-%03d", name, round))
}

// ScratchWorktreeDir is dot-prefixed, so ListFiles and importAll skip it, and
// ValidName forbids "." in binding names, so a binding's worktree can never
// collide with it.
func (s *Store) ScratchWorktreeDir() string {
	return filepath.Join(s.WorktreeDir(), ".scratch")
}

func (s *Store) ScratchWorktreePath(name string, round int) string {
	return filepath.Join(s.ScratchWorktreeDir(), fmt.Sprintf("%s-%03d", name, round))
}

// PruneWorktreeDirs removes the parents of relevo's worktrees once they are
// empty. os.Remove never removes a non-empty directory, so a sibling worktree
// keeps its parent; it never logs, because a racing `git worktree add`
// recreates its own parent.
func (s *Store) PruneWorktreeDirs() {
	_ = os.Remove(filepath.Join(s.WorktreeDir(), ".verify"))
	_ = os.Remove(s.ScratchWorktreeDir())
	_ = os.Remove(s.WorktreeDir())
}
