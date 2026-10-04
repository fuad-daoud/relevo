package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/store"
)

// agyTestConv is a valid agy conversation id, and agyTestToken is the captured
// credential every token-hygiene test hunts for.
const (
	agyTestConv  = "0f0e0d0c-0b0a-4998-8877-665544332211"
	agyTestToken = "the-csrf-token"
)

// agyTestOrigin and agyTestPayload are shaped like what Queue writes and
// Deliver reads: the origin line, a blank line, the report.
const (
	agyTestOrigin  = `relevo: round 1 · to MasterMind · about runner "w" (not the human)`
	agyTestPayload = agyTestOrigin + "\n\nThe runner finished round 1. Report: /x/001-report.md"
)

// fakeEnvExec is the fake EnvExec the deliverer tests control: it records every
// argument vector and environment it was given, and onRun lets a test play agy
// by writing into the inbox on "send".
type fakeEnvExec struct {
	calls int
	bins  []string
	args  [][]string
	envs  [][]string
	out   []byte
	err   error

	onRun func(extraEnv []string, bin string, args []string)
}

func (f *fakeEnvExec) Run(_ context.Context, extraEnv []string, bin string, args ...string) ([]byte, error) {
	f.calls++
	f.bins = append(f.bins, bin)
	f.args = append(f.args, append([]string(nil), args...))
	f.envs = append(f.envs, append([]string(nil), extraEnv...))

	if f.onRun != nil {
		f.onRun(append([]string(nil), extraEnv...), bin, append([]string(nil), args...))
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.out, nil
}

// lastArgs is the newest recorded argv, or nil when the fake never ran.
func (f *fakeEnvExec) lastArgs() []string {
	if len(f.args) == 0 {
		return nil
	}
	return f.args[len(f.args)-1]
}

// writeAgyCredsFixture stores one credentials secret the way CaptureAgyCreds
// does, so a test can pin the address, token and exe directly.
func writeAgyCredsFixture(t *testing.T, secrets SecretStore, creds AgyCreds) {
	t.Helper()
	raw, err := json.Marshal(creds)
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	if err := secrets.SecretPut(agySecretName(creds.ConversationID), raw, agyCredsNow); err != nil {
		t.Fatalf("write creds: %v", err)
	}
}

// newAgyRig builds a deliverer over a temp agy home and a machine database
// holding a valid capture, plus the fake exec it will use.
func newAgyRig(t *testing.T) (*AgyDeliverer, *fakeEnvExec, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "antigravity-cli")
	secrets := testSecrets(t)
	writeAgyCredsFixture(t, secrets, AgyCreds{
		ConversationID: agyTestConv,
		LSAddress:      "localhost:42139",
		CSRFToken:      agyTestToken,
		AgentAPIExe:    "/usr/local/bin/agy",
		CapturedAt:     agyCredsNow,
	})

	fake := &fakeEnvExec{}
	return &AgyDeliverer{
		Exec:          fake,
		Creds:         secrets,
		Home:          home,
		ConfirmWindow: 5 * time.Millisecond,
		ConfirmPoll:   time.Millisecond,
	}, fake, home
}

// writeAgyMessage writes one inbox message file, shaped like the spike's
// observation, optionally into messages/undelivered/.
func writeAgyMessage(t *testing.T, home, id, recipient, content string, ts time.Time, undelivered bool) {
	t.Helper()
	dir := agyMessagesDir(home)
	if undelivered {
		dir = filepath.Join(dir, "undelivered")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	raw, err := json.Marshal(map[string]any{
		"id":            id,
		"recipient":     recipient,
		"sender":        "relevo",
		"priority":      "normal",
		"timestamp":     ts.UTC().Format(time.RFC3339Nano),
		"renderDetails": map[string]string{"messageTitle": "relevo"},
		"content":       content,
	})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), raw, 0o600); err != nil {
		t.Fatalf("write message: %v", err)
	}
}

// markAgyRead adds id to messages/read.json, the way agy records a receipt.
func markAgyRead(t *testing.T, home, id string) {
	t.Helper()
	dir := agyMessagesDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, "read.json")
	ids := map[string]bool{}
	if raw, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(raw, &ids)
	}
	ids[id] = true
	raw, err := json.Marshal(ids)
	if err != nil {
		t.Fatalf("marshal read.json: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write read.json: %v", err)
	}
}

// agyMessagesDir is the conversation's inbox under a temp agy home.
func agyMessagesDir(home string) string {
	return filepath.Join(home, "brain", agyTestConv, ".system_generated", "messages")
}

// playAgyOnSend makes the fake exec answer a send the way agy does: write the
// message file, and mark it read when read is true.
func playAgyOnSend(t *testing.T, fake *fakeEnvExec, home string, read bool) {
	t.Helper()
	fake.onRun = func(_ []string, _ string, args []string) {
		if len(args) < 5 {
			t.Errorf("send argv = %q, want at least five elements", args)
			return
		}
		conv, content := args[3], args[4]
		writeAgyMessage(t, home, "m-"+conv[:8], conv, content, time.Now().UTC(), false)
		if read {
			markAgyRead(t, home, "m-"+conv[:8])
		}
	}
}

func TestAgyDeliverNotMineForOtherKind(t *testing.T) {
	t.Parallel()

	d, fake, _ := newAgyRig(t)

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "claude", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine {
		t.Errorf("outcome = %v, want OutcomeNotMine", out)
	}
	if reason != "" {
		t.Errorf("reason = %q, want the empty not-mine reason", reason)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times for another harness; want 0", fake.calls)
	}
}

func TestAgyDeliverBadConversationID(t *testing.T) {
	t.Parallel()

	d, fake, _ := newAgyRig(t)

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: "not-a-conversation"}, agyTestPayload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine {
		t.Errorf("outcome = %v, want OutcomeNotMine", out)
	}
	if want := "agy mastermind session is not a conversation id; run relevo mastermind init inside agy"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times for a bad session id; want 0", fake.calls)
	}
}

func TestAgyDeliverGaveUpAfterFallback(t *testing.T) {
	t.Parallel()

	d, fake, _ := newAgyRig(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d.Now = func() time.Time { return now }
	d.FallbackAfter = time.Second

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", now.Add(-2*time.Second))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine {
		t.Errorf("outcome = %v, want OutcomeNotMine", out)
	}
	if want := "agy push gave up after 1s"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times after giving up; want 0", fake.calls)
	}
}

// TestAgyDeliverConfirmsAReadMessagePastFallback proves the inbox scan precedes
// the give-up gate: a message matching the payload that is already marked read
// confirms past FallbackAfter, and never sends.
func TestAgyDeliverConfirmsAReadMessagePastFallback(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d.Now = func() time.Time { return now }
	d.FallbackAfter = time.Second

	queuedAt := now.Add(-2 * time.Second)
	writeAgyMessage(t, home, "m-late", agyTestConv, agyTestPayload, now, false)
	markAgyRead(t, home, "m-late")

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Fatalf("outcome/reason = %v/%q, want OutcomeDelivered/\"already present\"", out, reason)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times for a message already read; want 0", fake.calls)
	}
}

func TestAgyDeliverLogsGiveUpOncePerPayload(t *testing.T) {
	var logged bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	d, _, _ := newAgyRig(t)
	d.FallbackAfter = time.Second
	base := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	queuedAt := base.Add(-2 * time.Second)
	now := base
	d.Now = func() time.Time { return now }

	endpoint := store.Endpoint{Kind: "agy", SessionID: agyTestConv}

	for _, sec := range []time.Duration{0, time.Second, 2 * time.Second} {
		now = base.Add(sec)
		out, reason, err := d.Deliver(context.Background(), endpoint, agyTestPayload, "/x/001-report.md", queuedAt)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if out != OutcomeNotMine {
			t.Fatalf("outcome = %v, want OutcomeNotMine", out)
		}
		if want := "agy push gave up after 1s"; reason != want {
			t.Errorf("reason = %q, want %q", reason, want)
		}
	}

	if got := strings.Count(logged.String(), "push not confirmed"); got != 1 {
		t.Fatalf("got %d 'push not confirmed' log lines, want 1; logs:\n%s", got, logged.String())
	}
}

func TestAgyDeliverNoCredsIsUnavailable(t *testing.T) {
	t.Parallel()

	d, fake, _ := newAgyRig(t)
	d.Creds = testSecrets(t)

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeUnavailable {
		t.Errorf("outcome = %v, want OutcomeUnavailable", out)
	}
	if want := "no agy credentials for this conversation; run any relevo command inside the agy session"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times without credentials; want 0", fake.calls)
	}
}

func TestAgyDeliverNonLoopbackIsUnavailable(t *testing.T) {
	t.Parallel()

	d, fake, _ := newAgyRig(t)
	writeAgyCredsFixture(t, d.Creds, AgyCreds{
		ConversationID: agyTestConv,
		LSAddress:      "10.0.0.5:42139",
		CSRFToken:      agyTestToken,
		CapturedAt:     agyCredsNow,
	})

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeUnavailable {
		t.Errorf("outcome = %v, want OutcomeUnavailable", out)
	}
	if want := "agy language server address is not loopback"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times to a non-loopback address; want 0", fake.calls)
	}
}

func TestAgyDeliverAlreadyReadDoesNotSend(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	writeAgyMessage(t, home, "m-already", agyTestConv, agyTestPayload, time.Now().UTC(), false)
	markAgyRead(t, home, "m-already")

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeDelivered {
		t.Errorf("outcome = %v, want OutcomeDelivered", out)
	}
	if reason != "already present" {
		t.Errorf("reason = %q, want %q", reason, "already present")
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times for a message already read; want 0", fake.calls)
	}
}

func TestAgyDeliverSentNotReadDoesNotResend(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	writeAgyMessage(t, home, "m-sent", agyTestConv, agyTestPayload, time.Now().UTC(), false)

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeAdmitted {
		t.Errorf("outcome = %v, want OutcomeAdmitted", out)
	}
	if want := "sent to agy but not yet read"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times for a message already in the inbox; want 0", fake.calls)
	}
}

// TestAgyDeliverConfirmsViaReadJSON pins the two halves: the send admits the
// message, and the read mark in read.json, seen by Confirm, is what delivers
// it. Confirm itself never sends.
func TestAgyDeliverConfirmsViaReadJSON(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	playAgyOnSend(t, fake, home, true)

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeAdmitted {
		t.Errorf("Deliver outcome = %v (reason %q), want OutcomeAdmitted", out, reason)
	}
	if fake.calls != 1 {
		t.Errorf("sent %d times, want exactly 1", fake.calls)
	}

	out, reason, err = d.Confirm(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, time.Time{})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if out != OutcomeDelivered {
		t.Errorf("Confirm outcome = %v (reason %q), want OutcomeDelivered", out, reason)
	}
	if reason != "already present" {
		t.Errorf("Confirm reason = %q, want %q", reason, "already present")
	}
	if fake.calls != 1 {
		t.Errorf("Confirm sent %d times, want no send at all", fake.calls)
	}
}

// TestAgyDeliverSentButNeverReadIsAdmitted pins that a send with no read mark
// is an admit, not a failed delivery: the message is in agy's inbox, so
// relevo must report it admitted and read the mark back later rather than
// treating the missing receipt as a fault.
func TestAgyDeliverSentButNeverReadIsAdmitted(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	playAgyOnSend(t, fake, home, false)

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeAdmitted {
		t.Errorf("outcome = %v, want OutcomeAdmitted", out)
	}
	if want := "sent to agy but not yet read"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if fake.calls != 1 {
		t.Errorf("sent %d times, want exactly 1", fake.calls)
	}
}

func TestAgyDeliverTokenOnlyInEnv(t *testing.T) {
	t.Parallel()

	t.Run("with AgentAPIExe", func(t *testing.T) {
		d, fake, home := newAgyRig(t)
		playAgyOnSend(t, fake, home, true)

		if _, _, err := d.Deliver(context.Background(),
			store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{}); err != nil {
			t.Fatalf("Deliver: %v", err)
		}

		args := fake.lastArgs()
		assertAgySendArgs(t, args)
		if got := fake.bins[0]; got != "/usr/local/bin/agy" {
			t.Errorf("bin = %q, want the captured AgentAPIExe", got)
		}

		wantEnv := []string{"ANTIGRAVITY_LS_ADDRESS=localhost:42139", "ANTIGRAVITY_CSRF_TOKEN=" + agyTestToken}
		if got := fake.envs[len(fake.envs)-1]; !reflect.DeepEqual(got, wantEnv) {
			t.Errorf("extraEnv = %q, want %q", got, wantEnv)
		}
	})

	t.Run("without AgentAPIExe it falls back to agy on PATH", func(t *testing.T) {
		d, fake, home := newAgyRig(t)
		writeAgyCredsFixture(t, d.Creds, AgyCreds{
			ConversationID: agyTestConv,
			LSAddress:      "localhost:42139",
			CSRFToken:      agyTestToken,
			CapturedAt:     agyCredsNow,
		})
		playAgyOnSend(t, fake, home, true)

		if _, _, err := d.Deliver(context.Background(),
			store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{}); err != nil {
			t.Fatalf("Deliver: %v", err)
		}

		assertAgySendArgs(t, fake.lastArgs())
		if got := fake.bins[0]; got != "agy" {
			t.Errorf("bin = %q, want agy on PATH when no AgentAPIExe was captured", got)
		}
	})
}

// assertAgySendArgs checks the whole argv shape, including the one invariant
// the design exists for: the token is nowhere in it.
func assertAgySendArgs(t *testing.T, args []string) {
	t.Helper()
	if len(args) != 5 {
		t.Fatalf("argv = %q, want 5 elements", args)
	}
	if args[0] != "agentapi" || args[1] != "send-message" {
		t.Errorf("argv[0:2] = %q, want agentapi send-message", args[0:2])
	}
	if args[2] != "--title="+agyTestOrigin {
		t.Errorf("argv[2] = %q, want --title=%q", args[2], agyTestOrigin)
	}
	if args[3] != agyTestConv {
		t.Errorf("argv[3] = %q, want the conversation id", args[3])
	}
	if args[4] != agyTestPayload {
		t.Errorf("argv[4] = %q, want the payload verbatim", args[4])
	}
	for _, arg := range args {
		if strings.Contains(arg, agyTestToken) {
			t.Errorf("argv element %q contains the token", arg)
		}
	}
}

// TestAgyDeliverSendErrorIsRedacted pins both error spellings: a Run
// error whose text quotes the token, and a JSON {"error": ...} on stdout.
func TestAgyDeliverSendErrorIsRedacted(t *testing.T) {
	t.Parallel()

	t.Run("a Run error", func(t *testing.T) {
		d, fake, _ := newAgyRig(t)
		fake.err = errors.New("boom: " + agyTestToken + " leaked\nsecond line")

		out, reason, err := d.Deliver(context.Background(),
			store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if out != OutcomeUnavailable {
			t.Errorf("outcome = %v, want OutcomeUnavailable", out)
		}
		if want := "send-message: boom: <redacted> leaked"; reason != want {
			t.Errorf("reason = %q, want %q", reason, want)
		}
		if strings.Contains(reason, agyTestToken) {
			t.Errorf("reason carries the token: %q", reason)
		}
	})

	t.Run("a JSON error on stdout", func(t *testing.T) {
		d, fake, _ := newAgyRig(t)
		fake.out = []byte(`{"error":"bad ` + agyTestToken + "\\nmore\"}")

		out, reason, err := d.Deliver(context.Background(),
			store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", time.Time{})
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if out != OutcomeUnavailable {
			t.Errorf("outcome = %v, want OutcomeUnavailable", out)
		}
		if want := "send-message: bad <redacted>"; reason != want {
			t.Errorf("reason = %q, want %q", reason, want)
		}
		if strings.Contains(reason, agyTestToken) {
			t.Errorf("reason carries the token: %q", reason)
		}
	})
}

func TestAgyDeliverOversizeSendsPointer(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	playAgyOnSend(t, fake, home, true)

	payload := agyTestOrigin + "\n\n" + strings.Repeat("x", AgyMaxContent+1)
	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, payload, "/x/001-report.md", time.Time{})
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeAdmitted {
		t.Fatalf("outcome = %v (reason %q), want OutcomeAdmitted", out, reason)
	}

	content := fake.lastArgs()[4]
	if !strings.HasPrefix(content, agyTestOrigin+"\n\n") {
		t.Errorf("content = %q, want it to start with the origin line", content)
	}
	if !strings.Contains(content, "/x/001-report.md") {
		t.Errorf("content = %q, want it to name the report path", content)
	}
	if !strings.Contains(content, "The output is too long to push") {
		t.Errorf("content = %q, want the output-oversize sentence", content)
	}
	if len(content) >= AgyMaxContent {
		t.Errorf("content is %d bytes, want under AgyMaxContent (%d)", len(content), AgyMaxContent)
	}
}

func TestAgyDeliverOlderMessageDoesNotMatch(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	d.FallbackAfter = time.Hour

	queuedAt := time.Now().Add(-time.Minute)
	writeAgyMessage(t, home, "m-old", agyTestConv, agyTestPayload, queuedAt.Add(-time.Hour), false)

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("sent %d times, want 1: an older message is not this payload", fake.calls)
	}
	if out != OutcomeAdmitted || reason != "sent to agy but not yet read" {
		t.Errorf("outcome/reason = %v/%q, want Admitted and the sent-not-read reason", out, reason)
	}
}

// TestAgyConfirmRefusesWhatItCannotRead pins Confirm's guards: it is the
// read-back half, so it refuses exactly what Deliver refuses and never sends.
func TestAgyConfirmRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	d, fake, _ := newAgyRig(t)

	cases := []struct {
		name       string
		mastermind store.Endpoint
		reason     string
	}{
		{
			name:       "another kind",
			mastermind: store.Endpoint{Kind: "claude", SessionID: agyTestConv},
			reason:     "",
		},
		{
			name:       "bad conversation id",
			mastermind: store.Endpoint{Kind: "agy", SessionID: "not-a-conversation"},
			reason:     "agy mastermind session is not a conversation id; run relevo mastermind init inside agy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, reason, err := d.Confirm(context.Background(), tc.mastermind, agyTestPayload, time.Time{})
			if err != nil {
				t.Fatalf("Confirm: %v", err)
			}
			if out != OutcomeNotMine {
				t.Fatalf("outcome = %v, want OutcomeNotMine", out)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}

	t.Run("no exec", func(t *testing.T) {
		bare := &AgyDeliverer{}
		out, reason, err := bare.Confirm(context.Background(),
			store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, time.Time{})
		if err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		if out != OutcomeNotMine || reason != "no exec" {
			t.Errorf("Confirm = (%v, %q), want OutcomeNotMine and no exec", out, reason)
		}
	})

	if fake.calls != 0 {
		t.Errorf("Confirm sent %d times, want none", fake.calls)
	}
}

// TestAgyConfirmPollsTheInbox pins Confirm's read-back against the three states
// the inbox can be in: an unread message past the window stays admitted, an
// undelivered one is unavailable, and a read mark that lands during the poll
// delivers. Confirm never sends, whatever it finds.
func TestAgyConfirmPollsTheInbox(t *testing.T) {
	t.Parallel()

	confirm := func(t *testing.T, d *AgyDeliverer) (Outcome, string) {
		t.Helper()
		out, reason, err := d.Confirm(context.Background(),
			store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, time.Time{})
		if err != nil {
			t.Fatalf("Confirm: %v", err)
		}
		return out, reason
	}

	t.Run("an unread message past the window stays admitted", func(t *testing.T) {
		d, fake, home := newAgyRig(t)
		writeAgyMessage(t, home, "m-unread", agyTestConv, agyTestPayload, time.Now().UTC(), false)

		out, reason := confirm(t, d)
		if out != OutcomeAdmitted {
			t.Errorf("outcome = %v (reason %q), want OutcomeAdmitted", out, reason)
		}
		if want := "sent to agy but not yet read"; reason != want {
			t.Errorf("reason = %q, want %q", reason, want)
		}
		if fake.calls != 0 {
			t.Errorf("Confirm sent %d times, want none", fake.calls)
		}
	})

	t.Run("an undelivered message is unavailable", func(t *testing.T) {
		d, fake, home := newAgyRig(t)
		writeAgyMessage(t, home, "m-undelivered", agyTestConv, agyTestPayload, time.Now().UTC(), true)

		out, reason := confirm(t, d)
		if out != OutcomeUnavailable {
			t.Errorf("outcome = %v (reason %q), want OutcomeUnavailable", out, reason)
		}
		if want := "agy reports the message undelivered"; reason != want {
			t.Errorf("reason = %q, want %q", reason, want)
		}
		if fake.calls != 0 {
			t.Errorf("Confirm sent %d times, want none", fake.calls)
		}
	})

	t.Run("a read mark that lands during the poll delivers", func(t *testing.T) {
		d, fake, home := newAgyRig(t)
		d.ConfirmWindow = 3 * time.Second
		d.ConfirmPoll = time.Millisecond
		writeAgyMessage(t, home, "m-late-mark", agyTestConv, agyTestPayload, time.Now().UTC(), false)
		go func() {
			time.Sleep(20 * time.Millisecond)
			markAgyRead(t, home, "m-late-mark")
		}()

		out, reason := confirm(t, d)
		if out != OutcomeDelivered {
			t.Errorf("outcome = %v (reason %q), want OutcomeDelivered", out, reason)
		}
		if fake.calls != 0 {
			t.Errorf("Confirm sent %d times, want none", fake.calls)
		}
	})
}

// TestAgyConfirmOnceAdmitsThenDelivers pins the agy repeat tick's read-back:
// one inbox scan with no send, which reports the message admitted while it is
// unread and delivered once the read mark is there.
func TestAgyConfirmOnceAdmitsThenDelivers(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	writeAgyMessage(t, home, "m-once", agyTestConv, agyTestPayload, time.Now().UTC(), false)

	endpoint := store.Endpoint{Kind: "agy", SessionID: agyTestConv}

	out, reason, err := d.ConfirmOnce(context.Background(), endpoint, agyTestPayload, time.Time{})
	if err != nil {
		t.Fatalf("ConfirmOnce: %v", err)
	}
	if out != OutcomeAdmitted {
		t.Errorf("outcome = %v (reason %q), want OutcomeAdmitted", out, reason)
	}
	if want := "sent to agy but not yet read"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if fake.calls != 0 {
		t.Errorf("ConfirmOnce sent %d times, want none", fake.calls)
	}

	markAgyRead(t, home, "m-once")

	out, reason, err = d.ConfirmOnce(context.Background(), endpoint, agyTestPayload, time.Time{})
	if err != nil {
		t.Fatalf("ConfirmOnce after the read mark: %v", err)
	}
	if out != OutcomeDelivered || reason != "already present" {
		t.Errorf("outcome/reason = %v/%q, want OutcomeDelivered/\"already present\"", out, reason)
	}
	if fake.calls != 0 {
		t.Errorf("ConfirmOnce sent %d times after the read mark, want none", fake.calls)
	}
}

// TestAgyConfirmOnceRefusesWhatItCannotRead pins the repeat tick's guards: the
// single inbox scan is attempted only for the mastermind Deliver and Confirm
// accept, and it never runs a command whatever it finds.
func TestAgyConfirmOnceRefusesWhatItCannotRead(t *testing.T) {
	t.Parallel()

	d, fake, _ := newAgyRig(t)

	cases := []struct {
		name       string
		mastermind store.Endpoint
		reason     string
	}{
		{name: "another kind", mastermind: store.Endpoint{Kind: "claude", SessionID: agyTestConv}, reason: ""},
		{
			name:       "bad conversation id",
			mastermind: store.Endpoint{Kind: "agy", SessionID: "not-a-conversation"},
			reason:     "agy mastermind session is not a conversation id; run relevo mastermind init inside agy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, reason, err := d.ConfirmOnce(context.Background(), tc.mastermind, agyTestPayload, time.Time{})
			if err != nil {
				t.Fatalf("ConfirmOnce: %v", err)
			}
			if out != OutcomeNotMine {
				t.Fatalf("outcome = %v, want OutcomeNotMine", out)
			}
			if reason != tc.reason {
				t.Errorf("reason = %q, want %q", reason, tc.reason)
			}
		})
	}

	t.Run("no exec", func(t *testing.T) {
		bare := &AgyDeliverer{}
		out, reason, err := bare.ConfirmOnce(context.Background(),
			store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, time.Time{})
		if err != nil {
			t.Fatalf("ConfirmOnce: %v", err)
		}
		if out != OutcomeNotMine || reason != "no exec" {
			t.Errorf("ConfirmOnce = (%v, %q), want OutcomeNotMine and no exec", out, reason)
		}
	})

	if fake.calls != 0 {
		t.Errorf("ConfirmOnce ran %d commands, want none", fake.calls)
	}
}

// TestDeliverPendingAgyDeliversViaDeliverer is the routing test the plan asks
// for: DeliverPending over a real AgyDeliverer confirms the pending entry with
// route deliverer:agy.
func TestDeliverPendingAgyDeliversViaDeliverer(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	playAgyOnSend(t, fake, home, true)

	rt := Deps{
		Store:      store.New(t.TempDir()),
		Now:        time.Now,
		Deliverers: map[string]MasterMindDeliverer{"agy": d},
	}

	b := store.Binding{
		Name:         "webshop",
		CWD:          "/repo/webshop",
		Round:        1,
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: "agy", SessionID: agyTestConv},
		MasterMindID: "pl_aaaaaaaabbbb",
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return Queue(context.Background(), rt, tx, b.Name, store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Payload: "round 1 report", Path: "/tmp/report.md",
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", b.Name, err)
	}

	_, got := deliverOnce(t, rt, b)
	if !got.Delivered {
		t.Fatalf("Delivered = false (route %q, reason %q), want a delivery", got.Route, got.Reason)
	}
	if got.Route != "deliverer:agy" {
		t.Errorf("route = %q, want deliverer:agy", got.Route)
	}

	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	confirmed := false
	for _, e := range entries {
		if e.Direction == store.DirToMasterMind && e.Confirmed {
			confirmed = true
			if e.Route != "deliverer:agy" {
				t.Errorf("confirmed entry route = %q, want deliverer:agy", e.Route)
			}
		}
	}
	if !confirmed {
		t.Error("the mastermind-bound log entry was not confirmed")
	}
}

// TestAgyDeliverSentButUnreadPastFallbackIsNotMine pins the give-up gate ABOVE
// the "sent but not yet read" admit: an entry queued past the window whose
// message is in the inbox and still unread is NOT admitted. It is the outcome
// the caller turns back into an admission -- a fresh admit stamp plus Confirm's
// full window under the state lock, every tick -- so honouring it past the
// window re-admits the entry in the same pass and the entry never becomes
// claimable. Nothing is sent either: the inbox already holds the payload.
func TestAgyDeliverSentButUnreadPastFallbackIsNotMine(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	d.Now = func() time.Time { return now }
	d.FallbackAfter = time.Second

	queuedAt := now.Add(-2 * time.Second)
	writeAgyMessage(t, home, "m-unread", agyTestConv, agyTestPayload, queuedAt, false)

	out, reason, err := d.Deliver(context.Background(),
		store.Endpoint{Kind: "agy", SessionID: agyTestConv}, agyTestPayload, "/x/001-report.md", queuedAt)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if out != OutcomeNotMine {
		t.Errorf("outcome = %v (reason %q), want OutcomeNotMine: an entry past the window must not be re-admitted", out, reason)
	}
	if want := "agy push gave up after 1s"; reason != want {
		t.Errorf("reason = %q, want %q", reason, want)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times for a message already in the inbox; want 0", fake.calls)
	}
}

// TestDeliverPendingAgyExpiredAdmitGoesToPullNotReAdmitted is the end-to-end
// regression, over a REAL agy inbox in the "sent but unread" state: the entry
// was admitted, the admit outlived agy's horizon, and the message is sitting in
// the inbox unclaimed by the session. The tick must NOT re-admit it. It clears
// the admit, hands the entry to the pull route -- where a reader can claim it --
// and returns without polling Confirm, so it does not hold the state lock for
// the confirm window.
func TestDeliverPendingAgyExpiredAdmitGoesToPullNotReAdmitted(t *testing.T) {
	t.Parallel()

	d, fake, home := newAgyRig(t)
	// An hour ahead of the admit stamp the store just wrote, so that stamp is
	// already an hour old by this clock. The clock keeps advancing in real time:
	// a frozen one would never close Confirm's deadline, so a regression here
	// would hang the package instead of failing it after one confirm window.
	now := func() time.Time { return time.Now().UTC().Add(time.Hour) }
	d.Now = now
	d.FallbackAfter = time.Minute      // agy's admit horizon == its give-up window
	d.ConfirmWindow = AgyConfirmWindow // the 3s poll an admit would trigger
	d.ConfirmPoll = AgyConfirmPoll

	// The real inbox state: the payload arrived, agy never marked it read.
	queuedAt := now().Add(-2 * time.Minute)
	writeAgyMessage(t, home, "m-expired", agyTestConv, agyTestPayload, now(), false)

	rt := Deps{
		Store:      store.New(t.TempDir()),
		Now:        now,
		Deliverers: map[string]MasterMindDeliverer{"agy": d},
	}

	b := store.Binding{
		Name:         "webshop",
		CWD:          "/repo/webshop",
		Round:        1,
		State:        store.StateActive,
		MasterMind:   store.Endpoint{Kind: "agy", SessionID: agyTestConv},
		MasterMindID: "pl_aaaaaaaabbbb",
		Builder:      store.Endpoint{Mode: store.ModeHeadless},
	}
	if err := rt.Store.WithLock(func(tx *store.Tx) error {
		if err := tx.Save(b); err != nil {
			return err
		}
		return Queue(context.Background(), rt, tx, b.Name, store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport,
			Payload: agyTestPayload, TS: queuedAt,
		})
	}); err != nil {
		t.Fatalf("seed %s: %v", b.Name, err)
	}
	admit(t, rt, b.Name) // an earlier tick sent the payload and stamped the admit

	start := time.Now()
	_, got := deliverOnce(t, rt, b)
	held := time.Since(start)

	if got.Delivered {
		t.Errorf("Delivered = true (route %q), want the unread entry handed back", got.Route)
	}
	if got.Route != "pull" {
		t.Errorf("route = %q, want pull: an expired admit must leave the entry claimable", got.Route)
	}
	if got.Reason != "agy push gave up after 1m0s" {
		t.Errorf("reason = %q, want the give-up reason", got.Reason)
	}
	if fake.calls != 0 {
		t.Errorf("sent %d times; want 0: the payload is already in the inbox", fake.calls)
	}
	if held >= d.ConfirmWindow {
		t.Errorf("the tick held the state lock for %s, want under the %s confirm window: an expired admit must not be re-admitted and polled", held, d.ConfirmWindow)
	}
	if admitOf(t, rt, b.Name) != nil {
		t.Error("the expired admit is still stamped: the entry is not claimable for the background wait")
	}
	if _, pending, err := rt.Store.PendingForMasterMind(b.Name); err != nil || !pending {
		t.Errorf("pending = %v (err %v), want the entry still pending and claimable", pending, err)
	}
}
