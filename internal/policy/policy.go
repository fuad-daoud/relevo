// Package policy loads policy.json: where the mastermind tells relevo how to
// choose among candidates and tunes the daemon's knobs.
package policy

import (
	"runtime"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
)

// Policy is the mastermind's candidate preferences and daemon tuning, loaded
// from policy.json.
type Policy struct {
	// Order maps a role to its preferred candidate tokens, most preferred
	// first; an absent role is unordered.
	Order map[string][]string `json:"order,omitempty"`

	// MaxSwitches caps builder replacements within one round before the
	// binding goes NEEDS YOU; nil is DefaultMaxSwitches, 0 disables it.
	MaxSwitches *int `json:"max_switches,omitempty"`
	// ArtifactMaxMB caps a round's artifact directory; nil and 0 mean
	// DefaultArtifactMaxMB, and a negative value is refused.
	ArtifactMaxMB *int `json:"artifact_max_mb,omitempty"`
	// LimitGateDefaultMS is how long a matched limit gates the provider when
	// no reset time parses; nil is DefaultLimitGate.
	LimitGateDefaultMS *int `json:"limit_gate_default_ms,omitempty"`
	// StallAfterMS is how long a live builder's stream may go silent before
	// it is labelled stalled; nil is DefaultStallAfter.
	StallAfterMS *int `json:"stall_after_ms,omitempty"`
	// ProgressIntervalMS is how often the daemon samples a round's progress;
	// nil is DefaultProgressInterval.
	ProgressIntervalMS *int `json:"progress_interval_ms,omitempty"`
	// ExploreAfterMS is how long output may move without the tree changing
	// before a builder is labelled exploring; nil is DefaultExploreAfter.
	ExploreAfterMS *int `json:"explore_after_ms,omitempty"`
	// StaleAfterMS is how long NEEDS YOU or HELD may sit unacted before it
	// is labelled stale; nil is DefaultStaleAfter.
	StaleAfterMS *int `json:"stale_after_ms,omitempty"`

	// ScanPatterns extends the built-in instruction-shaped line patterns.
	ScanPatterns []string `json:"scan_patterns,omitempty"`
	// Classify configures the optional classifier beside the regex scan.
	Classify *Classify `json:"classify,omitempty"`

	// Tier maps a role to its default tier, capped by MaxTierOrDefault().
	Tier map[string]string `json:"tier,omitempty"`
	// MaxTier is the highest tier a command may request without
	// --allow-yolo; "" is DefaultMaxTier and must not be "harness".
	MaxTier string `json:"max_tier,omitempty"`

	// Gate configures the acceptance command run on a binding with no
	// completion marker of its own.
	Gate *GatePolicy `json:"gate,omitempty"`
	// Verify configures the default for `relevo send --verify`.
	Verify *VerifyPolicy `json:"verify,omitempty"`
	// Notify configures webhook sinks for lifecycle events.
	Notify *NotifyPolicy `json:"notify,omitempty"`
	// Serve configures relevo serve; nil is every default.
	Serve *ServePolicy `json:"serve,omitempty"`
	// Scope is the systemd scope template for rounds this host runs;
	// Serve.Scope replaces it entirely for served rounds.
	Scope *ScopePolicy `json:"scope,omitempty"`
}

// ServePolicy configures relevo serve.
type ServePolicy struct {
	// MaxBuilders caps headless builders running at once; nil is
	// max(1, runtime.NumCPU()-1).
	MaxBuilders *int `json:"max_builders,omitempty"`
	// Scope is the per-round systemd scope; nil is defaults.
	Scope *ScopePolicy `json:"scope,omitempty"`
}

// ScopePolicy configures the per-round systemd scope a served headless
// builder runs under.
type ScopePolicy struct {
	Enabled   *bool  `json:"enabled,omitempty"`    // nil = true
	Slice     string `json:"slice,omitempty"`      // "" = systemd default; else must end in ".slice"
	CPUWeight int    `json:"cpu_weight,omitempty"` // 0 = 100; else 1..10000
	MemoryMax string `json:"memory_max,omitempty"` // "" = none; else ^[0-9]+[KMGT]?$
	CPUQuota  string `json:"cpu_quota,omitempty"`  // "" = none; else ^[0-9]+%$, at least 1%
	// GateCPUQuota is the gate's own CPU ceiling; "" uses CPUQuota.
	GateCPUQuota string `json:"gate_cpu_quota,omitempty"`
	// AllowedCPUs is the pool of cores relevo hands out, one per round; ""
	// means no pinning. Needs cpuset delegated to the user manager.
	AllowedCPUs string `json:"allowed_cpus,omitempty"`
	TasksMax    int    `json:"tasks_max,omitempty"` // 0 = none; else >= 1
}

// NotifyPolicy configures webhook delivery of lifecycle events.
type NotifyPolicy struct {
	Webhooks []Webhook `json:"webhooks,omitempty"`
}

// Webhook is one HTTP sink that receives a JSON POST for each event it
// matches.
type Webhook struct {
	URL string `json:"url"` // required, http:// or https://
	// Events filters which events reach this webhook; empty means every event.
	Events []string `json:"events,omitempty"`
	// Format shapes the POST body: "json" (default), "slack", or "discord".
	Format string `json:"format,omitempty"`
}

// WebhookEvents is every event name a webhook may subscribe to, in the order
// Parse's own error message lists them. A subscription for state_changed may
// also carry a ":<state>" suffix naming one target state.
var WebhookEvents = []string{"state_changed", "round_started", "fork_created", "builder_stalled", "binding_stale"}

// VerifyPolicy configures the default verify flag for `relevo send`.
type VerifyPolicy struct {
	// Default is used when neither --verify nor --no-verify was given.
	Default bool `json:"default,omitempty"`
}

// GatePolicy configures the default gate command and its timeout.
type GatePolicy struct {
	Default   string `json:"default,omitempty"`    // "" = no gate unless --gate
	TimeoutMS *int   `json:"timeout_ms,omitempty"` // nil = DefaultGateTimeout; must be > 0
	// Regate is the automatic repair-round budget after a failing gate; nil
	// is 0 (no repair).
	Regate *int `json:"regate,omitempty"`
}

// Classify configures the optional classifier beside the regex scan.
type Classify struct {
	Provider           string   `json:"provider"`                      // required; only "jev" is known
	Model              string   `json:"model,omitempty"`               // default DefaultClassifyModel
	InjectionThreshold *float64 `json:"injection_threshold,omitempty"` // default DefaultInjectionThreshold; (0, 1]
	TimeoutMS          *int     `json:"timeout_ms,omitempty"`          // default DefaultClassifyTimeout; must be > 0
}

const DefaultClassifyModel = "jev-latest"
const DefaultInjectionThreshold = 0.7
const DefaultClassifyTimeout = 4 * time.Second

func (c *Classify) ModelName() string {
	if c == nil || c.Model == "" {
		return DefaultClassifyModel
	}
	return c.Model
}

func (c *Classify) Threshold() float64 {
	if c == nil || c.InjectionThreshold == nil {
		return DefaultInjectionThreshold
	}
	return *c.InjectionThreshold
}

func (c *Classify) Timeout() time.Duration {
	if c == nil || c.TimeoutMS == nil {
		return DefaultClassifyTimeout
	}
	return time.Duration(*c.TimeoutMS) * time.Millisecond
}

// DefaultMaxTier is the ceiling used when MaxTier is empty.
const DefaultMaxTier = harness.TierEdit

// DefaultMaxSwitches covers a gated pick plus a failed spawn; a third in one round is a pattern a human should see.
const DefaultMaxSwitches = 2

// DefaultArtifactMaxMB is the artifact directory cap when
// policy.artifact_max_mb is unset or 0: 25 MB.
const DefaultArtifactMaxMB = 25

const DefaultLimitGate = time.Hour

// DefaultStallAfter is long enough that a thinking builder is never
// labelled, short enough that a hung one is.
const DefaultStallAfter = 15 * time.Minute

// DefaultProgressInterval is often enough that a label appears promptly,
// rare enough that sampling stays cheap.
const DefaultProgressInterval = 30 * time.Second

// DefaultExploreAfter is long enough that a read-heavy plan is never
// labelled, short enough that a plan which stopped writing is.
const DefaultExploreAfter = 20 * time.Minute

// DefaultStaleAfter is hours, not minutes: a human's decision may wait on
// their next working day.
const DefaultStaleAfter = 4 * time.Hour

const DefaultGateTimeout = 10 * time.Minute

func (p Policy) SwitchLimit() int {
	if p.MaxSwitches == nil {
		return DefaultMaxSwitches
	}
	return *p.MaxSwitches
}

// ArtifactMaxBytes returns the artifact directory cap in bytes: the
// configured artifact_max_mb, or DefaultArtifactMaxMB when it is unset or 0.
func (p Policy) ArtifactMaxBytes() int64 {
	mb := DefaultArtifactMaxMB
	if p.ArtifactMaxMB != nil && *p.ArtifactMaxMB > 0 {
		mb = *p.ArtifactMaxMB
	}
	return int64(mb) * (1 << 20)
}

func (p Policy) LimitGateDefault() time.Duration {
	if p.LimitGateDefaultMS == nil {
		return DefaultLimitGate
	}
	return time.Duration(*p.LimitGateDefaultMS) * time.Millisecond
}

func (p Policy) StallAfter() time.Duration {
	if p.StallAfterMS == nil {
		return DefaultStallAfter
	}
	return time.Duration(*p.StallAfterMS) * time.Millisecond
}

func (p Policy) ProgressInterval() time.Duration {
	if p.ProgressIntervalMS == nil {
		return DefaultProgressInterval
	}
	return time.Duration(*p.ProgressIntervalMS) * time.Millisecond
}

func (p Policy) ExploreAfter() time.Duration {
	if p.ExploreAfterMS == nil {
		return DefaultExploreAfter
	}
	return time.Duration(*p.ExploreAfterMS) * time.Millisecond
}

func (p Policy) StaleAfter() time.Duration {
	if p.StaleAfterMS == nil {
		return DefaultStaleAfter
	}
	return time.Duration(*p.StaleAfterMS) * time.Millisecond
}

func (p Policy) MaxTierOrDefault() harness.Tier {
	if p.MaxTier == "" {
		return DefaultMaxTier
	}
	t, err := harness.ParseTier(p.MaxTier)
	if err != nil || t == harness.TierHarness {
		return DefaultMaxTier
	}
	return t
}

// TierFor returns the configured tier for role, or ok false if absent.
func (p Policy) TierFor(role string) (harness.Tier, bool) {
	if p.Tier == nil {
		return "", false
	}
	val, ok := p.Tier[role]
	if !ok {
		return "", false
	}
	t, err := harness.ParseTier(val)
	if err != nil {
		return "", false
	}
	return t, true
}

func (p Policy) GateDefault() string {
	if p.Gate == nil {
		return ""
	}
	return p.Gate.Default
}

func (p Policy) GateTimeout() time.Duration {
	if p.Gate == nil || p.Gate.TimeoutMS == nil {
		return DefaultGateTimeout
	}
	return time.Duration(*p.Gate.TimeoutMS) * time.Millisecond
}

func (p Policy) GateRegate() int {
	if p.Gate == nil || p.Gate.Regate == nil {
		return 0
	}
	return *p.Gate.Regate
}

func (p Policy) VerifyDefault() bool {
	if p.Verify == nil {
		return false
	}
	return p.Verify.Default
}

func (p Policy) MaxBuildersOrDefault() int {
	if p.Serve == nil || p.Serve.MaxBuilders == nil {
		if n := runtime.NumCPU() - 1; n > 1 {
			return n
		}
		return 1
	}
	return *p.Serve.MaxBuilders
}

// ScopeFor returns the scope for a context: a served round takes
// Serve.Scope when set, else the top-level Scope.
func (p Policy) ScopeFor(served bool) *ScopePolicy {
	if served && p.Serve != nil && p.Serve.Scope != nil {
		return p.Serve.Scope
	}
	return p.Scope
}

// OrderFor returns role's preferred candidate tokens, most preferred first,
// or nil when absent. The result is a copy, so a caller cannot reorder the
// loaded policy by accident.
func (p Policy) OrderFor(role string) []string {
	return append([]string(nil), p.Order[role]...)
}
