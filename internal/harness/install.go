package harness

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/fuad-daoud/relevo/internal/db"
)

// InstallEnv defines the filesystem and PATH seams required by Install.
type InstallEnv interface {
	LookPath(binary string) (string, error)
	HomePath(rel string) (string, error)
	// ReadFile: absent must satisfy errors.Is(err, fs.ErrNotExist), and any
	// other error is treated as "present, differs".
	ReadFile(path string) ([]byte, error)
	MkdirAll(dir string) error
	WriteFile(path string, data []byte) error
	// LoadManifest reads the role manifest: a home-relative path to the sha256
	// of what relevo last wrote there. A missing manifest is empty, not an error.
	LoadManifest() (map[string]string, error)
	// SaveManifest writes the manifest; Install calls it once, only when the
	// map changed and opts.DryRun is false.
	SaveManifest(map[string]string) error
}

// InstallOptions controls the behavior of Install.
type InstallOptions struct {
	Kind   string
	Role   string
	Force  bool
	DryRun bool
	// Files opts a kind's shipped files in; when false, an absent one produces
	// no result row. A file present on disk follows the definition rules either way.
	Files bool
}

// InstallOutcome represents the outcome of an install action for a single role.
type InstallOutcome string

const (
	OutcomeWrote          InstallOutcome = "wrote"
	OutcomeOverwrote      InstallOutcome = "overwrote"
	OutcomeUpdated        InstallOutcome = "updated (unchanged since relevo wrote it)"
	OutcomeKeptIdentical  InstallOutcome = "kept (identical)"
	OutcomeKeptDiffers    InstallOutcome = "kept (differs; --force to overwrite)"
	OutcomeWouldWrite     InstallOutcome = "would write"
	OutcomeWouldOverwrite InstallOutcome = "would overwrite"
	OutcomeWouldUpdate    InstallOutcome = "would update"
	OutcomeError          InstallOutcome = "error"
)

// InstallResult describes the outcome of installing one role definition.
type InstallResult struct {
	Kind    string
	Role    string
	Path    string
	Outcome InstallOutcome
	Err     string
	// NewerShipped reports that the kept difference sits on an older copy: the
	// manifest holds what relevo last wrote here, and that is not the
	// definition relevo ships now, so a reset brings a newer one rather than
	// only undoing the user's edit.
	NewerShipped bool
}

var ErrUnknownKind = errors.New("unknown harness kind")

var ErrUnknownRole = errors.New("unknown role")

// DocEqual reports whether shipped and installed are equal after trimming
// trailing whitespace (" \t\r\n").
func DocEqual(shipped, installed []byte) bool {
	return bytes.Equal(bytes.TrimRight(shipped, " \t\r\n"), bytes.TrimRight(installed, " \t\r\n"))
}

// Install decides, per (kind, role), whether the shipped definition lands on
// disk, and lands it. Results are ordered by harness.All() and each kind's
// Roles table order; per-file failures are error outcomes, never returned
// errors. The role manifest is loaded once, threaded through every decision,
// and saved once at the end. A manifest that cannot be read is reported once as
// the returned error (with the results still returned) and treated as empty.
func Install(env InstallEnv, opts InstallOptions) ([]InstallResult, error) {
	manifest, merr := env.LoadManifest()
	if merr != nil || manifest == nil {
		manifest = map[string]string{}
	}

	kinds, err := installKinds(env, opts)
	if err != nil {
		return nil, err
	}
	if err := checkInstallRole(kinds, opts.Role); err != nil {
		return nil, err
	}

	var results []InstallResult
	changed := false
	for _, h := range kinds {
		roleResults, roleChanged, err := installRoles(env, opts, h, manifest)
		if err != nil {
			return nil, err
		}
		fileResults, fileChanged, err := installFiles(env, opts, h, manifest)
		if err != nil {
			return nil, err
		}
		changed = changed || roleChanged || fileChanged
		results = append(results, roleResults...)
		results = append(results, fileResults...)
	}

	if changed && !opts.DryRun {
		if serr := env.SaveManifest(manifest); serr != nil {
			// The definitions landed; only the record of them did not.
			return results, serr
		}
	}
	return results, merr
}

// installKinds is the kinds Install touches: the named one, or every kind with
// its binary on PATH.
func installKinds(env InstallEnv, opts InstallOptions) ([]Harness, error) {
	if opts.Kind != "" {
		h, ok := Lookup(opts.Kind)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrUnknownKind, opts.Kind)
		}
		return []Harness{h}, nil
	}
	var kinds []Harness
	for _, h := range All() {
		if _, err := env.LookPath(h.Binary); err == nil {
			kinds = append(kinds, h)
		}
	}
	return kinds, nil
}

// checkInstallRole refuses role when no candidate kind ships it, naming the
// roles that do exist.
func checkInstallRole(kinds []Harness, role string) error {
	if role == "" {
		return nil
	}
	candidates := kinds
	if len(candidates) == 0 {
		candidates = All()
	}
	for _, c := range candidates {
		if _, ok := c.Role(role); ok {
			return nil
		}
	}
	knownMap := make(map[string]struct{})
	for _, c := range candidates {
		for _, name := range c.RoleNames() {
			knownMap[name] = struct{}{}
		}
	}
	known := make([]string, 0, len(knownMap))
	for name := range knownMap {
		known = append(known, name)
	}
	sort.Strings(known)
	return fmt.Errorf("%w: %q (known: %v)", ErrUnknownRole, role, known)
}

func installRoles(env InstallEnv, opts InstallOptions, h Harness, manifest map[string]string) ([]InstallResult, bool, error) {
	var results []InstallResult
	changed := false
	for _, r := range h.Roles {
		if opts.Role != "" && r.Name != opts.Role {
			continue
		}
		res, rchanged, err := installOne(env, opts, h.Kind, r, manifest)
		if err != nil {
			return nil, false, err
		}
		changed = changed || rchanged
		results = append(results, res)
	}
	return results, changed, nil
}

// installFiles handles a kind's opt-in shipped files. Only a full install (no
// --agent) touches them, never a single-role probe.
func installFiles(env InstallEnv, opts InstallOptions, h Harness, manifest map[string]string) ([]InstallResult, bool, error) {
	if opts.Role != "" {
		return nil, false, nil
	}
	var results []InstallResult
	changed := false
	for _, f := range h.Files {
		shipped, err := ShippedFileBytes(h.Kind, f.Name)
		if err != nil {
			results = append(results, InstallResult{
				Kind:    h.Kind,
				Role:    f.Name,
				Path:    f.Path,
				Outcome: OutcomeError,
				Err:     err.Error(),
			})
			continue
		}
		full, err := env.HomePath(f.Path)
		if err != nil {
			return nil, false, err
		}
		if _, rerr := env.ReadFile(full); errors.Is(rerr, fs.ErrNotExist) && !opts.Files {
			continue
		}
		res, fchanged, err := installBytes(env, opts, InstallResult{Kind: h.Kind, Role: f.Name, Path: f.Path}, f.Path, shipped, manifest, "")
		if err != nil {
			return nil, false, err
		}
		changed = changed || fchanged
		results = append(results, res)
	}
	return results, changed, nil
}

// agentDocBase is the embedded definition's basename for role r of kind,
// exactly as AgentDoc composes its embed path.
func agentDocBase(r Role, kind string) string {
	ext := "md"
	if h, ok := Lookup(kind); ok && h.DocExt != "" {
		ext = h.DocExt
	}
	return r.Doc + "." + ext
}

// installOne applies the decision table to one (kind, role); its bool reports
// whether the manifest changed.
func installOne(env InstallEnv, opts InstallOptions, kind string, r Role, manifest map[string]string) (InstallResult, bool, error) {
	shipped, err := AgentDoc(r.Name, kind)
	if err != nil {
		return InstallResult{
			Kind:    kind,
			Role:    r.Name,
			Path:    r.Path,
			Outcome: OutcomeError,
			Err:     err.Error(),
		}, false, nil
	}
	return installBytes(env, opts, InstallResult{Kind: kind, Role: r.Name, Path: r.Path}, r.Path, shipped, manifest, agentDocBase(r, kind))
}

// installBytes applies the decision table to one shipped file: an agent
// definition or an opt-in shipped file. homeRel is the home-relative path whose
// sha the manifest records; shippedBeforeKey is the embedded basename
// ShippedBefore indexes, "" for a shipped file with no pre-manifest history.
func installBytes(env InstallEnv, opts InstallOptions, res InstallResult, homeRel string, shipped []byte, manifest map[string]string, shippedBeforeKey string) (InstallResult, bool, error) {
	full, err := env.HomePath(homeRel)
	if err != nil {
		return InstallResult{}, false, err
	}
	existing, rerr := env.ReadFile(full)
	existingSHA := docSHA(existing)
	switch {
	case errors.Is(rerr, fs.ErrNotExist):
		if opts.DryRun {
			res.Outcome = OutcomeWouldWrite
			return res, false, nil
		}
		res = writeDoc(env, full, shipped, res, OutcomeWrote)
		if res.Outcome != OutcomeWrote {
			return res, false, nil
		}
		return res, record(manifest, homeRel, docSHA(shipped)), nil
	case rerr == nil && DocEqual(shipped, existing):
		res.Outcome = OutcomeKeptIdentical
		return res, record(manifest, homeRel, existingSHA), nil
	case rerr == nil && (manifest[homeRel] == existingSHA || (shippedBeforeKey != "" && ShippedBefore(shippedBeforeKey, existingSHA))):
		// The bytes are what relevo last wrote, or a blob some past relevo
		// shipped before manifests existed, so the difference is relevo's own
		// older release, not the user's edit: safe to refresh.
		if opts.DryRun {
			res.Outcome = OutcomeWouldUpdate
			return res, false, nil
		}
		res = writeDoc(env, full, shipped, res, OutcomeUpdated)
		if res.Outcome != OutcomeUpdated {
			return res, false, nil
		}
		return res, record(manifest, homeRel, docSHA(shipped)), nil
	default:
		// The manifest holds what relevo last wrote here. A record that is not
		// the definition relevo ships now means the user's edit sits on an
		// older copy, which a reset replaces with the newer one.
		res.NewerShipped = manifest[homeRel] != "" && manifest[homeRel] != docSHA(shipped)
		if !opts.Force {
			res.Outcome = OutcomeKeptDiffers
			return res, false, nil
		}
		if opts.DryRun {
			res.Outcome = OutcomeWouldOverwrite
			return res, false, nil
		}
		res = writeDoc(env, full, shipped, res, OutcomeOverwrote)
		if res.Outcome != OutcomeOverwrote {
			return res, false, nil
		}
		return res, record(manifest, homeRel, docSHA(shipped)), nil
	}
}

// record stores path's sha and reports whether that changed the map.
func record(manifest map[string]string, path, sha string) bool {
	if manifest[path] == sha {
		return false
	}
	manifest[path] = sha
	return true
}

func writeDoc(env InstallEnv, full string, data []byte, res InstallResult, success InstallOutcome) InstallResult {
	if err := env.MkdirAll(filepath.Dir(full)); err != nil {
		res.Outcome = OutcomeError
		res.Err = err.Error()
		return res
	}
	if err := env.WriteFile(full, data); err != nil {
		res.Outcome = OutcomeError
		res.Err = err.Error()
		return res
	}
	res.Outcome = success
	return res
}

// Line renders one output line: "<outcome>  ~/<path>", or for an error
// outcome "error  ~/<path>: <err>".
func (r InstallResult) Line() string {
	if r.Outcome == OutcomeError {
		return fmt.Sprintf("error  ~/%s: %s", r.Path, r.Err)
	}
	return fmt.Sprintf("%s  ~/%s", r.Outcome, r.Path)
}

type osInstallEnv struct {
	// kv is the database the role manifest lives in; an env with no database
	// reads as "nothing recorded" and refuses to save. Only OSInstallEnvKV
	// sets it.
	kv db.KV
}

// OSInstallEnv returns an InstallEnv backed by the OS and exec packages,
// without a role manifest: for seams that only resolve paths and read files.
// Callers that install definitions use OSInstallEnvKV.
func OSInstallEnv() InstallEnv {
	return osInstallEnv{}
}

// OSInstallEnvKV is OSInstallEnv with the role manifest kept in kv. The caller
// opens the machine database (this package cannot, since harness <- usage <-
// store) and passes the handle in.
func OSInstallEnvKV(kv db.KV) InstallEnv {
	return osInstallEnv{kv: kv}
}

func (osInstallEnv) LookPath(binary string) (string, error) {
	return exec.LookPath(binary)
}

func (osInstallEnv) HomePath(rel string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, rel), nil
}

func (osInstallEnv) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (osInstallEnv) MkdirAll(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// WriteFile writes through a temp file then renames, so a reader never sees a
// half-written definition.
func (osInstallEnv) WriteFile(path string, data []byte) error {
	return writeFileAtomic(path, data, 0o644)
}

func (e osInstallEnv) LoadManifest() (map[string]string, error) {
	if e.kv == nil {
		return map[string]string{}, nil
	}
	return ReadManifest(e.kv)
}

func (e osInstallEnv) SaveManifest(m map[string]string) error {
	if e.kv == nil {
		return errors.New("no role manifest store configured")
	}
	return WriteManifest(e.kv, m)
}
