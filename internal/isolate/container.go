package isolate

import (
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/fuad-daoud/relevo/internal/spawn"
)

// Mount is one host directory bound into a container round at the same
// absolute path, so argv, -C and the harness home variables need no rewriting.
// ReadOnly adds the runtime's read-only option.
type Mount struct {
	Path     string
	ReadOnly bool
}

// ContainerSpec is what a container-mode boundary needs beyond a round's
// ProcSpec: the image to run, the owner's bare repo bound read-write so a
// linked worktree can reach its common git dir and commit, any per-owner
// harness homes, and the git shim directory.
type ContainerSpec struct {
	Image      string
	RepoRoot   string
	Homes      []Mount
	GitShimDir string
}

// ContainerArgv renders spec into the ProcSpec a container round runs: the
// podman command that starts the builder under the container supervisor. Scope
// is cleared because container rounds run under no systemd scope, Credential
// because the container runs as the invoking user via keep-id, and Argv is
// replaced; a spec's TmpDir is inert on this path because the container's
// environment is only what the Env entries name.
func ContainerArgv(spec spawn.ProcSpec, c ContainerSpec) spawn.ProcSpec {
	out := spec
	out.Scope = nil
	out.Credential = nil
	out.Argv = containerCommand(spec, c)
	return out
}

// containerCommand builds the podman argv: the run flags, the mounts, the
// environment, the scope bounds, the image and the in-container supervisor
// command, then the builder's own argv. The in-container supervisor exits with
// the builder's code, so podman returns it and the outer supervisor's trailer
// carries it.
func containerCommand(spec spawn.ProcSpec, c ContainerSpec) []string {
	argv := []string{
		"podman", "run", "--rm", "--userns=keep-id",
		"--name", containerName(spec),
		"--cidfile", spec.StreamPath + ".cid",
	}
	argv = append(argv, containerMounts(spec, c)...)
	for _, e := range spec.Env {
		argv = append(argv, "--env", e)
	}
	argv = append(argv, Bounds(spec.Scope)...)
	argv = append(argv, c.Image, "/bin/sh", "-c", spawn.ContainerSupervisorScript, "relevo-supervisor", "")
	return append(argv, spec.Argv...)
}

// containerMounts renders every bind in the fixed order the contract names: the
// round tree, the binding's out/ directory, the owner's bare repo, the owner's
// harness homes, then the round's Env-derived harness homes, then the git shim
// directory, then a private /tmp. Host and container paths are identical, so
// the round tree's absolute paths stay valid inside the container.
func containerMounts(spec spawn.ProcSpec, c ContainerSpec) []string {
	var argv []string
	add := func(m Mount) {
		if m.Path == "" {
			return
		}
		vol := m.Path + ":" + m.Path
		if m.ReadOnly {
			vol += ":ro"
		}
		argv = append(argv, "-v", vol)
	}
	add(Mount{Path: spec.Dir})
	add(Mount{Path: filepath.Join(filepath.Dir(spec.StreamPath), "out")})
	add(Mount{Path: c.RepoRoot})
	seenHomes := make(map[string]bool)
	for _, h := range c.Homes {
		add(h)
		if h.Path != "" {
			seenHomes[h.Path] = true
		}
	}
	for _, e := range spec.Env {
		name, val, _ := cut(e)
		if !slices.Contains(AccountHomeVars, name) || val == "" || seenHomes[val] {
			continue
		}
		seenHomes[val] = true
		add(Mount{Path: val})
	}
	if shimOnPath(spec.Env, c.GitShimDir) {
		add(Mount{Path: c.GitShimDir, ReadOnly: true})
	}
	return append(argv, "--tmpfs", "/tmp")
}

// shimOnPath reports whether env's PATH starts with the git shim directory.
// Only a writer round's PATH carries it, and only then is the directory known to
// exist: podman refuses to bind a missing path, so a reader round on a server
// that never wrote the shim must not mount it.
func shimOnPath(env []string, dir string) bool {
	if dir == "" {
		return false
	}
	for _, e := range env {
		name, val, _ := cut(e)
		if name == "PATH" {
			first, _, _ := strings.Cut(val, ":")
			return first == dir
		}
	}
	return false
}

// containerName is the podman container name: the scope unit when Start had
// one, else the sanitized stream base.
func containerName(spec spawn.ProcSpec) string {
	if spec.Scope != nil && spec.Scope.Unit != "" {
		return sanitizeContainerName(spec.Scope.Unit)
	}
	return sanitizeContainerName(filepath.Base(spec.StreamPath))
}

// sanitizeContainerName replaces every character outside the runtime's name
// alphabet with "-" and strips leading characters a name may not begin with.
// An empty result becomes a fixed fallback, so a Start never renders --name "".
func sanitizeContainerName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	name := strings.TrimLeft(b.String(), "-.")
	if name == "" {
		return "relevo-round"
	}
	return name
}

// Bounds renders a scope's resource limits as podman run flags: --memory from
// MemoryMax, --cpus from CPUQuota (a percentage rendered as a decimal), and
// --pids-limit from TasksMax. An empty field is omitted; Slice, CPUWeight and
// AllowedCPUs have no podman equivalent and are dropped.
func Bounds(scope *spawn.ScopeSpec) []string {
	if scope == nil {
		return nil
	}
	var argv []string
	if scope.MemoryMax != "" {
		argv = append(argv, "--memory", scope.MemoryMax)
	}
	if cpus, ok := cpusFromQuota(scope.CPUQuota); ok {
		argv = append(argv, "--cpus", cpus)
	}
	if scope.TasksMax > 0 {
		argv = append(argv, "--pids-limit", strconv.Itoa(scope.TasksMax))
	}
	return argv
}

// cpusFromQuota converts a systemd CPU quota ("200%") to the decimal podman
// --cpus takes ("2"). A non-percentage, malformed or non-positive value yields
// ok false, so it contributes no flag rather than an invalid one.
func cpusFromQuota(quota string) (string, bool) {
	digits, ok := strings.CutSuffix(quota, "%")
	if !ok || digits == "" {
		return "", false
	}
	for _, r := range digits {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	n, err := strconv.Atoi(digits)
	if err != nil || n <= 0 {
		return "", false
	}
	if n%100 == 0 {
		return strconv.Itoa(n / 100), true
	}
	return strconv.FormatFloat(float64(n)/100, 'f', -1, 64), true
}
