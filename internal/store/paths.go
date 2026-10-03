package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
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

// ArtifactDir is a round's artifact directory, <state>/<binding>/out/NNN-<actor>/
// for the new layout, or <state>/<binding>/NNN-<actor>/ for one still on the
// old layout. It is a total resolver like ReportPath: out/ when it exists on
// disk, else the old directory when it exists, else out/ -- the name a fresh
// round writes. It may hold any files, in subdirectories too; summary.md in it
// is a reader runner's pre-rename final message.
//
// An actor that cannot be a directory name is a programming error, not input:
// the actor came from validated config, and every other helper here is total,
// so this panics rather than returning an error.
func (s *Store) ArtifactDir(name string, round int, actor string) string {
	mustActor(actor)
	base := fmt.Sprintf("%03d-%s", round, actor)
	newDir := filepath.Join(s.OutDir(name), base)
	if dirOnDisk(newDir) {
		return newDir
	}
	oldDir := filepath.Join(s.Dir(name), base)
	if dirOnDisk(oldDir) {
		return oldDir
	}
	return newDir
}

// dirOnDisk reports whether path is a directory on disk, following no symlink:
// a symlink (even one to a real directory) is not a directory here, so a
// symlinked artifact directory is refused rather than named.
func dirOnDisk(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}

// OutputPath is where a reader round's final message is saved: the artifact
// directory's <label>.md. label is the actor's resolved output label and is
// never empty.
func (s *Store) OutputPath(name string, round int, actor, label string) string {
	return filepath.Join(s.ArtifactDir(name, round, actor), label+".md")
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
// name it belongs to: the flat base of a path directly in dir -- or one element
// under dir's out/ child -- or "NNN-<actor>/<rel>" with forward slashes for a
// path inside a top-level round directory. One leading out/ element is stripped
// so a file's two homes resolve to the same round_file name. ok is false for a
// path outside dir, a name that is not a round file, and any path containing
// "..".
func roundFileRel(dir, path string) (string, bool) {
	if containsDotDot(path) {
		return "", false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) > 0 && parts[0] == outDirName {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return "", false
	}
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

// PromptPath returns the path of a round's prompt file: the new name when that
// file exists (on disk or as a sealed row), the pre-rename name when only that
// exists, or the new name when neither does (the name a fresh round writes).
// Total: errors and misses are both treated as not-found; the resolver never
// returns an error. Writers stage through it, so a resend into a round that
// already holds the old file keeps writing that one file.
func (s *Store) PromptPath(name string, round int) string {
	newPath := s.roundFile(name, round, "prompt", ".md")
	if _, _, ok, _ := s.StatFile(newPath); ok {
		return newPath
	}
	oldPath := s.roundFile(name, round, "plan", ".md")
	if _, _, ok, _ := s.StatFile(oldPath); ok {
		return oldPath
	}
	return newPath
}

// ReportPath returns the path of a round's report: out/ when it exists (on disk
// or as a sealed row), else the old flat path when it exists, else out/ -- the
// name a fresh round writes. Total, like PromptPath and StreamPath.
func (s *Store) ReportPath(name string, round int) string {
	return s.resolveRunnerOutput(name, round, "report", ".md")
}

// DonePath is the builder's completion marker for a round: an empty file it
// creates as its last action. relevo only ever stats it. It resolves the same
// way ReportPath does: out/ when it exists, else the old flat path, else out/.
func (s *Store) DonePath(name string, round int) string {
	return s.resolveRunnerOutput(name, round, "done", "")
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

// CheckLogPath is a round_file key for one chain check run's log, sealed at
// the check's end. The member is the chain's writer and round its newest
// closed round; the run number keeps a re-run from overwriting an earlier
// log. It is read with Store.ReadFile.
func (s *Store) CheckLogPath(name string, round, run int) string {
	return s.roundFile(name, round, fmt.Sprintf("check-%03d", run), ".log")
}

// ServedCheckLogPath is where a served binding's check run streams: a file
// under that binding's round-file area, named after the round so it sits beside
// the round's gate log without ever being mistaken for it. The name carries no
// run id, because one check runs at a time per binding and the next run on the
// same round takes the file over.
func (s *Store) ServedCheckLogPath(name string, round int) string {
	return s.roundFile(name, round, "served-check", ".log")
}

// CheckLogTarget resolves a check log path built by CheckLogPath back to the
// member and round it was keyed with, so a seal reuses the round the row was
// written under instead of recomputing one. It uses the store's own round-file
// rules, so a check log path always resolves; ok is false for a path outside
// the state root or a name that carries no round.
func (s *Store) CheckLogTarget(path string) (member string, round int, ok bool) {
	return s.RoundFileTarget(path)
}

// RoundFileTarget resolves any round-file key under the state root back to the
// binding and round it was keyed with. It is the resolver behind every
// row-only artifact a seal writes -- a check log, a fork's conflict report --
// so a seal reuses the round the key carries instead of recomputing one; ok is
// false for a path outside the state root or a name that carries no round.
func (s *Store) RoundFileTarget(path string) (member string, round int, ok bool) {
	member, name, ok := s.bindingRelOf(path)
	if !ok {
		return "", 0, false
	}
	round, ok = roundOfFile(name)
	if !ok {
		return "", 0, false
	}
	return member, round, true
}

// ForkConflictPath is a round_file key for the conflict report one fork merge
// leaves behind: the paths git marked unmerged and the branches the merge never
// reached. It is keyed to the chain's writer and its newest closed round, the
// same pair a check log uses, and is stored with Tx.PutRoundFile rather than
// written to disk; read it with Store.ReadFile. The fork's builder round is
// handed it through {{<fork>.conflict}}, so the key travels as that step's
// "conflict" artifact.
func (s *Store) ForkConflictPath(name string, round int) string {
	return s.roundFile(name, round, "fork-conflict", ".txt")
}

func (s *Store) QuestionPath(name string, round int) string {
	return s.roundFile(name, round, "question", ".md")
}

// DiffPath is a round_file key, stored with Tx.PutRoundFile and never written
// to disk; read it with Store.ReadFile.
func (s *Store) DiffPath(name string, round int) string {
	return s.roundFile(name, round, "diff", ".patch")
}

// PlanDiffPath is a round_file key for a plan's cumulative diff, stored with
// Tx.PutRoundFile and never written to disk; read it with Store.ReadFile. It is
// keyed to the closing round, beside the round diff.
func (s *Store) PlanDiffPath(name string, round int) string {
	return s.roundFile(name, round, "plan-diff", ".patch")
}

// ChainDiffPath is a round_file key for the whole branch's diff, stored with
// Tx.PutRoundFile and never written to disk; read it with Store.ReadFile. It is
// a row-only key like the round and plan diffs, keyed to the builder's newest
// closed round.
func (s *Store) ChainDiffPath(name string, round int) string {
	return s.roundFile(name, round, "chain-diff", ".patch")
}

func (s *Store) DriftPath(name string, round int) string {
	return s.roundFile(name, round, "drift", ".patch")
}

// MarkViewed is a no-op when the binding has no record: a read verb must not
// fail because a stamp could not be written.
func (s *Store) MarkViewed(name string, at time.Time) error {
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

func (s *Store) DBPath() string { return filepath.Join(s.root, "relevo.db") }

// MasterMindsDir is the parent of AgyCredsDir: <root>/planners, the name
// mastermind records used before they became kv rows, kept because the agy
// credentials directory still lives under it.
func (s *Store) MasterMindsDir() string { return filepath.Join(s.root, "planners") }

// AgyCredsDir is where captured agy credentials lived before they became
// secrets. Dot-prefixed so a directory scan never reads it as a record.
func (s *Store) AgyCredsDir() string { return filepath.Join(s.MasterMindsDir(), ".agy") }

// WorktreeDir is dot-prefixed, which is what keeps list() from walking into it
// and trying to read a working tree as a binding.
func (s *Store) WorktreeDir() string {
	return filepath.Join(s.root, ".worktrees")
}

func (s *Store) WorktreePath(name string) string {
	return filepath.Join(s.WorktreeDir(), name)
}

// ChainDir is a chain's plan directory: <root>/.chains/<name>. It is
// dot-prefixed, so the store's readers skip it, and a binding name can never
// start with "." (the first-character rule), so no binding's directory can
// collide with it — "." is otherwise permitted.
func (s *Store) ChainDir(name string) string {
	return filepath.Join(s.root, ".chains", name)
}

// ChainPlanPath is a chain's copy of its i-th plan, 1-based. The copies live
// beside the binding directories and their paths are what the chain row
// stores, so a later edit of a source plan changes nothing.
func (s *Store) ChainPlanPath(name string, i int) string {
	return filepath.Join(s.ChainDir(name), fmt.Sprintf("plan-%d.md", i))
}

// ChainTaskPath is a chain's copy of its task input: <chainDir>/task.md,
// beside the plan copies.
func (s *Store) ChainTaskPath(name string) string {
	return filepath.Join(s.ChainDir(name), "task.md")
}

// ChainInputDir is where a chain keeps the copies of the round files its seeds
// name: <chainDir>/inputs. It is under ChainDir, so it is dot-prefixed and no
// store walk, seal pass or binding name can reach it.
func (s *Store) ChainInputDir(name string) string {
	return filepath.Join(s.ChainDir(name), "inputs")
}

// ChainInputPath names the copy a chain keeps of a source round file:
// <chainDir>/inputs/<source binding>-<NNN>/<source base name>. source must be
// a path under the state root that resolves to a binding round file; ok is
// false for a path outside the root or one the store cannot name, so a source
// that is not a binding round file resolves no copy.
func (s *Store) ChainInputPath(chain, source string) (string, bool) {
	binding, name, ok := s.bindingRelOf(source)
	if !ok {
		return "", false
	}
	return filepath.Join(s.ChainInputDir(chain), binding+"-"+name[:3], path.Base(name)), true
}

// DiskRegularFile reports whether path is a regular file the store reads as
// itself: os.Lstat says a regular file, and the name is not a reserved
// round-file name. A reserved name is row-only -- the row is the record -- so a
// plant at one is never the record, and a symlink or any other non-regular file
// is never opened.
func (s *Store) DiskRegularFile(path string) bool {
	if _, name, ok := s.bindingRelOf(path); ok && reservedRoundFile(name) {
		return false
	}
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode().IsRegular()
}

// VerifyWorktreePath lives under its own dot-prefixed subdirectory so a human
// can see at a glance which trees are leftovers from a crashed verify, and so
// nothing mistakes one for a binding's worktree (Store.WorktreePath is the
// only path relevo ever removes).
func (s *Store) VerifyWorktreePath(name string, round int) string {
	return filepath.Join(s.WorktreeDir(), ".verify", fmt.Sprintf("%s-%03d", name, round))
}

// ScratchWorktreeDir is dot-prefixed, so the store's readers skip it, and a
// binding name can never start with "." (the first-character rule), so a
// binding's worktree can never collide with it — "." is otherwise permitted.
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

// EnsureScratchDir creates .worktrees/.scratch when it is absent, the directory
// a reader's throwaway worktree is cut into. It replaces the raw MkdirAll the
// scratch path used to call, so a user-mode server reaches the same tenant
// chown callback EnsureOutDir uses. A symlink or any other non-directory
// already at the path is refused: the tenant owns this directory, and a link
// planted there must not redirect relevo's writes.
func (s *Store) EnsureScratchDir() error {
	dir := s.ScratchWorktreeDir()
	if fi, err := os.Lstat(dir); err == nil {
		if !fi.IsDir() {
			return fmt.Errorf("scratch dir %s is not a directory", dir)
		}
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// .worktrees is the root the scratch directory lives under; create it when
	// it is absent, then create .scratch through the root, so a .scratch entry
	// swapped for a symlink out of .worktrees is refused rather than followed.
	if err := os.MkdirAll(s.WorktreeDir(), scratchDirMode); err != nil {
		return err
	}
	root, err := s.WorktreeRoot()
	if err != nil {
		return err
	}
	if err := root.MkdirAll(filepath.Base(dir), scratchDirMode); err != nil {
		_ = root.Close()
		return err
	}
	_ = root.Close()
	return s.chownCreated(dir)
}
