package relevo

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// ServerProbe is one configured server's reachability and enrollment, as
// `relevo config server list` and `relevo doctor` both report it (spec §5.5, §4.7).
type ServerProbe struct {
	Name  string
	URL   string
	State string // "enrolled" | "not enrolled" | "unreachable" | "cert changed" | "no key" | "error"
	Label string // enrolled as
	// Detail is the enrollment line for "not enrolled", the failure cause
	// for "unreachable", and the error text otherwise; "" for "enrolled".
	Detail string

	// TierAware, BuilderTier and MaxTier are filled only in the "enrolled"
	// arm, from WhoAmI.Features/BuilderTier/MaxTier (#141 remote half).
	TierAware   bool   // WhoAmI.Features contains FeatureTier
	BuilderTier string // WhoAmI.BuilderTier; "" when !TierAware
	MaxTier     string // WhoAmI.MaxTier;     "" when !TierAware

	// QueueAware and Builders are filled only in the "enrolled" arm, from
	// WhoAmI.Features/Builders (#285).
	QueueAware bool                 // WhoAmI.Features contains FeatureQueue
	Builders   *remote.BuildersView // nil when !QueueAware
}

// ProbeServers checks every configured server's reachability and this
// client's enrollment on it, in name order. It is pure over rt.Remote (a
// RemoteClient), so it is tested with fakeRemote, and shared by `relevo
// servers` and `relevo doctor`'s per-server checks.
//
// rt.Remote == nil means no client key: every server probes "no key",
// naming the fix. Otherwise each server is checked with WhoAmI: success is
// "enrolled"; a 401 is "not enrolled" (Detail is the caller's enrollLine,
// the line to hand the admin); ErrCertChanged is "cert changed";
// ErrUnreachable is "unreachable" (Detail is the failure cause); anything
// else is "error" (Detail is the error text).
func ProbeServers(ctx context.Context, rt Runtime, servers map[string]remote.ServerEntry, enrollLine string) []ServerProbe {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)

	probes := make([]ServerProbe, 0, len(names))
	for _, n := range names {
		p := ServerProbe{Name: n, URL: servers[n].URL}
		if rt.Remote == nil {
			p.State = "no key"
			p.Detail = "run relevo config server key"
			probes = append(probes, p)
			continue
		}

		who, err := rt.Remote.WhoAmI(ctx, n)
		switch {
		case err == nil:
			p.State = "enrolled"
			p.Label = who.Label
			p.TierAware = slices.Contains(who.Features, remote.FeatureTier)
			if p.TierAware {
				p.BuilderTier = who.BuilderTier
				p.MaxTier = who.MaxTier
			}
			p.QueueAware = slices.Contains(who.Features, remote.FeatureQueue)
			if p.QueueAware {
				p.Builders = who.Builders
			}
		case errors.Is(err, client.ErrCertChanged):
			p.State = "cert changed"
		case errors.Is(err, client.ErrUnreachable):
			p.State = "unreachable"
			p.Detail = strings.TrimPrefix(err.Error(), client.ErrUnreachable.Error()+": ")
		default:
			var httpErr *client.HTTPError
			if errors.As(err, &httpErr) && httpErr.Status == 401 {
				p.State = "not enrolled"
				p.Detail = enrollLine
			} else {
				p.State = "error"
				p.Detail = err.Error()
			}
		}
		probes = append(probes, p)
	}
	return probes
}

// probeStatusText is one ServerProbe's status word, exactly what `relevo
// servers` printed before ProbeServers existed (serverStatus in
// cmd/relevo/client.go).
func probeStatusText(p ServerProbe) string {
	switch p.State {
	case "enrolled":
		return "enrolled as " + p.Label
	case "no key":
		return "no client key"
	case "not enrolled", "unreachable", "cert changed":
		return p.State
	default:
		return p.Detail
	}
}

// ServerTierWarning is the one-line warning for a server that would launch
// headless builders at tier harness, "" otherwise (including !TierAware).
func ServerTierWarning(p ServerProbe) string {
	if p.TierAware && p.BuilderTier == string(harness.TierHarness) {
		return "headless builders at tier harness deny every tool unless the server host's harness settings allow them; set tier.builder in the server's config policy or pass --tier"
	}
	return ""
}

// RenderServers formats probes as the table `relevo config server list` prints: one row
// per server, name, url, and enrollment status, aligned on the longest name.
func RenderServers(probes []ServerProbe) string {
	if len(probes) == 0 {
		return "no servers configured; relevo config server add <name> <url>\n"
	}

	width := 0
	for _, p := range probes {
		if len(p.Name) > width {
			width = len(p.Name)
		}
	}

	var sb strings.Builder
	for _, p := range probes {
		row := fmt.Sprintf("%-*s  %-40s  %s", width, p.Name, p.URL, probeStatusText(p))
		if p.State == "enrolled" {
			if p.TierAware {
				row += fmt.Sprintf("  default tier: %s (max %s)", p.BuilderTier, p.MaxTier)
			} else {
				row += "  builder tier: unknown (pre-tier server)"
			}
			if p.QueueAware && p.Builders != nil {
				scopes := "off"
				switch {
				case p.Builders.Scopes && p.Builders.Slice != "" && p.Builders.Quota != "":
					scopes = fmt.Sprintf("on (%s, %s)", p.Builders.Slice, p.Builders.Quota)
				case p.Builders.Scopes && p.Builders.Quota != "":
					scopes = fmt.Sprintf("on (%s)", p.Builders.Quota)
				case p.Builders.Scopes && p.Builders.Slice != "":
					scopes = fmt.Sprintf("on (%s)", p.Builders.Slice)
				case p.Builders.Scopes:
					scopes = "on"
				}
				row += fmt.Sprintf("  runners %d/%d, %d queued, scopes %s",
					p.Builders.Running, p.Builders.Cap, p.Builders.Queued, scopes)
			}
		}
		sb.WriteString(row)
		sb.WriteString("\n")
		if warning := ServerTierWarning(p); warning != "" {
			fmt.Fprintf(&sb, "  !! %s\n", warning)
		}
	}
	return sb.String()
}
