package relevo

import (
	"context"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/chain"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// ServedChainRequest is what a server has to create and drive a chain for a
// tenant: the validated wire values, plus the server's own facts -- the owner,
// the bare repo it cuts the builder's tree from, the author the builder commits
// as, and the client's link ids. Every field is set by the HTTP layer before
// ServedChainPreflight reads it; nothing here is an option the server resolves.
type ServedChainRequest struct {
	// Name is the chain's name, and its builder member's.
	Name string
	// Plans are the plan texts, in order; at least one.
	Plans []string
	// Settings is the client's resolved chain configuration.
	Settings remote.ChainSettings
	// Feature and Ticket label every member, exactly as on a served create.
	Feature string
	Ticket  string
	// BuilderActor is the actor the builder member runs. It is "" for every
	// wire create -- the chain fixes the builder to the builder actor -- but a
	// caller may name one, and the preflight's shape check then decides.
	BuilderActor string
	// Owner is the enrolled client that asked for the chain.
	Owner string
	// Bare is the owner's bare repo the builder's worktree is cut from.
	Bare string
	// RepoID is the repo id the client resolved, kept on the members' Serve
	// facts so the client can match its copy.
	RepoID string
	// Base is the commit the builder starts from.
	Base string
	// AuthorName and AuthorEmail are the client's git identity; every builder
	// the server starts for a member commits as the client.
	AuthorName  string
	AuthorEmail string
	// ClientInstallation and ClientBindingIDs are the client's link facts,
	// keyed by part, written onto each member as its Link.
	ClientInstallation string
	ClientBindingIDs   map[string]string
}

// servedPick is one member's resolved launch: the candidate token the server's
// own registry picked, the harness kind that token carries, and the tier the
// server's policy resolved.
type servedPick struct {
	token string
	kind  string
	tier  string
}

// servedFacts is what every served member's binding mirrors: the builder's
// served facts a member is created beside. A reader is the same binding except
// that it shares the builder's worktree, carries no worktree and no branch of
// its own, and holds no gate.
type servedFacts struct {
	owner        string
	repoID       string
	worktree     string
	bare         string
	base         string
	feature      string
	ticket       string
	authorName   string
	authorEmail  string
	installation string
	clientIDs    map[string]string
	gate         string
	regate       int
	now          time.Time
}

// ServedChainPlan is everything a served chain's read-only preflight resolved:
// the request, the settings, the members every start names, and each member's
// own pick. Nothing exists yet, so a refusal has nothing to undo.
type ServedChainPlan struct {
	req      ServedChainRequest
	settings chain.Settings
	members  []chainMember
	picks    map[string]servedPick
}

// ServedChainPreflight runs every check a served chain's create can make before
// anything exists, all read-only: the name, the members' names, each actor's
// shape on this server's registry, and each member's candidate and tier. The
// caller's candidate and the actor's placement list are ignored -- the server
// picks with its own policy, exactly as a served binding's create does.
//
// Errors: store.ValidName / the name-length refusal; chainFreeNames' refusal; a
// shape error naming the part and actor; ErrTierAboveMax or a pick refusal from
// PickServedCandidateFor / ResolveServedTierFor.
func ServedChainPreflight(rt Runtime, req ServedChainRequest) (ServedChainPlan, error) {
	if err := store.ValidName(req.Name); err != nil {
		return ServedChainPlan{}, err
	}
	if len(req.Name) > chainMaxNameLen {
		return ServedChainPlan{}, fmt.Errorf("chain name %q exceeds %d characters (the longest member suffix is -plan)", req.Name, chainMaxNameLen)
	}
	settings := chainSettingsFromWire(req.Settings)
	opts := ChainOptions{Name: req.Name, BuilderActor: req.BuilderActor, Feature: req.Feature, Ticket: req.Ticket}
	members := chainMembersFor(opts, settings)
	if err := chainFreeNames(rt, members); err != nil {
		return ServedChainPlan{}, err
	}
	picks := make(map[string]servedPick, len(members))
	for _, m := range members {
		shape, err := ActorShape(rt, m.actor)
		if err != nil {
			return ServedChainPlan{}, err
		}
		if shape != m.shape {
			return ServedChainPlan{}, fmt.Errorf("chain %s actor %q must be a %s actor, not a %s", m.part, m.actor, m.shape, shape)
		}
		pick, err := servedActorPick(rt, m.actor)
		if err != nil {
			return ServedChainPlan{}, err
		}
		picks[m.part] = pick
	}
	return ServedChainPlan{req: req, settings: settings, members: members, picks: picks}, nil
}

// ServedChainCreate creates the chain a preflight resolved, all-or-none through
// the row: it builds every member in the served shape, writes the chain row and
// every member in one transaction, copies the plans into the chain's own
// directory, then hands plan 1 to the builder. worktree is the builder's served
// worktree, already cut from the base bundle by the caller.
//
// A failing transaction leaves neither the chain row nor any member. A failure
// after it -- the plan copies or plan 1 -- leaves the chain started, exactly as
// chainCreate does. Plan 1 goes through sendChainRound, so a served builder's
// first round is queued for admit rather than started here.
func ServedChainCreate(ctx context.Context, rt Runtime, plan ServedChainPlan, worktree string) (ChainResult, error) {
	req := plan.req
	name := req.Name
	facts := servedFacts{
		owner: req.Owner, repoID: req.RepoID, worktree: worktree, bare: req.Bare,
		base: req.Base, feature: req.Feature, ticket: req.Ticket,
		authorName: req.AuthorName, authorEmail: req.AuthorEmail,
		installation: req.ClientInstallation, clientIDs: req.ClientBindingIDs,
		gate: plan.settings.Gate, regate: plan.settings.Regate, now: rt.Now().UTC(),
	}
	built := make([]store.Binding, 0, len(plan.members))
	for _, m := range plan.members {
		built = append(built, servedChainMember(m, plan.picks[m.part], facts))
	}
	planPaths := make([]string, len(req.Plans))
	for i := range req.Plans {
		planPaths[i] = rt.Store.ChainPlanPath(name, i+1)
	}
	base := chainBase{
		cwd: worktree, repo: req.Bare, worktree: worktree, branch: "relevo/" + name,
		commit: req.Base, feature: req.Feature, ticket: req.Ticket,
	}
	opts := ChainOptions{Name: name, Feature: req.Feature, Ticket: req.Ticket}
	row, err := chainRow(opts, plan.settings, plan.members, base, planPaths, rt.Now())
	if err != nil {
		return ChainResult{}, err
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		return tx.CreateChain(row, built)
	}); err != nil {
		return ChainResult{}, err
	}

	stored, err := chainStoredMembers(rt, plan.members)
	if err != nil {
		return ChainResult{}, err
	}
	bodies := make([][]byte, len(req.Plans))
	for i, p := range req.Plans {
		bodies[i] = []byte(p)
	}
	if err := chainCopyPlans(rt, name, bodies); err != nil {
		return ChainResult{}, fmt.Errorf("chain %q started, but copying its plans failed: %w", name, err)
	}
	// Plan 1 goes through the chain's own sender, so a running chain's refusal
	// never bites its own start. A served builder's round is queued, not
	// started: admit runs it once a builder slot is free.
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		b, err := tx.Load(name)
		if err != nil {
			return err
		}
		body, err := rt.Store.ReadFile(rt.Store.ChainPlanPath(name, 1))
		if err != nil {
			return err
		}
		_, err = sendChainRound(ctx, rt, tx, b, string(body))
		return err
	}); err != nil {
		return ChainResult{}, fmt.Errorf("chain %q started, but plan 1 could not be handed to %s: %w", name, name, err)
	}
	return ChainResult{Chain: row, Members: stored, Plans: len(bodies), Check: chainBuilderCheck(stored, name)}, nil
}

// servedChainMember is one chain member's binding in the served shape. The
// builder mirrors buildServedBinding's literal: its own served worktree and
// branch, a headless endpoint, the resolved gate and regate, its Serve facts and
// its link. A reader shares the builder's tree -- CWD is the builder's worktree
// -- and carries no worktree, no branch and no gate.
func servedChainMember(m chainMember, pick servedPick, f servedFacts) store.Binding {
	now := f.now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	b := store.Binding{
		Name:             m.name,
		Owner:            f.owner,
		CWD:              f.worktree,
		Repo:             f.bare,
		Base:             f.base,
		Builder:          store.Endpoint{Kind: pick.kind, Mode: store.ModeHeadless, AgentName: m.name},
		BuilderCandidate: pick.token,
		Link:             servedMemberLink(m.part, f.installation, f.clientIDs),
		Tier:             pick.tier,
		Role:             normRole(m.actor),
		Shape:            m.shape,
		Round:            1,
		State:            store.StateActive,
		Feature:          f.feature,
		Ticket:           f.ticket,
		Serve: &store.ServeFacts{
			RepoID:      f.repoID,
			BareRepo:    f.bare,
			LastSeen:    now,
			AuthorName:  f.authorName,
			AuthorEmail: f.authorEmail,
		},
	}
	if m.writer {
		b.Worktree = f.worktree
		b.Branch = "relevo/" + m.name
		b.Gate = f.gate
		b.Regate = f.regate
	}
	return b
}

// servedMemberLink is the link a served member carries: nil when the client
// sent neither an installation nor an id for the part, exactly as servedLink
// leaves a lone served binding nil.
func servedMemberLink(part, installation string, ids map[string]string) *store.RemoteLink {
	id := ""
	if ids != nil {
		id = ids[part]
	}
	if installation == "" && id == "" {
		return nil
	}
	return &store.RemoteLink{Installation: installation, ID: id}
}

// servedActorPick resolves one member's candidate and tier from the server's own
// registry, the calls serve.pickServedTier makes: no client candidate, and the
// actor's placement list ignored. actor is the member's actor word; the empty
// builder role is resolved as "builder", exactly as a served create resolves it.
func servedActorPick(rt Runtime, actor string) (servedPick, error) {
	role := normRole(actor)
	roleName := role
	if roleName == "" {
		roleName = "builder"
	}
	token, kind, err := PickServedCandidateFor(rt, roleName, "")
	if err != nil {
		return servedPick{}, err
	}
	tier, err := ResolveServedTierFor(rt, roleName, token, "")
	if err != nil {
		return servedPick{}, err
	}
	return servedPick{token: token, kind: kind, tier: string(tier)}, nil
}

// chainSettingsFromWire converts the wire settings to the chain's own type.
// internal/remote does not import internal/chain, so the two are converted at
// this edge.
func chainSettingsFromWire(s remote.ChainSettings) chain.Settings {
	return chain.Settings{
		MaxCorrections: s.MaxCorrections,
		ReviewerActor:  s.ReviewerActor,
		PlannerActor:   s.PlannerActor,
		SecurityActor:  s.SecurityActor,
		Security:       s.Security,
		Gate:           s.Gate,
		Regate:         s.Regate,
	}
}

// chainSettingsToWire is chainSettingsFromWire's inverse for a chain view.
func chainSettingsToWire(s chain.Settings) remote.ChainSettings {
	return remote.ChainSettings{
		MaxCorrections: s.MaxCorrections,
		ReviewerActor:  s.ReviewerActor,
		PlannerActor:   s.PlannerActor,
		SecurityActor:  s.SecurityActor,
		Security:       s.Security,
		Gate:           s.Gate,
		Regate:         s.Regate,
	}
}

// chainServedCarrier reports whether a chain's end delivery must be skipped: a
// served member's MasterMind is on the client, which queues the delivery itself
// after its pull, so the server queues nothing.
func chainServedCarrier(b store.Binding) bool {
	return b.Owner != ""
}

// ServedChainView is a chain's wire representation on the server: its row, each
// member's current binding view plus the closed rounds in
// (acked_round, closed_round], the trace, and the security findings. recordID
// resolves a member's server binding_record id and installation is this
// server's own id, both exactly as ServedView takes them.
func ServedChainView(rt Runtime, name string, recordID func(string) string, installation string) (remote.ChainView, error) {
	c, err := rt.Store.Chain(name)
	if err != nil {
		return remote.ChainView{}, err
	}
	state, err := chainStateOf(c)
	if err != nil {
		return remote.ChainView{}, err
	}
	view := remote.ChainView{
		Name:            c.Name,
		Status:          c.Status,
		Reason:          c.Reason,
		Phase:           c.Phase,
		Step:            c.Step,
		Plan:            c.Plan,
		Plans:           c.Plans,
		Corrections:     c.Corrections,
		AwaitingMember:  c.AwaitingMember,
		AwaitingRound:   c.AwaitingRound,
		Base:            c.Base,
		Branch:          c.Branch,
		Feature:         c.Feature,
		Ticket:          c.Ticket,
		PlanStartCommit: c.PlanStartCommit,
		Settings:        chainSettingsToWire(state.Settings),
	}
	for _, member := range chainMembersOf(c) {
		b, err := rt.Store.Load(member)
		if err != nil {
			return remote.ChainView{}, err
		}
		entries, err := rt.Store.ReadLog(member)
		if err != nil {
			return remote.ChainView{}, err
		}
		mv := remote.ChainMemberView{
			Part:  chainPartOf(c, member),
			Name:  member,
			Actor: b.Role,
			View:  ServedView(b, entries, recordID(member), installation),
		}
		if b.Serve != nil {
			for n := b.Serve.AckedRound + 1; n <= b.Serve.ClosedRound; n++ {
				mv.Rounds = append(mv.Rounds, servedRoundFacts(entries, n))
			}
		}
		view.Members = append(view.Members, mv)
	}
	events, err := rt.Store.ChainEvents(name)
	if err != nil {
		return remote.ChainView{}, err
	}
	for _, e := range events {
		view.Trace = append(view.Trace, remote.ChainEventView{
			Seq: e.Seq, TS: e.TS, Phase: e.Phase, Step: e.Step, Member: e.Member,
			Round: e.Round, Plan: e.Plan, Event: e.Event, Action: e.Action, Reason: e.Reason,
		})
	}
	view.Findings = chainFindingsOf(events)
	return view, nil
}

// chainFindingsOf is chainFindings over an already-read trace: the last security
// close's finding count, or 0 for a chain that never ran security.
func chainFindingsOf(events []db.ChainEventRow) int {
	for _, e := range events {
		ev, err := chain.DecodeEvent(e.Event)
		if err != nil {
			continue
		}
		if ev.Kind == chain.EventSecurityClosed {
			return ev.Findings
		}
	}
	return 0
}
