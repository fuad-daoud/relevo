package serve

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/burst"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
)

// verifyOutcome is one WhoAmI answer: a clean check verifies the row, a
// failed one names the head-side problem, and an unmapped failure names
// none, leaving the row for the next cycle.
type verifyOutcome struct {
	row      burst.NodeRow
	verified bool
	problem  string
}

// verifyNodes checks every unverified live node against its pinned
// fingerprint before it takes its first forward. A node answers WhoAmI over
// the pinned TLS connection, so the pin itself is the check: a mismatch
// fails the handshake before any HTTP. Success marks the row verified; a
// failure records the head-side problem for the next report and leaves the
// row unverified, so the check runs again next cycle. Fetching runs without
// s.mu; each result applies under it only while the row still carries the
// checked URL and fingerprint.
func (s *Server) verifyNodes(ctx context.Context) {
	if !s.burstOn() {
		return
	}
	now := s.cfg.Now().UTC()
	t := s.copyBurstTable()
	if t == nil {
		return
	}
	var check []burst.NodeRow
	for _, r := range t.Nodes {
		if r.Verified || r.Gone || r.State != "ready" {
			continue
		}
		check = append(check, r)
	}
	if len(check) == 0 {
		return
	}
	cl, err := s.burstNodeClient(check)
	if err != nil {
		slog.Warn("burst: node check skipped, client key unusable", "err", err)
		return
	}
	results := make([]verifyOutcome, 0, len(check))
	for _, r := range check {
		_, werr := cl.WhoAmI(ctx, r.ID)
		if werr != nil {
			slog.Warn("burst: node check failed", "node", r.ID, "err", werr)
			results = append(results, verifyOutcome{row: r, problem: burstVerifyProblem(werr)})
			continue
		}
		results = append(results, verifyOutcome{row: r, verified: true})
	}
	s.applyVerifyResults(ctx, now, results)
}

// burstNodeClient dials table rows as named servers, each pinned to its own
// row fingerprint. The key is the head's client key, the one `relevo config
// server key` creates; bursting stays off without it, so an unparsable one
// only ever meets a test.
func (s *Server) burstNodeClient(rows []burst.NodeRow) (*client.Client, error) {
	kp, err := remote.ParsePrivate(s.cfg.ClientKey)
	if err != nil {
		return nil, err
	}
	servers := make(remote.Servers, len(rows))
	for _, r := range rows {
		servers[r.ID] = remote.ServerEntry{URL: r.URL, Fingerprint: r.Fingerprint}
	}
	return client.New(servers, kp, s.cfg.Now), nil
}

// burstVerifyProblem maps a WhoAmI failure onto the head-side problem the
// report carries. A pin mismatch may be an impersonator; a refused key is
// the node's word that this head is not enrolled; a dropped exchange is
// unreachable and retried. Anything else leaves the row alone for the next
// cycle rather than reporting a problem the head cannot name.
func burstVerifyProblem(err error) string {
	if err == nil {
		return ""
	}
	// The pin can surface as a bare message through a TLS stack the client
	// does not wrap, so the check reads the text too, the way the client's
	// own request path does.
	if errors.Is(err, client.ErrCertChanged) || strings.Contains(err.Error(), client.ErrCertChanged.Error()) {
		return burst.ReportProblemFingerprint
	}
	var herr *client.HTTPError
	if errors.As(err, &herr) {
		switch {
		case herr.Status == http.StatusUnauthorized || herr.Status == http.StatusForbidden:
			return burst.ReportProblemKey
		case herr.Status >= 500:
			return burst.ReportProblemUnreachable
		}
		return ""
	}
	if errors.Is(err, client.ErrUnreachable) {
		return burst.ReportProblemUnreachable
	}
	return ""
}

// applyVerifyResults folds one check pass into the table. A result lands
// only while its row is still unverified at the checked URL and
// fingerprint, so a report that merged mid-pass wins. A fingerprint problem
// dispatches the security hook only on the transition into it: the check
// runs every cycle while the row stays unverified, and a standing mismatch
// re-reported each time is noise, not an event.
func (s *Server) applyVerifyResults(ctx context.Context, now time.Time, results []verifyOutcome) {
	var events []hooks.Event
	s.mu.Lock()
	if s.burstTable == nil {
		s.mu.Unlock()
		return
	}
	for _, res := range results {
		row := burstTableRow(s.burstTable, res.row.ID)
		if row == nil || row.Verified || row.URL != res.row.URL || row.Fingerprint != res.row.Fingerprint {
			continue
		}
		if res.verified {
			row.Verified = true
			row.Problem = ""
			row.ProblemSince = time.Time{}
			continue
		}
		if res.problem == "" {
			continue
		}
		if row.Problem != res.problem {
			if res.problem == burst.ReportProblemFingerprint {
				events = append(events, hooks.Event{
					Type: hooks.EventBurstSecurity, BindingID: row.ID,
					State: res.problem, OldState: row.Problem, Timestamp: now,
				})
			}
			row.Problem = res.problem
			row.ProblemSince = now
		}
	}
	cp := *s.burstTable
	cp.Nodes = append([]burst.NodeRow(nil), s.burstTable.Nodes...)
	s.mu.Unlock()
	for _, e := range events {
		if s.cfg.Hooks != nil {
			s.cfg.Hooks.Dispatch(ctx, e)
		}
	}
	if err := saveBurstTable(s.cfg.DB.LocalOrSelf(), &cp); err != nil {
		slog.Warn("burst: verified node table not saved", "err", err)
	}
}

// burstTableRow finds the live row by id, or nil when a merge dropped it.
func burstTableRow(t *burst.NodeTable, id string) *burst.NodeRow {
	for i := range t.Nodes {
		if t.Nodes[i].ID == id {
			return &t.Nodes[i]
		}
	}
	return nil
}
