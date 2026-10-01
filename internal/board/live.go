package board

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/fuad-daoud/relevo/internal/mastermind"
)

// Scope names which of the two board scopes a resolved scene belongs to: the
// committed repo board (S1) or the per-MasterMind live board (board v2, D2).
type Scope string

const (
	// ScopeRepo is a scene committed with the repository (S1 semantics).
	ScopeRepo Scope = "repo"
	// ScopeLive is a scene under the state root's boards directory (D2).
	ScopeLive Scope = "live"
)

// DefaultBoard is the scene name a live board opens when neither --board nor
// the pointer names one (§4, S1/S4).
const DefaultBoard = "board"

// pointerName is the file, inside a live directory, that holds the current
// scene name (§4, S1).
const pointerName = "current"

// sceneNameRe is S3's slug rule: a lowercase letter or digit first, then up to
// 63 more of [a-z0-9-].
var sceneNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidSceneName reports whether name is a scene slug (S3).
func ValidSceneName(name string) error {
	if !sceneNameRe.MatchString(name) {
		return usagef("scene name %q must match %s", name, sceneNameRe.String())
	}
	return nil
}

// Resolved is one scene the verb resolved: its scope and name, the path the
// server reads and writes, and -- for a live scene -- the live directory that
// holds it and its pointer. FromPointer is true when the name came from the
// pointer file, so the caller knows not to rewrite it (S4).
type Resolved struct {
	Scope       Scope
	Scene       string
	Path        string
	LiveDir     string
	FromPointer bool
}

// ResolveLiveArg classifies arg as a live scene path. It returns ok true with
// the resolved scene when arg names <liveRoot>/<id>/<name>.excalidraw, and ok
// false when arg is not under the live root at all, so the caller falls back to
// the repo scope (S2). A path under the live root that is not live-shaped -- a
// malformed id, a bad slug, a wrong extension, nested directories, or a
// symlinked parent that escapes the live directory -- is a usage refusal
// (S1's confinement rule, S5).
func ResolveLiveArg(liveRoot, cwd, arg string) (Resolved, bool, error) {
	path := arg
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	path = filepath.Clean(path)

	realRoot := filepath.Clean(liveRoot)
	if real, err := filepath.EvalSymlinks(liveRoot); err == nil {
		realRoot = real
	}

	rel, err := filepath.Rel(realRoot, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Resolved{}, false, nil
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Resolved{}, false, usagef("live scene path must be <live root>/<mastermind-id>/<name>%s: %s", sceneExt, arg)
	}
	id, file := parts[0], parts[1]
	if err := mastermind.ValidID(id); err != nil {
		return Resolved{}, false, usagef("%v", err)
	}
	if !strings.HasSuffix(file, sceneExt) {
		return Resolved{}, false, usagef("scene path must end in %s: %s", sceneExt, arg)
	}
	name := strings.TrimSuffix(file, sceneExt)
	if err := ValidSceneName(name); err != nil {
		return Resolved{}, false, err
	}

	// Confinement (S1's rule, live scope): after EvalSymlinks on the deepest
	// existing ancestor the path must still sit under <live root>/<id>, so a
	// symlinked parent can never steer a write outside the live directory.
	liveDir := filepath.Join(realRoot, id)
	tail, anchor, err := splitExisting(path)
	if err != nil {
		return Resolved{}, false, usagef("%v", err)
	}
	real, err := filepath.EvalSymlinks(anchor)
	if err != nil {
		return Resolved{}, false, usagef("resolve %s: %v", anchor, err)
	}
	full := filepath.Join(real, tail)
	if !underRoot(liveDir, full) {
		return Resolved{}, false, usagef("%s is outside the live directory %s", path, liveDir)
	}
	return Resolved{Scope: ScopeLive, Scene: name, Path: path, LiveDir: liveDir}, true, nil
}

// ResolveLiveDir resolves name inside one MasterMind's live directory. When both
// roots are known it refuses two scopes that nest -- a live directory under the
// repo root, or the repo root under the live directory -- so the scopes can
// never overlap (S5).
func ResolveLiveDir(repoRoot, liveDir, name string) (Resolved, error) {
	if err := ValidSceneName(name); err != nil {
		return Resolved{}, err
	}
	if err := disjointScopes(repoRoot, liveDir); err != nil {
		return Resolved{}, err
	}
	liveDir = filepath.Clean(liveDir)
	return Resolved{
		Scope:   ScopeLive,
		Scene:   name,
		Path:    filepath.Join(liveDir, name+sceneExt),
		LiveDir: liveDir,
	}, nil
}

// disjointScopes refuses two scopes that nest (S5). It is a no-op when either
// root is unknown, which is what a caller that never consulted the repository
// passes.
func disjointScopes(repoRoot, liveDir string) error {
	if repoRoot == "" || liveDir == "" {
		return nil
	}
	repo := filepath.Clean(repoRoot)
	if real, err := filepath.EvalSymlinks(repoRoot); err == nil {
		repo = real
	}
	live := filepath.Clean(liveDir)
	if real, err := filepath.EvalSymlinks(liveDir); err == nil {
		live = real
	}
	if underRoot(repo, live) {
		return usagef("the live board directory %s is inside the repository root %s", liveDir, repoRoot)
	}
	if underRoot(live, repo) {
		return usagef("the repository root %s is inside the live board directory %s", repoRoot, liveDir)
	}
	return nil
}

// SelectScene picks the scene a live board opens: --board when given, else the
// pointer, else the default. fromPointer is true only when the name came from
// the pointer file, so the caller knows not to rewrite it (S2/S4).
func SelectScene(liveDir, flagName string) (name string, fromPointer bool, err error) {
	if flagName != "" {
		if err := ValidSceneName(flagName); err != nil {
			return "", false, err
		}
		return flagName, false, nil
	}
	name, present, err := Pointer(liveDir)
	if err != nil {
		return "", false, err
	}
	return name, present, nil
}

// Pointer reads a live directory's pointer. A missing or empty file is the
// default scene with present false. Content that is not a slug is a usage
// refusal naming the file, never silently replaced (S4).
func Pointer(liveDir string) (name string, present bool, err error) {
	path := filepath.Join(liveDir, pointerName)
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return DefaultBoard, false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", path, rerr)
	}
	name = strings.TrimSpace(string(data))
	if name == "" {
		return DefaultBoard, false, nil
	}
	if verr := ValidSceneName(name); verr != nil {
		return "", false, usagef("pointer %s holds %q, which is not a scene name", path, name)
	}
	return name, true, nil
}

// WritePointer writes name to a live directory's pointer, creating the live
// directory 0700 and the file 0600 through the existing atomic write. It writes
// nothing when the pointer already holds the name (S4: written only when it
// differs).
func WritePointer(liveDir, name string) error {
	if err := ValidSceneName(name); err != nil {
		return err
	}
	if existing, present, err := Pointer(liveDir); err == nil && present && existing == name {
		return nil
	}
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		return fmt.Errorf("create live directory %s: %w", liveDir, err)
	}
	return writeAtomic(filepath.Join(liveDir, pointerName), []byte(name+"\n"))
}

// serverFileName is the file a running live board writes its address into so a
// reader can find and copy the URL (S6).
const serverFileName = "server.json"

// ServerInfo is one live board server's advertisement (S6). The json keys are
// exactly scene, url, port, pid and started_at.
type ServerInfo struct {
	Scene     string `json:"scene"`
	URL       string `json:"url"`
	Port      int    `json:"port"`
	PID       int    `json:"pid"`
	StartedAt int64  `json:"started_at"`
}

// WriteServerInfo writes info to liveDir/server.json atomically (0600),
// creating the live directory 0700. A second server on the same board is
// allowed: the most recent writer wins, and nothing here refuses an existing
// advertisement (S6).
func WriteServerInfo(liveDir string, info ServerInfo) error {
	if err := os.MkdirAll(liveDir, 0o700); err != nil {
		return fmt.Errorf("create live directory %s: %w", liveDir, err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(liveDir, serverFileName), append(data, '\n'))
}

// ReadServerInfo reads liveDir/server.json. A missing or corrupt file reads as
// absent with no error: a stale advertisement is ignored, never fatal (S6).
func ReadServerInfo(liveDir string) (ServerInfo, bool, error) {
	data, err := os.ReadFile(filepath.Join(liveDir, serverFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return ServerInfo{}, false, nil
		}
		return ServerInfo{}, false, err
	}
	var info ServerInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return ServerInfo{}, false, nil
	}
	return info, true, nil
}

// RemoveServerInfo removes liveDir/server.json only when the file still carries
// own's pid and started_at, so a first server's shutdown never deletes a second
// server's advertisement (S6).
func RemoveServerInfo(liveDir string, own ServerInfo) error {
	cur, ok, err := ReadServerInfo(liveDir)
	if err != nil || !ok {
		return err
	}
	if cur.PID != own.PID || cur.StartedAt != own.StartedAt {
		return nil
	}
	if err := os.Remove(filepath.Join(liveDir, serverFileName)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LiveURL reads a live board's advertisement and returns its URL when the
// advertised scene matches and the server is still live: its pid is alive and
// its recorded start time still matches (S7/S9). procStart is the liveness
// seam the caller supplies (procStartUnix in production, a stub in tests). A
// missing, absent or stale advertisement is not live.
func LiveURL(liveDir, scene string, procStart func(pid int) (int64, error)) (string, bool, error) {
	info, ok, err := ReadServerInfo(liveDir)
	if err != nil || !ok {
		return "", false, err
	}
	if info.Scene != scene {
		return "", false, nil
	}
	if !serverLive(info, procStart) {
		return "", false, nil
	}
	return info.URL, true, nil
}

// serverLive reports whether info's process is live under the RecordState
// pattern (S7): a positive pid and start, and a measured start that still
// matches. started_at 0 -- the measurement failure at startup -- reads as not
// live; a dead or unmeasurable pid and a reused one read the same way.
func serverLive(info ServerInfo, procStart func(pid int) (int64, error)) bool {
	if info.PID <= 0 || info.StartedAt <= 0 || procStart == nil {
		return false
	}
	started, err := procStart(info.PID)
	if err != nil {
		return false
	}
	return started == info.StartedAt
}
