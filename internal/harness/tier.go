package harness

import (
	"errors"
	"fmt"
	"strings"
)

// Tier is the permission ceiling relevo renders into a harness's flags.
type Tier string

const (
	TierHarness Tier = "harness" // relevo adds nothing; outside the order
	TierRead    Tier = "read"
	TierEdit    Tier = "edit"
	TierYolo    Tier = "yolo"
)

var (
	ErrTierUnsupported     = errors.New("tier not supported by harness")
	ErrExtraArgsPermission = errors.New("extra_args carries a permission flag")
)

// ParseTier accepts exactly the four names; "" is an error, so callers decide
// their own default before calling.
func ParseTier(s string) (Tier, error) {
	switch Tier(s) {
	case TierHarness, TierRead, TierEdit, TierYolo:
		return Tier(s), nil
	default:
		return "", fmt.Errorf("unknown tier %q (known: harness, read, edit, yolo)", s)
	}
}

// Rank orders read < edit < yolo as 1 < 2 < 3; TierHarness is 0.
func (t Tier) Rank() int {
	switch t {
	case TierRead:
		return 1
	case TierEdit:
		return 2
	case TierYolo:
		return 3
	default:
		return 0
	}
}

// Above returns t.Rank() > u.Rank(), false if either is harness.
func (t Tier) Above(u Tier) bool {
	if t == TierHarness || u == TierHarness {
		return false
	}
	return t.Rank() > u.Rank()
}

// PermissionArgs is the tier-to-flags table. TierHarness and an unknown kind
// return nil; a refusal cell returns ErrTierUnsupported with kind and tier.
func (h Harness) PermissionArgs(tier Tier) ([]string, error) {
	if tier == TierHarness {
		return nil, nil
	}
	switch h.Kind {
	case "claude":
		switch tier {
		case TierRead:
			return []string{"--permission-mode", "plan"}, nil
		case TierEdit:
			return []string{"--permission-mode", "acceptEdits"}, nil
		case TierYolo:
			return []string{"--dangerously-skip-permissions"}, nil
		default:
			return nil, fmt.Errorf("%w: claude cannot honour tier %s", ErrTierUnsupported, tier)
		}
	case "agy":
		switch tier {
		case TierRead:
			return []string{"--mode", "plan"}, nil
		case TierEdit:
			return []string{"--mode", "accept-edits"}, nil
		case TierYolo:
			return []string{"--dangerously-skip-permissions"}, nil
		default:
			return nil, fmt.Errorf("%w: agy cannot honour tier %s", ErrTierUnsupported, tier)
		}
	case "opencode":
		switch tier {
		case TierRead:
			return nil, fmt.Errorf("%w: opencode cannot honour tier read: it has no read-only flag; use --tier harness (opencode.jsonc decides) or --tier yolo (--auto)", ErrTierUnsupported)
		case TierEdit:
			return nil, fmt.Errorf("%w: opencode cannot honour tier edit: it has no edit flag; use --tier harness (opencode.jsonc decides) or --tier yolo (--auto)", ErrTierUnsupported)
		case TierYolo:
			return []string{"--auto"}, nil
		default:
			return nil, fmt.Errorf("%w: opencode cannot honour tier %s", ErrTierUnsupported, tier)
		}
	case "codex":
		switch tier {
		case TierRead:
			return nil, fmt.Errorf("%w: codex cannot honour tier read: -s read-only cannot write the report relevo needs (writable_roots is ignored under read-only); use --tier edit or --tier harness", ErrTierUnsupported)
		case TierEdit:
			return []string{"-s", "workspace-write", "-c", StatePlaceholder}, nil
		case TierYolo:
			return []string{"--dangerously-bypass-approvals-and-sandbox"}, nil
		default:
			return nil, fmt.Errorf("%w: codex cannot honour tier %s", ErrTierUnsupported, tier)
		}
	default:
		return nil, nil
	}
}

// PermissionFlags lists the flags relevo treats as permission flags for this
// kind, matched against extra_args as whole elements or `flag=` prefixes. A
// two-element `-c sandbox_mode=...` pair is not detected: -c is a generic override.
func (h Harness) PermissionFlags() []string {
	switch h.Kind {
	case "claude":
		return []string{
			"--permission-mode",
			"--dangerously-skip-permissions",
			"--allow-dangerously-skip-permissions",
			"--allowedTools",
			"--allowed-tools",
			"--disallowedTools",
			"--disallowed-tools",
			"--permission-prompts",
		}
	case "agy":
		return []string{
			"--mode",
			"--dangerously-skip-permissions",
			"--sandbox",
		}
	case "opencode":
		return []string{
			"--auto",
		}
	case "codex":
		return []string{
			"-s",
			"--sandbox",
			"-a",
			"--ask-for-approval",
			"--full-auto",
			"--approve-for-me",
			"--dangerously-bypass-approvals-and-sandbox",
		}
	default:
		return nil
	}
}

// ExtraArgsPermissionFlag returns the first element of extra that is a
// permission flag for this kind, or "".
func (h Harness) ExtraArgsPermissionFlag(extra []string) string {
	flags := h.PermissionFlags()
	for _, arg := range extra {
		for _, f := range flags {
			if arg == f || strings.HasPrefix(arg, f+"=") {
				return arg
			}
		}
	}
	return ""
}
