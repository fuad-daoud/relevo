package relevo

import "github.com/fuad-daoud/relevo/internal/policy"

// timingSettings returns the six timing rows in display order.
func timingSettings(p policy.Policy) []Setting {
	return []Setting{
		{
			Group:   "timing",
			Key:     "limit_gate_default",
			Value:   FormatDuration(p.LimitGateDefault()),
			Default: FormatDuration(policy.DefaultLimitGate),
			Set:     p.LimitGateDefaultMS != nil,
			Form:    "timing",
		},
		{
			Group:   "timing",
			Key:     "stall_after",
			Value:   FormatDuration(p.StallAfter()),
			Default: FormatDuration(policy.DefaultStallAfter),
			Set:     p.StallAfterMS != nil,
			Form:    "timing",
		},
		{
			Group:   "timing",
			Key:     "silent_after",
			Value:   FormatDuration(p.SilentAfter()),
			Default: FormatDuration(policy.DefaultSilentAfter),
			Set:     p.SilentAfterMS != nil,
			Form:    "timing",
		},
		{
			Group:   "timing",
			Key:     "progress_interval",
			Value:   FormatDuration(p.ProgressInterval()),
			Default: FormatDuration(policy.DefaultProgressInterval),
			Set:     p.ProgressIntervalMS != nil,
			Form:    "timing",
		},
		{
			Group:   "timing",
			Key:     "explore_after",
			Value:   FormatDuration(p.ExploreAfter()),
			Default: FormatDuration(policy.DefaultExploreAfter),
			Set:     p.ExploreAfterMS != nil,
			Form:    "timing",
		},
		{
			Group:   "timing",
			Key:     "stale_after",
			Value:   FormatDuration(p.StaleAfter()),
			Default: FormatDuration(policy.DefaultStaleAfter),
			Set:     p.StaleAfterMS != nil,
			Form:    "timing",
		},
	}
}

// timingSettingPaths returns the policy path cleared by reset for a timing
// key, or nil when key is not a timing row.
func timingSettingPaths(key string) []string {
	switch key {
	case "limit_gate_default", "stall_after", "silent_after", "progress_interval", "explore_after", "stale_after":
		return []string{key + "_ms"}
	default:
		return nil
	}
}
