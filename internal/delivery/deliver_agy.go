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
	// AgyMaxContent bounds the content passed to `agy agentapi send-message`.
	// Linux's per-argument limit is 131072 bytes; this stays under it so an
	// argument carrying the whole report is always legal.
	AgyMaxContent = 100_000
	// AgyConfirmWindow is how long Deliver polls the conversation's inbox for
	// the read mark before giving up on this attempt. The message is already
	// sent by then, so giving up is not an error: the next tick finds the
	// message and reports the same state without resending.
	AgyConfirmWindow = 3 * time.Second
	// AgyConfirmPoll is the gap between inbox scans inside that window.
	AgyConfirmPoll = 250 * time.Millisecond
	// agyClockSkew tolerates the receiver's clock reading slightly behind the
	// releaser's when comparing a message's timestamp against queuedAt.
	agyClockSkew = 2 * time.Second
	// agyTitleMaxRunes bounds the --title= argument: the origin line, cut to
	// this many runes so a title stays a title.
	agyTitleMaxRunes = 100
)

// EnvExec runs one binary and returns its stdout. extraEnv is KEY=VALUE pairs
// appended to the parent environment; there is no shell, so the token rides in
// the environment and nowhere else.
type EnvExec interface {
	Run(ctx context.Context, extraEnv []string, bin string, args ...string) ([]byte, error)
}

// AgyDeliverer is the MasterMindDeliverer for agy masterminds: it wakes an
// idle agy session by dropping the payload in that conversation's agentapi
// inbox, and it confirms the receipt by finding the message read back out of
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

// agyMessage is the part of a message file the inbox scan reads. The real file
// also carries sender, priority and renderDetails; relevo needs none of them.
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

// home resolves agy's state root: the configured Home, else agy's default.
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

// Deliver implements MasterMindDeliverer for agy masterminds.
//
// It never sends a payload twice: the inbox is scanned before every send, so a
// retry after a crash between sending and confirming finds the message and
// reports its state instead. It never puts a credential in the reason: the
// token passes only through the extraEnv of one exec, and any text that came
// back from agy is redacted before it becomes a reason.
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
	if out, reason, gave := d.pastFallback(conv, payload, queuedAt); gave {
		return out, reason, nil
	}

	creds, err := ReadAgyCreds(d.Creds, conv)
	if err != nil {
		return OutcomeUnavailable, "no agy credentials for this conversation; run any relevo command inside the agy session", nil
	}
	if !loopbackAgyAddress(creds.LSAddress) {
		return OutcomeUnavailable, "agy language server address is not loopback", nil
	}

	origin := firstPayloadLine(payload)

	// Already there? A previous tick may have sent and crashed before
	// confirming. Check first, so a retry never sends twice.
	if state := d.inbox(conv, origin, queuedAt); state.read {
		return OutcomeDelivered, "already present", nil
	} else if state.undelivered {
		return OutcomeUnavailable, "agy reports the message undelivered", nil
	} else if state.sent {
		return OutcomeUnavailable, "sent to agy but not yet read", nil
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

	// Accepted is not read: confirm by finding the message in the inbox
	// and the mark in read.json, polling briefly because agy writes both.
	return d.confirm(ctx, conv, origin, queuedAt)
}

// pastFallback reports whether the payload has waited past the fallback
// window, logging once per payload when it has.
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

// confirm polls the conversation's inbox until the message is marked read,
// the confirm window closes, or the context ends.
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
			return OutcomeUnavailable, "sent to agy but not yet read", nil
		}
		select {
		case <-ctx.Done():
			return OutcomeUnavailable, "sent to agy but not yet read", nil
		case <-time.After(d.confirmPoll()):
		}
	}
}

// inbox scans the conversation's inbox for a message that matches this
// payload, and reports whether it is read, undelivered, or merely sent.
//
// A message file is skipped, not an error, when it cannot be read or does not
// parse: another process is writing these files while relevo reads them, and a
// half-written file must not fail a delivery.
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

// readAgyMessage reads one message file. An unreadable or malformed file, or
// one with no id, reports false.
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

// readAgyReadIDs reads messages/read.json, agy's map of read message ids. A
// missing or malformed file reads as "nothing is read yet".
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

// agyMessageMatches is the inbox matching rule: the message is addressed to
// this conversation, its content's first line is this payload's origin line,
// and it is not older than the entry that queued it. A zero queuedAt skips the
// time check.
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

// agyTitle is the --title= value: the origin line, cut to agyTitleMaxRunes
// runes.
func agyTitle(origin string) string {
	runes := []rune(origin)
	if len(runes) <= agyTitleMaxRunes {
		return origin
	}
	return string(runes[:agyTitleMaxRunes])
}

// agyOversizeContent is what replaces a payload too long for one argv element:
// the origin line, how big the report was, and where to read it. It keeps the
// mastermind's wake-up meaningful -- the title and the first line still identify
// the round -- without an argv the kernel would refuse. ref is the
// `relevo show …` command that prints the full text, or ""
// when the entry names no show section.
func agyOversizeContent(origin string, n int, ref string) string {
	if ref == "" {
		return fmt.Sprintf("%s\n\nThe output is too long to push (%d bytes); run relevo wait.", origin, n)
	}
	return fmt.Sprintf("%s\n\nThe output is too long to push (%d bytes). Read it: %s", origin, n, ref)
}

// agySendError returns the first line of a JSON error object send-message
// printed, or "" when its output is not JSON or carries no error. Output that
// is not an error object is not treated as a failure: the read-back in the
// inbox is the proof, and a success message is not required to be JSON.
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

// redactAgy replaces every occurrence of token in s with <redacted>, so text
// that came back from agy -- an error quoting the request, a JSON error field
// -- cannot carry the credential into a log line or a Delivery reason. It does
// nothing when the token is "".
func redactAgy(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, redactedToken)
}
