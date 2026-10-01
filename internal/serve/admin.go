package serve

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/isolate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

type OwnerStatus struct {
	Owner    remote.ClientID
	Label    string      // Clients.LabelOf
	LastSeen time.Time   // max over the owner's bindings of Serve.LastSeen; zero when none
	Report   view.Report // relevo.Status over that owner's runtime
}

// isolationView stamps the server's isolation facts onto v: the mode always,
// and the container image only in container mode. Both surfaces -- whoami and
// the status document -- report through it, so they cannot disagree.
func (s *Server) isolationView(v remote.BuildersView) remote.BuildersView {
	v.Isolation = string(s.cfg.Isolation)
	if s.cfg.Isolation == isolate.ModeContainer {
		v.Image = s.cfg.IsolationImage
	}
	return v
}

// AdminStatus returns every owner who has a bindings directory, sorted by
// Label, plus the builder census. Every queued row gains its Queued position
// and a "queued <age> (<ahead> ahead)" BuilderStatus.
func AdminStatus(ctx context.Context, s *Server) ([]OwnerStatus, remote.BuildersView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	builders := s.isolationView(remote.BuildersView{Cap: s.cap()})

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, builders, nil
		}
		return nil, remote.BuildersView{}, err
	}

	c, _ := s.census()
	builders.Running = c.Running
	builders.Queued = len(c.Queued)

	now := s.cfg.Now()
	position := make(map[string]int, len(c.Queued))
	for i, q := range c.Queued {
		position[string(q.Owner)+"/"+q.Name] = i
	}

	var owners []OwnerStatus
	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			continue
		}
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)
		rep, err := relevo.Status(ctx, rt)
		if err != nil {
			return nil, remote.BuildersView{}, err
		}
		var lastSeen time.Time
		for ri := range rep.Bindings {
			row := &rep.Bindings[ri]
			// Only a client request counts as contact: no RoundStartedAt fallback.
			b, err := rt.Store.Load(row.Name)
			if err != nil {
				return nil, remote.BuildersView{}, err
			}
			if b.Serve != nil && b.Serve.LastSeen.After(lastSeen) {
				lastSeen = b.Serve.LastSeen
			}
			i, ok := position[string(id)+"/"+row.Name]
			if !ok {
				continue
			}
			q := c.Queued[i]
			row.Queued = &remote.QueueView{Position: i + 1, Ahead: i, Running: c.Running, Cap: builders.Cap, Since: q.QueuedAt}
			row.BuilderStatus = fmt.Sprintf("queued %s (%d ahead)", view.AgeText(now.Sub(q.QueuedAt)), i)
		}
		label := s.clients.LabelOf(id)
		owners = append(owners, OwnerStatus{
			Owner:    id,
			Label:    label,
			LastSeen: lastSeen,
			Report:   rep,
		})
	}

	sort.Slice(owners, func(i, j int) bool {
		return owners[i].Label < owners[j].Label
	})

	return owners, builders, nil
}

// FlatStatus is the whole fleet as one report, Owner/OwnerLabel stamped on each
// row. Gated is the first owner's slice: the ledger is server-wide.
func FlatStatus(ctx context.Context, s *Server) (view.Report, error) {
	owners, _, err := AdminStatus(ctx, s)
	if err != nil {
		return view.Report{}, err
	}

	rows := make([]view.BindingStatus, 0)
	for _, o := range owners {
		label := o.Label
		if label == "" {
			label = view.ShortOwner(string(o.Owner))
		}
		for _, row := range o.Report.Bindings {
			row.Owner = string(o.Owner)
			row.OwnerLabel = label
			rows = append(rows, row)
		}
	}

	out := view.Report{Bindings: rows}
	if len(owners) > 0 {
		out.Gated = owners[0].Report.Gated
	}
	return out, nil
}

// StatusJSON is the document `relevo serve status --json` prints; the field
// names are a contract, and Owners is never null.
type StatusJSON struct {
	Builders    remote.BuildersView `json:"runners"`
	LastContact *time.Time          `json:"last_contact"`
	Owners      []OwnerJSON         `json:"owners"`
}

type OwnerJSON struct {
	Owner    string      `json:"owner"`
	Label    string      `json:"label"`
	LastSeen *time.Time  `json:"last_seen"`
	Report   view.Report `json:"report"`
}

func StatusDocument(owners []OwnerStatus, builders remote.BuildersView) StatusJSON {
	doc := StatusJSON{
		Builders: builders,
		Owners:   make([]OwnerJSON, 0, len(owners)),
	}

	var lastContact time.Time
	for _, o := range owners {
		row := OwnerJSON{
			Owner:  string(o.Owner),
			Label:  o.Label,
			Report: o.Report,
		}
		if !o.LastSeen.IsZero() {
			seen := o.LastSeen.UTC()
			row.LastSeen = &seen
			if seen.After(lastContact) {
				lastContact = seen
			}
		}
		doc.Owners = append(doc.Owners, row)
	}
	if !lastContact.IsZero() {
		doc.LastContact = &lastContact
	}
	return doc
}

func RenderClients(clients []Client) string {
	if len(clients) == 0 {
		return "no clients\n"
	}
	var sb strings.Builder
	for _, cl := range clients {
		line := fmt.Sprintf("%s  %s  enrolled %s", cl.ID, cl.Label, cl.EnrolledAt.Format("2006-01-02"))
		if !cl.RevokedAt.IsZero() {
			line += fmt.Sprintf("  revoked %s", cl.RevokedAt.Format("2006-01-02"))
		}
		sb.WriteString(line + "\n")
	}
	return sb.String()
}

type GCAbandonedResult struct {
	Owner    remote.ClientID
	Label    string
	Name     string
	LastSeen time.Time
	Archive  bool // true when archived; false on a dry run
}

func gcCandidate(b store.Binding, cutoff time.Time) (time.Time, bool) {
	lastSeen := b.RoundStartedAt
	if b.Serve != nil && !b.Serve.LastSeen.IsZero() {
		lastSeen = b.Serve.LastSeen
	}
	return lastSeen, lastSeen.Before(cutoff)
}

func gcArchive(ctx context.Context, rt relevo.Runtime, id remote.ClientID, b store.Binding, dryRun bool) (bool, error) {
	if dryRun {
		return false, nil
	}
	res, err := relevo.Unbind(ctx, rt, b.Name, true)
	if err != nil {
		return false, err
	}
	if res.WorktreeKept == "" {
		if err := releaseServedRefs(ctx, rt, b); err != nil {
			slog.Warn("release served refs", "owner", id, "binding", b.Name, "err", err)
		}
	}
	return res.Archived, nil
}

// GCAbandoned archives owned bindings older than olderThan that are not
// running; dryRun only lists. It releases the served refs when unbind keeps no
// worktree.
func GCAbandoned(ctx context.Context, s *Server, olderThan time.Duration, now time.Time, dryRun bool) ([]GCAbandonedResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bindingsDir := filepath.Join(s.cfg.Root, "bindings")
	entries, err := os.ReadDir(bindingsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	cutoff := now.Add(-olderThan)
	var results []GCAbandonedResult

	for _, entry := range entries {
		id, ok := remote.IDFromDir(entry.Name())
		if !entry.IsDir() || !ok {
			continue
		}
		label := s.clients.LabelOf(id)
		ownerPath := filepath.Join(bindingsDir, entry.Name())
		rt := s.runtimeAt(ownerPath)

		bindings, err := rt.Store.List()
		if err != nil {
			return nil, err
		}

		for _, b := range bindings {
			lastSeen, old := gcCandidate(b, cutoff)
			if !old {
				continue
			}
			logEntries, _ := rt.Store.ReadLog(b.Name)
			switch relevo.RoundStateOf(b, logEntries) {
			case remote.RoundRunning, remote.RoundQueued:
				continue
			}

			archived, err := gcArchive(ctx, rt, id, b, dryRun)
			if err != nil {
				return nil, err
			}
			results = append(results, GCAbandonedResult{
				Owner:    id,
				Label:    label,
				Name:     b.Name,
				LastSeen: lastSeen,
				Archive:  archived,
			})
		}
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Label != results[j].Label {
			return results[i].Label < results[j].Label
		}
		return results[i].Name < results[j].Name
	})

	return results, nil
}

// AdminUnbind archives a stale server binding: the admin's answer to a client's
// `add --server` whose 409 means the server already holds that name. A running
// round is refused unless force is set.
func AdminUnbind(ctx context.Context, s *Server, owner string, name string, force bool) (relevo.UnbindResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := s.resolveOwner(owner)
	if err != nil {
		return relevo.UnbindResult{}, err
	}

	rt, err := s.runtime(id)
	if err != nil {
		return relevo.UnbindResult{}, err
	}

	b, err := rt.Store.Load(name)
	if err != nil {
		return relevo.UnbindResult{}, err
	}

	if !force {
		logEntries, _ := rt.Store.ReadLog(b.Name)
		if relevo.RoundStateOf(b, logEntries) == remote.RoundRunning {
			return relevo.UnbindResult{}, fmt.Errorf("round %d is running; wait, or --force", b.Round)
		}
	}

	res, err := relevo.Unbind(ctx, rt, name, true)
	if err != nil {
		return res, err
	}
	if res.WorktreeKept == "" {
		if err := releaseServedRefs(ctx, rt, b); err != nil {
			slog.Warn("release served refs", "owner", id, "binding", b.Name, "err", err)
		}
	}
	return res, nil
}

// AdminOwnerRuntime resolves owner and returns its runtime and label for the
// server-side read verbs. A resolved owner whose bindings directory does not
// exist is store.ErrNotFound rather than a fresh store: Store.WithLock runs
// MkdirAll on the root, so the check must come first, or a read-only verb would
// create state for an owner that has never bound.
func AdminOwnerRuntime(s *Server, owner string) (relevo.Runtime, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, err := s.resolveOwner(owner)
	if err != nil {
		return relevo.Runtime{}, "", err
	}

	root, err := s.ownerRoot(id)
	if err != nil {
		return relevo.Runtime{}, "", err
	}
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return relevo.Runtime{}, "", store.ErrNotFound
		}
		return relevo.Runtime{}, "", err
	}

	return s.runtimeAt(root), s.clients.LabelOf(id), nil
}

func (s *Server) resolveOwner(owner string) (remote.ClientID, error) {
	clients := s.clients.List()

	for _, c := range clients {
		if string(c.ID) == owner {
			return c.ID, nil
		}
	}

	var matches []Client
	for _, c := range clients {
		if c.Label == owner {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].ID, nil
	case 0:
		return "", ErrNoSuchClient
	default:
		ids := make([]string, len(matches))
		for i, c := range matches {
			ids[i] = string(c.ID)
		}
		return "", fmt.Errorf("label %q is ambiguous: %s", owner, strings.Join(ids, ", "))
	}
}

// ledgerRuntime is the runtime the server-side gate verbs run on: the one
// server-wide gate record, not any owner's. Its store is over the serve root
// only because relevo's ledger mutation takes its lock through rt.Store.
// store.New creates nothing on its own, so locking through it cannot make an
// uninitialised root report as initialised.
func ledgerRuntime(s *Server) relevo.Runtime {
	return relevo.Runtime{
		Candidates: s.cfg.Candidates,
		Policy:     s.cfg.Policy,
		Store:      store.New(s.cfg.Root),
		Gates:      s.gates,
		Now:        s.cfg.Now,
	}
}

func AdminGates(s *Server) []availability.Gate {
	return availability.Gates(relevo.AvailabilityDeps(ledgerRuntime(s)))
}

func AdminAvailable(s *Server, subject string) (provider string, removed int, err error) {
	return availability.Available(relevo.AvailabilityDeps(ledgerRuntime(s)), subject, availability.ClearedByServer)
}

func AdminUnavailable(s *Server, token string, until time.Time, reason string) (provider string, err error) {
	return availability.Unavailable(relevo.AvailabilityDeps(ledgerRuntime(s)), token, until, reason)
}

// RenderGates formats `relevo serve gates`, printing the candidate's short name
// when the gate carries one.
func RenderGates(gates []availability.Gate, now time.Time) string {
	if len(gates) == 0 {
		return "no gates\n"
	}

	var sb strings.Builder
	for _, g := range gates {
		label := g.Token
		if g.Name != "" {
			label = g.Name
		}
		fmt.Fprintf(&sb, "%s  %s  %s  %s\n", label, availability.GateKindText(g.Kind), availability.GateUntilText(g.Until), sanitize.Text(g.Note))
	}
	return sb.String()
}
