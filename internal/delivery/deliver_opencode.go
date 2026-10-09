package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
)

const (
	OpencodeRequestTimeout = 5 * time.Second
	OpencodeConfirmWindow  = 3 * time.Second
	OpencodeConfirmPoll    = 250 * time.Millisecond
	DefaultFallbackAfter   = 30 * time.Second

	// OpencodePushHorizon is how long an opencode payload may keep being POSTed
	// before Deliver gives up on it. It is opencode's own and it is long: an
	// opencode mastermind has no collector on the pane path, so a give-up here
	// hands the payload to nothing that would deliver it -- it only makes the
	// entry visible to a human. Thirty minutes rides out a service restart or a
	// closed laptop, and still bounds the wait.
	OpencodePushHorizon = 30 * time.Minute

	// opencodeClockSkew tolerates opencode's clock reading slightly behind
	// the releaser's when comparing a session row's timestamp against the
	// entry's queuedAt. It is agy's value, so both routes tolerate the same
	// drift and a row stamped a hair before the queue time still counts.
	opencodeClockSkew = agyClockSkew
)

// OpencodeDeliverer is the MasterMindDeliverer for opencode masterminds
// (docs/specs/2026-09-22-opencode-delivery-design.md): it POSTs the
// payload to opencode's own HTTP API and confirms delivery by reading the
// session back out of opencode's sqlite db, because a 2xx from the wrong
// server process is not evidence anything arrived.
type OpencodeDeliverer struct {
	Client     *http.Client // nil -> &http.Client{Timeout: OpencodeRequestTimeout}
	StateFiles []string     // candidate service.json files, tried in order
	DBPath     string       // $XDG_DATA_HOME/opencode/opencode.db
	Exec       usage.Exec   // the sqlite3 shell-out; nil -> OutcomeNotMine
	Now        func() time.Time
	Alive      func(pid int) bool // nil -> the same rule the claim store uses
	// FallbackAfter is the give-up horizon; zero -> OpencodePushHorizon. agy
	// shares the 30s DefaultFallbackAfter instead, and that default is not moved.
	FallbackAfter time.Duration

	// gaveUp rate-limits the give-up log line.
	gaveUp giveUpLog
}

// opencodeService is $XDG_CONFIG_HOME/opencode/service.json. Verified shape
// on opencode 2.0.12: {"id","version","url","pid","password"}.
type opencodeService struct {
	URL      string `json:"url"`
	Password string `json:"password"`
	PID      int    `json:"pid"`
	Version  string `json:"version"`
}

var opencodeSessionIDPattern = regexp.MustCompile(`^ses_[A-Za-z0-9]+$`)

func validSessionID(id string) bool {
	return opencodeSessionIDPattern.MatchString(id)
}

func (d *OpencodeDeliverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *OpencodeDeliverer) alive(pid int) bool {
	if d.Alive != nil {
		return d.Alive(pid)
	}
	return defaultClaimAlive(pid)
}

func (d *OpencodeDeliverer) client() *http.Client {
	if d.Client != nil {
		return d.Client
	}
	return &http.Client{Timeout: OpencodeRequestTimeout}
}

// fallbackAfter is the give-up horizon: opencode's own long one, because an
// opencode give-up has nowhere to hand the payload but a human. The shared
// 30s DefaultFallbackAfter stays where agy reads it.
func (d *OpencodeDeliverer) fallbackAfter() time.Duration {
	if d.FallbackAfter > 0 {
		return d.FallbackAfter
	}
	return OpencodePushHorizon
}

// AdmitHorizon reports opencode's own long horizon: an admitted payload whose
// text never shows up in the session stops being an admit after this long, so
// the entry is delivered again instead of blocking its binding's queue forever.
func (d *OpencodeDeliverer) AdmitHorizon() time.Duration {
	return d.fallbackAfter()
}

// Deliver implements MasterMindDeliverer's push half for opencode
// masterminds.
//
// The step order is fixed and non-obvious: guards, origin, seen, pastFallback,
// service resolution, the single POST. The read-back runs before the give-up
// gate, so a payload whose text is already in the session is confirmed at any
// age -- a late-admitted payload must not be failed by the horizon before it
// is ever read. The gate stays above the service checks, so a dead service past
// the window still gives up.
//
// The POST is single-shot per call: Deliver never sleeps between POSTs, because
// the caller's lock is a global one over the whole state root and one tick
// holds it across the send. An opencode that is down for a moment is therefore
// ridden out across ticks, not within one: a refused POST reports
// OutcomeUnavailable, which leaves the entry pending, and the next tick POSTs
// again under the same horizon. Only the horizon ends the retries for good.
//
// A 2xx is admission, not delivery: Deliver returns OutcomeAdmitted without
// reading the session back, and the caller records that admit before calling
// Confirm. The entry's own AdmittedAt is the fact, so a restarted daemon and a
// second process agree on it.
func (d *OpencodeDeliverer) Deliver(ctx context.Context, mastermind store.Endpoint, payload, path string, queuedAt time.Time) (Outcome, string, error) {
	if mastermind.Kind != "opencode" {
		return OutcomeNotMine, "", nil
	}
	if d.Exec == nil {
		return OutcomeNotMine, "no sqlite3", nil
	}
	if !validSessionID(mastermind.SessionID) {
		return OutcomeNotMine, "no opencode session id", nil
	}

	origin := firstPayloadLine(payload)

	// Already there? A previous tick may have delivered and crashed before
	// confirming. Check before the give-up gate, so a payload admitted late is
	// still confirmed, and so a retry never double-posts. The read-back is
	// bounded by queuedAt, so it answers for this entry: a row an earlier
	// entry left in the session is not this payload's.
	alreadySeen, err := d.seen(ctx, mastermind.SessionID, origin, queuedAt)
	if err != nil {
		return OutcomeUnavailable, "sqlite3: " + firstErrorLine(err), nil
	}
	if alreadySeen {
		return OutcomeDelivered, "already present", nil
	}

	if out, reason, gave := d.pastFallback(mastermind.SessionID, payload, queuedAt); gave {
		return out, reason, nil
	}

	var (
		svc   opencodeService
		found bool
	)
	for _, path := range d.StateFiles {
		s, err := readOpencodeService(path)
		if err == nil && s.URL != "" {
			svc = s
			found = true
			break
		}
	}
	if !found || !d.alive(svc.PID) {
		return OutcomeUnavailable, "opencode service not running", nil
	}
	if !loopbackOpencodeURL(svc.URL) {
		return OutcomeUnavailable, "opencode service url is not loopback", nil
	}

	return d.post(ctx, svc, mastermind.SessionID, payload)
}

// Confirm implements MasterMindDeliverer's read-back half for opencode
// masterminds: it polls the session for the payload's origin and never POSTs,
// so a payload admitted by an earlier tick is confirmed rather than sent twice.
// queuedAt bounds the poll the same way it bounds Deliver's read-back, so a
// turn an earlier entry left in the session cannot confirm this one.
func (d *OpencodeDeliverer) Confirm(ctx context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error) {
	if mastermind.Kind != "opencode" {
		return OutcomeNotMine, "", nil
	}
	if d.Exec == nil {
		return OutcomeNotMine, "no sqlite3", nil
	}
	if !validSessionID(mastermind.SessionID) {
		return OutcomeNotMine, "no opencode session id", nil
	}
	return d.confirm(ctx, mastermind.SessionID, firstPayloadLine(payload), queuedAt)
}

// ConfirmOnce is the read-back for a repeat tick: one look at the session, no
// poll and never a POST. An origin the single query does not find leaves the
// payload admitted for the next tick. It carries the entry's own queuedAt, so
// the one read-back is bounded to the rows this entry could have written.
func (d *OpencodeDeliverer) ConfirmOnce(ctx context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error) {
	if mastermind.Kind != "opencode" {
		return OutcomeNotMine, "", nil
	}
	if d.Exec == nil {
		return OutcomeNotMine, "no sqlite3", nil
	}
	if !validSessionID(mastermind.SessionID) {
		return OutcomeNotMine, "no opencode session id", nil
	}

	seen, err := d.seen(ctx, mastermind.SessionID, firstPayloadLine(payload), queuedAt)
	if err != nil {
		return OutcomeUnavailable, "sqlite3: " + firstErrorLine(err), nil
	}
	if seen {
		return OutcomeDelivered, "", nil
	}
	return OutcomeAdmitted, "posted; awaiting the session", nil
}

// pastFallback reports whether the payload has waited past the fallback
// window, logging once per payload when it has.
func (d *OpencodeDeliverer) pastFallback(sessionID, payload string, queuedAt time.Time) (Outcome, string, bool) {
	if queuedAt.IsZero() || d.now().Sub(queuedAt) <= d.fallbackAfter() {
		return OutcomeNotMine, "", false
	}
	reason := fmt.Sprintf("opencode push gave up after %s", d.fallbackAfter())
	key := sessionID + "\x00" + strconv.FormatInt(queuedAt.UnixNano(), 10) + "\x00" + firstPayloadLine(payload)
	if d.gaveUp.shouldLog(key, d.now()) {
		slog.Info("opencode push not confirmed; payload stays pending for the background wait", "session", sessionID, "reason", reason)
	}
	return OutcomeNotMine, reason, true
}

// post POSTs the payload once and reports what the single attempt made of it.
// It is the whole per-call send: there is no backoff and no wait between
// attempts, so one Deliver can never hold the caller's global lock for the
// horizon's sake.
//
// A refused POST is a reason, not a give-up: the payload stays pending, the
// next tick POSTs it again under the same horizon, and only the horizon ends
// the retries. A 2xx ends the call and the caller confirms it.
func (d *OpencodeDeliverer) post(ctx context.Context, svc opencodeService, sessionID, payload string) (Outcome, string, error) {
	took, reason, err := d.postOnce(ctx, svc, sessionID, payload)
	if err != nil {
		return OutcomeNotMine, "", err
	}
	if took {
		// The 2xx only proves the service queued the prompt: the caller marks
		// the entry admitted and reads the session back through Confirm, in this
		// same tick.
		return OutcomeAdmitted, "posted; awaiting the session", nil
	}
	return OutcomeUnavailable, reason, nil
}

// postOnce POSTs the payload once. took reports whether the service took it; a
// service that refused it is a reason string, because an opencode that is down
// or busy is OutcomeUnavailable rather than a bug in relevo.
func (d *OpencodeDeliverer) postOnce(ctx context.Context, svc opencodeService, sessionID, payload string) (bool, string, error) {
	req, err := d.buildRequest(ctx, svc, sessionID, payload)
	if err != nil {
		return false, "", fmt.Errorf("build opencode request: %w", err)
	}

	resp, err := d.client().Do(req)
	if err != nil {
		return false, "post: " + firstErrorLine(err), nil
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("post: %d", resp.StatusCode), nil
	}
	return true, "", nil
}

// confirm polls the session until the origin is seen in it, the confirm
// window closes, or the context ends. A window that closes without the origin
// is not a failed delivery: the payload is already admitted, so it reports
// OutcomeAdmitted and the caller keeps the admit for a later read-back.
func (d *OpencodeDeliverer) confirm(ctx context.Context, sessionID, origin string, queuedAt time.Time) (Outcome, string, error) {
	deadline := time.Now().Add(OpencodeConfirmWindow)
	for {
		seen, err := d.seen(ctx, sessionID, origin, queuedAt)
		if err != nil {
			return OutcomeUnavailable, "sqlite3: " + firstErrorLine(err), nil
		}
		if seen {
			slog.Info("opencode push delivered", "session", sessionID)
			return OutcomeDelivered, "", nil
		}
		if !time.Now().Before(deadline) {
			return OutcomeAdmitted, "posted; awaiting the session", nil
		}
		select {
		case <-ctx.Done():
			return OutcomeAdmitted, "posted; awaiting the session", nil
		case <-time.After(OpencodeConfirmPoll):
		}
	}
}

// readOpencodeService reads and parses $XDG_CONFIG_HOME/opencode/service.json.
func readOpencodeService(path string) (opencodeService, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return opencodeService{}, err
	}
	var svc opencodeService
	if err := json.Unmarshal(raw, &svc); err != nil {
		return opencodeService{}, err
	}
	return svc, nil
}

// loopbackOpencodeURL reports whether raw is a http://127.0.0.1[:port] or
// http://localhost[:port] origin. relevo refuses anything else outright: it
// would be sending a mastermind's report, which can contain source, to
// whatever host the file names.
func loopbackOpencodeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost"
}

// buildRequest builds the POST that queues the prompt.
func (d *OpencodeDeliverer) buildRequest(ctx context.Context, svc opencodeService, sessionID, payload string) (*http.Request, error) {
	body, err := json.Marshal(struct {
		Text     string `json:"text"`
		Delivery string `json:"delivery"`
		Resume   bool   `json:"resume"`
	}{Text: payload, Delivery: "queue", Resume: true})
	if err != nil {
		return nil, fmt.Errorf("encode opencode prompt: %w", err)
	}

	target := strings.TrimRight(svc.URL, "/") + "/api/session/" + sessionID + "/prompt"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build opencode request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("opencode", svc.Password)
	return req, nil
}

// firstPayloadLine returns payload's first line -- the origin marker
// OriginLine wrote and PushText preserves. It is not recomputed with
// OriginLine: Deliver does not have the entry's name/round/kind, and this
// is exactly what Queue actually wrote.
func firstPayloadLine(payload string) string {
	line, _, _ := strings.Cut(payload, "\n")
	return line
}

// firstErrorLine is the first line of err's message, so a multi-line
// sqlite3 or transport error does not blow up a one-line reason string.
func firstErrorLine(err error) string {
	first, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	return first
}

// tableSet reports which of OpenCode's tables the database has, so a database
// that predates session_inbox does not turn into a failed query.
func (d *OpencodeDeliverer) tableSet(ctx context.Context) (map[string]bool, error) {
	out, err := d.Exec.Run(ctx, "sqlite3", "-readonly", "-json", d.DBPath,
		"select name from sqlite_master where type = 'table'")
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(out))
	tables := map[string]bool{}
	if len(trimmed) == 0 {
		return tables, nil
	}

	var rows []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, err
	}
	for _, r := range rows {
		tables[r.Name] = true
	}
	return tables, nil
}

// seen reports whether origin already appears in sessionID's messages as a
// user turn or queued in session_inbox: the inbox table holds admitted
// work and is consumed, so a user turn or a queued message is what proves the
// session received it. OpenCode 2.0.14 keeps user turns in session_message,
// while the part/message tables only hold pre-2.0 turns; any of them counts.
//
// The read-back answers for one entry: a row stamped before the entry's
// queuedAt (less opencodeClockSkew) is another entry's turn and does not
// count. Without that bound a second findings payload for a reused binding
// name -- whose origin line names no round -- matches the first forever, and a
// rebound round-1 report identical to the previous incarnation's matches too.
// A zero queuedAt skips the bound. A failing sqlite3 is not seen and not a Go
// error -- the call site turns it into OutcomeUnavailable.
func (d *OpencodeDeliverer) seen(ctx context.Context, sessionID, origin string, queuedAt time.Time) (bool, error) {
	tables, err := d.tableSet(ctx)
	if err != nil {
		return false, err
	}
	query := opencodeConfirmQuery(sessionID, origin, tables["session_inbox"], queuedAt)
	out, err := d.Exec.Run(ctx, "sqlite3", "-readonly", d.DBPath, query)
	if err != nil {
		return false, err
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	n, convErr := strconv.Atoi(first)
	if convErr != nil {
		return false, nil
	}
	return n > 0, nil
}

// opencodeConfirmQuery is the read-back that proves a session actually took
// the turn: a user text part in the pre-2.0 part/message tables, a
// user row in OpenCode 2.0's session_message, or a queued message in
// session_inbox. The counts are summed, so any matching row confirms.
// ' is doubled in all interpolated values, the SQL string-literal escape.
//
// A non-zero queuedAt bounds every counted row at queuedAt-opencodeClockSkew,
// in the epoch milliseconds all three time_created columns hold. part carries
// no clock, so the legacy pair's bound rides on the joined message row. A zero
// queuedAt emits the unbounded query, so a caller with no queue time keeps
// matching rows of any age.
func opencodeConfirmQuery(sessionID, origin string, hasInbox bool, queuedAt time.Time) string {
	sid := strings.ReplaceAll(sessionID, "'", "''")
	org := strings.ReplaceAll(origin, "'", "''")

	bound, joinedBound := "", ""
	if !queuedAt.IsZero() {
		notBefore := queuedAt.Add(-opencodeClockSkew).UnixMilli()
		bound = fmt.Sprintf(" and time_created >= %d", notBefore)
		joinedBound = fmt.Sprintf(" and m.time_created >= %d", notBefore)
	}

	query := fmt.Sprintf(
		"select (select count(*) from part p join message m on m.id = p.message_id"+
			" where p.session_id = '%s'"+
			" and json_extract(m.data, '$.role') = 'user'"+
			" and json_extract(p.data, '$.type') = 'text'"+
			" and json_extract(p.data, '$.text') like '%%%s%%'"+
			"%s)"+
			" + (select count(*) from session_message"+
			" where session_id = '%s'"+
			" and type = 'user'"+
			" and json_extract(data, '$.text') like '%%%s%%'"+
			"%s)",
		sid, org, joinedBound, sid, org, bound)
	if hasInbox {
		query += fmt.Sprintf(
			" + (select count(*) from session_inbox"+
				" where session_id = '%s'"+
				" and payload like '%%%s%%'"+
				"%s)",
			sid, org, bound)
	}
	return query
}
