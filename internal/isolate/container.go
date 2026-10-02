package isolate

import (
	"path/filepath"
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
// linked worktree can reach its common git dir and commit, and any per-owner
// harness homes to bind.
type ContainerSpec struct {
	Image    string
	RepoRoot string
	Homes    []Mount
}

// ContainerArgv renders spec into the ProcSpec a container round runs: the
// podman command that starts the builder under the container supervisor. The
// returned spec keeps Dir, Env, LogPath, StreamPath and DenyEnv unchanged,
// clears Scope (container rounds run under no systemd scope) and Credential
// (the container runs as the invoking user via keep-id), and replaces Argv with
// the podman command. Pure.
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

// containerMounts renders every bind in the fixed order the contract names:
// the round tree, the binding's out/ directory, the owner's bare repo, the
// owner's harness homes, then a private /tmp. Each mount uses the identical
// host and container path, which is what keeps the round tree's absolute paths
// valid inside the container.
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
	for _, h := range c.Homes {
		add(h)
	}
	return append(argv, "--tmpfs", "/tmp")
}

// containerName is the podman container name: the scope unit when Start had
// one, else the sanitized stream base. Either way it is sanitized to the
// runtime's name alphabet so a round names a legal, operator-visible container.
func containerName(spec spawn.ProcSpec) string {
	if spec.Scope != nil && spec.Scope.Unit != "" {
		return sanitizeContainerName(spec.Scope.Unit)
	}
	return sanitizeContainerName(filepath.Base(spec.StreamPath))
}

// sanitizeContainerName replaces every character outside the runtime's name
// alphabet with "-" and strips leading characters a container name may not
// begin with. An empty result (an empty or all-invalid input) becomes a fixed
// fallback so a Start never renders an empty --name.
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
// --pids-limit from TasksMax. An empty field is omitted. Slice, CPUWeight and
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
