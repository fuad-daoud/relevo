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

	// OpencodePushHorizon bounds POST retries. It is long on purpose: an opencode
	// give-up hands the payload to nothing deliverable, only to human eyes, and
	// thirty minutes rides out a restart or a closed laptop.
	OpencodePushHorizon = 30 * time.Minute

	// opencodeClockSkew shares agy's value, so both routes tolerate the same drift.
	opencodeClockSkew = agyClockSkew
)

// OpencodeDeliverer is the MasterMindDeliverer for opencode masterminds: it POSTs
// to opencode's HTTP API and confirms by reading the session back out of its
// sqlite db -- a 2xx alone is not evidence anything arrived.
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

// fallbackAfter is opencode's own long horizon: a give-up here hands the
// payload to a human, not to another route.
func (d *OpencodeDeliverer) fallbackAfter() time.Duration {
	if d.FallbackAfter > 0 {
		return d.FallbackAfter
	}
	return OpencodePushHorizon
}

// AdmitHorizon reports opencode's long horizon: a never-surfaced payload stops
// being an admit after this long instead of blocking its queue forever.
func (d *OpencodeDeliverer) AdmitHorizon() time.Duration {
	return d.fallbackAfter()
}

// Deliver implements MasterMindDeliverer's push half for opencode
// masterminds.
//
// The step order is fixed: the read-back runs before the give-up gate, so a
// payload already in the session confirms at any age, and the gate stays above
// the service checks, so a dead service past the window still gives up. The
// POST is single-shot per call -- the caller holds its global lock across the
// send, so a down moment rides out across ticks, never within one -- and a 2xx
// is admission, not delivery: the caller records the admit before Confirm.
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

	// Already there? Check before the gate so a late-admitted payload still
	// confirms and a retry never double-posts; the read-back is bounded by
	// queuedAt, so an earlier entry's row cannot answer for this one.
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

// Confirm is the read-back half for opencode: it polls the session and never
// POSTs, so an earlier tick's payload confirms rather than sends twice.
// queuedAt bounds the poll, so an earlier entry's turn cannot confirm this one.
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

// ConfirmOnce is the read-back for a repeat tick: one session look, no poll,
// never a POST, bounded to this entry's queuedAt.
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

// pastFallback reports whether the payload waited past the fallback window,
// logging once per payload.
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

// post POSTs the payload once: no backoff, no wait, so one Deliver never holds
// the caller's global lock for the horizon's sake. A refused POST stays
// pending for the next tick; only the horizon ends retries.
func (d *OpencodeDeliverer) post(ctx context.Context, svc opencodeService, sessionID, payload string) (Outcome, string, error) {
	took, reason, err := d.postOnce(ctx, svc, sessionID, payload)
	if err != nil {
		return OutcomeNotMine, "", err
	}
	if took {
		// A 2xx only proves the service queued the prompt; the caller admits
		// and reads back through Confirm in this same tick.
		return OutcomeAdmitted, "posted; awaiting the session", nil
	}
	return OutcomeUnavailable, reason, nil
}

// postOnce POSTs once; a refusal is a reason string, not a bug: a down or busy
// opencode is OutcomeUnavailable.
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

// confirm polls the session for the origin. A close without it is not failure:
// the payload is already admitted, so the admit stays for a later read-back.
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

// readOpencodeService reads and parses the opencode service file.
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

// loopbackOpencodeURL refuses anything but a loopback http origin: a report can
// contain source, so it must never go to whatever host a file names.
func loopbackOpencodeURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	return host == "127.0.0.1" || host == "localhost"
}

// buildRequest builds the prompt-queuing POST.
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

// firstPayloadLine returns payload's first line -- the origin marker Queue
// wrote. Deliver lacks the entry's name/round/kind, so it reads what is there.
func firstPayloadLine(payload string) string {
	line, _, _ := strings.Cut(payload, "\n")
	return line
}

// firstErrorLine keeps a multi-line error to one reason-string line.
func firstErrorLine(err error) string {
	first, _, _ := strings.Cut(strings.TrimSpace(err.Error()), "\n")
	return first
}

// tableSet reports which tables the database has, so an older one is not a failed query.
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

// seen reports whether origin already appears in the session as a user turn
// or queued inbox message. The read-back answers for one entry only: a row
// stamped before queuedAt (less opencodeClockSkew) is another entry's turn, or
// a reused origin line would match forever. A zero queuedAt skips the bound; a
// failing sqlite3 is unseen, and the call site reports it.
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

// opencodeConfirmQuery is the read-back proving the session took the turn: a
// pre-2.0 part/message user text, a 2.0 session_message user row, or a queued
// inbox message; any match confirms. ' is doubled in interpolated values.
//
// A non-zero queuedAt bounds every row at queuedAt-opencodeClockSkew in epoch
// milliseconds (the legacy pair's bound rides the joined message row, which
// carries the clock); zero emits the unbounded query.
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
