package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

const (
	// AgyMaxContent bounds the send-message content: under Linux's 131072-byte
	// per-argument limit, so the report argument is always legal.
	AgyMaxContent = 100_000
	// AgyConfirmWindow bounds inbox polling: the message is already sent, so a
	// close without the mark is not an error -- the next tick finds it without resending.
	AgyConfirmWindow = 3 * time.Second
	// AgyConfirmPoll is the gap between inbox scans inside that window.
	AgyConfirmPoll = 250 * time.Millisecond
	// agyClockSkew tolerates the receiver's clock reading slightly behind the releaser's.
	agyClockSkew = 2 * time.Second
	// agyTitleMaxRunes bounds --title= to a title-length cut of the origin line.
	agyTitleMaxRunes = 100
)

// EnvExec runs one binary and returns its stdout. There is no shell, so the
// token rides in extraEnv and nowhere else.
type EnvExec interface {
	Run(ctx context.Context, extraEnv []string, bin string, args ...string) ([]byte, error)
}

// AgyDeliverer is the MasterMindDeliverer for agy masterminds: it drops the
// payload in the conversation's agentapi inbox and confirms the read-back from
// agy's own state directory.
type AgyDeliverer struct {
	Exec EnvExec // nil -> OutcomeNotMine, "no exec"
	// Creds is the machine database's secret store; nil -> OutcomeNotMine.
	Creds SecretStore
	// Home is agy's state root (the directory holding brain/). "" means
	// ~/.gemini/antigravity-cli.
	Home          string
	Now           func() time.Time
	FallbackAfter time.Duration // zero -> DefaultFallbackAfter
	ConfirmWindow time.Duration // zero -> AgyConfirmWindow
	ConfirmPoll   time.Duration // zero -> AgyConfirmPoll
	// gaveUp rate-limits the give-up log line.
	gaveUp giveUpLog
}

// agyMessage is the part of a message file the inbox scan reads.
type agyMessage struct {
	ID        string    `json:"id"`
	Recipient string    `json:"recipient"`
	Timestamp time.Time `json:"timestamp"`
	Content   string    `json:"content"`
}

// agyInbox is what one scan of a conversation's inbox found about a payload.
type agyInbox struct {
	// sent is a matching message file exists at all.
	sent bool
	// read is a matching message id is marked true in read.json.
	read bool
	// undelivered is a matching message sits in messages/undelivered/.
	undelivered bool
}

func (d *AgyDeliverer) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *AgyDeliverer) fallbackAfter() time.Duration {
	if d.FallbackAfter > 0 {
		return d.FallbackAfter
	}
	return DefaultFallbackAfter
}

// AdmitHorizon reports agy's short horizon: a never-read message stops being
// an admit after this long, returning the entry to the pending scans.
func (d *AgyDeliverer) AdmitHorizon() time.Duration {
	return d.fallbackAfter()
}

func (d *AgyDeliverer) confirmWindow() time.Duration {
	if d.ConfirmWindow > 0 {
		return d.ConfirmWindow
	}
	return AgyConfirmWindow
}

func (d *AgyDeliverer) confirmPoll() time.Duration {
	if d.ConfirmPoll > 0 {
		return d.ConfirmPoll
	}
	return AgyConfirmPoll
}

// home resolves agy's state root, defaulting to agy's own directory.
func (d *AgyDeliverer) home() (string, error) {
	if d.Home != "" {
		return d.Home, nil
	}
	dir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".gemini", "antigravity-cli"), nil
}

// Deliver implements MasterMindDeliverer's push half for agy masterminds.
//
// The step order is fixed: the inbox scan runs before the give-up gate, so a
// message already marked read is confirmed at any age, and before every send,
// so a retry after a crash cannot post twice. The gate runs before the
// sent-but-unread admit -- the only outcome that re-enters the loop -- and
// above the creds check, so an expired entry goes to the pull route instead of
// being stamped and polled again.
//
// A send is admission, not delivery, and the reason never carries a credential:
// the token passes only through one exec's extraEnv and agy text is redacted.
func (d *AgyDeliverer) Deliver(ctx context.Context, mastermind store.Endpoint, payload, ref string, queuedAt time.Time) (Outcome, string, error) {
	if mastermind.Kind != "agy" {
		return OutcomeNotMine, "", nil
	}
	if d.Exec == nil || d.Creds == nil {
		return OutcomeNotMine, "no exec", nil
	}

	conv := mastermind.SessionID
	if !validConversationID(conv) {
		return OutcomeNotMine, "agy mastermind session is not a conversation id; run relevo mastermind init inside agy", nil
	}

	origin := firstPayloadLine(payload)

	// Already there? A previous tick may have sent and crashed; check before
	// the gate so a read message confirms at any age and a retry never sends twice.
	state := d.inbox(conv, origin, queuedAt)
	if state.read {
		return OutcomeDelivered, "already present", nil
	}
	if state.undelivered {
		return OutcomeUnavailable, "agy reports the message undelivered", nil
	}

	// Past the window the entry is no longer an admission candidate: the admit
	// below would re-enter the loop and stamp it every tick, so it stays
	// pending and claimable through the pull route instead.
	if out, reason, gave := d.pastFallback(conv, payload, queuedAt); gave {
		return out, reason, nil
	}

	// An unread inbox message is admission, not delivery, inside the window only.
	if state.sent {
		return OutcomeAdmitted, "sent to agy but not yet read", nil
	}

	creds, err := ReadAgyCreds(d.Creds, conv)
	if err != nil {
		return OutcomeUnavailable, "no agy credentials for this conversation; run any relevo command inside the agy session", nil
	}
	if !loopbackAgyAddress(creds.LSAddress) {
		return OutcomeUnavailable, "agy language server address is not loopback", nil
	}

	content := payload
	if len(content) > AgyMaxContent {
		content = agyOversizeContent(origin, len(content), ref)
	}
	exe := creds.AgentAPIExe
	if exe == "" {
		exe = "agy"
	}

	out, err := d.Exec.Run(ctx,
		[]string{agyLSAddressEnv + "=" + creds.LSAddress, agyCSRFTokenEnv + "=" + creds.CSRFToken},
		exe, "agentapi", "send-message", "--title="+agyTitle(origin), conv, content)
	if err != nil {
		return OutcomeUnavailable, redactAgy("send-message: "+firstErrorLine(err), creds.CSRFToken), nil
	}
	if msg := agySendError(out); msg != "" {
		return OutcomeUnavailable, redactAgy("send-message: "+msg, creds.CSRFToken), nil
	}

	// In the inbox now: admission, not delivery; the caller records the admit
	// and reads the mark back through Confirm.
	return OutcomeAdmitted, "sent to agy but not yet read", nil
}

// Confirm is the read-back half for agy: it reads the inbox's read mark and
// never sends, so an earlier tick's payload confirms rather than redelivers.
func (d *AgyDeliverer) Confirm(ctx context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error) {
	if mastermind.Kind != "agy" {
		return OutcomeNotMine, "", nil
	}
	if d.Exec == nil || d.Creds == nil {
		return OutcomeNotMine, "no exec", nil
	}

	conv := mastermind.SessionID
	if !validConversationID(conv) {
		return OutcomeNotMine, "agy mastermind session is not a conversation id; run relevo mastermind init inside agy", nil
	}

	origin := firstPayloadLine(payload)
	if state := d.inbox(conv, origin, queuedAt); state.read {
		return OutcomeDelivered, "already present", nil
	}
	return d.confirm(ctx, conv, origin, queuedAt)
}

// ConfirmOnce is the read-back for a repeat tick: one scan, no poll, never a
// send; an unread payload stays admitted.
func (d *AgyDeliverer) ConfirmOnce(_ context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error) {
	if mastermind.Kind != "agy" {
		return OutcomeNotMine, "", nil
	}
	if d.Exec == nil || d.Creds == nil {
		return OutcomeNotMine, "no exec", nil
	}

	conv := mastermind.SessionID
	if !validConversationID(conv) {
		return OutcomeNotMine, "agy mastermind session is not a conversation id; run relevo mastermind init inside agy", nil
	}

	if state := d.inbox(conv, firstPayloadLine(payload), queuedAt); state.read {
		return OutcomeDelivered, "already present", nil
	}
	return OutcomeAdmitted, "sent to agy but not yet read", nil
}

// pastFallback reports whether the payload waited past the fallback window,
// logging once per payload.
func (d *AgyDeliverer) pastFallback(conv, payload string, queuedAt time.Time) (Outcome, string, bool) {
	if queuedAt.IsZero() || d.now().Sub(queuedAt) <= d.fallbackAfter() {
		return OutcomeNotMine, "", false
	}
	reason := fmt.Sprintf("agy push gave up after %s", d.fallbackAfter())
	key := conv + "\x00" + strconv.FormatInt(queuedAt.UnixNano(), 10) + "\x00" + firstPayloadLine(payload)
	if d.gaveUp.shouldLog(key, d.now()) {
		slog.Info("agy push not confirmed; payload stays pending for the background wait", "conversation", conv, "reason", reason)
	}
	return OutcomeNotMine, reason, true
}

// confirm polls the inbox for the read mark. A close without the mark is not
// failure: the message is already sent, so the admit stays for a later read-back.
func (d *AgyDeliverer) confirm(ctx context.Context, conv, origin string, queuedAt time.Time) (Outcome, string, error) {
	deadline := d.now().Add(d.confirmWindow())
	for {
		state := d.inbox(conv, origin, queuedAt)
		if state.read {
			slog.Info("agy push delivered", "conversation", conv)
			return OutcomeDelivered, "", nil
		}
		if state.undelivered {
			return OutcomeUnavailable, "agy reports the message undelivered", nil
		}
		if !d.now().Before(deadline) {
			return OutcomeAdmitted, "sent to agy but not yet read", nil
		}
		select {
		case <-ctx.Done():
			return OutcomeAdmitted, "sent to agy but not yet read", nil
		case <-time.After(d.confirmPoll()):
		}
	}
}

// inbox scans the conversation's inbox for this payload's message. An
// unreadable or half-written file is skipped: another process writes these
// files while relevo reads them.
func (d *AgyDeliverer) inbox(conv, origin string, queuedAt time.Time) agyInbox {
	home, err := d.home()
	if err != nil {
		return agyInbox{}
	}

	base := filepath.Join(home, "brain", conv, ".system_generated", "messages")
	read := readAgyReadIDs(filepath.Join(base, "read.json"))

	var state agyInbox
	for _, dir := range []string{base, filepath.Join(base, "undelivered")} {
		undelivered := filepath.Base(dir) == "undelivered"
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == "read.json" {
				continue
			}
			msg, ok := readAgyMessage(filepath.Join(dir, e.Name()))
			if !ok || !agyMessageMatches(msg, conv, origin, queuedAt) {
				continue
			}
			state.sent = true
			if undelivered {
				state.undelivered = true
			}
			if read[msg.ID] {
				state.read = true
			}
		}
	}
	return state
}

// readAgyMessage reads one message file; unreadable, malformed or id-less reports false.
func readAgyMessage(path string) (agyMessage, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return agyMessage{}, false
	}
	var msg agyMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return agyMessage{}, false
	}
	if msg.ID == "" {
		return agyMessage{}, false
	}
	return msg, true
}

// readAgyReadIDs reads agy's map of read message ids; missing or malformed reads as none read.
func readAgyReadIDs(path string) map[string]bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var ids map[string]bool
	if err := json.Unmarshal(raw, &ids); err != nil {
		return nil
	}
	return ids
}

// agyMessageMatches is the inbox matching rule: addressed to this
// conversation, first line is the payload's origin line, not older than the
// queuing entry. A zero queuedAt skips the time check.
func agyMessageMatches(msg agyMessage, conv, origin string, queuedAt time.Time) bool {
	if msg.Recipient != conv {
		return false
	}
	if firstPayloadLine(msg.Content) != origin {
		return false
	}
	if !queuedAt.IsZero() && msg.Timestamp.Before(queuedAt.Add(-agyClockSkew)) {
		return false
	}
	return true
}

// agyTitle is the --title= value: the origin line cut to agyTitleMaxRunes runes.
func agyTitle(origin string) string {
	runes := []rune(origin)
	if len(runes) <= agyTitleMaxRunes {
		return origin
	}
	return string(runes[:agyTitleMaxRunes])
}

// agyOversizeContent replaces a payload too long for one argv element: the
// origin line still identifies the round without an argv the kernel would
// refuse. ref is the `relevo show …` command for the full text, or "".
func agyOversizeContent(origin string, n int, ref string) string {
	if ref == "" {
		return fmt.Sprintf("%s\n\nThe output is too long to push (%d bytes); run relevo wait.", origin, n)
	}
	return fmt.Sprintf("%s\n\nThe output is too long to push (%d bytes). Read it: %s", origin, n, ref)
}

// agySendError returns send-message's JSON error's first line, or "". Non-error
// output is not failure: the inbox read-back is the proof, not the message shape.
func agySendError(out []byte) string {
	var v struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &v); err != nil {
		return ""
	}
	if v.Error == "" {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(v.Error), "\n")
	return line
}

// redactAgy replaces token in s with <redacted>, so agy text cannot carry the
// credential into a log line or reason. Empty token is a no-op.
func redactAgy(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, redactedToken)
}
