package doctor

import (
	"fmt"
	"strings"
)

// ScopeBlock is one policy scope block whose allowed_cpus asks relevo to pin
// a round per core. Key is the block-qualified field name the row's messages
// use ("scope.allowed_cpus" or "serve.scope.allowed_cpus"); MaxCPU is the
// highest core AllowedCPUs names (the caller computes it with
// policy.ParseCPUList, so this package never imports policy).
type ScopeBlock struct {
	Key         string
	AllowedCPUs string
	MaxCPU      int
}

// UserManagerControllersPath is the user manager's cgroup.controllers file:
// read to tell whether cpuset is delegated, since an undelegated
// AllowedCPUs is silently ignored elsewhere.
func UserManagerControllersPath(uid int) string {
	return fmt.Sprintf("/sys/fs/cgroup/user.slice/user-%d.slice/user@%d.service/cgroup.controllers", uid, uid)
}

// ScopeChecks checks every scope block that asks for cpu pinning: the user
// manager must have cpuset delegated, and the pool must name only cores this
// host has. Returns nil when every block is empty; each non-empty block gets
// one row.
func ScopeChecks(env Env, blocks []ScopeBlock, controllersPath string, ncpu int) []Check {
	nonEmpty := blocks[:0:0]
	for _, b := range blocks {
		if b.AllowedCPUs != "" {
			nonEmpty = append(nonEmpty, b)
		}
	}
	if len(nonEmpty) == 0 {
		return nil
	}

	raw, readErr := env.ReadFile(controllersPath)

	checks := make([]Check, 0, len(nonEmpty))
	for _, b := range nonEmpty {
		c := Check{Group: "scope", Name: rowName(b.Key)}
		switch {
		case readErr != nil:
			c.Severity = SevWarn
			c.Detail = fmt.Sprintf("cannot read %s (%v); cpu pinning cannot be confirmed", controllersPath, readErr)
			c.ProbeFailed = true
		case !hasField(raw, "cpuset"):
			c.Severity = SevWarn
			c.Detail = fmt.Sprintf("%s = %s is set but cpuset is not delegated to your user manager; systemd refuses or ignores AllowedCPUs", b.Key, b.AllowedCPUs)
			c.Fix = "as root: mkdir -p /etc/systemd/system/user@.service.d && printf '[Service]\\nDelegate=cpu cpuset io memory pids\\n' > /etc/systemd/system/user@.service.d/delegate.conf && systemctl daemon-reload; then log out and back in"
		case b.MaxCPU >= ncpu:
			c.Severity = SevWarn
			c.Detail = fmt.Sprintf("%s names cpu %d but this host has %d (0-%d)", b.Key, b.MaxCPU, ncpu, ncpu-1)
			c.Fix = fmt.Sprintf("narrow %s in config policy to cpus this host has", b.Key)
		default:
			c.Severity = SevOK
			c.Detail = fmt.Sprintf("cpuset delegated; rounds pinned one per core from %s", b.AllowedCPUs)
		}
		checks = append(checks, c)
	}
	return checks
}

// rowName drops key's "scope" segment: "scope.allowed_cpus" -> "allowed_cpus",
// "serve.scope.allowed_cpus" -> "serve.allowed_cpus".
func rowName(key string) string {
	parts := strings.Split(key, ".")
	out := parts[:0:0]
	for _, p := range parts {
		if p == "scope" {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, ".")
}

func hasField(raw []byte, field string) bool {
	for _, f := range strings.Fields(string(raw)) {
		if f == field {
			return true
		}
	}
	return false
}
