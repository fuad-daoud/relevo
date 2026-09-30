package relevo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// localPlacement is the reserved placement entry that names this machine. It
// is also what an actor that names no placement at all gets.
const localPlacement = "local"

// The How words a PlacementResolution carries: explicit when a flag named the
// placement, actor when the actor's own list did.
const (
	placementHowExplicit = "explicit"
	placementHowActor    = "actor"
)

// PlacementSkip is one placement the resolver passed over, and why. The reason
// is one line: the bind line and the pick note both print it.
type PlacementSkip struct{ Name, Reason string }

// PlacementResolution is the placement a create runs under. Name is the local
// sentinel or a server name, How is "explicit" when a flag named it and
// "actor" when the actor's list did, and Skipped names every entry passed over,
// in list order. The zero value means the actor named no preference: nothing
// was probed and the local path stands.
type PlacementResolution struct {
	Name    string
	How     string
	Skipped []PlacementSkip
}

// local reports whether this placement runs here. The zero resolution does:
// an actor that names nothing keeps today's local path.
func (p PlacementResolution) local() bool {
	return p.Name == "" || p.Name == localPlacement
}

// placementFlags are the invocation's flags a probe must honour. A server that
// cannot carry one of them cannot serve this binding, so the placement is
// skipped and the next entry is tried -- instead of failing loudly at the
// create, after the client has committed to a server.
type placementFlags struct {
	Tier    string
	Feature string
	Ticket  string
}

// createPlacement is the placement a fresh create runs under. An explicit
// --server or --local wins and is taken as given, with no probe and no
// fallback; otherwise the actor's list decides, and an absent or empty list
// keeps the local path with no probe at all.
func createPlacement(ctx context.Context, rt Runtime, role, pinned, server string, local bool, flags placementFlags) (PlacementResolution, error) {
	switch {
	case server != "":
		return PlacementResolution{Name: server, How: placementHowExplicit}, nil
	case local:
		return PlacementResolution{Name: localPlacement, How: placementHowExplicit}, nil
	}
	return choosePlacement(ctx, rt, role, pinned, flags)
}

// choosePlacement reads the actor's placement list and returns the first entry
// that is viable, with every entry passed over and why. It creates nothing and
// changes nothing: every probe is a read, so a bind that lands somewhere else
// than the first choice leaves no trace on the machine it declined.
//
// An entry that is not viable is a skip. Only a failure the human must see --
// a changed certificate, or an answer this client does not understand -- is an
// error, because silently binding elsewhere after one of those would hide a
// security event behind a preference order.
func choosePlacement(ctx context.Context, rt Runtime, role, pinned string, flags placementFlags) (PlacementResolution, error) {
	info, ok := rt.RoleRegistry().Role(role)
	if !ok || len(info.Placement) == 0 {
		return PlacementResolution{}, nil
	}
	gates := availability.Gates(AvailabilityDeps(rt))

	var skipped []PlacementSkip
	for _, name := range info.Placement {
		if name == localPlacement {
			if _, err := resolveRole(rt.RoleRegistry(), rt.Candidates, gates, pinned, role); err != nil {
				skipped = append(skipped, PlacementSkip{Name: name, Reason: err.Error()})
				continue
			}
			return PlacementResolution{Name: name, How: placementHowActor, Skipped: skipped}, nil
		}
		reason, err := probePlacement(ctx, rt, name, role, pinned, flags)
		if err != nil {
			return PlacementResolution{}, err
		}
		if reason != "" {
			skipped = append(skipped, PlacementSkip{Name: name, Reason: reason})
			continue
		}
		return PlacementResolution{Name: name, How: placementHowActor, Skipped: skipped}, nil
	}
	return PlacementResolution{}, placementError(role, skipped)
}

// probePlacement asks one server whether it could run this binding. It returns
// "" when the placement is viable and a one-line reason when it is not; the
// error it also returns is reserved for the failures that must not be swallowed
// by falling through to another placement.
func probePlacement(ctx context.Context, rt Runtime, server, role, pinned string, flags placementFlags) (string, error) {
	if rt.Remote == nil {
		return "no client key", nil
	}
	who, err := rt.Remote.WhoAmI(ctx, server)
	if err != nil {
		return classifyProbeError(err)
	}
	if reason := unsupportedFeature(rt.RoleRegistry(), role, who, flags); reason != "" {
		return reason, nil
	}
	if reason := tierAboveMax(flags.Tier, who); reason != "" {
		return reason, nil
	}
	if !slices.Contains(who.Features, remote.FeaturePlacement) {
		// A server that predates the route cannot be asked about its actors.
		// Reachability and the features above are all the probe can check; a
		// refusal at the create then fails the bind loudly rather than
		// falling through.
		return "", nil
	}
	view, err := rt.Remote.Actor(ctx, server, role, pinned)
	if err != nil {
		return classifyProbeError(err)
	}
	if !view.Accepted {
		reason := view.Reason
		if reason == "" {
			reason = "actor not served"
		}
		return reason, nil
	}
	return "", nil
}

// unsupportedFeature names the first feature this invocation needs and the
// server does not advertise, or "" when it carries them all. Every reason
// names the upgrade, because the fix is the server's, not the flag's.
func unsupportedFeature(reg *roles.Registry, role string, who remote.WhoAmI, flags placementFlags) string {
	if isReaderRole(reg, role) && !slices.Contains(who.Features, remote.FeatureReaders) {
		return "reader rounds unsupported; upgrade the server"
	}
	if role != "builder" && !slices.Contains(who.Features, remote.FeatureRoles) {
		return "custom actors unsupported; upgrade the server"
	}
	if flags.Feature != "" || flags.Ticket != "" {
		if !slices.Contains(who.Features, remote.FeatureLabels) {
			return "binding labels unsupported; upgrade the server"
		}
	}
	if flags.Tier != "" && !slices.Contains(who.Features, remote.FeatureTier) {
		return "permission tiers unsupported; upgrade the server"
	}
	return ""
}

// tierAboveMax is the skip reason for a pinned tier the server would refuse,
// or "" when no tier is pinned, the server reports no cap, or the tier is
// within it.
func tierAboveMax(tier string, who remote.WhoAmI) string {
	if tier == "" || who.MaxTier == "" {
		return ""
	}
	t, err := harness.ParseTier(tier)
	if err != nil {
		return ""
	}
	max, err := harness.ParseTier(who.MaxTier)
	if err != nil {
		return ""
	}
	if t.Above(max) {
		return fmt.Sprintf("tier %s is above the server's max tier %s", t, max)
	}
	return ""
}

// classifyProbeError maps a probe failure to its class: an unreachable server
// and a client that is not enrolled are skips, and anything else -- a changed
// certificate most of all -- is returned as it came.
func classifyProbeError(err error) (string, error) {
	var httpErr *client.HTTPError
	switch {
	case errors.As(err, &httpErr) && httpErr.Status == http.StatusUnauthorized:
		return "not enrolled", nil
	case errors.Is(err, client.ErrUnreachable):
		return "unreachable", nil
	}
	return "", err
}

// isReaderRole reports whether role is a reader in reg.
func isReaderRole(reg *roles.Registry, role string) bool {
	r, ok := reg.Role(role)
	return ok && r.Shape == harness.ShapeConsult
}

// placementError is the bind failure when no placement is viable: every
// placement and its reason, so the human can see the whole list at once.
func placementError(role string, skipped []PlacementSkip) error {
	texts := make([]string, 0, len(skipped))
	for _, s := range skipped {
		texts = append(texts, fmt.Sprintf("%s (%s)", s.Name, s.Reason))
	}
	return fmt.Errorf("no viable placement for actor %q: %s", role, strings.Join(texts, "; "))
}
