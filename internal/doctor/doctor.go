// Package doctor builds the health checks `relevo doctor` renders: what to
// probe on this machine, and what severity and fix each result carries.
package doctor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
)

type Severity int

const (
	SevOK   Severity = iota // nothing to do; also used for "not checked"
	SevWarn                 // wrong, but relevo can still run
	SevFail                 // relevo cannot run
	SevInfo                 // an observation, counted as neither Failures nor Warnings
)

func (s Severity) String() string {
	switch s {
	case SevOK:
		return "ok"
	case SevWarn:
		return "warn"
	case SevFail:
		return "FAIL"
	case SevInfo:
		return "info"
	default:
		return "unknown"
	}
}

// Check is one probe's result. Fix is a literal command the user can paste,
// never prose, and is empty when Severity is SevOK.
type Check struct {
	Group       string // "" for global rows, else the harness kind
	Name        string // "daemon", "binary", "plugin", "plan-executor"
	Severity    Severity
	Detail      string // what was actually found
	Fix         string // the command that fixes it
	ProbeFailed bool   // relevo could not establish the fact at all, vs. establishing a fault
	Unsafe      int    // processes outside their own scope, set only on the restart row
}

// Report is every check, in render order, plus the derived verdict.
type Report struct {
	Checks []Check
	// UsableBuilder is true when at least one checked kind has its binary on
	// PATH; meaningless (and unread) in adopted mode.
	UsableBuilder bool

	NoCandidates   bool   // verdict input the caller sets; Run leaves it zero
	BuilderRefusal string // RoleRefusal.Text for builder, "" when bind would pick
}

// Failures counts checks with SevFail.
func (r Report) Failures() int { return r.countSeverity(SevFail) }

// Warnings counts checks with SevWarn.
func (r Report) Warnings() int { return r.countSeverity(SevWarn) }

func (r Report) countSeverity(sev Severity) int {
	n := 0
	for _, c := range r.Checks {
		if c.Severity == sev {
			n++
		}
	}
	return n
}

// RunOption configures doctor execution.
type RunOption func(*runConfig)

type runConfig struct {
	adopted        bool
	definitions    map[string][]string
	usagePrices    []byte
	usageOn        bool
	usageOpencode  bool
	extra          []Check
	stateRoot      string
	configWarnings []string
}

// WithAdopted scopes the per-kind checks to an adopted builder: the user
// launched that agent themselves, so its binary and role are not checked.
func WithAdopted(adopted bool) RunOption {
	return func(cfg *runConfig) { cfg.adopted = adopted }
}

// WithDefinitions limits the role rows for each kind to the named
// definitions. A kind absent from the map keeps every shipped definition.
func WithDefinitions(defs map[string][]string) RunOption {
	return func(cfg *runConfig) { cfg.definitions = defs }
}

// WithUsage enables the round-usage checks: sqlite3 on PATH, and the stored
// prices body's as_of age. prices is nil when the config section is absent.
func WithUsage(prices []byte, opencodeConfigured bool) RunOption {
	return func(cfg *runConfig) {
		cfg.usagePrices = prices
		cfg.usageOn = true
		cfg.usageOpencode = opencodeConfigured
	}
}

// WithExtraChecks appends checks verbatim at the end of the report, after
// every check Run itself builds: checks built from data Run never sees
// (server probes assembled in cmd/relevo).
func WithExtraChecks(checks []Check) RunOption {
	return func(cfg *runConfig) { cfg.extra = append(cfg.extra, checks...) }
}

// WithStateRoot lets the opencode branch check opencode's own
// permission.external_directory allowlist against relevo's state root.
// Empty disables the check.
func WithStateRoot(root string) RunOption {
	return func(cfg *runConfig) { cfg.stateRoot = root }
}

// WithConfigWarnings supplies the unknown-key and skipped-candidate warnings
// the config readers produced, rendered as the one global `config` row.
func WithConfigWarnings(warnings []string) RunOption {
	return func(cfg *runConfig) { cfg.configWarnings = warnings }
}

// ConfigCheck is the one global doctor row for config the readers could not
// fully use. No warnings -> OK; otherwise a Warn listing them all.
func ConfigCheck(warnings []string) Check {
	c := Check{Name: "config", Group: ""}
	if len(warnings) == 0 {
		c.Severity = SevOK
		c.Detail = "the policy and candidates sections have no unknown keys"
		return c
	}
	c.Severity = SevWarn
	c.Detail = strings.Join(warnings, "; ")
	c.Fix = "remove the unknown keys, or upgrade relevo to the version that reads them"
	return c
}

func (cfg runConfig) wants(kind, name string) bool {
	defs, limited := cfg.definitions[kind]
	if !limited {
		return true
	}
	for _, d := range defs {
		if d == name {
			return true
		}
	}
	return false
}

// Run executes every check for the given kinds against env and never returns
// an error: a failed probe becomes a Check saying so.
func Run(ctx context.Context, env Env, kinds []string, opts ...RunOption) Report {
	var cfg runConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	checks := []Check{releaseCheck(env), daemonRow(ctx, env), ConfigCheck(cfg.configWarnings)}

	usableBuilder := false
	for _, kind := range kinds {
		kindChecks, usable := checksForKind(ctx, env, cfg, kind)
		checks = append(checks, kindChecks...)
		usableBuilder = usableBuilder || usable
	}

	if cfg.usageOn {
		checks = append(checks, usageChecks(env, cfg)...)
	}
	checks = append(checks, cfg.extra...)

	return Report{Checks: checks, UsableBuilder: usableBuilder}
}

// daemonRow handles the two states Run itself can tell (a probe error, or
// the daemon not running) and defers to daemonCheck for the rest.
func daemonRow(ctx context.Context, env Env) Check {
	running, err := env.DaemonRunning(ctx)
	switch {
	case err != nil:
		return Check{Name: "daemon", Severity: SevWarn, Detail: fmt.Sprintf("probe error: %v", err), Fix: "relevo daemon", ProbeFailed: true}
	case !running:
		return Check{Name: "daemon", Severity: SevWarn, Detail: "not running", Fix: "relevo daemon"}
	default:
		return daemonCheck(env)
	}
}

// checksForKind builds every row for one harness kind. A missing binary
// stops here with only the binary row.
func checksForKind(ctx context.Context, env Env, cfg runConfig, kind string) ([]Check, bool) {
	h, known := harness.Lookup(kind)
	var checks []Check
	usable := false

	if !cfg.adopted {
		binName := kind
		if known && h.Binary != "" {
			binName = h.Binary
		}
		binPath, err := env.LookPath(binName)
		if err != nil {
			return []Check{{Group: kind, Name: "binary", Severity: SevWarn, Detail: "not on PATH -- skipping the rest of this harness"}}, false
		}
		checks = append(checks, Check{Group: kind, Name: "binary", Severity: SevOK, Detail: binPath})
		usable = true
		if known && h.MinVersion != "" {
			checks = append(checks, versionCheck(ctx, env, kind, h, binPath))
		}
	}

	if kind == "opencode" { // the service note applies even for an adopted pane
		if c := opencodeServiceCheck(ctx, env); c.Name != "" {
			checks = append(checks, c)
		}
	}

	if !cfg.adopted {
		checks = append(checks, roleRows(env, cfg, kind, h, known)...)
		if kind == "opencode" {
			if cfg.stateRoot != "" {
				checks = append(checks, opencodeAllowlistCheck(env, cfg.stateRoot))
			}
			checks = append(checks, opencodePluginCheck(env))
			if kc := opencodePluginKeysCheck(env); kc.Name != "" {
				checks = append(checks, kc)
			}
		}
	}

	return checks, usable
}

// versionCheck holds a harness with a version floor to it before its roles
// are checked: below the floor, every role row would report a file the
// binary cannot load.
func versionCheck(ctx context.Context, env Env, kind string, h harness.Harness, binPath string) Check {
	ver, err := env.BinaryVersion(ctx, binPath)
	if err != nil {
		return Check{Group: kind, Name: "version", Severity: SevWarn,
			Detail: fmt.Sprintf("could not read version: %v", err), ProbeFailed: true}
	}
	atLeast, err := semverAtLeast(ver, h.MinVersion)
	switch {
	case err != nil:
		return Check{Group: kind, Name: "version", Severity: SevWarn, Detail: fmt.Sprintf("unparseable version %q", ver)}
	case !atLeast:
		return Check{
			Group: kind, Name: "version", Severity: SevFail,
			Detail: fmt.Sprintf("%s (below floor %s)", ver, h.MinVersion),
			Fix:    fmt.Sprintf("upgrade %s to >= %s", h.Binary, h.MinVersion),
		}
	default:
		return Check{Group: kind, Name: "version", Severity: SevOK, Detail: fmt.Sprintf("%s (floor %s)", ver, h.MinVersion)}
	}
}

// roleRows builds the role-staleness row, one row per role definition, and a
// custom row per unshipped definition in scope; an unknown kind gets a
// single "not checked" placeholder instead.
func roleRows(env Env, cfg runConfig, kind string, h harness.Harness, known bool) []Check {
	var checks []Check
	if known {
		checks = append(checks, rolesCheck(env, kind))
	}
	if !known {
		return append(checks, Check{
			Group: kind, Name: "plan-executor", Severity: SevOK,
			Detail: fmt.Sprintf("not checked -- relevo has no role path for kind %q", kind),
		})
	}

	for _, r := range h.Roles {
		if cfg.wants(kind, r.Name) {
			checks = append(checks, roleCheck(env, kind, r))
		}
	}

	custom := append([]string(nil), cfg.definitions[kind]...)
	sort.Strings(custom)
	for _, name := range custom {
		if !harness.IsShipped(kind, name) {
			checks = append(checks, customRoleCheck(env, kind, name))
		}
	}
	return checks
}
